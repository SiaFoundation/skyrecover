package main

import (
	"go.sia.tech/siad/crypto"
)

type (
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
