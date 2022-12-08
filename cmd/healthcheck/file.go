package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/skyrecover/internal/renter"
	"go.sia.tech/skyrecover/internal/rhp/v2"
	"go.sia.tech/skyrecover/internal/siafile"
)

type (
	PieceHealth struct {
		MerkleRoot crypto.Hash     `json:"merkleRoot"`
		Hosts      []rhp.PublicKey `json:"hosts"`
	}

	ChunkHealth struct {
		MinPieces uint32          `json:"minPieces"`
		Pieces    [][]PieceHealth `json:"pieces"`
	}

	FileHealth struct {
		Chunks      []ChunkHealth `json:"chunks"`
		Recoverable bool          `json:"recoverable"`
	}
)

var (
	healthCheckCmd = &cobra.Command{
		Use:   "check <metadata file>",
		Short: "get information about a file",
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) != 1 {
				cmd.Usage()
				return
			}

			r, err := renter.New(dataDir)
			if err != nil {
				log.Fatal("failed to initialize renter:", err)
			}

			availableHosts, err := r.Hosts()
			if err != nil {
				log.Fatal("failed to get available hosts:", err)
			}

			f, err := os.Open(args[0])
			if err != nil {
				log.Fatalln("failed to open metadata file:", err)
			}
			defer f.Close()

			var sf siafile.SiaFile
			if err := json.NewDecoder(f).Decode(&sf); err != nil {
				log.Fatalln("failed to decode metadata file:", err)
			}

			log.Printf("Checking file health on %v hosts...", len(availableHosts))
			sectorAvailability := make(map[crypto.Hash][]rhp.PublicKey)
			var sectors []crypto.Hash
			added := make(map[crypto.Hash]bool)
			for _, chunk := range sf.Chunks {
				for _, piece := range chunk.Pieces {
					for _, p := range piece {
						var hostPub rhp.PublicKey
						if err := hostPub.UnmarshalText([]byte(p.HostKey)); err != nil {
							log.Fatalf("failed to decode host key %v: %v", p.HostKey, err)
						}
						if added[p.MerkleRoot] {
							continue
						}
						sectors = append(sectors, p.MerkleRoot)
						added[p.MerkleRoot] = true
					}
				}
			}

			// check each host for each sector
			for _, host := range availableHosts {
				for _, sector := range sectors {
					available, err := checkSector(r, host, sector)
					if err != nil {
						log.Printf("WARNING: failed to check sectors on host %v: %v", host, err)
					} else if !available {
						continue
					}
					sectorAvailability[sector] = append(sectorAvailability[sector], host)
				}
			}

			// build the health report
			var health FileHealth
			var unhealthy bool
			for _, chunk := range sf.Chunks {
				var chunkHealth ChunkHealth
				chunkHealth.MinPieces = sf.DataPieces
				for _, piece := range chunk.Pieces {
					var pieceHealth []PieceHealth
					for _, p := range piece {
						if len(sectorAvailability[p.MerkleRoot]) == 0 {
							unhealthy = true
						}
						pieceHealth = append(pieceHealth, PieceHealth{
							MerkleRoot: p.MerkleRoot,
							Hosts:      sectorAvailability[p.MerkleRoot],
						})
					}
					chunkHealth.Pieces = append(chunkHealth.Pieces, pieceHealth)
				}
				health.Chunks = append(health.Chunks, chunkHealth)
			}

			health.Recoverable = !unhealthy
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(health); err != nil {
				log.Fatal("failed to encode health report:", err)
			}
		},
	}
)

// checkSector checks if a sector is available on a host.
//
// note: cannot be batched in RHP2 because the host terminates the RPC loop if
// it encounters an error.
func checkSector(r *renter.Renter, hostPub rhp.PublicKey, sector crypto.Hash) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	sess, err := r.NewSession(ctx, hostPub)
	if err != nil {
		return false, fmt.Errorf("failed to create session: %w", err)
	}
	defer sess.Close()

	// get the host's current settings
	settings, err := rhp.RPCSettings(ctx, sess.Transport())
	if err != nil {
		return false, fmt.Errorf("failed to get settings: %w", err)
	}

	buf := bytes.NewBuffer(nil)

	sections := []rhp.RPCReadRequestSection{
		{MerkleRoot: rhp.Hash256(sector), Offset: 0, Length: rhp.SectorSize},
	}
	// try to read the sector
	cost := rhp.RPCReadCost(settings, sections)
	if err := sess.Read(ctx, buf, sections, cost); err != nil && strings.Contains(err.Error(), "could not find the desired sector") {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to read sector %v: %w", sector, err)
	} else if buf.Len() != rhp.SectorSize {
		return false, fmt.Errorf("unexpected sector size: %v", buf.Len())
	}

	// verify the downloaded data matches the merkle root
	root := rhp.SectorRoot((*[rhp.SectorSize]byte)(buf.Bytes()))
	return root == rhp.Hash256(sector), nil
}
