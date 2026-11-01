package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

const (
	MaxDevicePairs       = 64
	MaxDeviceRecordBytes = 64 << 10
	MaxManifestBytes     = 48 << 10
	PairRetention        = 24 * time.Hour
)

// PairDefinition separates definition/processing compatibility from image target
// compatibility. UIDs, never resource names or image digests, establish ownership.
type PairDefinition struct {
	Unit          Unit   `json:"unit"`
	InputRevision string `json:"inputRevision"`
	Compatibility string `json:"compatibility"`
}

type RendererPair struct {
	Definition   PairDefinition `json:"definition"`
	Epoch        uint64         `json:"epoch"`
	Generation   uint64         `json:"generation"`
	StateRef     string         `json:"stateRef,omitempty"`
	CandidateRef string         `json:"candidateRef,omitempty"`
	LastConsumed string         `json:"lastConsumed,omitempty"`
	LastSequence uint64         `json:"lastSequence"`
	DetachedAt   *time.Time     `json:"detachedAt,omitempty"`
}

// CandidateManifest is immutable and content addressed. NextState is the resolved
// next snapshot, with absence denoting no state, independently of input omission.
type CandidateManifest struct {
	Version       int             `json:"version"`
	Metadata      Metadata        `json:"metadata"`
	Compatibility string          `json:"compatibility"`
	Epoch         uint64          `json:"epoch"`
	Generation    uint64          `json:"generation"`
	NextState     json.RawMessage `json:"nextState,omitempty"`
}

// StateStore exposes storage transitions independently of Job/API operations.
// Callers release repository locks before API reads, gate acquisition or waiting
// for Jobs. The BYOS device gate may enclose these short repository operations;
// repository operations never acquire the reconciler lock or image admission.
type StateStore interface {
	CornerStore
	ConfigurePair(context.Context, CornerRecord, PairDefinition) (CornerRecord, error)
	PublishCandidate(context.Context, CornerRecord, string, Metadata, []byte, render.StateResult) (CandidateManifest, error)
	Candidate(context.Context, string) (CandidateManifest, error)
	CommittedState(context.Context, CornerRecord, string) (json.RawMessage, error)
	PrepareDelivery(context.Context, CornerRecord, CornerPending) error
	ResolveDelivery(context.Context, CornerRecord, bool) error
	DetachPair(context.Context, CornerRecord, string, time.Time) error
}

var _ StateStore = (*PVC)(nil)

func PairKey(namespace, screenUID string) string {
	return digest([]byte(namespace + "\x00" + screenUID))
}
func validDefinition(d PairDefinition, uid string) bool {
	return d.Unit.Validate() == nil && d.Unit.DeviceUID == uid && uid != "" && digestPattern.MatchString(d.InputRevision) && digestPattern.MatchString(d.Compatibility)
}
func validRef(ref string) bool { return ref == "" || digestPattern.MatchString(ref) }
func validPair(p RendererPair, uid string) bool {
	return validDefinition(p.Definition, uid) && p.Epoch > 0 && p.Epoch < math.MaxUint64 && p.Generation < math.MaxUint64 && validRef(p.StateRef) && validRef(p.CandidateRef) && (p.Generation == 0) == (p.StateRef == "") && (p.Generation == 0) == (p.LastConsumed == "") && (p.DetachedAt == nil || !p.DetachedAt.IsZero()) && (p.DetachedAt == nil || p.CandidateRef == "")
}
func validManifest(m CandidateManifest) bool {
	v := m.Metadata
	return m.Version == 1 && v.Unit.Validate() == nil && v.Unit.DeviceUID != "" && digestPattern.MatchString(v.InputRevision) && digestPattern.MatchString(v.ContextDigest) && digestPattern.MatchString(m.Compatibility) && m.Epoch > 0 && m.Generation < math.MaxUint64 && v.Sequence > 0 && v.AttemptID != "" && v.Size > 0 && v.Size < v.Unit.byteLimit() && digestPattern.MatchString(v.ImageDigest) && filenamePattern.MatchString(v.Filename) && v.ContentType == "image/png" && !v.RenderedAt.IsZero() && (len(m.NextState) == 0 || render.ValidateState(m.NextState) == nil && !bytes.Equal(bytes.TrimSpace(m.NextState), []byte("null")))
}

func strictRead(path string, limit int, v any) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return ErrCorrupt
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return err
	}
	if render.ValidateJSON(b, limit, render.MaxStateDepth+8) != nil {
		return ErrCorrupt
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return ErrCorrupt
	}
	return nil
}

func (s *PVC) writeDurable(path string, b []byte) error {
	return atomicWriteFault(path, b, nil, s.writeFault)
}
func (s *PVC) writeJSONDurable(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.writeDurable(path, b)
}
func (s *PVC) writeDevice(ctx context.Context, r CornerRecord, reserve bool) error {
	if !validCorner(r, r.DeviceUID) {
		return ErrCorrupt
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b) > MaxDeviceRecordBytes {
		return ErrCapacity
	}
	additional := int64(len(b)) + 8192
	if reserve {
		_, free, err := s.Usage(ctx)
		if err != nil {
			return err
		}
		if free < uint64(additional) {
			return ErrCapacity
		}
	} else if err := s.checkCapacity(ctx, additional); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return atomicWriteFault(filepath.Join(s.root, "corners", digest([]byte(r.DeviceUID))+".json"), b, ctx.Err, s.writeFault)
}

// cloneDevice prevents map mutation from changing a caller's CAS snapshot.
func cloneDevice(r CornerRecord) CornerRecord {
	b, _ := json.Marshal(r)
	var out CornerRecord
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *PVC) currentDevice(r CornerRecord) (CornerRecord, error) {
	current, err := s.corner(r.DeviceUID)
	if err != nil {
		return CornerRecord{}, err
	}
	if !equalCorner(current, r) || current.Pending != nil {
		return CornerRecord{}, ErrObsolete
	}
	return cloneDevice(current), nil
}

// ConfigurePair creates or resets a pair, incrementing its epoch even if a
// definition later returns to an older value. Pending delivery must resolve first.
func (s *PVC) ConfigurePair(ctx context.Context, r CornerRecord, d PairDefinition) (CornerRecord, error) {
	if err := ctx.Err(); err != nil {
		return CornerRecord{}, err
	}
	if !validDefinition(d, r.DeviceUID) {
		return CornerRecord{}, errors.New("invalid renderer pair definition")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.currentDevice(r)
	if err != nil {
		return CornerRecord{}, err
	}
	key := PairKey(d.Unit.Namespace, d.Unit.ScreenUID)
	old, exists := current.Pairs[key]
	if exists && old.Definition == d && old.DetachedAt == nil {
		if err := s.confirmDurable("corners"); err != nil {
			return CornerRecord{}, err
		}
		return current, nil
	}
	if !exists && len(current.Pairs) >= MaxDevicePairs {
		return CornerRecord{}, ErrCapacity
	}
	pair := RendererPair{Definition: d, Epoch: 1}
	if exists {
		pair.Epoch = old.Epoch + 1
		if old.Definition == d && old.DetachedAt != nil && time.Since(*old.DetachedAt) < PairRetention {
			pair = old
			pair.DetachedAt = nil
		}
	}
	if current.Pairs == nil {
		current.Pairs = map[string]RendererPair{}
	}
	current.Pairs[key] = pair
	current.Revision++
	if err := s.writeDevice(ctx, current, false); err != nil {
		return CornerRecord{}, err
	}
	return current, nil
}

func (s *PVC) Candidate(ctx context.Context, ref string) (CandidateManifest, error) {
	if err := ctx.Err(); err != nil {
		return CandidateManifest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest(ref)
}
func (s *PVC) manifest(ref string) (CandidateManifest, error) {
	if !digestPattern.MatchString(ref) {
		return CandidateManifest{}, ErrCorrupt
	}
	var m CandidateManifest
	path := filepath.Join(s.root, "manifests", ref+".json")
	if err := strictRead(path, MaxManifestBytes, &m); err != nil {
		return CandidateManifest{}, err
	}
	b, err := marshalManifest(m)
	if err != nil || digest(b) != ref || !validManifest(m) {
		return CandidateManifest{}, ErrCorrupt
	}
	image, err := s.Read(context.Background(), m.Metadata.ImageDigest)
	if errors.Is(err, ErrNotFound) {
		err = ErrCorrupt
	}
	if err != nil {
		return CandidateManifest{}, err
	}
	if len(image) != m.Metadata.Size {
		return CandidateManifest{}, ErrCorrupt
	}
	return m, nil
}
func (s *PVC) validateDeviceReferences(r CornerRecord) error {
	for _, pair := range r.Pairs {
		for _, ref := range []string{pair.StateRef, pair.CandidateRef} {
			if ref == "" {
				continue
			}
			m, err := s.manifest(ref)
			if errors.Is(err, ErrNotFound) {
				return ErrCorrupt
			}
			if err != nil {
				return err
			}
			if m.Metadata.Unit != pair.Definition.Unit || m.Metadata.InputRevision != pair.Definition.InputRevision || m.Compatibility != pair.Definition.Compatibility || m.Epoch > pair.Epoch {
				return ErrCorrupt
			}
			if ref == pair.CandidateRef && (m.Epoch != pair.Epoch || m.Generation != pair.Generation || m.Metadata.Sequence != pair.LastSequence || m.Metadata.AttemptID == pair.LastConsumed) {
				return ErrCorrupt
			}
			if ref == pair.StateRef && (m.Generation+1 != pair.Generation || m.Metadata.AttemptID != pair.LastConsumed) {
				return ErrCorrupt
			}
		}
	}
	return nil
}
func (s *PVC) CommittedState(ctx context.Context, r CornerRecord, key string) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.currentDevice(r)
	if err != nil {
		return nil, err
	}
	p, ok := current.Pairs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return s.pairState(p)
}
func (s *PVC) pairState(p RendererPair) (json.RawMessage, error) {
	if p.StateRef == "" {
		return nil, nil
	}
	m, err := s.manifest(p.StateRef)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(m.NextState), nil
}

// PublishCandidate saves image and manifest before a single authoritative CAS.
// A repeated attempt resolves to its original manifest, even after consumption.
func (s *PVC) PublishCandidate(ctx context.Context, r CornerRecord, key string, m Metadata, png []byte, result render.StateResult) (CandidateManifest, error) {
	if err := ctx.Err(); err != nil {
		return CandidateManifest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.corner(r.DeviceUID)
	if err != nil {
		return CandidateManifest{}, err
	}
	expected, ok := r.Pairs[key]
	pair, live := current.Pairs[key]
	if !ok || !live || expected.Definition != pair.Definition || expected.Epoch != pair.Epoch {
		return CandidateManifest{}, ErrObsolete
	}
	// Identity retries must also match original image, result, context and sequence.
	for _, ref := range []string{pair.CandidateRef, pair.StateRef} {
		if ref == "" {
			continue
		}
		old, err := s.manifest(ref)
		if err != nil {
			return CandidateManifest{}, err
		}
		if old.Metadata.AttemptID == m.AttemptID {
			state, err := s.pairState(expected)
			if err != nil {
				return CandidateManifest{}, err
			}
			next, err := result.Apply(state)
			if err != nil {
				return CandidateManifest{}, err
			}
			v := old.Metadata
			incoming := m
			incoming.ImageDigest = digest(png)
			incoming.Filename = v.Filename
			incoming.Size = len(png)
			incoming.ContentType = "image/png"
			incoming.RenderedAt = incoming.RenderedAt.UTC()
			if incoming != v || !bytes.Equal(next, old.NextState) || expected.Generation != old.Generation {
				return CandidateManifest{}, ErrObsolete
			}
			if err := s.confirmDurable("blobs", "records", "manifests", "corners"); err != nil {
				return CandidateManifest{}, err
			}
			return old, nil
		}
	}
	if !equalCorner(current, r) || current.Pending != nil || pair.CandidateRef != "" || pair.DetachedAt != nil || m.Unit != pair.Definition.Unit || m.InputRevision != pair.Definition.InputRevision || m.Sequence <= pair.LastSequence || m.AttemptID == pair.LastConsumed {
		return CandidateManifest{}, ErrObsolete
	}
	state, err := s.pairState(pair)
	if err != nil {
		return CandidateManifest{}, err
	}
	next, err := result.Apply(state)
	if err != nil {
		return CandidateManifest{}, err
	}
	m.ImageDigest = digest(png)
	m.Size = len(png)
	m.ContentType = "image/png"
	m.RenderedAt = m.RenderedAt.UTC()
	// A syntactically valid placeholder permits validation before any disk writes.
	m.Filename = "AAAAAAAAAAAAAAAAAAAAAAAAAA.png"
	manifest := CandidateManifest{Version: 1, Metadata: m, Compatibility: pair.Definition.Compatibility, Epoch: pair.Epoch, Generation: pair.Generation, NextState: next}
	if !validManifest(manifest) {
		return CandidateManifest{}, errors.New("invalid candidate publication")
	}
	if err := s.checkCapacity(ctx, int64(len(png))+MaxManifestBytes+MaxDeviceRecordBytes+16384); err != nil {
		return CandidateManifest{}, err
	}
	_, manifest.Metadata.Filename, err = s.storeBlob(png)
	if err != nil {
		return CandidateManifest{}, err
	}
	data, err := marshalManifest(manifest)
	if err != nil {
		return CandidateManifest{}, err
	}
	if len(data) > MaxManifestBytes {
		return CandidateManifest{}, ErrCapacity
	}
	ref := digest(data)
	p := filepath.Join(s.root, "manifests", ref+".json")
	if _, err := s.manifest(ref); errors.Is(err, ErrNotFound) {
		if err := s.writeDurable(p, data); err != nil {
			return CandidateManifest{}, err
		}
	} else if err != nil {
		return CandidateManifest{}, err
	}
	if err := s.confirmDurable("manifests"); err != nil {
		return CandidateManifest{}, err
	}
	current = cloneDevice(current)
	pair.CandidateRef = ref
	pair.LastSequence = m.Sequence
	current.Pairs[key] = pair
	current.Revision++
	if err := s.writeDevice(ctx, current, false); err != nil {
		return CandidateManifest{}, err
	}
	return manifest, nil
}

func validPending(r CornerRecord, p CornerPending) bool {
	if p.TransactionID == "" || r.Revision == 0 || p.RecordRevision != r.Revision-1 || p.Next != r.Current && p.Next != r.Current.Next() || !digestPattern.MatchString(p.ImageDigest) || !filenamePattern.MatchString(p.Filename) {
		return false
	}
	if p.Proposed != nil {
		if !validTurn(p.Proposed) || p.Previous != nil && !validTurn(p.Previous) || EqualCycle(p.Previous, p.Proposed) || p.Proposed.ImageDigest != p.ImageDigest || p.Proposed.Filename != p.Filename {
			return false
		}
	} else if p.Previous != nil {
		return false
	}
	if p.CandidateRef == "" {
		return p.PairKey == "" && p.Epoch == 0 && p.Generation == 0
	}
	pair, ok := r.Pairs[p.PairKey]
	return ok && pair.CandidateRef == p.CandidateRef && pair.Epoch == p.Epoch && pair.Generation == p.Generation && pair.DetachedAt == nil
}

func (s *PVC) PrepareDelivery(ctx context.Context, r CornerRecord, p CornerPending) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.corner(r.DeviceUID)
	if err != nil {
		return err
	}
	proposed := cloneDevice(r)
	proposed.Revision++
	proposed.Pending = &p
	if p.RecordRevision != r.Revision || !validCorner(proposed, r.DeviceUID) {
		return errors.New("invalid delivery proposal")
	}
	if equalCorner(current, proposed) {
		return s.confirmDurable("corners")
	}
	if !equalCorner(current, r) || current.Pending != nil || current.LastTransaction == p.TransactionID {
		return ErrObsolete
	}
	if _, err := s.Read(ctx, p.ImageDigest); err != nil {
		return err
	}
	var filename struct {
		Filename string `json:"filename"`
	}
	if err := readJSON(filepath.Join(s.root, "records", p.ImageDigest+".json"), &filename); err != nil {
		return err
	}
	if filename.Filename != p.Filename {
		return ErrCorrupt
	}
	return s.writeDevice(ctx, proposed, false)
}

// ResolveDelivery is called after a durable decision: single-screen issuance or
// an exact proposed/previous API turn comparison. Issue alone never proves a
// cycling commit. No API calls or waits occur while holding the repository mutex.
func (s *PVC) ResolveDelivery(ctx context.Context, r CornerRecord, commit bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Pending == nil || r.Pending.TransactionID == "" {
		return ErrObsolete
	}
	current, err := s.corner(r.DeviceUID)
	if err != nil {
		return err
	}
	if current.LastTransaction == r.Pending.TransactionID && current.LastCommitted == commit && current.Pending == nil {
		return s.confirmDurable("corners")
	}
	if !equalCorner(current, r) {
		return ErrObsolete
	}
	next := cloneDevice(r)
	p := next.Pending
	next.Pending = nil
	next.Revision++
	next.LastTransaction = p.TransactionID
	next.LastCommitted = commit
	if commit {
		next.Current = p.Next
		if p.CandidateRef != "" {
			pair := next.Pairs[p.PairKey]
			m, err := s.manifest(p.CandidateRef)
			if err != nil {
				return err
			}
			pair.StateRef = p.CandidateRef
			pair.CandidateRef = ""
			pair.Generation++
			pair.LastConsumed = m.Metadata.AttemptID
			next.Pairs[p.PairKey] = pair
		}
	}
	return s.writeDevice(ctx, next, true)
}

// DetachPair invalidates pending work immediately while retaining committed state
// for a bounded absence. Advancing epoch fences an attempt even after rejoining.
func (s *PVC) DetachPair(ctx context.Context, r CornerRecord, key string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if now.IsZero() {
		return errors.New("detach time required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.currentDevice(r)
	if err != nil {
		return err
	}
	pair, ok := current.Pairs[key]
	if !ok {
		return ErrNotFound
	}
	if pair.DetachedAt != nil {
		return s.confirmDurable("corners")
	}
	// Retain the committed manifest while fencing pending attempts with the
	// epoch and detached flag. Committed manifests may belong to an earlier epoch.
	pair.CandidateRef = ""
	pair.Epoch++
	stamp := now.UTC()
	pair.DetachedAt = &stamp
	current.Pairs[key] = pair
	current.Revision++
	return s.writeDevice(ctx, current, false)
}

// Preserve renderer JSON without HTML expansion in the immutable storage format.
func marshalManifest(m CandidateManifest) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// A rename may have succeeded before its directory fsync reported failure.
// Reusing observed bytes/decisions must complete that sync before reporting success.
func (s *PVC) confirmDurable(dirs ...string) error {
	for _, dir := range dirs {
		if err := syncDir(filepath.Join(s.root, dir)); err != nil {
			return err
		}
		if s.writeFault != nil {
			if err := s.writeFault(dir + "/directory-sync"); err != nil {
				return err
			}
		}
	}
	return nil
}
