package imageprocessing

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const ProcessingTimeout = 10 * time.Second

// Admission is a shared memory/CPU gate. Its zero value is ready for use.
// Runtime wiring must share one instance across reception, Job collection,
// overlays and buffered image serving. Acquire before sending HTTP or opening
// image files; never acquire while holding repository/publication locks.
type Admission struct {
	once sync.Once
	slot chan struct{}
}

// Do holds capacity until synchronous work has stopped, including publication
// and release of owned image buffers. Waiting callers must retain only metadata.
// Callbacks must not launch image work that outlives their return.
func (a *Admission) Do(ctx context.Context, work func(context.Context) error) error {
	a.once.Do(func() { a.slot = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case a.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-a.slot }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return work(ctx)
}

// Process starts the processing deadline after reception/collection, while the
// admission callback continues to hold capacity. Enclosing deadlines win. All
// work is synchronous; cancellation never abandons a running worker goroutine.
func Process(ctx context.Context, work func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, ProcessingTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := work(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

// ReadInput uses one capped allocation instead of io.ReadAll's growing copies.
// It must run inside admission. The entire file, including ignored chunks, is
// limited; exact-limit input is allowed and an extra byte is rejected.
func ReadInput(ctx context.Context, r io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := make([]byte, MaxInputBytes)
	n, err := io.ReadFull(contextReader{ctx, r}, b)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return b[:n], ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	var extra [1]byte
	n, err = io.ReadFull(contextReader{ctx, r}, extra[:])
	if n != 0 {
		return nil, errors.New("PNG input exceeds complete-file limit")
	}
	if err != io.EOF {
		return nil, err
	}
	return b, ctx.Err()
}
