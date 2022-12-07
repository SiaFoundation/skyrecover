package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/siacentral/apisdkgo"
	"go.sia.tech/renterd/wallet"
	"go.sia.tech/siad/types"
	rhpv2 "go.sia.tech/skyrecover/internal/rhp/v2"
)

type (
	saveMeta struct {
		RenterKey rhpv2.PrivateKey `json:"renterKey"`
		Contracts []contractMeta   `json:"contracts"`
	}

	contractMeta struct {
		ID               types.FileContractID `json:"id"`
		HostKey          rhpv2.PublicKey      `json:"hostKey"`
		ExpirationHeight uint64               `json:"expirationHeight"`
	}

	// A renter is a helper type that manages the formation of contracts and rhp
	// sessions. It is not safe for concurrent use.
	renter struct {
		renterKey rhpv2.PrivateKey

		dir       string
		contracts map[rhpv2.PublicKey]contractMeta
		w         *singleAddressWallet
	}
)

func (r *renter) formDownloadContract(hostKey rhpv2.PublicKey, downloadAmount, duration uint64) (contractMeta, error) {
	siacentralClient := apisdkgo.NewSiaClient()
	block, err := siacentralClient.GetLatestBlock()
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to get latest block: %w", err)
	}
	host, err := siacentralClient.GetHost(hostKey.String())
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to get host: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t, err := dialTransport(ctx, host.NetAddress, hostKey)
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to dial host: %w", err)
	}
	defer t.Close()

	settings, err := rhpv2.RPCSettings(ctx, t)
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to get host settings: %w", err)
	}

	// estimate the funding required to download the data
	sectorAccesses := downloadAmount / rhpv2.SectorSize
	fundAmount := settings.DownloadBandwidthPrice.Mul64(downloadAmount).Add(settings.SectorAccessPrice.Mul64(sectorAccesses + 1))
	// create the contract
	contract := rhpv2.PrepareContractFormation(r.renterKey, hostKey, fundAmount, types.ZeroCurrency, block.Height+duration, settings, r.w.Address())
	// estimate miner fee
	_, max, err := siacentralClient.GetTransactionFees()
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to get transaction fees: %w", err)
	}
	fee := max.Mul64(1200)
	formationCost := rhpv2.ContractFormationCost(contract, settings.ContractPrice)
	// fund and sign the formation transaction
	formationTxn := types.Transaction{
		MinerFees:     []types.Currency{fee},
		FileContracts: []types.FileContract{contract},
	}
	toSign, release, err := r.w.FundTransaction(&formationTxn, formationCost.Add(fee))
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to fund transaction: %w", err)
	}
	defer release()
	if err := r.w.SignTransaction(&formationTxn, toSign, wallet.ExplicitCoveredFields(formationTxn)); err != nil {
		return contractMeta{}, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// send the contract to the host
	var blockID rhpv2.BlockID
	if n, err := hex.Decode(blockID[:], []byte(block.ID)); err != nil {
		return contractMeta{}, fmt.Errorf("failed to decode block id: %w", err)
	} else if n != 32 {
		return contractMeta{}, fmt.Errorf("invalid block id length: %d", n)
	}
	tip := rhpv2.ConsensusState{
		Index: rhpv2.ChainIndex{
			Height: block.Height,
			ID:     blockID,
		},
	}
	renterContract, _, err := rhpv2.RPCFormContract(t, tip, r.renterKey, hostKey, []types.Transaction{formationTxn})
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to form contract: %w", err)
	}
	return contractMeta{
		ID:               renterContract.ID(),
		HostKey:          hostKey,
		ExpirationHeight: uint64(renterContract.Revision.NewWindowStart) - 5,
	}, nil
}

func (r *renter) getOrFormContract(hostID rhpv2.PublicKey) (contractMeta, error) {
	siaCentralClient := apisdkgo.NewSiaClient()
	block, err := siaCentralClient.GetLatestBlock()
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to get latest block: %w", err)
	}
	meta, ok := r.contracts[hostID]
	if ok && meta.ExpirationHeight > block.Height {
		return meta, nil
	}
	// form a contract able to download 100GB of data
	contract, err := r.formDownloadContract(hostID, 100*(1<<30), 144*14)
	if err != nil {
		return contractMeta{}, fmt.Errorf("failed to form contract: %w", err)
	}
	r.contracts[hostID] = contract
	if err := r.save(); err != nil {
		return contractMeta{}, fmt.Errorf("failed to save contracts: %w", err)
	}
	return contract, nil
}

func (r *renter) save() error {
	if err := os.MkdirAll(r.dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	meta := saveMeta{
		RenterKey: r.renterKey,
		Contracts: make([]contractMeta, 0, len(r.contracts)),
	}
	for _, contract := range r.contracts {
		meta.Contracts = append(meta.Contracts, contract)
	}

	tmpFile := filepath.Join(r.dir, "contracts.json.tmp")
	outputFile := filepath.Join(r.dir, "contracts.json")
	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to open contracts file: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return fmt.Errorf("failed to encode contracts: %w", err)
	}
	// sync and automically replace the old file
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync contracts file: %w", err)
	} else if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close contracts file: %w", err)
	} else if err := os.Rename(tmpFile, outputFile); err != nil {
		return fmt.Errorf("failed to rename contracts file: %w", err)
	}
	return nil
}

func (r *renter) load() error {
	inputFile := filepath.Join(r.dir, "contracts.json")
	f, err := os.Open(inputFile)
	if err != nil {
		return fmt.Errorf("failed to open contracts file: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var meta saveMeta
	if err := dec.Decode(&meta); err != nil {
		return fmt.Errorf("failed to decode contracts: %w", err)
	}
	r.renterKey = meta.RenterKey
	r.contracts = make(map[rhpv2.PublicKey]contractMeta)
	for _, contract := range meta.Contracts {
		r.contracts[contract.HostKey] = contract
	}
	return nil
}

// VerifySector verifies that a sector is stored on a host.
func (r *renter) VerifySector(merkleRoot rhpv2.Hash256, hostPub rhpv2.PublicKey) error {
	// get an existing contract or form a new one
	contract, err := r.getOrFormContract(hostPub)
	if err != nil {
		return fmt.Errorf("failed to get contract: %w", err)
	}

	// get the host's net address
	siaCentralClient := apisdkgo.NewSiaClient()
	host, err := siaCentralClient.GetHost(contract.HostKey.String())
	if err != nil {
		return fmt.Errorf("failed to get host: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// start an RHPv2 session
	sess, err := rhpv2.DialSession(ctx, host.NetAddress, contract.HostKey, contract.ID, r.renterKey)
	if err != nil {
		return fmt.Errorf("failed to create transport: %w", err)
	}
	defer sess.Close()

	// get the host's current settings
	settings, err := rhpv2.RPCSettings(ctx, sess.Transport())
	if err != nil {
		return fmt.Errorf("failed to get settings: %w", err)
	}

	// read the sector
	var buf bytes.Buffer
	sections := []rhpv2.RPCReadRequestSection{
		{MerkleRoot: merkleRoot, Offset: 0, Length: rhpv2.SectorSize},
	}
	cost := rhpv2.RPCReadCost(settings, sections)
	if err := sess.Read(ctx, &buf, sections, cost); err != nil {
		return fmt.Errorf("failed to read sector: %w", err)
	}

	// verify the downloaded data matches the merkle root
	root := rhpv2.SectorRoot((*[rhpv2.SectorSize]byte)(buf.Bytes()))
	if root != merkleRoot {
		return fmt.Errorf("sector root mismatch: %v != %v", root, merkleRoot)
	}
	return nil
}

func newRenter(dir string, w *singleAddressWallet) (*renter, error) {
	r := &renter{
		dir: dir,

		renterKey: rhpv2.GeneratePrivateKey(),
		contracts: make(map[rhpv2.PublicKey]contractMeta),

		w: w,
	}
	// renter key and contracts will be overwritten if the file exists
	if err := r.load(); !errors.Is(err, os.ErrNotExist) && err != nil {
		return nil, fmt.Errorf("failed to load contracts: %w", err)
	}
	return r, nil
}
