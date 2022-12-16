package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/siacentral/apisdkgo"
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
		MinPieces       uint32          `json:"minPieces"`
		AvailablePieces uint32          `json:"availablePieces"`
		Pieces          [][]PieceHealth `json:"pieces"`
	}

	FileHealth struct {
		Chunks      []ChunkHealth `json:"chunks"`
		Recoverable bool          `json:"recoverable"`
	}
)

var (
	inputFile  string
	outputFile string

	fileCmd = &cobra.Command{
		Use:   "file",
		Short: "file information commands",
		Run:   func(cmd *cobra.Command, args []string) { cmd.Usage() },
	}

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
				log.Fatalln("failed to initialize renter:", err)
			}

			availableHosts := r.Hosts()
			if err != nil {
				log.Fatalln("failed to get available hosts:", err)
			}

			inputPath := args[0]
			sf, err := siafile.Load(inputPath)
			if err != nil {
				log.Fatalln("failed to parse skyfile:", err)
			}

			// check that we have contracts with all hosts listed in the file
			var missingHosts []rhp.PublicKey
			fileHosts := make(map[rhp.PublicKey]bool)
			for _, chunk := range sf.Chunks {
				for _, piece := range chunk.Pieces {
					for _, p := range piece {
						fileHosts[p.HostKey] = true
					}
				}
			}

			for host := range fileHosts {
				if _, err := r.HostContract(host); err != nil {
					missingHosts = append(missingHosts, host)
				}
			}

			if len(missingHosts) > 0 {
				client := apisdkgo.NewSiaClient()
				log.Println("missing contracts for hosts listed in the sia file:")
				for _, hostPub := range missingHosts {
					host, err := client.GetHost(hostPub.String())
					if err != nil {
						log.Fatalln("failed to get host info:", err)
					}
					log.Printf(" - %v %v last seen %v", host.PublicKey, host.NetAddress, time.Since(host.LastSuccessScan))
				}
			}

			if len(availableHosts) == 0 {
				log.Fatalln("no hosts available")
			}

			log.Printf("Checking file health on %v hosts...", len(availableHosts))
			sectorAvailability := make(map[crypto.Hash][]rhp.PublicKey)
			var sectors []crypto.Hash
			added := make(map[crypto.Hash]bool)
			for _, chunk := range sf.Chunks {
				for _, piece := range chunk.Pieces {
					for _, p := range piece {
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
					available := true
					var pieceHealth []PieceHealth
					for _, sector := range piece {
						if len(sectorAvailability[sector.MerkleRoot]) == 0 {
							available = false
							break
						}
						pieceHealth = append(pieceHealth, PieceHealth{
							MerkleRoot: sector.MerkleRoot,
							Hosts:      sectorAvailability[sector.MerkleRoot],
						})
					}
					if available {
						chunkHealth.AvailablePieces++
					}
					chunkHealth.Pieces = append(chunkHealth.Pieces, pieceHealth)
				}
				health.Chunks = append(health.Chunks, chunkHealth)
				if chunkHealth.AvailablePieces < chunkHealth.MinPieces {
					unhealthy = true
				}
			}

			outputPath := filepath.Join(dataDir, filepath.Base(inputPath)+".health.json")
			output, err := os.Create(outputPath)
			if err != nil {
				log.Fatalln("failed to create output file:", err)
			}
			defer output.Close()

			health.Recoverable = !unhealthy
			enc := json.NewEncoder(output)
			enc.SetIndent("", "  ")
			if err := enc.Encode(health); err != nil {
				log.Fatalln("failed to encode health report:", err)
			}
			if health.Recoverable {
				log.Println("File is recoverable")
			} else {
				log.Println("File is not recoverable")
			}
			log.Printf("Health report written to %v", outputPath)
		},
	}

	recoverCmd = &cobra.Command{
		Use:   "recover -i <input file> -o <output file>",
		Short: "Recover a file from the Sia network.",
		Run: func(cmd *cobra.Command, args []string) {
			if len(inputFile) == 0 || len(outputFile) == 0 {
				cmd.Usage()
				log.Fatalln("flags -i and -o are required")
			}

			r, err := renter.New(dataDir)
			if err != nil {
				log.Fatalln("failed to initialize renter:", err)
			}

			sf, err := siafile.Load(inputFile)
			if err != nil {
				log.Fatalln("failed to parse skyfile:", err)
			}

			// check that we have contracts with all hosts listed in the file
			var missingHosts []rhp.PublicKey
			fileHosts := make(map[rhp.PublicKey]bool)
			for _, chunk := range sf.Chunks {
				for _, piece := range chunk.Pieces {
					for _, p := range piece {
						fileHosts[p.HostKey] = true
					}
				}
			}

			for host := range fileHosts {
				if _, err := r.HostContract(host); err != nil {
					missingHosts = append(missingHosts, host)
				}
			}

			if len(missingHosts) > 0 {
				client := apisdkgo.NewSiaClient()
				log.Println("missing contracts for hosts listed in the sia file:")
				for _, hostPub := range missingHosts {
					host, err := client.GetHost(hostPub.String())
					if err != nil {
						log.Fatalln("failed to get host info:", err)
					}
					log.Printf(" - %v %v last seen %v", host.PublicKey, host.NetAddress, time.Since(host.LastSuccessScan))
				}
			}

			if len(r.Hosts()) == 0 {
				log.Fatalln("no hosts available")
			}

			ec, err := siafile.InitErasureCoder(sf.EncoderType, sf.DataPieces, sf.ParityPieces)
			if err != nil {
				log.Fatalln("failed to initialize erasure coder:", err)
			}

			var ct crypto.CipherType
			if err := ct.FromString(sf.MasterKeyType); err != nil {
				log.Fatalln("failed to decode master key:", err)
			}

			masterKey, err := crypto.NewSiaKey(ct, sf.MasterKey)
			if err != nil {
				log.Fatalln("failed to decode master key:", err)
			}

			output, err := os.Create(outputFile)
			if err != nil {
				log.Fatalln("failed to create output file:", err)
			}
			defer output.Close()

			chunkSize := sf.PieceSize * uint64(ec.MinPieces())
			remainingSize := sf.FileSize
			// map merkle roots to the data that was recovered for that root
			recoveredSectors := make(map[crypto.Hash][]byte)
			for chunkIdx, chunk := range sf.Chunks {
				if remainingSize < chunkSize {
					chunkSize = remainingSize
				}
				remainingSize -= chunkSize

				var recovered int
				recoveredPieces := make([][]byte, ec.NumPieces())
				var missingPieces []int
				for pieceIdx, piece := range chunk.Pieces {
					// skip empty pieces
					if len(piece) == 0 {
						continue
					}

					key := masterKey.Derive(uint64(chunkIdx), uint64(pieceIdx))
					var sectorsRecovered int
					var recoveredData []byte
					for _, sector := range piece {
						if buf, ok := recoveredSectors[sector.MerkleRoot]; ok {
							// we already have this sector, no need to download it again
							sectorsRecovered++
							recoveredData = append(recoveredData, buf...)
							log.Printf("Sector %v already in cache", sector.MerkleRoot)
							continue
						}

						// check the listed host first
						buf, err := downloadSector(r, sector.HostKey, sector.MerkleRoot)
						if err == nil {
							sectorsRecovered++
							recoveredSectors[sector.MerkleRoot] = buf
							recoveredData = append(recoveredData, buf...)
							log.Printf("Recovered sector %v from host %v", sector.MerkleRoot, sector.HostKey)
							continue
						} else if strings.Contains(err.Error(), "no record of that contract") {
							// remove the host from the list of available hosts
							r.RemoveHostContract(sector.HostKey)
							log.Printf("[WARN] removed host %v from available hosts: contract not found -- form new contract", sector.HostKey)
						} else {
							log.Printf("[WARN] failed to download sector %v from host %v: %v", sector.MerkleRoot, sector.HostKey, err)
						}
					}
					if sectorsRecovered != len(piece) {
						log.Printf("Failed to recover piece %v for chunk %v", pieceIdx+1, chunkIdx+1)
						missingPieces = append(missingPieces, pieceIdx)
						continue
					}

					decrypted, err := key.DecryptBytesInPlace(recoveredData, 0)
					if err != nil {
						log.Printf("Failed to decrypt piece %v for chunk %v", pieceIdx+1, chunkIdx+1)
					}
					recoveredPieces[pieceIdx] = decrypted
					recovered++
					log.Printf("Recovered piece %v for chunk %v (%v/%v)", pieceIdx+1, chunkIdx+1, recovered, ec.MinPieces())
					if recovered >= ec.MinPieces() {
						break
					}
				}

				// if enough pieces have been downloaded, recover the chunk
				if recovered >= ec.MinPieces() {
					if err := ec.Recover(recoveredPieces, chunkSize, output); err != nil {
						log.Fatalf("failed to recover chunk %v: %v", chunkIdx, err)
					}
					continue
				}

				log.Printf("Checking for missing pieces -- need %v more to recover...", ec.MinPieces()-recovered)
				// try to recover the missing pieces
				for _, pieceIdx := range missingPieces {
					log.Printf("Looking for piece %v (%v/%v)", pieceIdx+1, recovered, ec.MinPieces())
					piece := chunk.Pieces[pieceIdx]
					key := masterKey.Derive(uint64(chunkIdx), uint64(pieceIdx))
					var sectorsRecovered int
					var recoveredData []byte
					for _, sector := range piece {
						if buf, ok := recoveredSectors[sector.MerkleRoot]; ok {
							sectorsRecovered++
							recoveredData = append(recoveredData, buf...)
							continue
						}

						buf, recoveredSector := recoverSector(context.Background(), r, sector.MerkleRoot, workers)
						if recoveredSector {
							sectorsRecovered++
							recoveredData = append(recoveredData, buf...)
							log.Println("Recovered sector", sector.MerkleRoot)
						} else {
							log.Printf("Failed to recover sector %v", sector.MerkleRoot)
						}
					}

					if sectorsRecovered != len(piece) {
						log.Printf("Failed to recover piece %v for chunk %v", pieceIdx+1, chunkIdx+1)
						continue
					}

					decrypted, err := key.DecryptBytesInPlace(recoveredData, 0)
					if err != nil {
						log.Printf("Failed to decrypt piece %v for chunk %v", pieceIdx+1, chunkIdx+1)
					}
					recoveredPieces[pieceIdx] = decrypted
					recovered++
					log.Printf("Recovered piece %v for chunk %v (%v/%v)", pieceIdx+1, chunkIdx+1, recovered, ec.MinPieces())
					if recovered >= ec.MinPieces() {
						break
					}
				}

				if err := ec.Recover(recoveredPieces, chunkSize, output); err != nil {
					log.Fatalf("failed to recover chunk %v: %v", chunkIdx+1, err)
				}
				log.Printf("Recovered chunk %v/%v", chunkIdx+1, len(sf.Chunks))
			}
		},
	}
)

// downloadSector attempts to download a sector from a host.
func downloadSector(r *renter.Renter, hostPub rhp.PublicKey, sector crypto.Hash) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sess, err := r.NewSession(ctx, hostPub)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer sess.Close()

	// get the host's current settings
	settings, err := rhp.RPCSettings(ctx, sess.Transport())
	if err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	buf := bytes.NewBuffer(nil)
	sections := []rhp.RPCReadRequestSection{
		{MerkleRoot: rhp.Hash256(sector), Offset: 0, Length: rhp.SectorSize},
	}
	// try to read the sector
	cost := rhp.RPCReadCost(settings, sections)
	if err := sess.Read(ctx, buf, sections, cost); err != nil {
		return nil, fmt.Errorf("failed to read sector %v: %w", sector, err)
	} else if buf.Len() != rhp.SectorSize {
		return nil, fmt.Errorf("unexpected sector size: %v", buf.Len())
	}

	// verify the downloaded data matches the merkle root
	root := rhp.SectorRoot((*[rhp.SectorSize]byte)(buf.Bytes()))
	if root != rhp.Hash256(sector) {
		return nil, errors.New("downloaded sector has incorrect merkle root")
	}
	return buf.Bytes(), nil
}

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
