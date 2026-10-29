package byos

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/credentials"
	"github.com/dayflower/arprt/internal/deviceidentity"
	"github.com/dayflower/arprt/internal/observe"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var imageName = regexp.MustCompile(`^[a-f0-9]{64}\.png$`)

type admissionTimeKey struct{}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		observe.Rejected.Inc()
		reply(w, 503, 503)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ctx, cancel := context.WithDeadline(context.WithValue(r.Context(), admissionTimeKey{}, startedAt), startedAt.Add(13*time.Second))
	defer cancel()
	r = r.WithContext(ctx)
	name, endpoint, ok := s.route(r)
	route := endpoint
	if strings.HasPrefix(route, "images/") {
		route = "image"
	}
	if !ok {
		route = "unknown"
	}
	response := &responseWriter{ResponseWriter: w, status: 200}
	w = response
	defer func() {
		result := "success"
		if response.status >= 400 {
			result = "failure"
		}
		observe.Requests.WithLabelValues(route, result).Inc()
	}()
	if !ok {
		reply(w, 404, 404)
		return
	}
	method := http.MethodGet
	if endpoint == "api/log" {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		reply(w, 405, 405)
		return
	}
	var d api.ArprtDevice
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Config.Namespace, Name: name}, &d); err != nil || !d.DeletionTimestamp.IsZero() {
		if err != nil && !apierrors.IsNotFound(err) {
			reply(w, 503, 503)
		} else {
			reply(w, 404, 404)
		}
		return
	}
	if endpoint == "setup-logo.png" {
		pngResponse(w, setupLogo)
		return
	}
	h, err := Headers(r.Header)
	if err != nil {
		reply(w, 400, 400)
		return
	}
	id, err := HardwareID(h["id"])
	if err != nil {
		reply(w, 403, 403)
		return
	}
	// Setup admission is independent of identity registration and credentials.
	if endpoint == "api/setup" && !s.Config.EnableSetup {
		reply(w, 403, 403)
		return
	}
	var key string
	if endpoint == "api/setup" {
		if err := s.Identity.ValidateRegistration(ctx, &d, id, true); err != nil {
			s.identityError(w, err)
			return
		}
		key, err = credentials.Ensure(ctx, s.Reader, s.Client, &d)
		if err != nil {
			code := http.StatusServiceUnavailable
			if errors.Is(err, credentials.ErrInvalid) {
				code = http.StatusUnauthorized
			}
			reply(w, code, code)
			return
		}
	} else {
		key, err = s.key(ctx, &d)
	}
	if err != nil || (endpoint != "api/setup" && (h["access-token"] == "" || !equal(key, h["access-token"]))) {
		reply(w, 401, 401)
		return
	}
	if endpoint == "api/setup" || endpoint == "api/display" {
		err = s.Identity.Register(ctx, &d, id, endpoint == "api/setup")
	} else {
		err = s.Identity.Check(ctx, &d, id)
	}
	if err != nil {
		s.identityError(w, err)
		return
	}
	switch {
	case endpoint == "api/setup":
		s.setup(w, r, &d, id)
	case endpoint == "api/display":
		s.display(w, r, &d, Observe(h))
	case endpoint == "api/log":
		// Parse one bounded JSON value and discard it without logging arbitrary data.
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			reply(w, 413, 413)
			return
		}
		if !json.Valid(b) {
			reply(w, 400, 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(endpoint, "images/"):
		s.image(w, r, &d, endpoint)
	default:
		reply(w, 404, 404)
	}
}

func (s *Server) route(r *http.Request) (string, string, bool) {
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || !CleanPath(r.URL.Path) {
		return "", "", false
	}
	// RequestURI preserves escapes that URL.Path decoding otherwise erases.
	if strings.Contains(r.RequestURI, "%") {
		return "", "", false
	}
	path := r.URL.Path
	prefix := s.base.Path
	if s.Config.ProxyStripsPrefix {
		prefix = ""
	}
	prefix += "/devices/" + s.Config.Namespace + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(path, prefix), "/", 2)
	if len(parts) != 2 || len(validation.IsDNS1123Subdomain(parts[0])) > 0 || parts[0] == "" {
		return "", "", false
	}
	e := parts[1]
	if e != "api/setup" && e != "api/display" && e != "api/log" && e != "setup-logo.png" && !(strings.HasPrefix(e, "images/") && imageName.MatchString(strings.TrimPrefix(e, "images/"))) {
		return "", "", false
	}
	return parts[0], e, true
}
func (s *Server) deviceURL(name string) string {
	return s.base.String() + "/devices/" + s.Config.Namespace + "/" + name
}
func equal(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func (s *Server) identityError(w http.ResponseWriter, err error) {
	code := http.StatusServiceUnavailable
	if errors.Is(err, deviceidentity.ErrMismatch) || errors.Is(err, deviceidentity.ErrUnbound) {
		code = http.StatusForbidden
	}
	reply(w, code, code)
}

func reply(w http.ResponseWriter, httpStatus, status int) {
	jsonResponse(w, httpStatus, struct {
		Status int `json:"status"`
	}{status})
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	b, _ := json.Marshal(value)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func pngResponse(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

// Only the normalized route and result reach metrics, never URL/header values.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
