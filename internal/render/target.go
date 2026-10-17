package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/dayflower/arprt/internal/imageprocessing"
)

const OutputCompatibilityVersion = "output-v1"

// OutputConstraints is internal delivery identity, separate from context v1.
// A zero value explicitly denotes legacy delivery, never current defaults.
type OutputConstraints struct {
	PNGByteLimit  int    `json:"pngByteLimit"`
	Compatibility string `json:"compatibility"`
}

func (o OutputConstraints) IsZero() bool { return o == (OutputConstraints{}) }

// ResolvedTarget contains only effective capabilities; model, firmware and
// provenance do not fragment equivalent GET work or persisted target keys.
type ResolvedTarget struct {
	Target Target            `json:"target"`
	Output OutputConstraints `json:"output"`
}

func (t ResolvedTarget) ImageTarget() imageprocessing.Target {
	return imageprocessing.Target{Width: int(t.Target.Width), Height: int(t.Target.Height), Palette: imageprocessing.Palette(t.Target.Palette), PNGByteLimit: t.Output.PNGByteLimit}
}

func (t ResolvedTarget) Validate() error {
	if err := t.Target.Validate(); err != nil {
		return err
	}
	if t.Output.Compatibility != OutputCompatibilityVersion {
		return errors.New("unknown output compatibility version")
	}
	return t.ImageTarget().Validate()
}

// ValidateOutput never changes stored bytes. Legacy output is checked with its
// original rules; current delivery uses the complete-file compatibility table.
func (t ResolvedTarget) ValidateOutput(ctx context.Context, b []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.Output.IsZero() {
		return ValidatePNG(b, t.Target)
	}
	if err := t.Validate(); err != nil {
		return err
	}
	d, err := imageprocessing.Decode(ctx, b)
	if err != nil {
		return err
	}
	ok, err := d.Compatible(ctx, t.ImageTarget())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("PNG is incompatible with resolved output target")
	}
	return nil
}

// Fingerprint identifies a replay turn, including policy and semantic versions.
// It is deliberately separate from the target identity used for stale fallback.
func (t ResolvedTarget) Fingerprint(policy imageprocessing.Policy) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	revision, err := policy.Revision()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(struct {
		Target     ResolvedTarget
		Processing string
	}{t, revision})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// CurrentTarget constructs the historical OG output contract for fixtures and
// callers with an explicitly selected 90000-byte budget. Device resolution lives
// in devicestate; production delivery always carries its resolved constraints.
func CurrentTarget(t Target) ResolvedTarget {
	return ResolvedTarget{Target: t, Output: OutputConstraints{PNGByteLimit: 90000, Compatibility: OutputCompatibilityVersion}}
}

// ProcessPNG is the common HTTP/Job input-to-publication boundary. The caller
// owns runtime admission from before reception until durable publication. The
// processing deadline covers validation, conversion and final compatibility.
func ProcessPNG(ctx context.Context, source []byte, target ResolvedTarget, policy imageprocessing.Policy) ([]byte, error) {
	var output []byte
	err := imageprocessing.Process(ctx, func(work context.Context) error {
		var err error
		output, err = imageprocessing.Normalize(work, source, target.ImageTarget(), policy)
		if err != nil {
			return err
		}
		return target.ValidateOutput(work, output)
	})
	return output, err
}
