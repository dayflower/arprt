package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/dayflower/arprt/internal/overlay"
)

type derivedRecord struct {
	Version    int             `json:"version"`
	Base       Metadata        `json:"base"`
	Drawing    overlay.Drawing `json:"drawing"`
	Output     Metadata        `json:"output"`
	LastUsedAt time.Time       `json:"lastUsedAt"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// Include original success identity as well as drawing inputs. Equal bytes from
// separate units or rendering times do not merge provenance; filenames do.
func derivedKey(base Metadata, drawing overlay.Drawing) (string, error) {
	if _, err := unitKey(base.Unit); err != nil {
		return "", err
	}
	if !digestPattern.MatchString(base.ImageDigest) || !digestPattern.MatchString(base.InputRevision) || base.RenderedAt.IsZero() || base.Sequence == 0 || base.AttemptID == "" || drawing.Empty() || drawing.Version == "" || len(drawing.Version) > 64 {
		return "", errors.New("invalid derived inputs")
	}
	if _, _, _, _, err := drawing.Bounds(base.Unit.Target); err != nil {
		return "", err
	}
	base.RenderedAt = base.RenderedAt.UTC()
	b, err := json.Marshal(struct {
		Base    Metadata
		Drawing overlay.Drawing
	}{base, drawing})
	if err != nil {
		return "", err
	}
	return digest(b), nil
}
func (s *PVC) pin(imageDigest string) func() {
	s.pins[imageDigest]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.pins[imageDigest]--
			if s.pins[imageDigest] == 0 {
				delete(s.pins, imageDigest)
			}
		})
	}
}
func (s *PVC) FindDerived(ctx context.Context, base Metadata, drawing overlay.Drawing) (Metadata, func(), error) {
	key, err := derivedKey(base, drawing)
	if err != nil {
		return Metadata{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var record derivedRecord
	path := filepath.Join(s.root, "derived", key+".json")
	if err := readJSON(path, &record); err != nil {
		if errors.Is(err, ErrCorrupt) {
			err = s.discardPointer(path)
		}
		return Metadata{}, nil, err
	}
	if !validDerived(record, key) {
		return Metadata{}, nil, s.discardPointer(path)
	}
	b, err := s.Read(ctx, record.Output.ImageDigest)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrCorrupt) {
		return Metadata{}, nil, s.discardPointer(path)
	}
	if err != nil {
		return Metadata{}, nil, err
	}
	validationErr := base.Unit.ResolvedTarget().ValidateOutput(ctx, b)
	if ctx.Err() != nil {
		return Metadata{}, nil, ctx.Err()
	}
	if len(b) != record.Output.Size || validationErr != nil {
		return Metadata{}, nil, s.discardPointer(path)
	}
	_, free, err := s.Usage(ctx)
	if err != nil {
		return Metadata{}, nil, err
	}
	if free < 8192 {
		return Metadata{}, nil, ErrCapacity
	}
	var filename struct {
		Filename string `json:"filename"`
	}
	if err := readJSON(filepath.Join(s.root, "records", record.Output.ImageDigest+".json"), &filename); err != nil {
		return Metadata{}, nil, err
	}
	if filename.Filename != record.Output.Filename {
		return Metadata{}, nil, s.discardPointer(path)
	}
	record.LastUsedAt = time.Now().UTC()
	if err := writeJSON(path, record); err != nil {
		return Metadata{}, nil, err
	}
	return record.Output, s.pin(record.Output.ImageDigest), nil
}
func (s *PVC) StoreDerived(ctx context.Context, base Metadata, drawing overlay.Drawing, png []byte) (Metadata, func(), error) {
	key, err := derivedKey(base, drawing)
	if err != nil {
		return Metadata{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return Metadata{}, nil, err
	}
	if err := base.Unit.ResolvedTarget().ValidateOutput(ctx, png); err != nil {
		return Metadata{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkCapacity(ctx, int64(len(png))+16384); err != nil {
		return Metadata{}, nil, err
	}
	imageDigest, filename, err := s.storeBlob(png)
	if err != nil {
		return Metadata{}, nil, err
	}
	output := base
	output.RenderedAt = output.RenderedAt.UTC()
	output.ImageDigest = imageDigest
	output.Filename = filename
	output.Size = len(png)
	output.ContentType = "image/png"
	base.RenderedAt = base.RenderedAt.UTC()
	if err := ctx.Err(); err != nil {
		return Metadata{}, nil, err
	}
	version := 1
	if !base.Unit.Output.IsZero() {
		version = 2
	}
	if err := writeJSON(filepath.Join(s.root, "derived", key+".json"), derivedRecord{version, base, drawing, output, time.Now().UTC(), time.Now().UTC()}); err != nil {
		return Metadata{}, nil, err
	}
	return output, s.pin(imageDigest), nil
}

func validDerived(r derivedRecord, key string) bool {
	actual, err := derivedKey(r.Base, r.Drawing)
	version := 1
	if !r.Base.Unit.Output.IsZero() {
		version = 2
	}
	if err != nil || actual != key || r.Version != version || r.CreatedAt.IsZero() || r.LastUsedAt.IsZero() || !digestPattern.MatchString(r.Output.ImageDigest) || !filenamePattern.MatchString(r.Output.Filename) || r.Output.Size <= 0 || r.Output.Size >= r.Output.Unit.byteLimit() || r.Output.ContentType != "image/png" {
		return false
	}
	expected := r.Base
	expected.RenderedAt = expected.RenderedAt.UTC()
	expected.ImageDigest = r.Output.ImageDigest
	expected.Filename = r.Output.Filename
	expected.Size = r.Output.Size
	expected.ContentType = "image/png"
	actualOutput := r.Output
	actualOutput.RenderedAt = actualOutput.RenderedAt.UTC()
	return reflect.DeepEqual(expected, actualOutput)
}
