package jobrender

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/render"
	"golang.org/x/sys/unix"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fixture(t *testing.T) (*Reconciler, *api.TrmnlDevice, *api.TrmnlScreen, *time.Time) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	d := &api.TrmnlDevice{ObjectMeta: metav1.ObjectMeta{Name: "device", Namespace: "arprt", UID: "device-uid", Generation: 1}, Spec: api.TrmnlDeviceSpec{ScreenRef: &api.LocalReference{Name: "screen"}}, Status: api.TrmnlDeviceStatus{Display: &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}, Battery: &api.BatteryStatus{Voltage: ptr.To(3.91)}}}
	s := &api.TrmnlScreen{ObjectMeta: metav1.ObjectMeta{Name: "screen", Namespace: "arprt", UID: "screen-uid", Generation: 1}, Spec: api.TrmnlScreenSpec{RefreshInterval: "10s", Source: api.ScreenSource{Job: &api.JobSource{Renderer: api.RendererContainer{Image: "busybox:1.37.0"}}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.TrmnlDevice{}, &api.TrmnlScreen{}, &batchv1.Job{}, &corev1.Pod{}).WithObjects(d, s).Build()
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	r := &Reconciler{Client: c, Reader: c, Store: store, Repository: repo, Config: Config{Namespace: "arprt", PVC: "data", DeadlineSeconds: 20, MaxActive: 2}, Now: func() time.Time { return now }}
	return r, d, s, &now
}

func step(t *testing.T, r *Reconciler, d *api.TrmnlDevice) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func load(t *testing.T, r *Reconciler, d *api.TrmnlDevice) *state {
	t.Helper()
	st, err := r.Store.load(d.Namespace, d.Name, "screen")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func finish(t *testing.T, r *Reconciler, d *api.TrmnlDevice, output []byte, failed bool) *batchv1.Job {
	t.Helper()
	ctx := context.Background()
	st := load(t, r, d)
	var job batchv1.Job
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: "arprt-render-" + st.Active.ID}, &job); err != nil {
		t.Fatal(err)
	}
	job.UID = types.UID(st.Active.ID)
	if err := r.Client.Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if output != nil {
		if err := os.WriteFile(filepath.Join(r.Store.run(st.Active.ID), "output", filepath.Base(st.Active.Source.ImagePath)), output, 0600); err != nil {
			t.Fatal(err)
		}
	}
	kind := batchv1.JobComplete
	if failed {
		kind = batchv1.JobFailed
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: kind, Status: corev1.ConditionTrue}}
	job.Status.CompletionTime = ptr.To(metav1.NewTime(r.now()))
	if err := r.Client.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if !failed {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name, Namespace: d.Namespace, Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true)}}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "renderer", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}}}
		if err := r.Client.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	return &job
}

func png(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/phase0/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func deletePods(t *testing.T, r *Reconciler) {
	t.Helper()
	var pods corev1.PodList
	if err := r.Client.List(context.Background(), &pods); err != nil {
		t.Fatal(err)
	}
	for i := range pods.Items {
		if err := r.Client.Delete(context.Background(), &pods.Items[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRestartSchedulingAndSnapshots(t *testing.T) {
	r, d, _, now := fixture(t)
	step(t, r, d)
	initial := load(t, r, d)
	var contextData render.Context
	b, err := os.ReadFile(filepath.Join(r.Store.run(initial.Active.ID), "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &contextData); err != nil {
		t.Fatal(err)
	}
	if contextData.Device.Name != d.Name || *contextData.Device.Battery.Voltage != 3.91 || hash(b) != initial.Active.ContextDigest {
		t.Fatal("snapshot contract")
	}
	if err = r.Client.Get(context.Background(), client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	d.Status.Battery.Voltage = ptr.To(3.7)
	if err = r.Client.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	if load(t, r, d).Active.ID != initial.Active.ID {
		t.Fatal("telemetry duplicated work")
	}
	store, _ := NewStore(r.Store.Root)
	repo, _ := artifact.NewPVC(r.Store.Root)
	restarted := &Reconciler{Client: r.Client, Reader: r.Reader, Store: store, Repository: repo, Config: r.Config, Now: r.Now}
	step(t, restarted, d)
	if load(t, restarted, d).Active.ID != initial.Active.ID {
		t.Fatal("restart duplicated work")
	}
	finish(t, restarted, d, png(t), false)
	step(t, restarted, d)
	m, err := repo.Latest(context.Background(), initial.Unit)
	if err != nil || m.Sequence != 1 || m.ContextDigest != hash(b) {
		t.Fatalf("publication: %+v %v", m, err)
	}
	if st := load(t, restarted, d); st.Active != nil || st.Due.Sub(*now) != 10*time.Second {
		t.Fatal("due time not persisted")
	}
	deletePods(t, restarted)
	step(t, restarted, d)
	if load(t, restarted, d).Active != nil {
		t.Fatal("poll started fresh work")
	}
	*now = now.Add(10 * time.Second)
	step(t, restarted, d)
	st := load(t, restarted, d)
	b, _ = os.ReadFile(filepath.Join(store.run(st.Active.ID), "context.json"))
	_ = json.Unmarshal(b, &contextData)
	if st.Active.Sequence != 2 || *contextData.Device.Battery.Voltage != 3.7 {
		t.Fatal("interval snapshot not current")
	}
	// Replay collection after publication but before terminal state save.
	finish(t, restarted, d, png(t), false)
	st.Active.Sequence = 2
	if err = store.save(st); err != nil {
		t.Fatal(err)
	}
	meta := artifact.Metadata{Unit: st.Unit, ScreenGeneration: st.ScreenGeneration, InputRevision: st.Revision, Sequence: 2, AttemptID: st.Active.ID, ContextDigest: st.Active.ContextDigest, RenderedAt: *now}
	published, err := repo.Publish(context.Background(), meta, png(t))
	if err != nil {
		t.Fatal(err)
	}
	// Reopen both persistence owners at the publish-before-active-clear boundary.
	store, err = NewStore(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err = artifact.NewPVC(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	restarted = &Reconciler{Client: r.Client, Reader: r.Reader, Store: store, Repository: repo, Config: r.Config, Now: r.Now}
	step(t, restarted, d)
	latest, err := repo.Latest(context.Background(), st.Unit)
	if err != nil || latest != published || load(t, restarted, d).Active != nil {
		t.Fatal("collection replay failed", err)
	}
}

func TestFailuresBackoffAndObsoleteCompletion(t *testing.T) {
	for _, test := range []struct {
		name   string
		output []byte
		failed bool
		reason string
	}{{"missing", nil, false, "CollectionFailed"}, {"partial", []byte("partial PNG"), false, "CollectionFailed"}, {"failed", nil, true, "JobFailed"}} {
		t.Run(test.name, func(t *testing.T) {
			r, d, s, now := fixture(t)
			step(t, r, d)
			st := load(t, r, d)
			old := artifact.Metadata{Unit: st.Unit, ScreenGeneration: st.ScreenGeneration, InputRevision: st.Revision, AttemptID: "prior", Sequence: 1, RenderedAt: *now}
			if _, err := r.Repository.Publish(context.Background(), old, png(t)); err != nil {
				t.Fatal(err)
			}
			finish(t, r, d, test.output, test.failed)
			step(t, r, d)
			deletePods(t, r)
			step(t, r, d)
			st = load(t, r, d)
			if st.Active != nil || st.Reason != test.reason || st.Due.Sub(*now) != 5*time.Second {
				t.Fatalf("backoff %+v", st)
			}
			var current api.TrmnlDevice
			_ = r.Client.Get(context.Background(), client.ObjectKeyFromObject(d), &current)
			if c := meta.FindStatusCondition(current.Status.Conditions, "RenderReady"); c == nil || c.Reason != test.reason {
				t.Fatal("missing failure condition")
			}
			m, err := r.Repository.Latest(context.Background(), st.Unit)
			if err != nil || m.AttemptID != "prior" {
				t.Fatal("previous output replaced")
			}
			step(t, r, d)
			if load(t, r, d).Active != nil {
				t.Fatal("tight retry")
			}
			// A changed definition triggers a new attempt without waiting for backoff.
			if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			s.Generation++
			s.Spec.Source.Job.Renderer.Image = "busybox:new"
			if err = r.Client.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			if load(t, r, d).Active == nil {
				t.Fatal("spec did not trigger work")
			}
		})
	}
	r, d, s, _ := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	finish(t, r, d, png(t), false)
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	s.Generation++
	s.Spec.Source.Job.Renderer.Image = "changed"
	if err := r.Client.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	deletePods(t, r)
	step(t, r, d)
	if _, err := r.Repository.Latest(context.Background(), st.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("obsolete image published", err)
	}
	if load(t, r, d).Active.ID == st.Active.ID {
		t.Fatal("obsolete attempt reused")
	}
}

func TestUnknownTargetConcurrencyAndHang(t *testing.T) {
	r, d, _, now := fixture(t)
	d.Status.Display = nil
	if err := r.Client.Status().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	step(t, r, d)
	if load(t, r, d) != nil {
		t.Fatal("invented target")
	}
	d.Status.Display = &api.DisplayStatus{Width: 800, Height: 480, Palette: "gray4"}
	_ = r.Client.Status().Update(context.Background(), d)
	r.Config.MaxActive = 1
	step(t, r, d)
	old := load(t, r, d)
	peer := d.DeepCopy()
	peer.Name = "peer"
	peer.UID = "peer-uid"
	peer.ResourceVersion = ""
	if err := r.Client.Create(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	step(t, r, peer)
	var jobs batchv1.JobList
	_ = r.Client.List(context.Background(), &jobs)
	if len(jobs.Items) != 1 {
		t.Fatal("concurrency exceeded")
	}
	*now = now.Add(141 * time.Second)
	step(t, r, d)
	if st := load(t, r, d); st.Active != nil || st.Reason != "JobTimedOut" {
		t.Fatalf("hang not bounded: %+v", st)
	}
	if _, err := r.Repository.Latest(context.Background(), old.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("hang published")
	}
}

func TestMountsAndCollectionSafety(t *testing.T) {
	for _, paths := range [][2]string{{"/trmnl/context.json", "/trmnl/output.png"}, {"/input/context.json", "/work/weather.png"}, {"/work/input/context.json", "/work/weather.png"}} {
		r, d, s, _ := fixture(t)
		s.Spec.Source.Job.ContextPath = paths[0]
		s.Spec.Source.Job.ImagePath = paths[1]
		_ = r.Client.Update(context.Background(), s)
		step(t, r, d)
		st := load(t, r, d)
		job := r.Config.job(st)
		pod := job.Spec.Template.Spec
		container := pod.Containers[0]
		if *pod.AutomountServiceAccountToken || *pod.SecurityContext.RunAsUser != 1000 || *container.SecurityContext.AllowPrivilegeEscalation || container.VolumeMounts[1].MountPath != paths[0] || container.VolumeMounts[0].MountPath != filepath.Dir(paths[1]) || !container.VolumeMounts[1].ReadOnly || len(pod.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) != 1 {
			t.Fatal("mount/security contract")
		}
		out := filepath.Join(r.Store.run(st.Active.ID), "output", filepath.Base(paths[1]))
		if err := os.Symlink("/etc/passwd", out); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Store.collect(st.Active, st.Unit.Target); err == nil {
			t.Fatal("accepted escaped symlink")
		}
		_ = os.Remove(out)
		if err := unix.Mkfifo(out, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Store.collect(st.Active, st.Unit.Target); err == nil {
			t.Fatal("accepted FIFO")
		}
	}
}

func TestReplacementAssignmentAndTargetRejectOldJobs(t *testing.T) {
	for _, change := range []string{"device-uid", "screen-uid", "assignment", "target"} {
		t.Run(change, func(t *testing.T) {
			r, d, s, _ := fixture(t)
			step(t, r, d)
			prior := load(t, r, d)
			finish(t, r, d, png(t), false)
			ctx := context.Background()
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				t.Fatal(err)
			}
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "device-uid":
				if err := r.Client.Delete(ctx, d); err != nil {
					t.Fatal(err)
				}
				d.UID = "new-device"
				d.ResourceVersion = ""
				if err := r.Client.Create(ctx, d); err != nil {
					t.Fatal(err)
				}
			case "screen-uid":
				if err := r.Client.Delete(ctx, s); err != nil {
					t.Fatal(err)
				}
				s.UID = "new-screen"
				s.ResourceVersion = ""
				if err := r.Client.Create(ctx, s); err != nil {
					t.Fatal(err)
				}
			case "assignment":
				d.Spec.ScreenRef = &api.LocalReference{Name: "missing"}
				d.Generation++
				if err := r.Client.Update(ctx, d); err != nil {
					t.Fatal(err)
				}
			case "target":
				d.Status.Display = nil
				if err := r.Client.Status().Update(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
			step(t, r, d)
			deletePods(t, r)
			step(t, r, d)
			if _, err := r.Repository.Latest(ctx, prior.Unit); !errors.Is(err, artifact.ErrNotFound) {
				t.Fatal("obsolete Job published", err)
			}
			if st := load(t, r, d); st != nil && st.Active != nil && st.Active.ID == prior.Active.ID {
				t.Fatal("old attempt reused")
			}
		})
	}
}

func TestPreparedAttemptAndRunRetention(t *testing.T) {
	r, d, _, now := fixture(t)
	step(t, r, d)
	st := load(t, r, d)
	// Crash after persistence but before Kubernetes creation: deterministic Job
	// recreation uses exactly the durable snapshot and attempt identity.
	st.Active.Submitted = false
	if err := r.Store.save(st); err != nil {
		t.Fatal(err)
	}
	job := r.Config.job(st)
	if err := r.Client.Delete(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := artifact.NewPVC(r.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	r = &Reconciler{Client: r.Client, Reader: r.Reader, Store: store, Repository: repo, Config: r.Config, Now: r.Now}
	step(t, r, d)
	expected := *st.Active
	expected.Submitted = true
	if recovered := load(t, r, d); !reflect.DeepEqual(recovered.Active, &expected) {
		t.Fatal("prepared attempt duplicated")
	}
	var jobs batchv1.JobList
	_ = r.Client.List(context.Background(), &jobs)
	if len(jobs.Items) != 1 {
		t.Fatal("prepared attempt not created")
	}
	orphan := "00000000000000000000000000000000"
	if err := os.MkdirAll(r.Store.run(orphan), 0750); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	if err := r.maintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Store.run(st.Active.ID)); err != nil {
		t.Fatal("live run removed", err)
	}
	if _, err := os.Stat(r.Store.run(orphan)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan run not collected")
	}
}

func TestTopologyRejectsUnsupportedPVCAndMount(t *testing.T) {
	r, _, _, _ := fixture(t)
	ctx := context.Background()
	cfg := r.Config
	cfg.ControllerPod = "controller"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: cfg.PVC, Namespace: cfg.Namespace}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: cfg.ControllerPod, Namespace: cfg.Namespace, Labels: map[string]string{"app.kubernetes.io/name": "arprt"}}, Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "controller", VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: r.Store.Root}}}}, Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cfg.PVC}}}}}}
	if err := r.Client.Create(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if err := r.Client.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateTopology(ctx, r.Reader, r.Store.Root); err != nil {
		t.Fatal(err)
	}
	pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
	_ = r.Client.Update(ctx, pvc)
	if err := cfg.ValidateTopology(ctx, r.Reader, r.Store.Root); err == nil {
		t.Fatal("RWOP was accepted")
	}
	pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	_ = r.Client.Update(ctx, pvc)
	pod.Spec.Containers[0].VolumeMounts[0].SubPath = "subtree"
	_ = r.Client.Update(ctx, pod)
	if err := cfg.ValidateTopology(ctx, r.Reader, r.Store.Root); err == nil {
		t.Fatal("mismatched PVC root was accepted")
	}
}

func TestLostJobNeverReusesPartialOutput(t *testing.T) {
	for _, submitted := range []bool{true, false} {
		r, d, _, now := fixture(t)
		step(t, r, d)
		st := load(t, r, d)
		st.Active.Submitted = submitted
		if err := r.Store.save(st); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.Store.run(st.Active.ID), "output", "output.png"), png(t), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.Client.Delete(context.Background(), r.Config.job(st)); err != nil {
			t.Fatal(err)
		}
		step(t, r, d)
		lost := load(t, r, d)
		if lost.Active != nil || lost.Reason != "JobLost" {
			t.Fatal("lost Job was recreated over prior output")
		}
		*now = now.Add(5 * time.Second)
		step(t, r, d)
		next := load(t, r, d)
		if next.Active == nil || next.Active.ID == st.Active.ID {
			t.Fatal("lost Job retry reused run")
		}
		if _, err := r.Repository.Latest(context.Background(), st.Unit); !errors.Is(err, artifact.ErrNotFound) {
			t.Fatal("unconfirmed output published", err)
		}
	}
}

func TestMaintenanceDropsPointersForPreviousTransport(t *testing.T) {
	r, d, s, now := fixture(t)
	ctx := context.Background()
	_, _, desired, err := r.desired(ctx, client.ObjectKeyFromObject(d))
	if err != nil {
		t.Fatal(err)
	}
	metadata := artifact.Metadata{Unit: desired.Unit, ScreenGeneration: s.Generation, InputRevision: desired.Revision, AttemptID: "success", Sequence: 1, RenderedAt: *now}
	if _, err = r.Repository.Publish(ctx, metadata, png(t)); err != nil {
		t.Fatal(err)
	}
	if err = r.maintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Repository.Latest(ctx, desired.Unit); err != nil {
		t.Fatal("current pair output collected", err)
	}
	s.Spec.Source = api.ScreenSource{HTTP: &api.HTTPSource{URL: "http://renderer/render"}}
	s.Generation++
	if err = r.Client.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err = r.maintenance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Repository.Latest(ctx, desired.Unit); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatal("former pair pointer retained")
	}
}

func TestRotationChangeReschedulesAndDiscardsObsoleteOutput(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprintf("completed-%t", completed), func(t *testing.T) {
			r, d, _, _ := fixture(t)
			step(t, r, d)
			original := load(t, r, d)
			finish(t, r, d, png(t), false)
			if completed {
				step(t, r, d)
			}
			deletePods(t, r)
			if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(d), d); err != nil {
				t.Fatal(err)
			}
			d.Spec.Rotation = 90
			d.Generation++
			if err := r.Client.Update(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			step(t, r, d)
			step(t, r, d)
			changed := load(t, r, d)
			if changed.Active == nil || changed.Active.ID == original.Active.ID || changed.Unit.Target.Rotation != 90 {
				t.Fatal("rotation change did not immediately schedule a new attempt")
			}
			contextBytes, err := os.ReadFile(filepath.Join(r.Store.run(changed.Active.ID), "context.json"))
			if err != nil {
				t.Fatal(err)
			}
			var snapshot render.Context
			if err := json.Unmarshal(contextBytes, &snapshot); err != nil || snapshot.RenderTarget != changed.Unit.Target {
				t.Fatal("rotation missing from immutable Job context", err)
			}
			if _, err := r.Repository.Latest(context.Background(), changed.Unit); !errors.Is(err, artifact.ErrNotFound) {
				t.Fatal("new rotation inherited old output", err)
			}
			_, err = r.Repository.Latest(context.Background(), original.Unit)
			if !completed && !errors.Is(err, artifact.ErrNotFound) {
				t.Fatal("obsolete Job output published", err)
			}
			if completed && err != nil {
				t.Fatal("prior completed output lost", err)
			}
			finish(t, r, d, png(t), false)
			step(t, r, d)
			if _, err := r.Repository.Latest(context.Background(), changed.Unit); err != nil {
				t.Fatal("rotated Job output was not published", err)
			}
		})
	}
}
