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
