package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/modules/renter/filesystem/siafile"
)

type (
	fileMetadata struct {
		UniqueID string `json:"uniqueid"` // unique identifier for file

		PagesPerChunk uint8    `json:"pagesperchunk"` // number of pages reserved for storing a chunk.
		Version       [16]byte `json:"version"`       // version of the sia file format used
		FileSize      int64    `json:"filesize"`      // total size of the file
		PieceSize     uint64   `json:"piecesize"`     // size of a single piece of the file

		// The following fields are the offsets for data that is written to disk
		// after the pubKeyTable. We reserve a generous amount of space for the
		// table and extra fields, but we need to remember those offsets in case we
		// need to resize later on.
		//
		// chunkOffset is the offset of the first chunk, forced to be a factor of
		// 4096, default 4kib
		//
		// pubKeyTableOffset is the offset of the publicKeyTable within the
		// file.
		//
		ChunkOffset       int64 `json:"chunkoffset"`
		PubKeyTableOffset int64 `json:"pubkeytableoffset"`

		// erasure code settings.
		//
		// ErasureCodeType specifies the algorithm used for erasure coding
		// chunks. Available types are:
		//   0 - Invalid / Missing Code
		//   1 - Reed Solomon Code
		//
		// erasureCodeParams specifies possible parameters for a certain
		// ErasureCodeType. Currently params will be parsed as follows:
		//   Reed Solomon Code - 4 bytes dataPieces / 4 bytes parityPieces
		//
		ErasureCodeType   [4]byte `json:"erasurecodetype"`
		ErasureCodeParams [8]byte `json:"erasurecodeparams"`

		// Skylink tracking. If this siafile is known to have sectors of any
		// skyfiles, those skyfiles will be listed here. It should be noted that
		// a single siafile can be responsible for tracking many skyfiles.
		Skylinks []string `json:"skylinks"`
	}

	Piece struct {
		MerkleRoot crypto.Hash // merkle root of the piece
		HostKey    string      // public key of the host
	}

	Chunk struct {
		Pieces [][]Piece `json:"pieces"`
	}

	SiaFile struct {
		FileSize     int64  `json:"filesize"`  // total size of the file
		PieceSize    uint64 `json:"piecesize"` // size of a single piece of the file
		EncoderType  uint32 `json:"encodertype"`
		DataPieces   uint32 `json:"datapieces"`
		ParityPieces uint32 `json:"paritypieces"`

		// Skylink tracking. If this siafile is known to have sectors of any
		// skyfiles, those skyfiles will be listed here. It should be noted that
		// a single siafile can be responsible for tracking many skyfiles.
		Skylinks []string `json:"skylinks"`

		Chunks []Chunk `json:"chunks"`
	}
)

func initErasureCoder(ecType, dataPieces, parityPieces uint32) (modules.ErasureCoder, error) {
	switch ecType {
	case 1:
		return modules.NewRSCode(int(dataPieces), int(parityPieces))
	case 2:
		return modules.NewRSSubCode(int(dataPieces), int(parityPieces), 64)
	default:
		return nil, fmt.Errorf("unknown erasure coder type: %d", ecType)
	}
}

func parseSkyFile(fp string) (sf SiaFile, _ error) {
	f, err := os.Open(fp)
	if err != nil {
		return SiaFile{}, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	// decode the JSON metadata
	var meta fileMetadata
	dec := json.NewDecoder(f)
	if err := dec.Decode(&meta); err != nil {
		return SiaFile{}, fmt.Errorf("failed to decode file: %w", err)
	}

	sf.FileSize = meta.FileSize
	sf.PieceSize = meta.PieceSize
	sf.Skylinks = meta.Skylinks
	sf.EncoderType = binary.BigEndian.Uint32(meta.ErasureCodeType[:])
	sf.DataPieces = binary.LittleEndian.Uint32(meta.ErasureCodeParams[:4])
	sf.ParityPieces = binary.LittleEndian.Uint32(meta.ErasureCodeParams[4:])

	// read the raw host table data
	hostKeys := (meta.ChunkOffset - meta.PubKeyTableOffset) / (16 + 8 + 32 + 1)
	if _, err := f.Seek(meta.PubKeyTableOffset, io.SeekStart); err != nil {
		return SiaFile{}, fmt.Errorf("failed to seek to host table: %w", err)
	}

	hostTable := make([]siafile.HostPublicKey, hostKeys)
	for i := range hostTable {
		if err := hostTable[i].UnmarshalSia(f); err != nil {
			return SiaFile{}, fmt.Errorf("failed to decode host key: %w", err)
		}
	}

	ec, err := initErasureCoder(sf.EncoderType, sf.DataPieces, sf.ParityPieces)
	if err != nil {
		return SiaFile{}, fmt.Errorf("failed to init erasure coder: %w", err)
	}

	if _, err := f.Seek(meta.ChunkOffset, io.SeekStart); err != nil {
		return SiaFile{}, fmt.Errorf("failed to seek to chunk table: %w", err)
	}

	chunkSize := meta.PieceSize * uint64(ec.MinPieces())
	chunks := uint64(meta.FileSize) / chunkSize
	if uint64(meta.FileSize)%chunkSize != 0 || chunks == 0 {
		chunks++
	}

	// each chunk is encoded to a minimum of 4096 bytes
	chunkBuf := make([]byte, 4096)
	for i := 0; i < int(chunks); i++ {
		if _, err := io.ReadFull(f, chunkBuf); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return SiaFile{}, fmt.Errorf("failed to read chunk: %w", err)
		}

		chunk := Chunk{
			Pieces: make([][]Piece, ec.NumPieces()),
		}

		r := bytes.NewBuffer(chunkBuf)
		// skip the extension info and stuck bytes
		r.Next(17)

		// read the pieces length prefix
		var pieces uint16
		if err := binary.Read(r, binary.LittleEndian, &pieces); err != nil {
			return SiaFile{}, fmt.Errorf("failed to read piece length: %w", err)
		}

		// parse each piece
		for j := 0; j < int(pieces); j++ {
			var piece Piece

			var pieceIndex, hostIndex uint32
			if err := binary.Read(r, binary.LittleEndian, &pieceIndex); err != nil {
				return SiaFile{}, fmt.Errorf("failed to read piece index: %w", err)
			} else if err := binary.Read(r, binary.LittleEndian, &hostIndex); err != nil {
				return SiaFile{}, fmt.Errorf("failed to read host index: %w", err)
			} else if _, err := io.ReadFull(r, piece.MerkleRoot[:]); err != nil {
				return SiaFile{}, fmt.Errorf("failed to read merkle root: %w", err)
			}

			if pieceIndex >= uint32(len(chunk.Pieces)) {
				return SiaFile{}, fmt.Errorf("piece index %v out of range", pieceIndex)
			} else if hostIndex >= uint32(len(hostTable)) {
				return SiaFile{}, fmt.Errorf("host index %v out of range", hostIndex)
			}
			piece.HostKey = hostTable[hostIndex].PublicKey.String()
			chunk.Pieces[pieceIndex] = append(chunk.Pieces[pieceIndex], piece)
		}
		sf.Chunks = append(sf.Chunks, chunk)
	}

	return sf, nil
}

func main() {
	sf, err := parseSkyFile(os.Args[1])
	if err != nil {
		log.Fatalln("failed to parse skyfile:", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sf); err != nil {
		log.Fatalln("failed to encode skyfile:", err)
	}

}
