package main

import (
	"log"
	"os"
	"time"

	"github.com/rodaine/table"
	"github.com/siacentral/apisdkgo"
	"github.com/siacentral/apisdkgo/sia"
	"github.com/spf13/cobra"
	"go.sia.tech/siad/types"
	"go.sia.tech/skyrecover/internal/renter"
	"go.sia.tech/skyrecover/internal/rhp/v2"
)

var (
	contractsCmd = &cobra.Command{
		Use:   "contracts",
		Short: "list current contracts",
		Run: func(cmd *cobra.Command, args []string) {
			r, err := renter.New(dataDir)
			if err != nil {
				log.Fatalln("failed to initialize renter:", err)
			}

			tbl := table.New("Host Key", "Contract ID", "Expiration Height")
			for _, contract := range r.Contracts() {
				tbl.AddRow(contract.HostKey, contract.ID, contract.ExpirationHeight)
			}
			tbl.Print()
		},
	}

	contractsHostsCmd = &cobra.Command{
		Use:   "hosts",
		Short: "get a list of contracts the renter has formed",
		Run: func(cmd *cobra.Command, args []string) {
			siaCentralClient := apisdkgo.NewSiaClient()
			filter := make(sia.HostFilter)
			filter.WithAcceptingContracts(true)
			filter.WithMinUptime(0.6)
			filter.WithMaxContractPrice(types.SiacoinPrecision.Div64(2))

			tbl := table.New("Public Key", "Net Address", "Last Seen")

			for i := 0; true; i++ {
				activeHosts, err := siaCentralClient.GetActiveHosts(filter, i, 500)
				if err != nil {
					log.Fatalln("failed to get active hosts:", err)
				} else if len(activeHosts) == 0 {
					break
				}

				for _, host := range activeHosts {
					tbl.AddRow(host.PublicKey, host.NetAddress, host.LastSuccessScan.Format(time.RFC1123))
				}
			}

			tbl.Print()
		},
	}

	contractsFormCmd = &cobra.Command{
		Use:   "form <host key>...",
		Short: "form contracts with hosts.",
		Run: func(cmd *cobra.Command, args []string) {
			w := mustLoadWallet()
			r, err := renter.New(dataDir)
			if err != nil {
				log.Fatalln("failed to initialize contractor:", err)
			} else if len(args) == 0 {
				cmd.Usage()
				os.Exit(1)
			}

			var hosts []rhp.PublicKey
			for _, key := range args {
				var hostPub rhp.PublicKey
				if err := hostPub.UnmarshalText([]byte(key)); err != nil {
					log.Fatalln("failed to unmarshal host public key:", err)
				}
				hosts = append(hosts, hostPub)
			}

			for i, hostPub := range hosts {
				// if a contract already exists, skip
				if _, err := r.HostContract(hostPub); err == nil && !force {
					log.Printf("Skipping host %v, contract exists", hostPub)
					continue
				}
				log.Printf("Forming contract with host %v (%v/%v)", hostPub, i+1, len(hosts))

				if _, err := r.FormDownloadContract(hostPub, 10*(1<<30), 144*14, w); err != nil {
					log.Println(" WARNING: failed to update contract:", err)
				}
			}
		},
	}
)
