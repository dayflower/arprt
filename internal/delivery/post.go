package delivery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type order struct {
	sequence uint64
	active   int
}

type pair struct{ namespace, screenUID, deviceUID string }

// POST renders each request independently. Attempts are ordered per pair across
// targets, so a late response cannot publish after a newer request starts.
type POST struct {
	Repository  artifact.Repository
	Reader      client.Reader
	NewExecutor func(string) render.Executor
	mu          sync.Mutex
	attempts    map[pair]*order
	slots       chan struct{}
}

// NewPOST shares GET's repository, direct reader and HTTP concurrency budget.
func NewPOST(get *GET) *POST {
	return &POST{Repository: get.Repository, Reader: get.Reader, slots: get.slots, attempts: map[pair]*order{}, NewExecutor: func(raw string) render.Executor { return render.HTTPPOST{URL: raw} }}
}

// Image returns this pair's compatible prior success on failure, never another
// device's output. There is no fresh cache reuse or context coalescing for POST.
func (p *POST) Image(ctx context.Context, s *api.ArprtScreen, d *api.ArprtDevice, input render.Context) (artifact.Metadata, error) {
	s, d = s.DeepCopy(), d.DeepCopy()
	if s.Spec.Source.HTTP == nil || s.Spec.Source.HTTP.Method != "POST" || d.UID == "" || d.Namespace != s.Namespace || input.Version != "v1" || input.Device.Name != d.Name {
		return artifact.Metadata{}, errors.New("POST requires a screen/device pair and Render Context v1")
	}
	resolved := devicestate.Resolved(d)
	if resolved.Target != input.RenderTarget {
		return artifact.Metadata{}, artifact.ErrObsolete
	}
	if err := resolved.Validate(); err != nil {
		return artifact.Metadata{}, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return artifact.Metadata{}, err
	}
	// Own the snapshot, including optional pointers, for the whole attempt.
	var snapshot render.Context
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return artifact.Metadata{}, err
	}
	input = snapshot
	digest := sha256.Sum256(body)
	revision, err := screen.InputRevision(s.Spec)
	if err != nil {
		return artifact.Metadata{}, err
	}
	u := artifact.Unit{Namespace: s.Namespace, ScreenUID: string(s.UID), DeviceUID: string(d.UID), Target: input.RenderTarget, Output: resolved.Output}
	key := pair{u.Namespace, u.ScreenUID, u.DeviceUID}
	p.mu.Lock()
	entry := p.attempts[key]
	if entry == nil {
		entry = &order{}
		p.attempts[key] = entry
	}
	entry.sequence++
	entry.active++
	sequence := entry.sequence
	defer func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		entry.active--
		if entry.active == 0 {
			delete(p.attempts, key)
		}
	}()
	p.mu.Unlock()

	work, cancel := context.WithTimeout(ctx, render.HTTPTimeout)
	defer cancel()
	select {
	case p.slots <- struct{}{}:
		observe.HTTPActive.Inc()
		defer func() { <-p.slots; observe.HTTPActive.Dec() }()
	case <-work.Done():
		p.mu.Lock()
		current := entry.sequence == sequence
		p.mu.Unlock()
		if !current {
			return artifact.Metadata{}, artifact.ErrObsolete
		}
		return artifact.Metadata{}, errors.New("render concurrency wait timed out")
	}
	var result artifact.Metadata
	err = imageprocessing.RuntimeAdmission.Do(work, func(work context.Context) error {
		old, err := p.Repository.Latest(work, u)
		if err != nil && !errors.Is(err, artifact.ErrNotFound) {
			return err
		}
		result = old
		policy, err := screen.ProcessingPolicy(s.Spec)
		if err != nil {
			return err
		}
		p.mu.Lock()
		current := entry.sequence == sequence
		p.mu.Unlock()
		if !current {
			result = artifact.Metadata{}
			return artifact.ErrObsolete
		}
		out, renderErr := p.NewExecutor(s.Spec.Source.HTTP.URL).Execute(work, render.Request{Target: u.Target, Output: u.Output, Context: &input, Policy: policy})
		renderedAt := out.RenderedAt
		if renderedAt.IsZero() {
			renderedAt = time.Now().UTC()
		}
		if renderErr == nil {
			renderErr = imageprocessing.Process(work, func(ctx context.Context) error {
				return u.ResolvedTarget().ValidateOutput(ctx, out.PNG)
			})
		}

		p.mu.Lock()
		defer p.mu.Unlock()
		if entry.sequence != sequence {
			result = artifact.Metadata{}
			return artifact.ErrObsolete
		}
		if renderErr != nil {
			return renderErr
		}
		// Re-read both desired state and observed target without informer delay.
		// Pair-owned publication requires assignment membership; BYOS separately
		// checks the complete assignment fingerprint before issuing the image.
		var currentScreen api.ArprtScreen
		var currentDevice api.ArprtDevice
		if p.Reader.Get(work, client.ObjectKeyFromObject(s), &currentScreen) != nil || currentScreen.UID != s.UID || currentScreen.Generation != s.Generation || !reflect.DeepEqual(currentScreen.Spec, s.Spec) || !currentScreen.DeletionTimestamp.IsZero() {
			result = artifact.Metadata{}
			return artifact.ErrObsolete
		}
		if p.Reader.Get(work, client.ObjectKeyFromObject(d), &currentDevice) != nil || currentDevice.UID != d.UID || currentDevice.Generation != d.Generation || !reflect.DeepEqual(currentDevice.Spec, d.Spec) || !currentDevice.DeletionTimestamp.IsZero() || devicestate.Resolved(&currentDevice) != u.ResolvedTarget() {
			result = artifact.Metadata{}
			return artifact.ErrObsolete
		}
		if eligible, err := (assignment.Explicit{Reader: p.Reader}).Contains(work, &currentDevice, s); err != nil || !eligible {
			result = artifact.Metadata{}
			return artifact.ErrObsolete
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		publishSequence := old.Sequence
		if repository, ok := p.Repository.(*artifact.PVC); ok {
			publishSequence, err = repository.Sequence(work, u)
			if err != nil {
				return err
			}
		}
		m := artifact.Metadata{Unit: u, ScreenGeneration: s.Generation, InputRevision: revision, AttemptID: hex.EncodeToString(id[:]), Sequence: publishSequence + 1, ContextDigest: hex.EncodeToString(digest[:]), RenderedAt: renderedAt}
		m, err = p.Repository.Publish(work, m, out.PNG)
		if err != nil {
			if errors.Is(err, artifact.ErrObsolete) {
				result = artifact.Metadata{}
				return err
			}
			return err
		}
		result = m
		return nil
	})
	return result, err
}
