package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/aead/chacha20/chacha"
	"gitlab.com/SkynetLabs/skyd/build"
	"gitlab.com/SkynetLabs/skyd/skykey"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

const sectorSize = 1 << 22 // 4 MiB

// writeSubFile writes a subfile from the reader to disk
func writeSubFile(r io.Reader, fp string, n int64) error {
	f, err := os.Create(fp)
	if err != nil {
		return fmt.Errorf("failed to create file %v: %w", fp, err)
	}
	defer f.Close()
	if n, err = io.CopyN(f, r, n); err != nil {
		return fmt.Errorf("failed to copy data (%v bytes written): %w", n, err)
	} else if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}
	return nil
}

// findMatchingSkyKey tries to find a Skykey that can decrypt the identifier and
// be used for decrypting the associated skyfile. It returns an error if it is
// not found.
func findMatchingSkyKey(skykeyDB *skykey.SkykeyManager, encryptionIdentifier []byte, nonce []byte) (skykey.Skykey, error) {
	allSkykeys := skykeyDB.Skykeys()
	for _, sk := range allSkykeys {
		matches, err := sk.MatchesSkyfileEncryptionID(encryptionIdentifier, nonce)
		if err != nil {
			continue
		} else if matches {
			return sk, nil
		}
	}
	return skykey.Skykey{}, errors.New("not found")
}

// parseMetadata parses a base sector and returns the Skyfile metadata.
func parseMetadata(skykeyDB *skykey.SkykeyManager, skylink, metaPath string) (skymodules.SkyfileMetadata, []byte, error) {
	f, err := os.Open(metaPath)
	if err != nil {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to open metadata file: %w", err)
	}
	defer f.Close()

	var sl skymodules.Skylink
	if err := sl.LoadString(skylink); err != nil {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to parse skylink: %w", err)
	}

	offset, length, err := sl.OffsetAndFetchSize()
	if err != nil {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to get offset and fetch size: %w", err)
	}

	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to seek to offset %v: %w", offset, err)
	}

	baseSector := make([]byte, length)
	if _, err := io.ReadFull(f, baseSector); err != nil {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to read base sector: %w", err)
	}

	// if the layout is encrypted, decrypt it first
	if skymodules.IsEncryptedBaseSector(baseSector) {
		log.Println("base sector is encrypted")
		var layout skymodules.SkyfileLayout
		layout.Decode(baseSector)

		// get the nonce to be used for getting private-id skykeys, and for
		// deriving the file-specific skykey
		nonce := make([]byte, chacha.XNonceSize)
		copy(nonce[:], layout.KeyData[skykey.SkykeyIDLen:skykey.SkykeyIDLen+chacha.XNonceSize])

		// grab the key ID from the layout
		var keyID skykey.SkykeyID
		copy(keyID[:], layout.KeyData[:skykey.SkykeyIDLen])

		// try to get the skykey associated with that ID
		masterSkykey, err := skykeyDB.KeyByID(keyID)
		// if the ID is unknown, use the key ID as an encryption identifier and
		// try finding the associated skykey.
		if strings.Contains(err.Error(), skykey.ErrNoSkykeysWithThatID.Error()) {
			masterSkykey, err = findMatchingSkyKey(skykeyDB, keyID[:], nonce)
		}
		if err != nil {
			return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to get skykey: %w", err)
		}

		// derive the file-specific key
		fileSkykey, err := masterSkykey.SubkeyWithNonce(nonce)
		if err != nil {
			return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to derive file-specific skykey: %w", err)
		}

		// derive the base sector subkey and use it to decrypt the base sector
		baseSectorKey, err := fileSkykey.DeriveSubkey(skymodules.BaseSectorNonceDerivation[:])
		if err != nil {
			return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to derive base sector subkey: %w", err)
		}

		// get the cipherkey
		ck, err := baseSectorKey.CipherKey()
		if err != nil {
			return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to get cipherkey: %w", err)
		}

		_, err = ck.DecryptBytesInPlace(baseSector, 0)
		if err != nil {
			return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to decrypt base sector: %w", err)
		}

		// save the visible-by-default fields of the baseSector's layout
		version, cipherType := layout.Version, layout.CipherType
		var keyData [64]byte
		copy(keyData[:], layout.KeyData[:])

		// decode the now decrypted layout
		layout.Decode(baseSector)
		// reset the visible-by-default fields
		layout.Version = version
		layout.CipherType = cipherType
		layout.KeyData = keyData

		// overwrite the base sector layout with the decrypted layout
		copy(baseSector[:skymodules.SkyfileLayoutSize], layout.Encode())
	}

	// attempt to parse the metadata from the base sector. May return a
	// recursive base sector error.
	_, _, meta, _, payload, err := skymodules.ParseSkyfileMetadata(baseSector)
	if err == nil {
		return meta, payload, nil
	} else if err != nil && !strings.Contains(err.Error(), "can't use skymodules.ParseSkyfileMetadata to parse recursive base sector - use renter.ParseSkyfileMetadata instead") {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to parse base sector: %w", err)
	}

	// Since its a recursive base sector, only parse the layout
	layout := skymodules.ParseSkyfileLayout(baseSector)

	// get the size of the payload and the fanout offset in the metadata file
	payloadSize := layout.FanoutSize + layout.MetadataSize
	translatedOffset, _ := skymodules.TranslateBaseSectorExtensionOffset(0, payloadSize, payloadSize, uint64(sectorSize-skymodules.SkyfileLayoutSize))

	// seek to the start of the JSON payload and parse it
	if _, err := f.Seek(int64(sectorSize+translatedOffset+layout.FanoutSize), io.SeekStart); err != nil {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to seek to metadata pos %v: %w", translatedOffset+layout.FanoutSize, err)
	} else if err := json.NewDecoder(f).Decode(&meta); err != nil {
		return skymodules.SkyfileMetadata{}, nil, fmt.Errorf("failed to decode metadata: %w", err)
	}
	return meta, nil, nil
}

// recoverFiles recovers the files from the metadata and saves them in outputDir
func recoverFiles(r io.ReadSeeker, meta skymodules.SkyfileMetadata, outputDir, algo string) {
	// pipe the -extended data to a hasher to calculate the checksum
	var h hash.Hash
	switch strings.ToLower(algo) {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	case "md5":
		h = md5.New()
	default:
		log.Fatalln("unknown checksum algorithm:", algo)
	}

	tr := io.TeeReader(r, h)
	if len(meta.Subfiles) == 0 {
		log.Println("Found 1 file")
		outPath := filepath.Join(outputDir, meta.Filename)
		if err := writeSubFile(tr, outPath, int64(meta.Length)); err != nil {
			log.Fatalln("failed to write file:", err)
		}
		log.Printf("Recovered file %v (%v/%v) %v bytes %x checksum", meta.Filename, 1, 1, meta.Length, h.Sum(nil))
		return
	}

	log.Printf("Found %v files", len(meta.Subfiles))

	var i int
	n := len(meta.Subfiles)
	for _, subfile := range meta.Subfiles {
		i++
		// reset the hasher
		h.Reset()
		// seek to the file offset in the -extended file
		if _, err := r.Seek(int64(subfile.Offset), io.SeekStart); err != nil {
			log.Fatalln("failed to seek to subfile:", err)
		}
		// write the subfile to disk and calculate its sha256 checksum
		outPath := filepath.Join(outputDir, subfile.Filename)
		if err := writeSubFile(tr, outPath, int64(subfile.Len)); err != nil {
			log.Fatalln("failed to write subfile:", err)
		}
		log.Printf("Recovered file %v (%v/%v) %v bytes %x checksum", subfile.Filename, i, n, subfile.Len, h.Sum(nil))
	}
}

func main() {
	skylink := flag.String("skylink", "", "skylink to get metadata from")
	skykeyPath := flag.String("skynetdir", build.SkynetDir(), "path to skykey directory - default of ~/.skynet on linux")
	basePath := flag.String("base", "", "path to base sector file")
	extendedPath := flag.String("extended", "", "path to extended sector file")
	outputDir := flag.String("output", ".", "output directory")
	checksumAlgo := flag.String("algo", "sha256", "checksum algorithm to use")
	flag.Parse()

	// open the skykey database
	skykeyDB, err := skykey.NewSkykeyManager(*skykeyPath)
	if err != nil {
		log.Fatalln("failed to open skykey database:", err)
	}

	// parse the skyfile metadata from the -base file
	meta, payload, err := parseMetadata(skykeyDB, *skylink, *basePath)
	if err != nil {
		log.Fatalln("failed to parse base sectors:", err)
	}

	// the entire payload is in the base sector, recover files from it
	if uint64(len(payload)) == meta.Length {
		log.Println("base sector contains entire payload")
		recoverFiles(bytes.NewReader(payload), meta, *outputDir, *checksumAlgo)
		return
	}

	// check that the -extended file is the correct size
	stat, err := os.Stat(*extendedPath)
	if err != nil {
		log.Fatalln("failed to stat extended file:", err)
	} else if n := stat.Size(); n != int64(meta.Length) {
		log.Fatalf("extended file is the wrong size, expected %v bytes but got %v bytes", meta.Length, n)
	}

	// open the -extended file
	ef, err := os.Open(*extendedPath)
	if err != nil {
		log.Fatalln("failed to open extended sector:", err)
	}

	// recover the files from the -extended file
	recoverFiles(ef, meta, *outputDir, *checksumAlgo)
}
