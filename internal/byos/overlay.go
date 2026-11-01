package byos

import (
	"context"
	"errors"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/deliverycommit"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
	"github.com/dayflower/arprt/internal/overlay"
	"github.com/dayflower/arprt/internal/screen"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Recover under the device gate before selection or corner reads. The status
// and PVC are separate durable resources: only the complete proposed turn is
// proof that a pending playlist advancement committed.
func (s *Server) recoverCorner(ctx context.Context, d *api.ArprtDevice) (artifact.CornerRecord, error) {
	key := client.ObjectKeyFromObject(d)
	r, err := deliverycommit.RecoverDevice(ctx, s.Repository, s.Reader, string(d.UID), &key, d.Namespace)
	if err != nil {
		return r, err
	}
	var current api.ArprtDevice
	if err := s.Reader.Get(ctx, key, &current); err != nil {
		return r, err
	}
	if current.UID != d.UID {
		return r, errDisplayPending
	}
	*d = current
	return r, nil
}

// The base success and render error stay separate from the issued candidate.
// Only compositing/cache persistence failures fall back, before authorization
// and Issue. Required delivery-state failures never use this fallback.
func (s *Server) overlayImageAdmitted(ctx context.Context, d *api.ArprtDevice, o Observation, selected selectedDelivery, image deliveryImage, corner artifact.CornerRecord) (deliveryImage, func(), error) {
	release := func() {}
	if image.metadata.ImageDigest == "" {
		return image, release, nil
	}
	base, err := s.Repository.Read(ctx, image.metadata.ImageDigest)
	if err != nil {
		return image, release, err
	}
	if err := selected.resolvedTarget().ValidateOutput(ctx, base); err != nil {
		return image, release, err
	}
	image.issued = image.metadata
	// A compatible legacy base retains its bytes and rendering provenance, while
	// newly derived images use current output constraints and encoder identity.
	baseMetadata := image.metadata
	baseMetadata.Unit.Output = selected.resolvedTarget().Output
	if selected.replay != nil {
		return image, release, nil
	}
	effective := screen.EffectiveOverlay(selected.screen.Spec.Overlay)
	if effective == nil {
		return image, release, nil
	}
	actual := overlay.Corner(effective.Position)
	if effective.CycleCorners {
		actual = corner.Current
	}
	settings := overlay.Settings{}
	if effective.Margin != nil {
		settings.Margin = overlay.Margins{Top: int(effective.Margin.Top), Bottom: int(effective.Margin.Bottom), Left: int(effective.Margin.Left), Right: int(effective.Margin.Right)}
	}
	if effective.Scale != nil {
		settings.Scale = *effective.Scale
	}
	for _, item := range *effective.Items {
		settings.Items = append(settings.Items, string(item))
	}
	drawing, err := overlay.NormalizeResolved(selected.resolvedTarget(), settings, image.metadata.RenderedAt, s.location, overlay.Telemetry{Percent: o.Percent, Voltage: o.Voltage, RSSI: o.RSSI, Charging: o.Charging}, actual)
	if err != nil {
		observe.OverlayFallbacks.Inc()
		return image, release, nil
	}
	if drawing.Empty() {
		return image, release, nil
	}
	composite, unpin, err := s.Repository.FindDerived(ctx, baseMetadata, drawing)
	if errors.Is(err, artifact.ErrNotFound) {
		var bytes []byte
		bytes, err = overlay.ComposeResolved(ctx, base, selected.resolvedTarget(), drawing)
		if err == nil {
			composite, unpin, err = s.Repository.StoreDerived(ctx, baseMetadata, drawing, bytes)
		}
	}
	if err != nil {
		observe.OverlayFallbacks.Inc()
		return image, release, nil
	}
	image.issued = composite
	image.advanceCorner = effective.CycleCorners
	return image, unpin, nil
}

func (s *Server) overlayImage(ctx context.Context, d *api.ArprtDevice, o Observation, selected selectedDelivery, image deliveryImage, corner artifact.CornerRecord) (deliveryImage, func(), error) {
	release := func() {}
	err := imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
		err := imageprocessing.Process(ctx, func(work context.Context) error {
			var err error
			image, release, err = s.overlayImageAdmitted(work, d, o, selected, image, corner)
			return err
		})
		// A processing deadline during optional drawing must not discard the
		// already validated base. Enclosing cancellation still stops delivery.
		if err != nil && ctx.Err() == nil && image.issued.ImageDigest != "" {
			release()
			release = func() {}
			image.issued = image.metadata
			image.advanceCorner = false
			observe.OverlayFallbacks.Inc()
			return nil
		}
		return err
	})
	if err != nil {
		release()
		release = func() {}
	}
	return image, release, err
}
