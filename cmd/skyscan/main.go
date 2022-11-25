package main

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"flag"
	"hash"
	"log"
	"os"
	"strings"
)

func main() {
	fileChecksum := flag.String("checksum", "", "checksum of the file")
	fileLength := flag.Uint64("len", 0, "length of the file")
	inputFilePath := flag.String("input", "", "path to the input file")
	outputFilePath := flag.String("output", ".", "path to the output file")
	checksumAlgo := flag.String("algo", "sha256", "checksum algorithm to use")
	flag.Parse()

	switch {
	case len(*fileChecksum) == 0:
		log.Fatalln("missing -checksum")
	case *fileLength == 0:
		log.Fatalln("missing -len")
	}

	var h hash.Hash
	switch strings.ToLower(*checksumAlgo) {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	case "md5":
		h = md5.New()
	default:
		log.Fatalln("unknown checksum algorithm:", *checksumAlgo)
	}

	stat, err := os.Stat(*inputFilePath)
	if err != nil {
		log.Fatalln("failed to stat input file:", err)
	} else if stat.Size() < int64(*fileLength) {
		log.Fatalln("input file size does not match -len")
	}

	input, err := os.ReadFile(*inputFilePath)
	if err != nil {
		log.Fatalln("failed to read input file:", err)
	}

	n := uint64(len(input)) - *fileLength
	for i := uint64(0); i < n; i++ {
		// reset the hasher
		h.Reset()
		// get the current chunk range
		start := i
		end := i + *fileLength
		chunk := input[start:end]
		// check the checksum
		if _, err := h.Write(chunk); err != nil {
			log.Fatalln("failed to write chunk to hasher:", err)
		} else if *fileChecksum == hex.EncodeToString(h.Sum(nil)) {
			log.Printf("Found match at %v-%v", start, end)
			if err := os.WriteFile(*outputFilePath, chunk, 0644); err != nil {
				log.Fatalln("failed to write to output file:", err)
			}
			return
		}
	}

	log.Println("no matching file found")
}
