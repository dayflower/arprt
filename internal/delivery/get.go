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
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CandidateError identifies renderer/PNG failures eligible for playlist Skip.
// Repository, authorization and publication failures retain their original errors.
type CandidateError struct{ Err error }

func (e *CandidateError) Error() string { return e.Err.Error() }
func (e *CandidateError) Unwrap() error { return e.Err }

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
func (g *GET) Image(ctx context.Context, s *api.ArprtScreen, target render.ResolvedTarget) (artifact.Metadata, error) {
	return g.ImageBefore(ctx, s, target, time.Time{})
}

// ImageBefore bounds a newly started shared render by an absolute playlist
// cutoff. Joining callers cannot extend the original flight or cancel peers.
func (g *GET) ImageBefore(ctx context.Context, s *api.ArprtScreen, target render.ResolvedTarget, deadline time.Time) (artifact.Metadata, error) {
	if err := target.Validate(); err != nil {
		return artifact.Metadata{}, err
	}
	unit := artifact.Unit{Namespace: s.Namespace, ScreenUID: string(s.UID), Target: target.Target, Output: target.Output}
	g.mu.Lock()
	f, joined := g.flights[unit]
	if !joined {
		f = &flight{done: make(chan struct{})}
		g.flights[unit] = f
		snapshot := s.DeepCopy()
		go func() {
			cutoff := time.Now().Add(render.HTTPTimeout)
			if !deadline.IsZero() && deadline.Before(cutoff) {
				cutoff = deadline
			}
			work, cancel := context.WithDeadline(g.Context, cutoff)
			defer cancel()
			f.err = imageprocessing.RuntimeAdmission.Do(work, func(ctx context.Context) error {
				var err error
				f.meta, err = g.image(ctx, snapshot, unit)
				return err
			})
			if work.Err() != nil && errors.Is(f.err, work.Err()) {
				f.err = &CandidateError{Err: f.err}
			}
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
		return g.ImageBefore(ctx, s, target, deadline)
	}
	return f.meta, f.err
}

func (g *GET) image(ctx context.Context, s *api.ArprtScreen, u artifact.Unit) (artifact.Metadata, error) {
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
	if old.ImageDigest != "" && old.Fresh(u, revision) && old.ScreenGeneration == s.Generation && time.Since(old.RenderedAt) < interval {
		return old, nil
	}
	select {
	case g.slots <- struct{}{}:
		observe.HTTPActive.Inc()
		defer func() { <-g.slots; observe.HTTPActive.Dec() }()
	case <-ctx.Done():
		return old, &CandidateError{Err: errors.New("render concurrency wait timed out")}
	}
	if s.Spec.Source.HTTP == nil || (s.Spec.Source.HTTP.Method != "" && s.Spec.Source.HTTP.Method != "GET") {
		return artifact.Metadata{}, errors.New("only HTTP GET is supported")
	}
	policy, err := screen.ProcessingPolicy(s.Spec)
	if err != nil {
		return old, err
	}
	out, err := g.NewExecutor(s.Spec.Source.HTTP.URL).Execute(ctx, render.Request{Target: u.Target, Output: u.Output, Policy: policy})
	if err != nil {
		return old, &CandidateError{Err: err}
	}
	renderedAt := out.RenderedAt
	if renderedAt.IsZero() {
		renderedAt = time.Now().UTC()
	}
	err = imageprocessing.Process(ctx, func(work context.Context) error {
		return u.ResolvedTarget().ValidateOutput(work, out.PNG)
	})
	if err != nil {
		return old, &CandidateError{Err: err}
	}
	// Re-read desired state directly before publication, without informer delay.
	// GET artifacts are shared by screen/target, so publication is independent
	// of any one device's current assignment.
	var current api.ArprtScreen
	if err := g.Reader.Get(ctx, client.ObjectKeyFromObject(s), &current); err != nil {
		if apierrors.IsNotFound(err) {
			return artifact.Metadata{}, artifact.ErrObsolete
		}
		return artifact.Metadata{}, err
	}
	currentRevision, err := screen.InputRevision(current.Spec)
	if err != nil || current.UID != s.UID || current.Generation != s.Generation || currentRevision != revision || !current.DeletionTimestamp.IsZero() {
		return artifact.Metadata{}, artifact.ErrObsolete
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return old, err
	}
	sequence := old.Sequence
	if repository, ok := g.Repository.(*artifact.PVC); ok {
		sequence, err = repository.Sequence(ctx, u)
		if err != nil {
			return old, err
		}
	}
	m := artifact.Metadata{Unit: u, ScreenGeneration: s.Generation, InputRevision: revision, AttemptID: hex.EncodeToString(id[:]), Sequence: sequence + 1, RenderedAt: renderedAt}
	m, err = g.Repository.Publish(ctx, m, out.PNG)
	if err != nil {
		return old, err
	}
	return m, nil
}
