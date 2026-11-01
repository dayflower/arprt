// Package deliverycommit coordinates delivery decisions with background recovery.
package deliverycommit

import (
	"context"
	"crypto/sha256"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var locks = func() [256]chan struct{} {
	var out [256]chan struct{}
	for i := range out {
		out[i] = make(chan struct{}, 1)
	}
	return out
}()

// Lock serializes live finalization and recovery, including single-screen aborts.
// Order: BYOS gate or reconciler operation, optional image admission, decision lock,
// repository mutex. Recovery never acquires a BYOS gate or reconciler operation.
// Job waits and Kubernetes reads never hold the repository mutex.
func Lock(ctx context.Context, uid string) (func(), error) {
	key := sha256.Sum256([]byte(uid))
	lock := locks[key[0]]
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Recover must run under Lock. A complete API turn is the only cycling decision;
// single-screen work interrupted before finalization is always aborted.
func Recover(ctx context.Context, store artifact.CornerStore, reader client.Reader, uid string, key *client.ObjectKey, namespace string) (artifact.CornerRecord, error) {
	r, err := store.Corner(ctx, uid)
	if err != nil || r.Pending == nil {
		return r, err
	}
	commit := false
	if r.Pending.Proposed != nil {
		var current *api.ArprtDevice
		if key != nil {
			d := &api.ArprtDevice{}
			err := reader.Get(ctx, *key, d)
			if err != nil && !apierrors.IsNotFound(err) {
				return r, err
			}
			if err == nil && string(d.UID) == uid {
				current = d
			}
		} else {
			var devices api.ArprtDeviceList
			if err := reader.List(ctx, &devices, client.InNamespace(namespace)); err != nil {
				return r, err
			}
			for i := range devices.Items {
				if string(devices.Items[i].UID) == uid {
					current = &devices.Items[i]
					break
				}
			}
		}
		// A deleted UID has an independent terminal abort. Never transfer a decision
		// to a resource recreated with the same name.
		if current != nil {
			commit = artifact.EqualCycle(current.Status.ScreenCycle, r.Pending.Proposed)
			if !commit && !artifact.EqualCycle(current.Status.ScreenCycle, r.Pending.Previous) {
				return r, artifact.ErrCorrupt
			}
		}
	}
	if r.Pending.TransactionID != "" {
		state, ok := store.(artifact.StateStore)
		if !ok {
			return r, artifact.ErrCorrupt
		}
		err = state.ResolveDelivery(ctx, r, commit)
	} else {
		err = store.ResolveCorner(ctx, r, commit)
	}
	if err != nil {
		return r, err
	}
	return store.Corner(ctx, uid)
}

// RecoverDevice is also used before Job input snapshots, publication and reset.
func RecoverDevice(ctx context.Context, store artifact.CornerStore, reader client.Reader, uid string, key *client.ObjectKey, namespace string) (artifact.CornerRecord, error) {
	unlock, err := Lock(ctx, uid)
	if err != nil {
		return artifact.CornerRecord{}, err
	}
	defer unlock()
	return Recover(ctx, store, reader, uid, key, namespace)
}

// RecoverAll scans authoritative records, including deleted UIDs, at startup and
// maintenance. Each record is re-read under the decision lock before resolution.
func RecoverAll(ctx context.Context, store *artifact.PVC, reader client.Reader, namespace string) error {
	records, err := store.DeviceRecords(ctx)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Pending == nil {
			continue
		}
		if _, err := RecoverDevice(ctx, store, reader, r.DeviceUID, nil, namespace); err != nil {
			return err
		}
	}
	return nil
}
