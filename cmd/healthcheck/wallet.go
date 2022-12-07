package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"reflect"
	"sync"

	"github.com/siacentral/apisdkgo"
	"go.sia.tech/renterd/wallet"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
	"lukechampine.com/frand"
)

var siaCentralClient = apisdkgo.NewSiaClient()

type (
	singleAddressWallet struct {
		priv ed25519.PrivateKey
		addr types.UnlockHash

		mu   sync.Mutex
		used map[types.SiacoinOutputID]bool
	}
)

func (w *singleAddressWallet) Address() types.UnlockHash {
	return w.addr
}

func (w *singleAddressWallet) Balance() (types.Currency, error) {
	resp, err := siaCentralClient.GetAddressBalance(0, 0, w.addr.String())
	return resp.UnspentSiacoins, err
}

func (w *singleAddressWallet) FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, func(), error) {
	if amount.IsZero() {
		return nil, nil, nil
	}

	block, err := siaCentralClient.GetLatestBlock()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get consensus state: %w", err)
	}

	resp, err := siaCentralClient.GetAddressBalance(0, 0, w.addr.String())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get address balance: %w", err)
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
				return nil, nil, fmt.Errorf("failed to decode output id: %w", err)
			}
			unconfirmedSpent[outputID] = true
		}
	}

	// lock the used mutex
	w.mu.Lock()
	defer w.mu.Unlock()

	var outputSum types.Currency
	var toSign []crypto.Hash
	for _, utxo := range utxos {
		var outputID types.SiacoinOutputID
		if _, err := hex.Decode(outputID[:], []byte(utxo.OutputID)); err != nil {
			return nil, nil, fmt.Errorf("failed to decode output id: %w", err)
		} else if w.used[outputID] || unconfirmedSpent[outputID] || utxo.MaturityHeight > block.Height {
			continue
		}

		w.used[outputID] = true
		toSign = append(toSign, crypto.Hash(outputID))
		outputSum = outputSum.Add(utxo.Value)
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID: outputID,
			UnlockConditions: types.UnlockConditions{
				PublicKeys: []types.SiaPublicKey{
					{Algorithm: types.SignatureEd25519, Key: w.priv.Public().(ed25519.PublicKey)},
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
			UnlockHash: w.addr,
		})
	}
	return toSign, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		for _, id := range toSign {
			delete(w.used, types.SiacoinOutputID(id))
		}
	}, nil
}

func (sw *singleAddressWallet) SignTransaction(txn *types.Transaction, toSign []crypto.Hash, cf types.CoveredFields) error {
	block, err := siaCentralClient.GetLatestBlock()
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
		sigHash := txn.SigHash(i, types.BlockHeight(block.Height))
		txn.TransactionSignatures[i].Signature = ed25519.Sign(sw.priv, sigHash[:])
	}
	return nil
}

func initWallet(recoveryPhrase string) (*singleAddressWallet, error) {
	key, err := wallet.KeyFromPhrase(recoveryPhrase)
	if err != nil {
		return nil, fmt.Errorf("failed to create seed: %w", err)
	}
	return &singleAddressWallet{
		priv: ed25519.PrivateKey(key),
		addr: wallet.StandardAddress(key.PublicKey()),
		used: make(map[types.SiacoinOutputID]bool),
	}, nil
}
