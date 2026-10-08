package artifact

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestIdentityPersistenceAndConcurrentClaims(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			id := "02:00:00:00:00:01"
			if i%2 == 1 {
				id = "02:00:00:00:00:02"
			}
			err := store.Bind(ctx, "device/uid", id)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrIdentityConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 8 {
		t.Fatalf("multiple identities accepted: %d", accepted.Load())
	}
	id, err := store.Binding(ctx, "device/uid")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Binding(ctx, "device/uid"); err != nil || got != id {
		t.Fatal("binding lost on reopen", err)
	}
	if err := reopened.Bind(ctx, "device/uid", id); err != nil {
		t.Fatal("idempotent retry failed", err)
	}
	if _, err := reopened.Binding(ctx, "new-device-uid"); !errors.Is(err, ErrNotFound) {
		t.Fatal("replacement inherited identity", err)
	}
	if err := reopened.Bind(ctx, "new-device-uid", "02:00:00:00:00:03"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "02:aa:00:00:00:01", "02:00:00:00:00:01\n"} {
		if err := reopened.Bind(ctx, "invalid", invalid); err == nil {
			t.Fatal("invalid identity persisted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := reopened.Bind(cancelled, "cancelled", id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
