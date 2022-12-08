package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

var (
	dataDir string

	contractDownloadSize uint64 = 1 << 30 // 1 GiB of downloaded data
	contractDuration     uint64 = 144 * 7 // 1 week

	rootCmd = &cobra.Command{
		Use:   "healthcheck",
		Short: "",
		Run:   func(cmd *cobra.Command, args []string) {},
	}
)

func init() {
	log.SetFlags(0)

	defaultDataDir := "."
	switch runtime.GOOS {
	case "windows":
		defaultDataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "renterc")
	case "darwin":
		defaultDataDir = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "renterc")
	default:
		defaultDataDir = filepath.Join(os.Getenv("HOME"), ".local/renterc")
	}

	contractsFormCmd.Flags().Uint64Var(&contractDownloadSize, "download-size", contractDownloadSize, "contract download size")
	contractsFormCmd.Flags().Uint64Var(&contractDuration, "duration", contractDuration, "contract duration")

	contractsCmd.AddCommand(contractsFormCmd)
	walletCmd.AddCommand(walletDistributeCmd)

	rootCmd.AddCommand(walletCmd, contractsCmd, healthCheckCmd)

	rootCmd.PersistentFlags().StringVarP(&dataDir, "dir", "d", defaultDataDir, "data directory")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalln(err)
	}
}
