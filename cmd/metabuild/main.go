package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/SkynetLabs/skyd/skymodules"
)

const sectorSize = 1 << 22 // 4 MiB

func writeSubFile(r io.Reader, fp string) error {
	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("failed to create file %v: %w", fp, err)
	}
	defer f.Close()

	_, err = io.Copy(f, r)
	return err
}

func parseMetadata(metaPath string) (skymodules.SkyfileMetadata, error) {
	f, err := os.Open(metaPath)
	if err != nil {
		log.Fatalln("failed to open base sector file:", err)
	}
	defer f.Close()

	// read the base sector
	var sector [sectorSize]byte
	if _, err := io.ReadFull(f, sector[:]); err != nil {
		return skymodules.SkyfileMetadata{}, fmt.Errorf("failed to read base sector: %w", err)
	}

	// attempt to parse the metadata from the base sector. May return a
	// recursive base sector error.
	_, _, meta, _, _, err := skymodules.ParseSkyfileMetadata(sector[:])
	if err == nil {
		return meta, nil
	} else if err != nil && !strings.Contains(err.Error(), "can't use skymodules.ParseSkyfileMetadata to parse recursive base sector - use renter.ParseSkyfileMetadata instead") {
		return skymodules.SkyfileMetadata{}, fmt.Errorf("failed to parse base sector: %w", err)
	}

	// Since its a recursive base sector, only parse the layout
	layout := skymodules.ParseSkyfileLayout(sector[:])
	// Get the size of the payload
	payloadSize := layout.FanoutSize + layout.MetadataSize

	// calculate the fanout offset in the meta file
	translatedOffset, _ := skymodules.TranslateBaseSectorExtensionOffset(0, payloadSize, payloadSize, uint64(sectorSize-skymodules.SkyfileLayoutSize))

	// seek to the start of the JSON payload
	if _, err := f.Seek(int64(translatedOffset+layout.FanoutSize), io.SeekCurrent); err != nil {
		return skymodules.SkyfileMetadata{}, fmt.Errorf("failed to seek to metadata: %w", err)
	}

	// parse the JSON
	lr := io.LimitReader(f, int64(payloadSize))
	if err := json.NewDecoder(lr).Decode(&meta); err != nil {
		return skymodules.SkyfileMetadata{}, fmt.Errorf("failed to decode metadata: %w", err)
	}
	return meta, nil
}

func main() {
	metadataPath := os.Args[1]
	extendedPath := os.Args[2]
	outputPath := os.Args[3]

	meta, err := parseMetadata(metadataPath)
	if err != nil {
		log.Fatalln("failed to parse base sectors:", err)
	}

	ef, err := os.Open(extendedPath)
	if err != nil {
		log.Fatalln("failed to open extended sector:", err)
	}

	if len(meta.Subfiles) == 0 {
		log.Fatalln("no subfiles found")
	}

	log.Printf("Found %v subfiles", len(meta.Subfiles))
	h := sha256.New()
	for _, subfile := range meta.Subfiles {
		log.Printf("Found file %v %v bytes at %v offset", subfile.Filename, subfile.Len, subfile.Offset)
		h.Reset()
		if _, err := ef.Seek(int64(subfile.Offset), io.SeekStart); err != nil {
			log.Fatalln("failed to seek to subfile:", err)
		}

		lr := io.LimitReader(ef, int64(subfile.Len))
		tr := io.TeeReader(lr, h)
		out := filepath.Join(outputPath, subfile.Filename)
		if err := writeSubFile(tr, out); err != nil {
			log.Fatalln("failed to write subfile:", err)
		}
		log.Printf("%v %v bytes %x checksum", out, subfile.Len, h.Sum(nil))
	}
}
