package jobrender

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayflower/arprt/internal/artifact"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCapacityQueuesWithoutAllocatingAttemptAndResumes(t *testing.T) {
	r, d, _, _ := fixture(t)
	repo := r.Repository.(*artifact.PVC)
	repo.MaxBytes = 1
	step(t, r, d)
	st := load(t, r, d)
	if st != nil && st.Active != nil {
		t.Fatal("full storage allocated an attempt")
	}
	entries, err := os.ReadDir(filepath.Join(r.Store.Root, "runs"))
	if err != nil || len(entries) != 0 {
		t.Fatal("full storage allocated run data", err)
	}
	var jobs batchv1.JobList
	if err := r.Reader.List(context.Background(), &jobs, client.InNamespace(d.Namespace)); err != nil || len(jobs.Items) != 0 {
		t.Fatal("full storage created Jobs", err)
	}
	repo.MaxBytes = 768 << 20
	step(t, r, d)
	if load(t, r, d).Active == nil {
		t.Fatal("space recovery did not resume scheduling")
	}
}

func TestPersistentFailureRateIsBoundedAndCanRecover(t *testing.T) {
	r, d, s, now := fixture(t)
	for i := 0; i < 8; i++ {
		step(t, r, d)
		finish(t, r, d, nil, true)
		result := step(t, r, d)
		st := load(t, r, d)
		if i >= 6 && result.RequeueAfter < 5*time.Minute {
			t.Fatal("persistent failure retry loop too fast")
		}
		*now = st.Due.Add(time.Second)
	}
	step(t, r, d)
	finish(t, r, d, png(t), false)
	step(t, r, d)
	if st := load(t, r, d); st.Failures != 0 || st.Active != nil {
		t.Fatal("successful retry did not recover", s.Name)
	}
}

func TestCorruptSchedulerFailsClosedAtStartup(t *testing.T) {
	r, d, _, _ := fixture(t)
	step(t, r, d)
	path := r.Store.path(d.Namespace, d.Name, "screen")
	if err := os.WriteFile(path, []byte(`{"active":{"id":"../../escape"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(r.Store.Root); err == nil {
		t.Fatal("unsafe scheduler record accepted")
	}
}

func TestWorstCaseFanoutHasBoundedActiveJobsAndRunDirectories(t *testing.T) {
	r, d, s, _ := fixture(t)
	ctx := context.Background()
	// 16 devices x the discovery limit of 64 = 1024 desired pairs.
	d.Spec.ScreenRef = nil
	if err := r.Client.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 64; i++ {
		next := s.DeepCopy()
		next.Name = fmt.Sprintf("screen-%02d", i)
		next.UID = types.UID(next.Name)
		next.ResourceVersion = ""
		if err := r.Client.Create(ctx, next); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 16; i++ {
		next := d.DeepCopy()
		if i > 0 {
			next.Name = fmt.Sprintf("device-%02d", i)
			next.UID = types.UID(next.Name)
			next.ResourceVersion = ""
			if err := r.Client.Create(ctx, next); err != nil {
				t.Fatal(err)
			}
		}
		step(t, r, next)
	}
	var jobs batchv1.JobList
	if err := r.Reader.List(ctx, &jobs, client.InNamespace(d.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != r.Config.MaxActive {
		t.Fatalf("fanout launched %d jobs", len(jobs.Items))
	}
	runs, err := os.ReadDir(filepath.Join(r.Store.Root, "runs"))
	if err != nil || len(runs) != r.Config.MaxActive {
		t.Fatalf("fanout allocated %d runs: %v", len(runs), err)
	}
	records, err := r.Store.records()
	if err != nil || len(records) != 1024 {
		t.Fatalf("pair count %d: %v", len(records), err)
	}
}

func TestDamagedContextCannotPublishClaimedSnapshot(t *testing.T) {
	r, d, _, _ := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	contextPath := filepath.Join(r.Store.run(st.Active.ID), "context.json")
	if err := os.Chmod(contextPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contextPath, []byte(`{"version":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	finish(t, r, d, png(t), false)
	step(t, r, d)
	if got := load(t, r, d); got.Reason != "CollectionFailed" {
		t.Fatal("damaged context was published", got.Reason)
	}
	if _, err := r.Repository.Latest(context.Background(), st.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("damaged context has artifact", err)
	}
}
