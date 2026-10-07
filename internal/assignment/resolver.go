// Package assignment resolves desired assignments within one namespace.
package assignment

import (
	"context"
	"errors"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrDiscoveryDeferred = errors.New("automatic discovery requires Phase 5")
	ErrPlaylistDeferred  = errors.New("playlist assignment requires Phase 5")
)

type Resolver interface {
	Resolve(context.Context, *api.TrmnlDevice) (*api.TrmnlScreen, error)
}

type Explicit struct{ Reader client.Reader }

func (r Explicit) Resolve(ctx context.Context, device *api.TrmnlDevice) (*api.TrmnlScreen, error) {
	if device.Spec.PlaylistRef != nil {
		return nil, ErrPlaylistDeferred
	}
	if device.Spec.ScreenRef == nil {
		return nil, ErrDiscoveryDeferred
	}
	var screen api.TrmnlScreen
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: device.Namespace, Name: device.Spec.ScreenRef.Name}, &screen)
	return &screen, err
}
