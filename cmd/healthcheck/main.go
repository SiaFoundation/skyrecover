package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"

	rhpv2 "go.sia.tech/skyrecover/internal/rhp/v2"
)

type (
	ChunkHealth struct {
		PiecesAvailable int `json:"piecesAvailable"`
		MinPieces       int `json:"minPieces"`
	}
)

var (
	dir            string
	inputFile      string
	recoveryPhrase string
)

func init() {
	flag.StringVar(&dir, "dir", "", "directory for contracts and private key")
	flag.StringVar(&inputFile, "input", "", "input file to extract metadata from")
	flag.StringVar(&recoveryPhrase, "phrase", "", "recovery phrase for the wallet")
	flag.Parse()
}

func main() {
	wallet, err := initWallet(recoveryPhrase)
	if err != nil {
		log.Fatal("failed to initialize wallet:", err)
	}

	balance, err := wallet.Balance()
	if err != nil {
		log.Fatal("failed to get wallet balance:", err)
	}

	log.Println("Wallet Address:", wallet.Address())
	log.Println("Wallet Balance:", balance.HumanString())

	renter, err := newRenter(dir, wallet)
	if err != nil {
		log.Fatal("failed to initialize contractor:", err)
	}

	log.Println("Loaded renter with", len(renter.contracts), "contracts")

	f, err := os.Open(inputFile)
	if err != nil {
		log.Fatalln("failed to open metadata file:", err)
	}
	defer f.Close()

	var sf SiaFile
	if err := json.NewDecoder(f).Decode(&sf); err != nil {
		log.Fatalln("failed to decode metadata file:", err)
	}

	for i, chunk := range sf.Chunks {
		health := ChunkHealth{
			MinPieces: int(sf.DataPieces),
		}
		log.Printf("Checking chunk %v", i)
		for _, piece := range chunk.Pieces {
			for _, p := range piece {
				var hostPub rhpv2.PublicKey
				if err := hostPub.UnmarshalText([]byte(p.HostKey)); err != nil {
					log.Fatalf("failed to decode host key %v: %v", p.HostKey, err)
				}

				if err := renter.VerifySector(rhpv2.Hash256(p.MerkleRoot), hostPub); err != nil {
					log.Printf("INFO: unable to retrieve sector %v from host %v: %v", p.MerkleRoot, hostPub, err)
				} else {
					health.PiecesAvailable++
					log.Printf("INFO: sector %v available", p.MerkleRoot)
				}
			}
		}
	}

}
