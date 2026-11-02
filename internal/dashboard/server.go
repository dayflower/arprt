// Package dashboard provides read-only, namespace-scoped dashboard projections.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/imageprocessing"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Config struct {
	BasePath       string
	Namespace      string
	RefreshSeconds int
}

// Server has no Kubernetes writer, renderer, identity or credential service.
// Listener lifecycle is wired separately from this package.
type Server struct {
	config Config
	reader client.Reader
	store  artifact.DeliveryReader
	slots  chan struct{}
}

func NewServer(c Config, reader client.Reader, store artifact.DeliveryReader) (*Server, error) {
	if c.Namespace == "" || len(validation.IsDNS1123Label(c.Namespace)) != 0 || reader == nil || store == nil {
		return nil, errors.New("dashboard requires a namespace, reader and delivery store")
	}
	basePath, err := normalizeBasePath(c.BasePath)
	if err != nil {
		return nil, err
	}
	c.BasePath = basePath
	if c.RefreshSeconds == 0 {
		c.RefreshSeconds = 300
	}
	if c.RefreshSeconds < 1 || c.RefreshSeconds > 86400 {
		return nil, errors.New("device refresh must be 1..86400 seconds")
	}
	return &Server{config: c, reader: reader, store: store, slots: make(chan struct{}, 16)}, nil
}

var previewPath = regexp.MustCompile(`^/api/devices/([a-z0-9][a-z0-9.-]*)/preview/([a-f0-9]{64})\.png$`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		fail(w, http.StatusServiceUnavailable)
		return
	}
	// Validate the original escaped path before removing the configured prefix.
	if r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") {
		fail(w, http.StatusNotFound)
		return
	}
	if s.config.BasePath != "/" && r.URL.Path == strings.TrimSuffix(s.config.BasePath, "/") {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			fail(w, http.StatusMethodNotAllowed)
		} else if r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail(w, http.StatusBadRequest)
		} else {
			http.Redirect(w, r, s.config.BasePath, http.StatusPermanentRedirect)
		}
		return
	}
	if !strings.HasPrefix(r.URL.Path, s.config.BasePath) {
		fail(w, http.StatusNotFound)
		return
	}
	// Clone both request and URL; callers may retain the original request.
	r = r.Clone(r.Context())
	r.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, s.config.BasePath)
	if s.serveUI(w, r) {
		return
	}
	match := previewPath.FindStringSubmatch(r.URL.Path)
	if r.URL.Path != "/api/devices" && (match == nil || len(validation.IsDNS1123Subdomain(match[1])) != 0) {
		fail(w, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(w, http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if match != nil {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail(w, http.StatusBadRequest)
			return
		}
		s.preview(ctx, w, match[1], match[2])
		return
	}
	s.devices(ctx, w, r)
}

type DevicePage struct {
	Devices     []Device  `json:"devices"`
	Continue    string    `json:"continue,omitempty"`
	ObservedAt  time.Time `json:"observedAt"`
	PollSeconds int       `json:"pollSeconds"`
}

func (s *Server) devices(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	query, err := parsePage(r)
	if err != nil {
		fail(w, http.StatusBadRequest)
		return
	}
	var list api.ArprtDeviceList
	if err := s.reader.List(ctx, &list, client.InNamespace(s.config.Namespace), client.Limit(query.limit), client.Continue(query.next)); err != nil {
		if apierrors.IsResourceExpired(err) {
			fail(w, http.StatusGone)
		} else {
			fail(w, http.StatusServiceUnavailable)
		}
		return
	}
	if len(list.Items) > int(query.limit) {
		// The reader must honor server-side pagination (use an uncached reader).
		fail(w, http.StatusServiceUnavailable)
		return
	}
	page := DevicePage{Devices: []Device{}, Continue: list.Continue, ObservedAt: time.Now().UTC(), PollSeconds: s.config.RefreshSeconds}
	// Sharing a read cache bounds repeated screen/playlist reads within a page.
	reader := &pageReader{Reader: s.reader, objects: map[pageObjectKey]client.Object{}}
	for i := range list.Items {
		d := &list.Items[i]
		if d.Namespace != s.config.Namespace || !d.DeletionTimestamp.IsZero() {
			continue
		}
		row, err := s.device(ctx, reader, d, page.ObservedAt)
		if err != nil {
			fail(w, http.StatusServiceUnavailable)
			return
		}
		page.Devices = append(page.Devices, row)
	}
	respond(w, http.StatusOK, page)
}

type pageQuery struct {
	limit int64
	next  string
}

func parsePage(r *http.Request) (pageQuery, error) {
	// Query() ignores malformed escapes; ParseQuery must reject them instead.
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return pageQuery{}, err
	}
	for key, values := range q {
		if (key != "limit" && key != "continue") || len(values) != 1 {
			return pageQuery{}, errors.New("invalid page query")
		}
	}
	p := pageQuery{limit: 20, next: q.Get("continue")}
	if values, ok := q["limit"]; ok {
		p.limit, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || p.limit < 1 || p.limit > 100 {
			return pageQuery{}, errors.New("page limit must be 1..100")
		}
	}
	if len(p.next) > 8192 {
		return pageQuery{}, errors.New("page cursor is too long")
	}
	return p, nil
}

func (s *Server) preview(ctx context.Context, w http.ResponseWriter, name, digest string) {
	key := client.ObjectKey{Namespace: s.config.Namespace, Name: name}
	var d api.ArprtDevice
	if err := s.reader.Get(ctx, key, &d); err != nil || !d.DeletionTimestamp.IsZero() {
		if err != nil && !apierrors.IsNotFound(err) {
			fail(w, http.StatusServiceUnavailable)
		} else {
			fail(w, http.StatusNotFound)
		}
		return
	}
	err := imageprocessing.RuntimeAdmission.Do(ctx, func(ctx context.Context) error {
		b, err := s.store.ReadDeliveryPreview(ctx, string(d.UID), digest)
		if errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrCorrupt) {
			fail(w, http.StatusNotFound)
			return nil
		}
		if err != nil {
			return err
		}
		var current api.ArprtDevice
		if err := s.reader.Get(ctx, key, &current); err != nil {
			if apierrors.IsNotFound(err) {
				fail(w, http.StatusNotFound)
				return nil
			}
			return err
		}
		if current.UID != d.UID || !current.DeletionTimestamp.IsZero() {
			fail(w, http.StatusNotFound)
			return nil
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return nil
	})
	if err != nil {
		fail(w, http.StatusServiceUnavailable)
	}
}

func fail(w http.ResponseWriter, status int) {
	respond(w, status, struct {
		Error string `json:"error"`
	}{http.StatusText(status)})
}

func respond(w http.ResponseWriter, status int, value any) {
	b, _ := json.Marshal(value)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
