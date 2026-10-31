package byos

import (
	"testing"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRepeatedTurnPersistenceAndCornerRecovery(t *testing.T) {
	for _, recovery := range []string{"completed", "previous", "proposed"} {
		t.Run(recovery, func(t *testing.T) {
			s, c, root := newTestServer(t, "http://unused.invalid")
			b, calls := pngFixture(t), 0
			fixtureGET(s, b, &calls)
			var original api.ArprtScreen
			if err := c.Get(t.Context(), client.ObjectKey{Namespace: "arprt", Name: "screen"}, &original); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"second", "last"} {
				sc := original.DeepCopy()
				sc.Name, sc.UID, sc.ResourceVersion = name, types.UID("uid-"+name), ""
				if err := c.Create(t.Context(), sc); err != nil {
					t.Fatal(err)
				}
			}
			p := &api.ArprtPlaylist{ObjectMeta: metav1.ObjectMeta{Namespace: "arprt", Name: "repeated", UID: "playlist-1"}, Spec: api.ArprtPlaylistSpec{Screens: []api.LocalReference{{Name: "screen"}, {Name: "second"}, {Name: "screen"}, {Name: "last"}}}}
			if err := c.Create(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			d := loadDevice(t, c)
			d.Spec.ScreenRef, d.Spec.PlaylistRef = nil, &api.LocalReference{Name: p.Name}
			if err := c.Update(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			first := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
			if first.Status != 0 {
				t.Fatal(first)
			}
			if recovery == "completed" {
				for _, index := range []int32{1, 2} {
					expireTurn(t, c)
					got := decodeDisplay(t, request(s, "GET", devicePath+"/api/display", auth(), nil))
					d = loadDevice(t, c)
					r, err := s.Repository.Delivery(t.Context(), string(d.UID))
					if got != first || d.Status.ScreenCycle.EntryIndex != index || err != nil || !artifact.EqualCycle(r.Cycle, d.Status.ScreenCycle) {
						t.Fatal("equal image lost successful position", got, r, err)
					}
				}
			} else {
				// Position alone distinguishes these complete turns. Recovery must
				// neither infer commitment from the digest nor lose either position.
				d = loadDevice(t, c)
				previous := d.Status.ScreenCycle.DeepCopy()
				proposed := previous.DeepCopy()
				proposed.EntryIndex = 2
				corner, err := s.Repository.Corner(t.Context(), string(d.UID))
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Repository.PrepareCorner(t.Context(), corner, artifact.CornerPending{Next: corner.Current.Next(), Previous: previous, Proposed: proposed}); err != nil {
					t.Fatal(err)
				}
				if recovery == "proposed" {
					d.Status.ScreenCycle = proposed
					if err := c.Status().Update(t.Context(), d); err != nil {
						t.Fatal(err)
					}
				}
			}
			turn := loadDevice(t, c).Status.ScreenCycle.DeepCopy()
			repo, err := artifact.NewPVC(root)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewServer(s.Config, c, c, repo)
			if err != nil {
				t.Fatal(err)
			}
			fixtureGET(restarted, b, &calls)
			before := calls
			if got := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", auth(), nil)); got != first || calls != before || !artifact.EqualCycle(turn, loadDevice(t, c).Status.ScreenCycle) {
				t.Fatal("restart replay changed turn or rendered", got)
			}
			corner, err := repo.Corner(t.Context(), "device-1")
			wantRevision := uint64(0)
			if recovery == "proposed" {
				wantRevision = 1
			}
			if err != nil || corner.Pending != nil || corner.Revision != wantRevision {
				t.Fatal("position-only corner transaction recovered incorrectly", corner, err)
			}
			if ok, err := repo.Authorized(t.Context(), "device-1", turn.ImageDigest); err != nil || !ok {
				t.Fatal("restart lost issued image access", ok, err)
			}
			expireTurn(t, c)
			if got := decodeDisplay(t, request(restarted, "GET", devicePath+"/api/display", auth(), nil)); got.Status != 0 || loadDevice(t, c).Status.ScreenCycle.EntryIndex != turn.EntryIndex+1 {
				t.Fatal("restart advanced from wrong occurrence", got, loadDevice(t, c).Status.ScreenCycle)
			}
		})
	}
}
