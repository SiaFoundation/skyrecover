// Package wallet provides a lite-wallet implementation for the Sia blockchain.
package wallet

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/siacentral/apisdkgo"
	"gitlab.com/NebulousLabs/encoding"
	"go.sia.tech/renterd/wallet"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
)

var siaCentralClient = apisdkgo.NewSiaClient()

type (
	// A SingleAddressWallet is a Siacoin wallet that only uses a single address.
	SingleAddressWallet struct {
		priv ed25519.PrivateKey
		addr types.UnlockHash

		mu   sync.Mutex
		used map[types.SiacoinOutputID]bool
	}

	SiacoinElement struct {
		ID         types.SiacoinOutputID
		Value      types.Currency
		UnlockHash types.UnlockHash
	}
)

// Address returns the wallet's address.
func (sw *SingleAddressWallet) Address() types.UnlockHash {
	return sw.addr
}

// Balance returns the wallet's balance.
func (sw *SingleAddressWallet) Balance() (types.Currency, error) {
	resp, err := siaCentralClient.GetAddressBalance(0, 0, sw.addr.String())
	return resp.UnspentSiacoins, err
}

// SpendableUTXOs returns a list of spendable UTXOs.
func (sw *SingleAddressWallet) SpendableUTXOs() (spendable []SiacoinElement, _ error) {
	tip, err := siaCentralClient.GetChainIndex()
	if err != nil {
		return nil, fmt.Errorf("failed to get consensus state: %w", err)
	}

	resp, err := siaCentralClient.GetAddressBalance(0, 0, sw.addr.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get address balance: %w", err)
	}
	utxos := resp.UnspentSiacoinOutputs
	// choose outputs randomly
	frand.Shuffle(len(utxos), reflect.Swapper(utxos))

	// get unconfirmed spent utxos
	unconfirmedSpent := make(map[types.SiacoinOutputID]bool)
	for _, txn := range resp.UnconfirmedTransactions {
		for _, input := range txn.SiacoinInputs {
			var outputID types.SiacoinOutputID
			if _, err := hex.Decode(outputID[:], []byte(input.OutputID)); err != nil {
				return nil, fmt.Errorf("failed to decode output id: %w", err)
			}
			unconfirmedSpent[outputID] = true
		}
	}

	// check for unused spendable outputs
	sw.mu.Lock()
	defer sw.mu.Unlock()

	for _, utxo := range utxos {
		var outputID types.SiacoinOutputID
		if _, err := hex.Decode(outputID[:], []byte(utxo.OutputID)); err != nil {
			return nil, fmt.Errorf("failed to decode output id: %w", err)
		} else if sw.used[outputID] || unconfirmedSpent[outputID] || utxo.MaturityHeight > tip.Height {
			continue
		}
		spendable = append(spendable, SiacoinElement{
			ID:         outputID,
			Value:      utxo.Value,
			UnlockHash: sw.addr,
		})
	}
	return
}

// FundTransaction adds inputs to txn until it has at least amount siacoins.
func (sw *SingleAddressWallet) FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error) {
	if amount.IsZero() {
		return nil, nil, nil
	}

	utxos, err := sw.SpendableUTXOs()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get spendable utxos: %w", err)
	}

	var outputSum types.Currency
	var toSign []crypto.Hash

	sw.mu.Lock()
	defer sw.mu.Unlock()
	for _, utxo := range utxos {
		if sw.used[utxo.ID] {
			continue
		}

		toSign = append(toSign, crypto.Hash(utxo.ID))
		outputSum = outputSum.Add(utxo.Value)
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID: utxo.ID,
			UnlockConditions: types.UnlockConditions{
				PublicKeys: []types.SiaPublicKey{
					{Algorithm: types.SignatureEd25519, Key: sw.priv.Public().(ed25519.PublicKey)},
				},
				SignaturesRequired: 1,
			},
		})
		if outputSum.Cmp(amount) >= 0 {
			break
		}
	}

	if outputSum.Cmp(amount) < 0 {
		return nil, nil, fmt.Errorf("not enough funds to fund transaction: %v < %v", outputSum, amount)
	} else if outputSum.Cmp(amount) > 0 {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Value:      outputSum.Sub(amount),
			UnlockHash: sw.addr,
		})
	}

	// mark the outputs as spent
	for _, id := range toSign {
		sw.used[types.SiacoinOutputID(id)] = true
	}

	return toSign, func() {
		sw.mu.Lock()
		defer sw.mu.Unlock()
		for _, id := range toSign {
			delete(sw.used, types.SiacoinOutputID(id))
		}
	}, nil
}

// SignTransaction signs txn with the wallet's private key.
func (sw *SingleAddressWallet) SignTransaction(txn *types.Transaction, toSign []crypto.Hash, cf types.CoveredFields) error {
	tip, err := siaCentralClient.GetChainIndex()
	if err != nil {
		return fmt.Errorf("failed to get consensus state: %w", err)
	}
	for _, id := range toSign {
		i := len(txn.TransactionSignatures)
		txn.TransactionSignatures = append(txn.TransactionSignatures, types.TransactionSignature{
			ParentID:       id,
			CoveredFields:  cf,
			PublicKeyIndex: 0,
		})
		sigHash := txn.SigHash(i, types.BlockHeight(tip.Height))
		txn.TransactionSignatures[i].Signature = ed25519.Sign(sw.priv, sigHash[:])
	}
	return nil
}

// Redistribute returns a transaction that redistributes money in the wallet by
// selecting a minimal set of inputs to cover the creation of the requested
// outputs. It also returns a list of output IDs that need to be signed.
//
// NOTE: we can not reuse 'FundTransaction' because it randomizes the unspent
// transaction outputs it uses and we need a minimal set of inputs
func (sw *SingleAddressWallet) Redistribute(outputs uint64, amount types.Currency) (types.Transaction, func(), error) {
	// prepare all outputs
	var txn types.Transaction
	for i := 0; i < int(outputs); i++ {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Value:      amount,
			UnlockHash: sw.Address(),
		})
	}

	utxos, err := sw.SpendableUTXOs()
	if err != nil {
		return types.Transaction{}, nil, fmt.Errorf("failed to get spendable utxos: %w", err)
	}
	// desc sort
	sort.Slice(utxos, func(i, j int) bool {
		return utxos[i].Value.Cmp(utxos[j].Value) > 0
	})

	_, max, err := siaCentralClient.GetTransactionFees()
	if err != nil {
		return types.Transaction{}, nil, fmt.Errorf("failed to get transaction fees: %w", err)
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	outputSum := amount.Mul64(outputs)
	var inputSum types.Currency
	var toSign []crypto.Hash

	// estimate the fees
	transactionFee := max.Mul64(uint64(len(encoding.Marshal(txn.SiacoinOutputs)))).Add(max.Mul64(300 * 15))
	txn.MinerFees = []types.Currency{transactionFee}
	fundAmount := outputSum.Add(transactionFee)

	for _, utxo := range utxos {
		if sw.used[utxo.ID] {
			continue
		}

		toSign = append(toSign, crypto.Hash(utxo.ID))
		inputSum = inputSum.Add(utxo.Value)
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID: utxo.ID,
			UnlockConditions: types.UnlockConditions{
				PublicKeys: []types.SiaPublicKey{
					{Algorithm: types.SignatureEd25519, Key: sw.priv.Public().(ed25519.PublicKey)},
				},
				SignaturesRequired: 1,
			},
		})
		if inputSum.Cmp(fundAmount) >= 0 {
			break
		}
	}

	if inputSum.Cmp(fundAmount) < 0 {
		return types.Transaction{}, nil, fmt.Errorf("not enough funds to fund transaction: %v < %v", inputSum, amount)
	} else if inputSum.Cmp(fundAmount) > 0 {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Value:      inputSum.Sub(fundAmount),
			UnlockHash: sw.addr,
		})
	}

	// sign the transaction
	if err := sw.SignTransaction(&txn, toSign, types.FullCoveredFields); err != nil {
		return types.Transaction{}, nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	for _, id := range toSign {
		sw.used[types.SiacoinOutputID(id)] = true
	}

	return txn, func() {
		for _, id := range toSign {
			delete(sw.used, types.SiacoinOutputID(id))
		}
	}, nil
}

// ExplicitCoveredFields returns a CoveredFields that covers all elements
// present in txn.
func ExplicitCoveredFields(txn types.Transaction) (cf types.CoveredFields) {
	for i := range txn.SiacoinInputs {
		cf.SiacoinInputs = append(cf.SiacoinInputs, uint64(i))
	}
	for i := range txn.SiacoinOutputs {
		cf.SiacoinOutputs = append(cf.SiacoinOutputs, uint64(i))
	}
	for i := range txn.FileContracts {
		cf.FileContracts = append(cf.FileContracts, uint64(i))
	}
	for i := range txn.FileContractRevisions {
		cf.FileContractRevisions = append(cf.FileContractRevisions, uint64(i))
	}
	for i := range txn.StorageProofs {
		cf.StorageProofs = append(cf.StorageProofs, uint64(i))
	}
	for i := range txn.SiafundInputs {
		cf.SiafundInputs = append(cf.SiafundInputs, uint64(i))
	}
	for i := range txn.SiafundOutputs {
		cf.SiafundOutputs = append(cf.SiafundOutputs, uint64(i))
	}
	for i := range txn.MinerFees {
		cf.MinerFees = append(cf.MinerFees, uint64(i))
	}
	for i := range txn.ArbitraryData {
		cf.ArbitraryData = append(cf.ArbitraryData, uint64(i))
	}
	for i := range txn.TransactionSignatures {
		cf.TransactionSignatures = append(cf.TransactionSignatures, uint64(i))
	}
	return
}

// New initializes a new SingleAddressWallet.
func New(recoveryPhrase string) (*SingleAddressWallet, error) {
	key, err := wallet.KeyFromPhrase(recoveryPhrase)
	if err != nil {
		return nil, fmt.Errorf("failed to create seed: %w", err)
	}
	return &SingleAddressWallet{
		priv: ed25519.PrivateKey(key),
		addr: wallet.StandardAddress(key.PublicKey()),
		used: make(map[types.SiacoinOutputID]bool),
	}, nil
}
