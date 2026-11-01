package byos

import (
	"context"
	"errors"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
)

// rendererCandidate selects authoritative work, never inferring candidate
// identity from PNG bytes. expected binds asynchronous Job results to an attempt.
func (s *Server) rendererCandidate(ctx context.Context, d *api.ArprtDevice, selected selectedDelivery, expected *artifact.Metadata) (deliveryImage, bool, error) {
	if selected.replay != nil || selected.screen.Spec.Source.Job == nil || selected.screen.Spec.Source.Job.State == nil {
		return deliveryImage{}, false, nil
	}
	r, err := s.Repository.Corner(ctx, string(d.UID))
	if err != nil {
		return deliveryImage{}, false, err
	}
	if r.Pending != nil {
		return deliveryImage{}, false, artifact.ErrPending
	}
	key := artifact.PairKey(d.Namespace, string(selected.screen.UID))
	p, ok := r.Pairs[key]
	unit := artifact.Unit{Namespace: d.Namespace, DeviceUID: string(d.UID), ScreenUID: string(selected.screen.UID), Target: selected.target, Output: selected.output}
	if !ok || p.DetachedAt != nil || p.Definition.Unit != unit || p.Definition.InputRevision != selected.revision || p.CandidateRef == "" {
		return deliveryImage{}, false, nil
	}
	m, err := s.Repository.Candidate(ctx, p.CandidateRef)
	if err != nil {
		return deliveryImage{}, false, err
	}
	if expected != nil && (m.Metadata.AttemptID != expected.AttemptID || m.Metadata.Sequence != expected.Sequence || m.Metadata.ContextDigest != expected.ContextDigest) {
		return deliveryImage{}, false, nil
	}
	return deliveryImage{metadata: m.Metadata, candidate: &candidateSource{ref: p.CandidateRef, key: key, epoch: p.Epoch, generation: p.Generation}}, true, nil
}

// rendererFallback treats latest as a derivative cache. A consumed manifest
// remains the authoritative fallback when maintenance removed that pointer.
func (s *Server) rendererFallback(ctx context.Context, d *api.ArprtDevice, selected selectedDelivery) (artifact.Metadata, error) {
	unit := artifact.Unit{Namespace: d.Namespace, DeviceUID: string(d.UID), ScreenUID: string(selected.screen.UID), Target: selected.target, Output: selected.output}
	m, err := s.Repository.Latest(ctx, unit)
	if !errors.Is(err, artifact.ErrNotFound) || selected.screen.Spec.Source.Job.State == nil {
		return m, err
	}
	r, err := s.Repository.Corner(ctx, string(d.UID))
	if err != nil {
		return artifact.Metadata{}, err
	}
	if r.Pending != nil {
		return artifact.Metadata{}, artifact.ErrPending
	}
	p := r.Pairs[artifact.PairKey(d.Namespace, string(selected.screen.UID))]
	if p.StateRef == "" {
		return artifact.Metadata{}, artifact.ErrNotFound
	}
	manifest, err := s.Repository.Candidate(ctx, p.StateRef)
	if err != nil {
		return artifact.Metadata{}, err
	}
	if manifest.Metadata.Unit != unit {
		return artifact.Metadata{}, artifact.ErrNotFound
	}
	return manifest.Metadata, nil
}
