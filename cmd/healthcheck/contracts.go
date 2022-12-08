package main

import (
	"log"

	"github.com/siacentral/apisdkgo"
	"github.com/siacentral/apisdkgo/sia"
	"github.com/spf13/cobra"
	"go.sia.tech/siad/types"
	"go.sia.tech/skyrecover/internal/renter"
	rhpv2 "go.sia.tech/skyrecover/internal/rhp/v2"
)

var (
	contractsCmd = &cobra.Command{
		Use:   "contracts",
		Short: "get a list of contracts the renter has formed",
		Run:   func(cmd *cobra.Command, args []string) {},
	}

	contractsFormCmd = &cobra.Command{
		Use:   "form",
		Short: "form contracts with all available hosts, hosts that are already contracted with will be skipped",
		Run: func(cmd *cobra.Command, args []string) {
			w := mustLoadWallet()
			r, err := renter.New(dataDir)
			if err != nil {
				log.Fatalln("failed to initialize contractor:", err)
			}

			siaCentralClient := apisdkgo.NewSiaClient()
			filter := make(sia.HostFilter)
			filter.WithAcceptingContracts(true)
			filter.WithMinUptime(0.75)
			filter.WithMaxContractPrice(types.SiacoinPrecision.Div64(2))

			// check for contracts with all potential hosts and form new contracts if
			// necessary
			for i := 0; true; i++ {
				hosts, err := siaCentralClient.GetActiveHosts(filter, i, 500)
				if err != nil {
					log.Fatalln("failed to get active hosts:", err)
				}

				if len(hosts) == 0 {
					break
				}

				for i, host := range hosts {
					log.Printf("Updating contract with host %v %v (%v/%v)", host.PublicKey, host.NetAddress, i, len(hosts))
					var hostPub rhpv2.PublicKey
					if err := hostPub.UnmarshalText([]byte(host.PublicKey)); err != nil {
						log.Fatalf("failed to decode host key %v: %v", host.PublicKey, err)
					}

					// if a contract already exists, skip
					if _, err := r.HostContract(hostPub); err == nil {
						continue
					}

					if _, err := r.FormDownloadContract(hostPub, 10*(1<<30), 144*14, w); err != nil {
						log.Println(" WARNING: failed to update contract:", err)
					}
				}
			}
		},
	}
)
