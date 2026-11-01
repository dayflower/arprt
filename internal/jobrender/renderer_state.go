package jobrender

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/devicestate"
	"github.com/dayflower/arprt/internal/screen"
)

var errPendingDelivery = artifact.ErrPending

// Lock order is reconciler operation, image admission (during collection), then
// repository. No device gate is acquired here. API reads and Job waits never
// occur under the repository mutex. Pending delivery blocks mutations until the
// delivery owner resolves it; collection must not infer a decision from images.
func (r *Reconciler) rendererRecord(ctx context.Context, st *state, configure bool) (artifact.CornerRecord, error) {
	store, ok := r.Repository.(artifact.StateStore)
	if !ok {
		return artifact.CornerRecord{}, errors.New("renderer state requires a state repository")
	}
	record, err := store.Corner(ctx, st.DeviceUID)
	if err != nil {
		return record, err
	}
	if record.Pending != nil {
		return record, errPendingDelivery
	}
	if !configure {
		return record, nil
	}
	// InputRevision includes normalized processing, overlay and reset settings;
	// Unit adds the resolved target and output constraints, including rotation.
	b, err := json.Marshal(struct {
		Unit     artifact.Unit
		Revision string
	}{st.Unit, st.Revision})
	if err != nil {
		return record, err
	}
	return store.ConfigurePair(ctx, record, artifact.PairDefinition{Unit: st.Unit, InputRevision: st.Revision, Compatibility: hash(b)})
}

// syncRendererPair shares reset/departure ordering between watch and maintenance.
func (r *Reconciler) syncRendererPair(ctx context.Context, previous, wanted *state, d *api.ArprtDevice, s *api.ArprtScreen) error {
	if previous != nil && (wanted == nil || previous.DeviceUID != wanted.DeviceUID || previous.Unit.ScreenUID != wanted.Unit.ScreenUID) {
		var replacement *state
		// A still-assigned HTTP definition is a reset, not a temporary departure.
		if wanted == nil && d != nil && s != nil && s.Spec.Source.Job == nil && d.DeletionTimestamp.IsZero() && s.DeletionTimestamp.IsZero() && string(d.UID) == previous.DeviceUID && string(s.UID) == previous.Unit.ScreenUID {
			revision, err := screen.InputRevision(s.Spec)
			if err != nil {
				return err
			}
			resolved := devicestate.Resolved(d)
			if resolved.Validate() == nil {
				copy := *previous
				copy.Revision = revision
				copy.Unit.Target, copy.Unit.Output = resolved.Target, resolved.Output
				replacement = &copy
			}
		}
		if err := r.retireRenderer(ctx, previous, replacement); err != nil {
			return err
		}
	}
	if wanted == nil {
		return nil
	}
	if s.Spec.Source.Job.State != nil {
		_, err := r.rendererRecord(ctx, wanted, true)
		return err
	}
	return r.retireRenderer(ctx, wanted, wanted)
}

func (r *Reconciler) rendererCandidate(ctx context.Context, st *state) (artifact.Metadata, error) {
	record, err := r.rendererRecord(ctx, st, false)
	if err != nil {
		return artifact.Metadata{}, err
	}
	pair, ok := record.Pairs[artifact.PairKey(st.Namespace, st.Unit.ScreenUID)]
	if !ok || pair.DetachedAt != nil || pair.Definition.Unit != st.Unit || pair.Definition.InputRevision != st.Revision || pair.CandidateRef == "" {
		return artifact.Metadata{}, nil
	}
	candidate, err := r.Repository.(artifact.StateStore).Candidate(ctx, pair.CandidateRef)
	return candidate.Metadata, err
}

func (r *Reconciler) retireRenderer(ctx context.Context, st, replacement *state) error {
	store, ok := r.Repository.(artifact.StateStore)
	if !ok {
		return nil
	}
	record, err := store.Corner(ctx, st.DeviceUID)
	if err != nil {
		return err
	}
	if record.Pending != nil {
		return errPendingDelivery
	}
	key := artifact.PairKey(st.Namespace, st.Unit.ScreenUID)
	pair, ok := record.Pairs[key]
	if !ok {
		return nil
	}
	if pair.DetachedAt != nil && (replacement == nil || pair.Definition.Unit == replacement.Unit && pair.Definition.InputRevision == replacement.Revision) {
		return nil
	}
	// Opting out is a definition reset, rather than an assignment departure.
	// Persist the new definition so opting back in cannot reuse the old state.
	if replacement != nil && replacement.DeviceUID == st.DeviceUID && replacement.Unit.ScreenUID == st.Unit.ScreenUID {
		record, err = r.rendererRecord(ctx, replacement, true)
		if err != nil {
			return err
		}
	}
	return store.DetachPair(ctx, record, key, r.now())
}

// Recover from publication before Active cleanup, including a candidate already
// consumed by delivery. Only the authoritative references establish success.
func (r *Reconciler) rendererPublished(ctx context.Context, st *state) (artifact.Metadata, error) {
	record, err := r.rendererRecord(ctx, st, false)
	if err != nil {
		return artifact.Metadata{}, err
	}
	key := artifact.PairKey(st.Namespace, st.Unit.ScreenUID)
	pair := record.Pairs[key]
	expected := st.Active.RendererPair
	if expected == nil || pair.Epoch != expected.Epoch || pair.Definition != expected.Definition || pair.DetachedAt != nil {
		return artifact.Metadata{}, nil
	}
	for _, ref := range []string{pair.CandidateRef, pair.StateRef} {
		if ref == "" {
			continue
		}
		candidate, err := r.Repository.(artifact.StateStore).Candidate(ctx, ref)
		if err != nil {
			return artifact.Metadata{}, err
		}
		m := candidate.Metadata
		if m.AttemptID == st.Active.ID && m.Sequence == st.Active.Sequence && m.ContextDigest == st.Active.ContextDigest && candidate.Generation == expected.Generation {
			confirmed, err := r.Repository.(artifact.StateStore).ConfirmCandidate(ctx, ref)
			return confirmed.Metadata, err
		}
	}
	return artifact.Metadata{}, nil
}

const candidatePollInterval = time.Minute
