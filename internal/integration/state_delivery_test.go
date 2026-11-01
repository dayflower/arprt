//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/byos"
	"github.com/dayflower/arprt/internal/deliverycommit"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The finalization fault occurs after the real API has made its commit decision.
type interruptedStateRepository struct{ artifact.Repository }

func (r interruptedStateRepository) ResolveDelivery(context.Context, artifact.CornerRecord, bool) error {
	return errors.New("injected finalization interruption")
}

func verifyRendererStateDelivery(t *testing.T, c client.Client) {
	t.Helper()
	ctx := t.Context()
	sc := &api.ArprtScreen{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "state-delivery"}, Spec: api.ArprtScreenSpec{Source: api.ScreenSource{Job: &api.JobSource{State: &api.JobState{}, Renderer: api.RendererContainer{Image: "example.invalid/renderer:v1"}}}}}
	p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: sc.Namespace, Name: sc.Name}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: sc.Name}, {Name: sc.Name}}}}
	d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: sc.Namespace, Name: sc.Name}, Spec: api.ArprtDeviceSpec{HardwareID: "02:00:00:00:00:71", AuthSecretRef: api.LocalReference{Name: sc.Name + "-auth"}, PlaylistRef: &api.LocalReference{Name: p.Name}}}
	token := strings.Repeat("A", 43)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: sc.Namespace, Name: d.Spec.AuthSecretRef.Name}, Data: map[string][]byte{"apiKey": []byte(token)}}
	for _, obj := range []client.Object{sc, p, secret, d} {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	repo, err := artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	target := render.CurrentTarget(render.Target{Width: 800, Height: 480, Palette: "gray4"})
	unit := artifact.Unit{Namespace: d.Namespace, DeviceUID: string(d.UID), ScreenUID: string(sc.UID), Target: target.Target, Output: target.Output}
	revision, err := screen.InputRevision(sc.Spec)
	if err != nil {
		t.Fatal(err)
	}
	record, err := repo.Corner(ctx, string(d.UID))
	if err != nil {
		t.Fatal(err)
	}
	record, err = repo.ConfigurePair(ctx, record, artifact.DefinePair(unit, revision))
	if err != nil {
		t.Fatal(err)
	}
	pairKey := artifact.PairKey(d.Namespace, string(sc.UID))
	png, err := os.ReadFile("../../testdata/png/target-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	publish := func(seq uint64) artifact.CandidateManifest {
		t.Helper()
		record, err = repo.Corner(ctx, string(d.UID))
		if err != nil {
			t.Fatal(err)
		}
		m, err := repo.PublishCandidate(ctx, record, pairKey, artifact.Metadata{Unit: unit, InputRevision: revision, ScreenGeneration: sc.Generation, ContextDigest: strings.Repeat("c", 64), AttemptID: time.Now().Format("150405.000000000"), Sequence: seq, RenderedAt: time.Now()}, png, render.StateResult{NextState: json.RawMessage(`{"cursor":9007199254740993}`)})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	first := publish(1)
	newServer := func(repository artifact.Repository) *byos.Server {
		t.Helper()
		s, err := byos.NewServer(byos.Config{Namespace: d.Namespace, PublicBaseURL: "https://byos.example.test"}, c, c, repository)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	poll := func(s *byos.Server) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/devices/arprt/state-delivery/api/display", nil)
		for k, v := range map[string]string{"ID": d.Spec.HardwareID, "Access-Token": token, "Model": "og", "FW-Version": "1.8.17"} {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	// Kubernetes rejects a real stale status write while the candidate remains
	// pending. Recovery must observe the complete previous turn and abort it.
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	stale := d.DeepCopy()
	d.Status.LastSeenAt = &metav1.Time{Time: time.Now()}
	if err := c.Status().Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	record, err = repo.Corner(ctx, string(d.UID))
	if err != nil {
		t.Fatal(err)
	}
	pair := record.Pairs[pairKey]
	proposed := &api.ScreenCycleStatus{CompatibilityFingerprint: strings.Repeat("a", 64), Assignment: strings.Repeat("b", 64), ScreenUID: string(sc.UID), EntryIndex: 0, InputRevision: revision, Target: api.CycleTargetStatus{Width: 800, Height: 480, Palette: "gray4"}, ImageDigest: first.Metadata.ImageDigest, Filename: first.Metadata.Filename, ServedAt: metav1.NewMicroTime(time.Now().UTC().Truncate(time.Microsecond))}
	pending := artifact.CornerPending{TransactionID: "status-conflict", RecordRevision: record.Revision, Next: record.Current, Proposed: proposed, PairKey: pairKey, CandidateRef: pair.CandidateRef, Epoch: pair.Epoch, Generation: pair.Generation, ImageDigest: first.Metadata.ImageDigest, Filename: first.Metadata.Filename}
	if err := repo.PrepareDelivery(ctx, record, pending); err != nil {
		t.Fatal(err)
	}
	if err := repo.Issue(ctx, string(d.UID), first.Metadata.ImageDigest, time.Now()); err != nil {
		t.Fatal(err)
	}
	stale.Status.ScreenCycle = proposed
	if err := c.Status().Update(ctx, stale); !apierrors.IsConflict(err) {
		t.Fatal("expected actual status conflict", err)
	}
	repo, err = artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := deliverycommit.RecoverAll(ctx, repo, c, d.Namespace); err != nil {
		t.Fatal(err)
	}
	record, err = repo.Corner(ctx, string(d.UID))
	if err != nil || record.Pending != nil || record.Pairs[pairKey].Generation != 0 || record.Pairs[pairKey].CandidateRef == "" {
		t.Fatal("conflict consumed candidate", record, err)
	}
	// A durable proposed turn followed by a PVC interruption rolls forward on
	// restart. Real API MicroTime serialization must preserve the entire decision.
	if w := poll(newServer(interruptedStateRepository{repo})); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	repo, err = artifact.NewPVC(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := deliverycommit.RecoverAll(ctx, repo, c, d.Namespace); err != nil {
		t.Fatal(err)
	}
	record, err = repo.Corner(ctx, string(d.UID))
	if err != nil || record.Pending != nil || record.Pairs[pairKey].Generation != 1 {
		t.Fatal("proposed turn did not roll forward", record, err)
	}
	second := publish(2)
	if second.Metadata.ImageDigest != first.Metadata.ImageDigest {
		t.Fatal("fixture must reuse PNG")
	}
	w := poll(newServer(repo))
	var response byos.DisplayResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Status != 0 || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	record, err = repo.Corner(ctx, string(d.UID))
	if err != nil || record.Pairs[pairKey].Generation != 1 || record.Pairs[pairKey].CandidateRef == "" {
		t.Fatal("replay consumed next candidate", record, err)
	}
	// A same-name Device has a new ownership domain. It cannot inherit a pending
	// decision or any cursor from the deleted UID.
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	previous := d.Status.ScreenCycle.DeepCopy()
	proposed = previous.DeepCopy()
	proposed.ServedAt.Time = proposed.ServedAt.Add(time.Second)
	record, err = repo.Corner(ctx, string(d.UID))
	if err != nil {
		t.Fatal(err)
	}
	pair = record.Pairs[pairKey]
	pending = artifact.CornerPending{TransactionID: "deleted-owner", RecordRevision: record.Revision, Next: record.Current, Previous: previous, Proposed: proposed, PairKey: pairKey, CandidateRef: pair.CandidateRef, Epoch: pair.Epoch, Generation: pair.Generation, ImageDigest: second.Metadata.ImageDigest, Filename: second.Metadata.Filename}
	if err := repo.PrepareDelivery(ctx, record, pending); err != nil {
		t.Fatal(err)
	}
	oldUID := d.UID
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	replacement := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: d.Name}, Spec: d.DeepCopy().Spec}
	if err := c.Delete(ctx, d); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		return apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(d), &api.ArprtDevice{}))
	})
	if err := c.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.UID == oldUID {
		t.Fatal("replacement retained UID")
	}
	if err := deliverycommit.RecoverAll(ctx, repo, c, d.Namespace); err != nil {
		t.Fatal(err)
	}
	old, err := repo.Corner(ctx, string(oldUID))
	if err != nil || old.Pending != nil || old.Pairs[pairKey].Generation != 1 {
		t.Fatal("deleted owner committed a pending candidate", old, err)
	}
	fresh, err := repo.Corner(ctx, string(replacement.UID))
	if err != nil || len(fresh.Pairs) != 0 {
		t.Fatal("replacement inherited renderer state", fresh, err)
	}
}
