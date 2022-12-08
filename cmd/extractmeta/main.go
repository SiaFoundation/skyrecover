package main

import (
	"encoding/json"
	"log"
	"os"

	"go.sia.tech/skyrecover/internal/siafile"
)

func main() {
	sf, err := siafile.Load(os.Args[1])
	if err != nil {
		log.Fatalln("failed to parse skyfile:", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sf); err != nil {
		log.Fatalln("failed to encode skyfile:", err)
	}

}
