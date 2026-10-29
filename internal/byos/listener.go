package byos

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Listener integrates serving and graceful shutdown with the manager lifecycle.
type Listener struct {
	Address string
	Handler http.Handler
}

func (l *Listener) NeedLeaderElection() bool { return false }
func (l *Listener) Start(ctx context.Context) error {
	if s, ok := l.Handler.(*Server); ok {
		s.GET.Context = ctx
	}
	listener, err := net.Listen("tcp", l.Address)
	if err != nil {
		return err
	}
	return l.serve(ctx, listener)
}

func (l *Listener) httpServer(ctx context.Context) *http.Server {
	return &http.Server{
		Handler:           l.Handler,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

func (l *Listener) serve(ctx context.Context, listener net.Listener) error {
	srv := l.httpServer(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("BYOS listener: %w", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return err
		}
		return nil
	}
}
