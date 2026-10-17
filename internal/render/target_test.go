package render

import (
	"github.com/dayflower/arprt/internal/imageprocessing"
	"testing"
)

func TestResolvedTargetIdentity(t *testing.T) {
	base := ResolvedTarget{Target: Target{Width: 1200, Height: 825, Palette: "spectra6"}, Output: OutputConstraints{PNGByteLimit: 750000, Compatibility: OutputCompatibilityVersion}}
	first, err := base.Fingerprint(imageprocessing.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := base.Fingerprint(imageprocessing.Policy{Fit: imageprocessing.Contain, Dither: imageprocessing.Atkinson})
	if err != nil || first != explicit {
		t.Fatal("default processing fragmented identity", err)
	}
	for _, change := range []func(*ResolvedTarget){
		func(t *ResolvedTarget) { t.Target.Rotation = 90 },
		func(t *ResolvedTarget) { t.Target.Width++ },
		func(t *ResolvedTarget) { t.Target.Palette = "gray16" },
		func(t *ResolvedTarget) { t.Output.PNGByteLimit-- },
	} {
		next := base
		change(&next)
		fingerprint, err := next.Fingerprint(imageprocessing.Policy{})
		if err != nil || fingerprint == first {
			t.Fatal("effective target failed to invalidate replay", err)
		}
	}
	changed, err := base.Fingerprint(imageprocessing.Policy{Dither: imageprocessing.None})
	if err != nil || changed == first {
		t.Fatal("processing did not invalidate replay", err)
	}
	for _, p := range []string{"bw", "bwry"} {
		old := base
		old.Target.Palette = p
		if old.Target.Validate() != nil || old.Validate() == nil {
			t.Fatal("legacy semantic palette was relabeled as supported output")
		}
	}
}
