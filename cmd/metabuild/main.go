package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"gitlab.com/SkynetLabs/skyd/skymodules"
)

func writeSubFile(r io.Reader, fp string) error {
	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("failed to create file %v: %w", fp, err)
	}
	defer f.Close()

	_, err = io.Copy(f, r)
	return err
}

func main() {
	baseSectorPath := os.Args[1]
	extendedPath := os.Args[2]
	outputPath := os.Args[3]

	bsf, err := os.Open(baseSectorPath)
	if err != nil {
		log.Fatalln("failed to open base sector:", err)
	}
	defer bsf.Close()

	baseSector, err := io.ReadAll(bsf)
	if err != nil {
		log.Fatalln("failed to read base sector:", err)
	}

	_, _, meta, _, _, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err != nil {
		log.Fatalln("failed to parse base sector:", err)
	}

	ef, err := os.Open(extendedPath)
	if err != nil {
		log.Fatalln("failed to open extended sector:", err)
	}

	if len(meta.Subfiles) == 0 {
		log.Fatalln("no subfiles found in base sector")
	}

	log.Printf("Found %v subfiles", len(meta.Subfiles))
	h := sha256.New()
	for _, subfile := range meta.Subfiles {
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
