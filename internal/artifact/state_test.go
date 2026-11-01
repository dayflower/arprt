package artifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/render"
)

func stateFixture(t *testing.T) (*PVC, CornerRecord, string, Metadata, []byte) {
	t.Helper()
	s, err := NewPVC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Corner(t.Context(), "device")
	if err != nil {
		t.Fatal(err)
	}
	d := PairDefinition{Unit: Unit{Namespace: "arprt", DeviceUID: "device", ScreenUID: "screen", Target: render.Target{Width: 800, Height: 480, Palette: "gray4"}}, InputRevision: strings.Repeat("a", 64), Compatibility: strings.Repeat("b", 64)}
	r, err = s.ConfigurePair(t.Context(), r, d)
	if err != nil {
		t.Fatal(err)
	}
	png, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	m := Metadata{Unit: d.Unit, ScreenGeneration: 1, InputRevision: d.InputRevision, AttemptID: "attempt-1", Sequence: 1, ContextDigest: strings.Repeat("c", 64), RenderedAt: time.Now().UTC()}
	return s, r, PairKey(d.Unit.Namespace, d.Unit.ScreenUID), m, png
}
func deviceSnapshot(t *testing.T, s *PVC) CornerRecord {
	t.Helper()
	r, err := s.Corner(t.Context(), "device")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func manifestRef(t *testing.T, m CandidateManifest) string {
	t.Helper()
	b, err := marshalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	return digest(b)
}
func statePending(r CornerRecord, key string, m CandidateManifest, ref, tx string) CornerPending {
	p := r.Pairs[key]
	return CornerPending{TransactionID: tx, RecordRevision: r.Revision, PairKey: key, CandidateRef: ref, Epoch: p.Epoch, Generation: p.Generation, ImageDigest: m.Metadata.ImageDigest, Filename: m.Metadata.Filename, Next: r.Current.Next()}
}
func restartState(t *testing.T, s *PVC) *PVC {
	t.Helper()
	out, err := NewPVC(s.root)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCandidateConsumptionAndCAS(t *testing.T) {
	s, initial, key, m, png := stateFixture(t)
	ctx := t.Context()
	result := render.StateResult{NextState: json.RawMessage(`{"cursor":9007199254740993}`)}
	candidate, err := s.PublishCandidate(ctx, initial, key, m, png, result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishCandidate(ctx, initial, key, m, png, result); err != nil {
		t.Fatal("publish retry", err)
	}
	r := deviceSnapshot(t, s)
	if state, err := s.CommittedState(ctx, r, key); err != nil || len(state) != 0 || r.Pairs[key].Generation != 0 {
		t.Fatal("publication advanced state", err)
	}
	later := m
	later.Sequence++
	later.AttemptID = "later"
	if _, err := s.PublishCandidate(ctx, r, key, later, png, result); !errors.Is(err, ErrObsolete) {
		t.Fatal("replaced unconsumed candidate", err)
	}
	pending := statePending(r, key, candidate, manifestRef(t, candidate), "delivery-1")
	if err := s.PrepareDelivery(ctx, r, pending); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareDelivery(ctx, r, pending); err != nil {
		t.Fatal("prepare retry", err)
	}
	s = restartState(t, s)
	prepared := deviceSnapshot(t, s)
	if err := s.Collect(ctx, func(Unit) bool { return false }, map[string]bool{"device": true}, time.Now().Add(48*time.Hour)); !errors.Is(err, ErrPending) {
		t.Fatal("GC proceeded with pending delivery", err)
	}
	if _, err := s.ConfigurePair(ctx, prepared, r.Pairs[key].Definition); !errors.Is(err, ErrObsolete) {
		t.Fatal("mutation during pending", err)
	}
	if err := s.Issue(ctx, "device", candidate.Metadata.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveDelivery(ctx, prepared, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveDelivery(ctx, prepared, false); err != nil {
		t.Fatal("abort retry", err)
	}
	r = deviceSnapshot(t, s)
	if r.Pairs[key].Generation != 0 || r.Pairs[key].CandidateRef == "" || r.Current != initial.Current {
		t.Fatal("abort advanced state or corner")
	}
	pending = statePending(r, key, candidate, manifestRef(t, candidate), "delivery-2")
	if err := s.PrepareDelivery(ctx, r, pending); err != nil {
		t.Fatal(err)
	}
	prepared = deviceSnapshot(t, s)
	if err := s.ResolveDelivery(ctx, prepared, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveDelivery(ctx, prepared, true); err != nil {
		t.Fatal("finalize retry", err)
	}
	s = restartState(t, s)
	r = deviceSnapshot(t, s)
	state, err := s.CommittedState(ctx, r, key)
	if err != nil || string(state) != string(result.NextState) || r.Pairs[key].Generation != 1 || r.Current != initial.Current.Next() {
		t.Fatal("lost atomic state/corner update", err)
	}
	if _, err := s.PublishCandidate(ctx, initial, key, m, png, result); err != nil {
		t.Fatal("consumed publication retry", err)
	}
	if deviceSnapshot(t, s).Pairs[key].CandidateRef != "" {
		t.Fatal("consumed candidate republished")
	}
	if _, err := s.PublishCandidate(ctx, initial, key, later, png, result); !errors.Is(err, ErrObsolete) {
		t.Fatal("stale CAS accepted", err)
	}
	// Equal PNG and equal next state remain distinct candidates and generations.
	second, err := s.PublishCandidate(ctx, r, key, later, png, result)
	if err != nil {
		t.Fatal(err)
	}
	if manifestRef(t, second) == manifestRef(t, candidate) || second.Metadata.Filename != candidate.Metadata.Filename {
		t.Fatal("candidate and image identities confused")
	}
	r = deviceSnapshot(t, s)
	p := statePending(r, key, second, manifestRef(t, second), "delivery-3")
	p.Next = r.Current
	if err := s.PrepareDelivery(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveDelivery(ctx, deviceSnapshot(t, s), true); err != nil {
		t.Fatal(err)
	}
	r = deviceSnapshot(t, s)
	if r.Pairs[key].Generation != 2 || r.Current != initial.Current.Next() {
		t.Fatal("unchanged state failed to consume generation")
	}
}

func TestStateDurabilityFaultBoundaries(t *testing.T) {
	injected := errors.New("injected storage failure")
	for _, dir := range []string{"blobs", "records", "manifests", "corners"} {
		for _, stage := range []string{"write", "file-sync", "rename", "directory-sync"} {
			t.Run("publish/"+dir+"/"+stage, func(t *testing.T) {
				s, r, key, m, png := stateFixture(t)
				hit := false
				s.writeFault = func(at string) error {
					if at == dir+"/"+stage {
						hit = true
						return injected
					}
					return nil
				}
				result := render.StateResult{NextState: json.RawMessage(`false`)}
				if _, err := s.PublishCandidate(t.Context(), r, key, m, png, result); !errors.Is(err, injected) || !hit {
					t.Fatal("missing fault", err)
				}
				s = restartState(t, s)
				live := deviceSnapshot(t, s)
				if live.Pairs[key].Generation != 0 || live.Current != r.Current {
					t.Fatal("failed publication consumed state")
				}
				if _, err := s.PublishCandidate(t.Context(), r, key, m, png, result); err != nil {
					t.Fatal("publication did not recover", err)
				}
				live = deviceSnapshot(t, s)
				if live.Pairs[key].CandidateRef == "" {
					t.Fatal("retry lost candidate")
				}
			})
		}
	}
	for _, operation := range []string{"configure", "prepare", "commit", "abort", "corner"} {
		for _, stage := range []string{"write", "file-sync", "rename", "directory-sync"} {
			t.Run(operation+"/"+stage, func(t *testing.T) {
				s, r, key, m, png := stateFixture(t)
				candidate, err := s.PublishCandidate(t.Context(), r, key, m, png, render.StateResult{NextState: json.RawMessage(`0`)})
				if err != nil {
					t.Fatal(err)
				}
				r = deviceSnapshot(t, s)
				p := statePending(r, key, candidate, manifestRef(t, candidate), "transaction")
				if operation == "commit" || operation == "abort" {
					if err := s.PrepareDelivery(t.Context(), r, p); err != nil {
						t.Fatal(err)
					}
					r = deviceSnapshot(t, s)
				}
				invoke := func(repo *PVC) error {
					switch operation {
					case "configure":
						d := r.Pairs[key].Definition
						d.InputRevision = strings.Repeat("d", 64)
						_, err := repo.ConfigurePair(t.Context(), r, d)
						return err
					case "prepare":
						return repo.PrepareDelivery(t.Context(), r, p)
					case "corner":
						return repo.ResolveCorner(t.Context(), r, true)
					default:
						return repo.ResolveDelivery(t.Context(), r, operation == "commit")
					}
				}
				s.writeFault = func(at string) error {
					if at == "corners/"+stage {
						return injected
					}
					return nil
				}
				if err := invoke(s); !errors.Is(err, injected) {
					t.Fatal("missing failure", err)
				}
				s = restartState(t, s)
				live := deviceSnapshot(t, s)
				if operation == "configure" {
					d := r.Pairs[key].Definition
					d.InputRevision = strings.Repeat("d", 64)
					if _, err := s.ConfigurePair(t.Context(), live, d); err != nil {
						t.Fatal(err)
					}
				} else if err := invoke(s); err != nil {
					t.Fatal("transition retry failed", err)
				}
				live = deviceSnapshot(t, s)
				if operation == "commit" {
					if live.Pairs[key].Generation != 1 || live.Current != r.Current.Next() {
						t.Fatal("partial finalization")
					}
				}
				if operation == "abort" && (live.Pairs[key].Generation != 0 || live.Current != r.Current) {
					t.Fatal("abort advanced")
				}
			})
		}
	}
}

func TestStateRetentionResetAndCorruption(t *testing.T) {
	s, initial, key, m, png := stateFixture(t)
	ctx := t.Context()
	candidate, err := s.PublishCandidate(ctx, initial, key, m, png, render.StateResult{NextState: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	ref := manifestRef(t, candidate)
	r := deviceSnapshot(t, s)
	p := statePending(r, key, candidate, ref, "commit")
	if err := s.PrepareDelivery(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveDelivery(ctx, deviceSnapshot(t, s), true); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, dir := range []string{"blobs", "manifests"} {
		entries, err := os.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := os.Chtimes(filepath.Join(s.root, dir, e.Name()), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Collect(ctx, func(Unit) bool { return false }, map[string]bool{"device": true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Candidate(ctx, ref); err != nil {
		t.Fatal("GC removed committed root", err)
	}
	r = deviceSnapshot(t, s)
	if err := s.DetachPair(ctx, r, key, time.Now()); err != nil {
		t.Fatal(err)
	}
	detached := deviceSnapshot(t, s)
	if detached.Pairs[key].Epoch <= r.Pairs[key].Epoch {
		t.Fatal("departure did not fence attempts")
	}
	rejoined, err := s.ConfigurePair(ctx, detached, r.Pairs[key].Definition)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.CommittedState(ctx, rejoined, key)
	if err != nil || string(state) != "[]" {
		t.Fatal("rejoin lost committed state", err)
	}
	d := r.Pairs[key].Definition
	d.InputRevision = strings.Repeat("d", 64)
	reset, err := s.ConfigurePair(ctx, rejoined, d)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Pairs[key].Epoch <= rejoined.Pairs[key].Epoch || reset.Pairs[key].StateRef != "" || reset.Pairs[key].Generation != 0 {
		t.Fatal("reset retained old state")
	}
	if _, err := s.PublishCandidate(ctx, initial, key, m, png, render.StateResult{}); !errors.Is(err, ErrObsolete) {
		t.Fatal("old epoch accepted", err)
	}
	// Unknown and old versions are errors, never an implicit reset or migration.
	path := filepath.Join(s.root, "corners", digest([]byte("device"))+".json")
	for _, data := range []string{`{"version":1,"deviceUID":"device","revision":0,"current":"top-right"}`, `{"version":99}`, `{"version":2,"version":2}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Corner(ctx, "device"); !errors.Is(err, ErrCorrupt) {
			t.Fatal("invalid record accepted", err)
		}
		if err := s.Collect(ctx, func(Unit) bool { return false }, nil, time.Now().Add(72*time.Hour)); !errors.Is(err, ErrCorrupt) {
			t.Fatal("corrupt root allowed GC", err)
		}
		if _, err := s.Read(ctx, candidate.Metadata.ImageDigest); err != nil {
			t.Fatal("GC deleted before validation", err)
		}
	}
}

func TestCandidateConcurrencyAndCapacity(t *testing.T) {
	s, r, key, m, png := stateFixture(t)
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			attempt := m
			attempt.AttemptID = fmt.Sprintf("attempt-%d", i)
			attempt.Sequence = uint64(i + 1)
			_, err := s.PublishCandidate(t.Context(), r, key, attempt, png, render.StateResult{})
			out <- err
		})
	}
	wg.Wait()
	close(out)
	success, obsolete := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, ErrObsolete) {
			obsolete++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || obsolete != 1 {
		t.Fatal("CAS allowed multiple candidates", success, obsolete)
	}
	r = deviceSnapshot(t, s)
	s.MaxBytes = 1
	d := r.Pairs[key].Definition
	d.InputRevision = strings.Repeat("f", 64)
	if _, err := s.ConfigurePair(t.Context(), r, d); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity admission ignored", err)
	}
	if !equalCorner(deviceSnapshot(t, s), r) {
		t.Fatal("capacity failure changed record")
	}
}

func TestStateRepresentationRetriesAndMissingRoots(t *testing.T) {
	s, r, key, m, png := stateFixture(t)
	result := render.StateResult{NextState: json.RawMessage("{\n \"text\":\"<>&\", \"cursor\":9007199254740993\n}")}
	candidate, err := s.PublishCandidate(t.Context(), r, key, m, png, result)
	if err != nil {
		t.Fatal(err)
	}
	s = restartState(t, s)
	if _, err := s.PublishCandidate(t.Context(), r, key, m, png, result); err != nil {
		t.Fatal("JSON formatting broke idempotent retry", err)
	}
	ref := manifestRef(t, candidate)
	if err := os.Remove(filepath.Join(s.root, "manifests", ref+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Corner(t.Context(), "device"); !errors.Is(err, ErrCorrupt) {
		t.Fatal("missing manifest accepted", err)
	}
	if err := s.Collect(t.Context(), func(Unit) bool { return false }, nil, time.Now().Add(72*time.Hour)); !errors.Is(err, ErrCorrupt) {
		t.Fatal("GC ignored missing root", err)
	}
	if _, err := s.Read(t.Context(), candidate.Metadata.ImageDigest); err != nil {
		t.Fatal("GC removed image after broken root", err)
	}
}

func TestStateRecordBoundsAndDetachedExpiry(t *testing.T) {
	s, r, key, m, png := stateFixture(t)
	candidate, err := s.PublishCandidate(t.Context(), r, key, m, png, render.StateResult{NextState: json.RawMessage(`"retained"`)})
	if err != nil {
		t.Fatal(err)
	}
	r = deviceSnapshot(t, s)
	p := statePending(r, key, candidate, manifestRef(t, candidate), "commit-expiry")
	if err := s.PrepareDelivery(t.Context(), r, p); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveDelivery(t.Context(), deviceSnapshot(t, s), true); err != nil {
		t.Fatal(err)
	}
	r = deviceSnapshot(t, s)
	if err := s.DetachPair(t.Context(), r, key, time.Now().Add(-PairRetention)); err != nil {
		t.Fatal(err)
	}
	departed := deviceSnapshot(t, s)
	if err := s.Collect(t.Context(), func(Unit) bool { return false }, map[string]bool{"device": true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	r = deviceSnapshot(t, s)
	pair := r.Pairs[key]
	if pair.StateRef != "" || pair.Generation != 0 || pair.Epoch <= departed.Pairs[key].Epoch {
		t.Fatal("expired state payload retained")
	}
	d := pair.Definition
	for i := 1; i < MaxDevicePairs; i++ {
		d.Unit.ScreenUID = fmt.Sprintf("screen-%d", i)
		r, err = s.ConfigurePair(t.Context(), r, d)
		if err != nil {
			t.Fatal(err)
		}
	}
	d.Unit.ScreenUID = "one-too-many"
	if _, err := s.ConfigurePair(t.Context(), r, d); !errors.Is(err, ErrCapacity) {
		t.Fatal("pair count limit bypassed", err)
	}
	oversized := cloneDevice(r)
	one := oversized.Pairs[key]
	one.Definition.Unit.ScreenUID = strings.Repeat("s", MaxDeviceRecordBytes)
	delete(oversized.Pairs, key)
	oversized.Pairs[PairKey(one.Definition.Unit.Namespace, one.Definition.Unit.ScreenUID)] = one
	if err := s.writeDevice(t.Context(), oversized, false); !errors.Is(err, ErrCapacity) {
		t.Fatal("serialized record size limit bypassed", err)
	}
	if !equalCorner(deviceSnapshot(t, s), r) {
		t.Fatal("capacity failure lost retained pairs")
	}
}

func TestAmbiguousDecisionRetryCompletesDirectorySync(t *testing.T) {
	s, r, key, m, png := stateFixture(t)
	candidate, err := s.PublishCandidate(t.Context(), r, key, m, png, render.StateResult{NextState: json.RawMessage(`true`)})
	if err != nil {
		t.Fatal(err)
	}
	r = deviceSnapshot(t, s)
	p := statePending(r, key, candidate, manifestRef(t, candidate), "ambiguous")
	if err := s.PrepareDelivery(t.Context(), r, p); err != nil {
		t.Fatal(err)
	}
	prepared := deviceSnapshot(t, s)
	injected := errors.New("directory sync failed")
	s.writeFault = func(at string) error {
		if at == "corners/rename" {
			return injected
		}
		return nil
	}
	if err := s.ResolveDelivery(t.Context(), prepared, true); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	s = restartState(t, s)
	s.writeFault = func(at string) error {
		if at == "corners/directory-sync" {
			return injected
		}
		return nil
	}
	if err := s.ResolveDelivery(t.Context(), prepared, true); !errors.Is(err, injected) {
		t.Fatal("observed decision bypassed durability confirmation", err)
	}
	s.writeFault = nil
	if err := s.ResolveDelivery(t.Context(), prepared, true); err != nil {
		t.Fatal(err)
	}
	if deviceSnapshot(t, s).Pairs[key].Generation != 1 {
		t.Fatal("retry consumed twice")
	}
}
