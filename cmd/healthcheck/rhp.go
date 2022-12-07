package main

import (
	"context"
	"fmt"
	"net"

	rhpv2 "go.sia.tech/skyrecover/internal/rhp/v2"
)

// dialTransport is a convenience function that connects to the specified host
func dialTransport(ctx context.Context, hostIP string, hostKey rhpv2.PublicKey) (_ *rhpv2.Transport, err error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", hostIP)
	if err != nil {
		return nil, fmt.Errorf("failed to dial host: %w", err)
	}
	t, err := rhpv2.NewRenterTransport(conn, hostKey)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return t, nil
}
