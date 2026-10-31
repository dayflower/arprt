package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Listener serves the operator UI/API independently of the device listener.
// The controller only registers it when the configured address is not "0".
type Listener struct {
	Address string
	Handler http.Handler
}

func (l *Listener) NeedLeaderElection() bool { return false }

func (l *Listener) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", l.Address)
	if err != nil {
		return fmt.Errorf("dashboard listener: %w", err)
	}
	return l.serve(ctx, listener)
}

func (l *Listener) serve(ctx context.Context, listener net.Listener) error {
	srv := &http.Server{
		Handler:           l.Handler,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("dashboard listener: %w", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return fmt.Errorf("shutdown dashboard listener: %w", err)
		}
		return nil
	}
}
