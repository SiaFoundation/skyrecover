package main

import (
	"log"

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
		Short: "get a list of contracts the renter has formed",
		Run:   func(cmd *cobra.Command, args []string) {},
	}

	contractsFormCmd = &cobra.Command{
		Use:   "form [host keys]...",
		Short: "form contracts with hosts. If no hosts are specified, contracts will be formed with all hosts that meet the criteria",
		Run: func(cmd *cobra.Command, args []string) {
			w := mustLoadWallet()
			r, err := renter.New(dataDir)
			if err != nil {
				log.Fatalln("failed to initialize contractor:", err)
			}

			var hosts []rhp.PublicKey
			if len(args) == 0 {
				siaCentralClient := apisdkgo.NewSiaClient()
				filter := make(sia.HostFilter)
				filter.WithAcceptingContracts(true)
				filter.WithMinUptime(0.75)
				filter.WithMaxContractPrice(types.SiacoinPrecision.Div64(2))

				for i := 0; true; i++ {
					activeHosts, err := siaCentralClient.GetActiveHosts(filter, i, 500)
					if err != nil {
						log.Fatalln("failed to get active hosts:", err)
					} else if len(activeHosts) == 0 {
						break
					}

					for _, host := range activeHosts {
						var hostPub rhp.PublicKey
						if err := hostPub.UnmarshalText([]byte(host.PublicKey)); err != nil {
							log.Fatalln("failed to unmarshal host public key:", err)
						}
						hosts = append(hosts, hostPub)
					}
				}
			}

			for _, key := range args {
				var hostPub rhp.PublicKey
				if err := hostPub.UnmarshalText([]byte(key)); err != nil {
					log.Fatalln("failed to unmarshal host public key:", err)
				}
				hosts = append(hosts, hostPub)
			}

			for i, hostPub := range hosts {
				log.Printf("Forming contract with host %v (%v/%v)", hostPub, i+1, len(hosts))
				// if a contract already exists, skip
				if _, err := r.HostContract(hostPub); err == nil && !force {
					continue
				}

				if _, err := r.FormDownloadContract(hostPub, 10*(1<<30), 144*14, w); err != nil {
					log.Println(" WARNING: failed to update contract:", err)
				}
			}
		},
	}
)
