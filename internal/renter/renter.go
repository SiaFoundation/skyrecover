package renter

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/siacentral/apisdkgo"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
	"go.sia.tech/skyrecover/internal/rhp/v2"
	"go.sia.tech/skyrecover/internal/wallet"
)

type (
	saveMeta struct {
		RenterKey rhp.PrivateKey `json:"renterKey"`
		Contracts []ContractMeta `json:"contracts"`
	}

	ContractMeta struct {
		ID               types.FileContractID `json:"id"`
		HostKey          rhp.PublicKey        `json:"hostKey"`
		ExpirationHeight uint64               `json:"expirationHeight"`
	}

	Wallet interface {
		Address() types.UnlockHash
		FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error)
		SignTransaction(txn *types.Transaction, toSign []crypto.Hash, cf types.CoveredFields) error
	}

	// A Renter is a helper type that manages the formation of contracts and rhp
	// sessions.
	Renter struct {
		renterKey rhp.PrivateKey
		dir       string

		mu        sync.Mutex
		contracts map[rhp.PublicKey]ContractMeta
	}
)

var (
	ErrNoContract = errors.New("no contract formed")
)

func (r *Renter) FormDownloadContract(hostKey rhp.PublicKey, downloadAmount, duration uint64, w Wallet) (ContractMeta, error) {
	siacentralClient := apisdkgo.NewSiaClient()
	block, err := siacentralClient.GetLatestBlock()
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to get latest block: %w", err)
	}
	host, err := siacentralClient.GetHost(hostKey.String())
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to get host: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	t, err := dialTransport(ctx, host.NetAddress, hostKey)
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to dial host: %w", err)
	}
	defer t.Close()

	settings, err := rhp.RPCSettings(ctx, t)
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to get host settings: %w", err)
	}

	// estimate the funding required to download the data
	sectorAccesses := downloadAmount / rhp.SectorSize
	fundAmount := settings.DownloadBandwidthPrice.Mul64(downloadAmount).Add(settings.SectorAccessPrice.Mul64(sectorAccesses + 1))
	// create the contract
	contract := rhp.PrepareContractFormation(r.renterKey, hostKey, fundAmount, types.ZeroCurrency, block.Height+duration, settings, w.Address())
	// estimate miner fee
	_, max, err := siacentralClient.GetTransactionFees()
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to get transaction fees: %w", err)
	}
	fee := max.Mul64(1200)
	formationCost := rhp.ContractFormationCost(contract, settings.ContractPrice)
	// fund and sign the formation transaction
	formationTxn := types.Transaction{
		MinerFees:     []types.Currency{fee},
		FileContracts: []types.FileContract{contract},
	}
	toSign, release, err := w.FundTransaction(&formationTxn, formationCost.Add(fee))
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to fund transaction: %w", err)
	}
	defer release()
	if err := w.SignTransaction(&formationTxn, toSign, wallet.ExplicitCoveredFields(formationTxn)); err != nil {
		return ContractMeta{}, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// send the contract to the host
	var blockID rhp.BlockID
	if n, err := hex.Decode(blockID[:], []byte(block.ID)); err != nil {
		return ContractMeta{}, fmt.Errorf("failed to decode block id: %w", err)
	} else if n != 32 {
		return ContractMeta{}, fmt.Errorf("invalid block id length: %d", n)
	}
	tip := rhp.ConsensusState{
		Index: rhp.ChainIndex{
			Height: block.Height,
			ID:     blockID,
		},
	}
	renterContract, _, err := rhp.RPCFormContract(ctx, t, tip, r.renterKey, hostKey, []types.Transaction{formationTxn})
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to form contract: %w", err)
	}
	meta := ContractMeta{
		ID:               renterContract.ID(),
		HostKey:          hostKey,
		ExpirationHeight: uint64(renterContract.Revision.NewWindowStart) - 5,
	}
	r.mu.Lock()
	r.contracts[hostKey] = meta
	r.mu.Unlock()
	return meta, r.save()
}

func (r *Renter) save() error {
	if err := os.MkdirAll(r.dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	meta := saveMeta{
		RenterKey: r.renterKey,
		Contracts: make([]ContractMeta, 0, len(r.contracts)),
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

func (r *Renter) load() error {
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
	r.contracts = make(map[rhp.PublicKey]ContractMeta)
	for _, contract := range meta.Contracts {
		r.contracts[contract.HostKey] = contract
	}
	return nil
}

func (r *Renter) HostContract(hostID rhp.PublicKey) (ContractMeta, error) {
	siaCentralClient := apisdkgo.NewSiaClient()
	block, err := siaCentralClient.GetLatestBlock()
	if err != nil {
		return ContractMeta{}, fmt.Errorf("failed to get latest block: %w", err)
	}

	r.mu.Lock()
	meta, ok := r.contracts[hostID]
	r.mu.Unlock()
	// check that a contract exists and has not expired
	if !ok || meta.ExpirationHeight <= block.Height {
		return ContractMeta{}, ErrNoContract
	}
	return meta, nil
}

func (r *Renter) Hosts() ([]rhp.PublicKey, error) {
	siaCentralClient := apisdkgo.NewSiaClient()
	block, err := siaCentralClient.GetLatestBlock()
	if err != nil {
		return nil, fmt.Errorf("failed to get latest block: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	var hosts []rhp.PublicKey
	for _, meta := range r.contracts {
		if meta.ExpirationHeight > block.Height {
			hosts = append(hosts, meta.HostKey)
		}
	}
	return hosts, nil
}

func (r *Renter) Contracts() (contracts []ContractMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, meta := range r.contracts {
		contracts = append(contracts, meta)
	}
	return contracts
}

// NewSession initializes a new rhp session with the given host and locks the
// contract.
func (r *Renter) NewSession(ctx context.Context, hostPub rhp.PublicKey) (*rhp.Session, error) {
	// get an existing contract or form a new one
	contract, err := r.HostContract(hostPub)
	if err != nil {
		return nil, fmt.Errorf("failed to get contract: %w", err)
	}

	// get the host's net address
	siaCentralClient := apisdkgo.NewSiaClient()
	host, err := siaCentralClient.GetHost(contract.HostKey.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}

	// start an rhp session
	return rhp.DialSession(ctx, host.NetAddress, contract.HostKey, contract.ID, r.renterKey)
}

func New(dir string) (*Renter, error) {
	r := &Renter{
		renterKey: rhp.GeneratePrivateKey(),
		dir:       dir,

		contracts: make(map[rhp.PublicKey]ContractMeta),
	}
	// renter key and contracts will be overwritten if the file exists
	if err := r.load(); !errors.Is(err, os.ErrNotExist) && err != nil {
		return nil, fmt.Errorf("failed to load contracts: %w", err)
	}
	return r, nil
}
