package main

import (
	"context"
	"log"
	"strings"
	"sync"

	"go.sia.tech/siad/crypto"
	"go.sia.tech/skyrecover/internal/renter"
	"go.sia.tech/skyrecover/internal/rhp/v2"
)

type (
	work struct {
		SectorRoot crypto.Hash
		HostKey    rhp.PublicKey
	}

	result struct {
		SectorRoot crypto.Hash
		HostKey    rhp.PublicKey
		Err        error
		Data       []byte
	}
)

var (
	workers int
)

func downloadWorker(ctx context.Context, r *renter.Renter, workChan <-chan work, resultsChan chan<- result) {
	for {
		select {
		case <-ctx.Done():
			return
		case piece, ok := <-workChan:
			if !ok {
				return // work chan is closed and empty, stop the worker
			}
			buf, err := downloadSector(r, piece.HostKey, piece.SectorRoot)
			resultsChan <- result{
				SectorRoot: piece.SectorRoot,
				HostKey:    piece.HostKey,
				Err:        err,
				Data:       buf,
			}
		}
	}

}

func recoverSector(ctx context.Context, r *renter.Renter, sector crypto.Hash, workers int) ([]byte, bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workChan := make(chan work, workers)
	resultsChan := make(chan result, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			downloadWorker(ctx, r, workChan, resultsChan)
			wg.Done()
		}()
	}

	go func() {
		wg.Wait()          // wait for all workers to finish
		close(resultsChan) // close the results chan to signal to break out of the loop
	}()

	go func() {
		availableHosts := r.Hosts()
		log.Printf("Checking %v hosts for sector %v", len(availableHosts), sector.String())
		for _, host := range availableHosts {
			select {
			case <-ctx.Done():
				return
			default:
			}
			workChan <- work{
				SectorRoot: sector,
				HostKey:    host,
			}
		}
		// close the work chan to signal that no more hosts are available
		close(workChan)
	}()

	for result := range resultsChan {
		switch {
		case result.Err == nil: // sector has been recovered
			// cancel the context to stop the workers
			cancel()
			return result.Data, true
		case strings.Contains(result.Err.Error(), "could not find the desired sector"): // host does not have the sector, try another host
			continue
		case strings.Contains(result.Err.Error(), "no record of that contract"): // sync issue -- host is missing contract, remove host from available hosts
			// remove the host from the list of available hosts
			r.RemoveHostContract(result.HostKey)
			log.Printf("[WARN] removed host %v from available hosts: contract not found -- form new contract", result.HostKey)
		}
	}
	return nil, false
}
