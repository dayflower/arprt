package imageprocessing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type Fit string
type Dither string

const (
	Contain        Fit    = "contain"
	Stretch        Fit    = "stretch"
	Atkinson       Dither = "atkinson"
	FloydSteinberg Dither = "floyd-steinberg"
	Bayer          Dither = "bayer"
	None           Dither = "none"

	// Bump these when processing, encoding or canonical colors change. These
	// identities are internal and must not enter renderer context v1.
	ProcessingVersion = "normalize-v1"
	PaletteVersion    = "palette-v1"
)

type Policy struct {
	Fit    Fit    `json:"fit"`
	Dither Dither `json:"dither"`
}

func (p Policy) Normalize() (Policy, error) {
	if p.Fit == "" {
		p.Fit = Contain
	}
	if p.Dither == "" {
		p.Dither = Atkinson
	}
	if p.Fit != Contain && p.Fit != Stretch {
		return Policy{}, errors.New("unknown image fit")
	}
	switch p.Dither {
	case Atkinson, FloydSteinberg, Bayer, None:
	default:
		return Policy{}, errors.New("unknown image dithering")
	}
	return p, nil
}

// Revision canonicalizes omitted defaults and includes all processing semantics.
// Integration adds this identity to Screen freshness and cycle replay, rather
// than the target key, so a prior-policy image can remain a validated fallback.
func (p Policy) Revision() (string, error) {
	p, err := p.Normalize()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(struct {
		Policy                       Policy
		Processing, Encoder, Palette string
	}{p, ProcessingVersion, EncoderVersion, PaletteVersion})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
