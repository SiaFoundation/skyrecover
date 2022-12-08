package main

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/siacentral/apisdkgo"
	"github.com/spf13/cobra"
	"go.sia.tech/siad/types"
	"go.sia.tech/skyrecover/internal/wallet"
)

var (
	walletCmd = &cobra.Command{
		Use:   "wallet",
		Short: "get the wallet address and balance",
		Run: func(cmd *cobra.Command, args []string) {
			w := mustLoadWallet()
			balance, err := w.Balance()
			if err != nil {
				log.Fatalln("failed to get wallet balance:", err)
			}

			log.Println("Wallet Address:", w.Address())
			log.Println("Wallet Balance:", balance.HumanString())
		},
	}

	walletDistributeCmd = &cobra.Command{
		Use:   "redistribute <number of outputs> <output amount>",
		Short: "redistributes UTXOs to better form contracts",
		Run: func(cmd *cobra.Command, args []string) {
			if len(args) != 2 {
				cmd.Usage()
				os.Exit(1)
			}

			count, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				log.Fatalln("failed to parse output count:", err)
			}
			hastings, err := types.ParseCurrency(args[1])
			if err != nil {
				log.Fatalln("failed to parse output hastings:", err)
			}
			var outputAmount types.Currency
			if _, err := fmt.Sscan(hastings, &outputAmount); err != nil {
				log.Fatalln("failed to parse output amount:", err)
			}

			w := mustLoadWallet()
			balance, err := w.Balance()
			if err != nil {
				log.Fatalln("failed to get wallet balance:", err)
			}

			n, err := balance.Div(outputAmount).Uint64()
			if err != nil {
				log.Fatalln("failed to get number of outputs:", err)
			} else if n < count {
				log.Println("not enough funds to redistribute")
			}

			txn, release, err := w.Redistribute(count, outputAmount)
			if err != nil {
				log.Fatalln("failed to redistribute funds:", err)
			}
			defer release()

			log.Printf("Creating %v outputs of %v each", count, outputAmount.HumanString())
			siaCentralClient := apisdkgo.NewSiaClient()
			if err := siaCentralClient.BroadcastTransactionSet([]types.Transaction{txn}); err != nil {
				log.Fatalln("failed to broadcast transaction:", err)
			}
			log.Printf("Transaction %v broadcast", txn.ID())
		},
	}
)

func mustLoadWallet() *wallet.SingleAddressWallet {
	recoveryPhrase := os.Getenv("RECOVERY_PHRASE")
	if recoveryPhrase == "" {
		log.Fatalln("RECOVERY_PHRASE environment variable not set")
	}
	wallet, err := wallet.New(recoveryPhrase)
	if err != nil {
		log.Fatalln("failed to initialize wallet:", err)
	}
	return wallet
}
