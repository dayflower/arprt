package dashboard

import (
	"context"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type pageObjectKey struct {
	key  client.ObjectKey
	kind string
}

// pageReader only caches screen/playlist reads during one bounded response.
// It is not an authorization cache or a snapshot across Kubernetes and the PVC.
type pageReader struct {
	client.Reader
	objects map[pageObjectKey]client.Object
	screens *api.ArprtScreenList
}

func (r *pageReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	k := pageObjectKey{key: key}
	switch obj.(type) {
	case *api.ArprtScreen:
		k.kind = "screen"
	case *api.ArprtPlaylist:
		k.kind = "playlist"
	default:
		return r.Reader.Get(ctx, key, obj, opts...)
	}
	if previous := r.objects[k]; previous != nil {
		switch v := obj.(type) {
		case *api.ArprtScreen:
			*v = *previous.(*api.ArprtScreen).DeepCopy()
		case *api.ArprtPlaylist:
			*v = *previous.(*api.ArprtPlaylist).DeepCopy()
		}
		return nil
	}
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	r.objects[k] = obj.DeepCopyObject().(client.Object)
	return nil
}

func (r *pageReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if screens, ok := list.(*api.ArprtScreenList); ok {
		if r.screens == nil {
			if err := r.Reader.List(ctx, screens, opts...); err != nil {
				return err
			}
			r.screens = screens.DeepCopy()
		} else {
			*screens = *r.screens.DeepCopy()
		}
		return nil
	}
	return r.Reader.List(ctx, list, opts...)
}
