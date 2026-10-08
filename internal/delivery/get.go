// Package delivery joins declarative screens with validated, persistent artifacts.
package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type flight struct {
	done chan struct{}
	meta artifact.Metadata
	err  error
}
type GET struct {
	Context     context.Context
	Repository  artifact.Repository
	Reader      client.Reader
	NewExecutor func(string) render.Executor
	mu          sync.Mutex
	flights     map[artifact.Unit]*flight
	slots       chan struct{}
}

func NewGET(repo artifact.Repository, reader client.Reader) *GET {
	return &GET{Context: context.Background(), Repository: repo, Reader: reader, flights: map[artifact.Unit]*flight{}, slots: make(chan struct{}, 8), NewExecutor: func(raw string) render.Executor { return render.HTTPGET{URL: raw} }}
}

// Image returns eligible old output with a nonnil render error after a failed
// refresh. Joining callers wait independently; one disconnect cannot cancel all.
func (g *GET) Image(ctx context.Context, s *api.TrmnlScreen, target render.Target) (artifact.Metadata, error) {
	unit := artifact.Unit{Namespace: s.Namespace, ScreenUID: string(s.UID), Target: target}
	g.mu.Lock()
	f, joined := g.flights[unit]
	if !joined {
		f = &flight{done: make(chan struct{})}
		g.flights[unit] = f
		snapshot := s.DeepCopy()
		go func() {
			work, cancel := context.WithTimeout(g.Context, render.HTTPTimeout)
			defer cancel()
			f.meta, f.err = g.image(work, snapshot, unit)
			g.mu.Lock()
			delete(g.flights, unit)
			close(f.done)
			g.mu.Unlock()
		}()
	}
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return artifact.Metadata{}, ctx.Err()
	case <-f.done:
	}
	// A concurrent definition edit joins the old unit's flight; it must not be
	// treated as fresh output for the new definition.
	revision, err := screen.InputRevision(s.Spec)
	if err != nil {
		return artifact.Metadata{}, err
	}
	if joined && f.err == nil && (f.meta.InputRevision != revision || f.meta.ScreenGeneration != s.Generation) {
		return g.Image(ctx, s, target)
	}
	return f.meta, f.err
}

func (g *GET) image(ctx context.Context, s *api.TrmnlScreen, u artifact.Unit) (artifact.Metadata, error) {
	old, err := g.Repository.Latest(ctx, u)
	if err != nil && !errors.Is(err, artifact.ErrNotFound) {
		return artifact.Metadata{}, err
	}
	revision, err := screen.InputRevision(s.Spec)
	if err != nil {
		return old, err
	}
	interval, err := screen.EffectiveInterval(s.Spec)
	if err != nil {
		return old, err
	}
	if old.ImageDigest != "" && old.InputRevision == revision && old.ScreenGeneration == s.Generation && time.Since(old.RenderedAt) < interval {
		return old, nil
	}
	select {
	case g.slots <- struct{}{}:
		observe.HTTPActive.Inc()
		defer func() { <-g.slots; observe.HTTPActive.Dec() }()
	case <-ctx.Done():
		return old, errors.New("render concurrency wait timed out")
	}
	if s.Spec.Source.HTTP == nil || (s.Spec.Source.HTTP.Method != "" && s.Spec.Source.HTTP.Method != "GET") {
		return artifact.Metadata{}, errors.New("only HTTP GET is supported")
	}
	out, err := g.NewExecutor(s.Spec.Source.HTTP.URL).Execute(ctx, render.Request{Target: u.Target})
	if err != nil {
		return old, err
	}
	// Re-read desired state directly before publication, without informer delay.
	var current api.TrmnlScreen
	if err := g.Reader.Get(ctx, client.ObjectKeyFromObject(s), &current); err != nil {
		return artifact.Metadata{}, artifact.ErrObsolete
	}
	currentRevision, err := screen.InputRevision(current.Spec)
	if err != nil || current.UID != s.UID || current.Generation != s.Generation || currentRevision != revision || !current.DeletionTimestamp.IsZero() {
		return artifact.Metadata{}, artifact.ErrObsolete
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return old, err
	}
	m := artifact.Metadata{Unit: u, ScreenGeneration: s.Generation, InputRevision: revision, AttemptID: hex.EncodeToString(id[:]), Sequence: old.Sequence + 1, RenderedAt: time.Now().UTC()}
	m, err = g.Repository.Publish(ctx, m, out.PNG)
	if err != nil {
		return old, err
	}
	return m, nil
}
