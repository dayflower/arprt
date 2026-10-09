package jobrender

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSchedulerRecordKeyPolicies(t *testing.T) {
	for _, caller := range []string{"startup", "reconcile", "maintenance"} {
		for _, key := range []string{"pair", "legacy", "wrong"} {
			t.Run(caller+"/"+key, func(t *testing.T) {
				r, d, _, _ := fixture(t)
				ctx := context.Background()
				_, _, st, err := r.desired(ctx, client.ObjectKeyFromObject(d))
				if err != nil {
					t.Fatal(err)
				}
				st.Due = r.now().Add(time.Hour)
				if err := r.Store.save(st); err != nil {
					t.Fatal(err)
				}
				path := r.Store.path(st.Namespace, st.DeviceName, st.ScreenName)
				switch key {
				case "legacy":
					if err := os.Rename(path, r.Store.path(st.Namespace, st.DeviceName)); err != nil {
						t.Fatal(err)
					}
				case "wrong":
					if err := os.Rename(path, filepath.Join(r.Store.Root, "jobs", strings.Repeat("0", 64)+".json")); err != nil {
						t.Fatal(err)
					}
				}
				switch caller {
				case "startup":
					_, err = NewStore(r.Store.Root)
				case "reconcile":
					_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
				case "maintenance":
					err = r.maintenance(ctx)
				}
				wantRejection := key == "wrong" || (key == "legacy" && caller == "maintenance")
				if wantRejection {
					if err == nil || err.Error() != "invalid scheduler key" {
						t.Fatalf("key must be rejected: %v", err)
					}
				} else if err != nil {
					t.Fatalf("compatible key rejected: %v", err)
				}
			})
		}
	}
}

func TestSchedulerMigrationRejectsConflictingRecords(t *testing.T) {
	r, d, _, _ := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	b, err := os.ReadFile(r.Store.path(st.Namespace, st.DeviceName, st.ScreenName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Store.path(st.Namespace, st.DeviceName), b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(r.Store.Root); err == nil || err.Error() != "conflicting scheduler migration records" {
		t.Fatalf("conflicting migration accepted: %v", err)
	}
}

func TestSchedulerCorruptionStopsMaintenanceInEntryOrder(t *testing.T) {
	for _, order := range []string{"before", "after"} {
		for _, damage := range []string{"unreadable", "malformed", "invalid"} {
			t.Run(order+"/"+damage, func(t *testing.T) {
				r, d, _, now := fixture(t)
				ctx := context.Background()
				step(t, r, d)
				*now = now.Add(2 * time.Hour)
				st := load(t, r, d)
				jobKey := client.ObjectKey{Namespace: st.Namespace, Name: "arprt-render-" + st.Active.ID}
				// Removing the device makes this record require cancellation/removal.
				if err := r.Client.Delete(ctx, d); err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(r.Store.Root, "jobs")
				name := strings.Repeat("0", 64) + ".json"
				if order == "after" {
					name = "zzzz.json"
				}
				badPath := filepath.Join(dir, name)
				switch damage {
				case "unreadable":
					if err := os.Mkdir(badPath, 0700); err != nil {
						t.Fatal(err)
					}
				case "malformed", "invalid":
					b := []byte("{")
					if damage == "invalid" {
						b = []byte(`{}`)
					}
					if err := os.WriteFile(badPath, b, 0600); err != nil {
						t.Fatal(err)
					}
				}
				// Pending files sort first. Only old regular files are removed,
				// including names ending in .json; directories/links stay protected.
				old := filepath.Join(dir, ".pending-old.json")
				young := filepath.Join(dir, ".pending-young.json")
				pendingDir := filepath.Join(dir, ".pending-dir")
				link := filepath.Join(dir, ".pending-link")
				for _, path := range []string{old, young} {
					if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(pendingDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(old, link); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(young, r.now(), r.now()); err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{old, pendingDir} {
					when := r.now().Add(-time.Hour)
					if err := os.Chtimes(path, when, when); err != nil {
						t.Fatal(err)
					}
				}
				if err := r.maintenance(ctx); err == nil {
					t.Fatal("scheduler corruption was ignored")
				} else if damage == "malformed" && err.Error() != "corrupt scheduler metadata" {
					t.Fatalf("malformed record error changed: %v", err)
				} else if damage == "invalid" && err.Error() != "invalid scheduler record" {
					t.Fatalf("invalid record error changed: %v", err)
				}
				if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("abandoned pending file not removed before corruption: %v", err)
				}
				for _, path := range []string{young, pendingDir, link} {
					if _, err := os.Lstat(path); err != nil {
						t.Fatalf("protected pending entry removed: %s: %v", path, err)
					}
				}
				_, recordErr := os.Stat(r.Store.path(st.Namespace, st.DeviceName, st.ScreenName))
				var job batchv1.Job
				jobErr := r.Reader.Get(ctx, jobKey, &job)
				if order == "before" {
					if recordErr != nil || jobErr != nil {
						t.Fatalf("effects moved ahead of corruption: record=%v job=%v", recordErr, jobErr)
					}
				} else if !errors.Is(recordErr, os.ErrNotExist) || !apierrors.IsNotFound(jobErr) {
					t.Fatalf("corruption moved ahead of cancellation/removal: record=%v job=%v", recordErr, jobErr)
				}
			})
		}
	}
}
