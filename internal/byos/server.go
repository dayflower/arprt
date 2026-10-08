package byos

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/artifact"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/controller"
	"github.com/dayflower/arprt/internal/credentials"
	"github.com/dayflower/arprt/internal/delivery"
	"github.com/dayflower/arprt/internal/deviceidentity"
	"github.com/dayflower/arprt/internal/render"
	"github.com/dayflower/arprt/internal/screen"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed setup-logo.png
var setupLogo []byte
var imageName = regexp.MustCompile(`^[a-f0-9]{64}\.png$`)

type Server struct {
	Config     Config
	Reader     client.Reader // Uncached API reads for auth, identity and publication gates.
	Client     client.Client
	Repository artifact.Repository
	GET        *delivery.GET
	POST       *delivery.POST
	Identity   *deviceidentity.Service
	base       *url.URL
	gates      sync.Map
}

func NewServer(c Config, reader client.Reader, writer client.Client, repo artifact.Repository) (*Server, error) {
	base, err := c.Base()
	if err != nil {
		return nil, err
	}
	if len(validation.IsDNS1123Label(c.Namespace)) > 0 || c.Namespace == "" {
		return nil, errors.New("invalid BYOS namespace")
	}
	if c.RefreshSeconds == 0 {
		c.RefreshSeconds = 300
	}
	if c.RefreshSeconds < 1 || c.RefreshSeconds > 86400 {
		return nil, errors.New("device refresh must be 1..86400 seconds")
	}
	get := delivery.NewGET(repo, reader)
	return &Server{Config: c, Reader: reader, Client: writer, Repository: repo, GET: get, POST: delivery.NewPOST(get), Identity: &deviceidentity.Service{Reader: reader, Writer: writer, Store: repo}, base: base}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ctx, cancel := context.WithTimeout(r.Context(), 13*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	name, endpoint, ok := s.route(r)
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
	var d api.TrmnlDevice
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
	key, err := s.key(ctx, &d)
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
		jsonResponse(w, 200, struct {
			Status  int    `json:"status"`
			Key     string `json:"api_key"`
			ID      string `json:"friendly_id"`
			Image   string `json:"image_url"`
			Message string `json:"message"`
		}{200, key, d.Name, s.deviceURL(d.Name) + "/setup-logo.png", "Device registered with arprt"})
	case endpoint == "api/display":
		gateValue, _ := s.gates.LoadOrStore(string(d.UID), make(chan struct{}, 1))
		gate := gateValue.(chan struct{})
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		case <-ctx.Done():
			reply(w, 503, 503)
			return
		}
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
		digest := strings.TrimSuffix(strings.TrimPrefix(endpoint, "images/"), ".png")
		allowed, err := s.Repository.Authorized(ctx, string(d.UID), digest)
		if err != nil {
			reply(w, 503, 503)
			return
		}
		if !allowed {
			reply(w, 404, 404)
			return
		}
		b, err := s.Repository.Read(ctx, digest)
		if err != nil {
			reply(w, 404, 404)
			return
		}
		pngResponse(w, b)
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

func (s *Server) key(ctx context.Context, d *api.TrmnlDevice) (string, error) {
	return credentials.Read(ctx, s.Reader, d)
}

func (s *Server) identityError(w http.ResponseWriter, err error) {
	code := http.StatusServiceUnavailable
	if errors.Is(err, deviceidentity.ErrMismatch) || errors.Is(err, deviceidentity.ErrUnbound) {
		code = http.StatusForbidden
	}
	reply(w, code, code)
}

// mutate reads each retry directly and patches only status with a resourceVersion
// precondition. It refuses results belonging to replaced or edited devices.
func (s *Server) mutate(ctx context.Context, d *api.TrmnlDevice, fn func(*api.TrmnlDevice)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current api.TrmnlDevice
		if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(d), &current); err != nil {
			return err
		}
		if current.UID != d.UID || current.Generation != d.Generation || !reflect.DeepEqual(current.Spec, d.Spec) || !current.DeletionTimestamp.IsZero() {
			return artifact.ErrObsolete
		}
		before := current.DeepCopy()
		fn(&current)
		current.Status.ObservedGeneration = current.Generation
		if reflect.DeepEqual(before.Status, current.Status) {
			*d = current
			return nil
		}
		err := s.Client.Status().Patch(ctx, &current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		if err == nil {
			*d = current
		}
		return err
	})
}
func condition(d *api.TrmnlDevice, kind string, ok bool, reason, message string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: d.Generation})
}

func (s *Server) display(w http.ResponseWriter, r *http.Request, d *api.TrmnlDevice, o Observation) {
	ctx := r.Context()
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	var target render.Target
	var targetErr error
	if err := s.mutate(ctx, d, func(current *api.TrmnlDevice) {
		model, fw := "", ""
		if current.Status.Device != nil {
			model = current.Status.Device.Model
			fw = current.Status.Device.FirmwareVersion
		}
		target, model, fw, targetErr = o.Target(model, fw)
		// A slower earlier request cannot overwrite later observations.
		if current.Status.LastSeenAt != nil && current.Status.LastSeenAt.After(now.Time) {
			targetErr = artifact.ErrObsolete
			return
		}
		condition(current, "AuthenticationReady", true, "CredentialsConfigured", "Authentication Secret is usable")
		current.Status.LastSeenAt = &now
		current.Status.Device = &api.DeviceInfoStatus{Model: model, FirmwareVersion: fw}
		current.Status.Display = nil
		if targetErr == nil {
			current.Status.Display = &api.DisplayStatus{Width: target.Width, Height: target.Height, Palette: target.Palette}
		}
		if o.Voltage != nil {
			if current.Status.Battery == nil {
				current.Status.Battery = &api.BatteryStatus{}
			}
			current.Status.Battery.Voltage = o.Voltage
		}
		if o.RSSI != nil {
			current.Status.Network = &api.NetworkStatus{RSSI: o.RSSI}
		}
		condition(current, "TargetKnown", targetErr == nil, "CapabilitiesVerified", "Verified OG / 1.8.17 target")
		if targetErr != nil {
			condition(current, "TargetKnown", false, "UnknownCapabilities", "Supply a reviewed model/firmware tuple and matching dimensions")
		}
		condition(current, "TelemetryValid", len(o.Invalid) == 0, "TelemetryAccepted", "Optional telemetry parsed")
		if len(o.Invalid) > 0 {
			condition(current, "TelemetryValid", false, "InvalidTelemetry", "Invalid fields omitted: "+strings.Join(o.Invalid, ", "))
		}
	}); err != nil {
		reply(w, 503, 503)
		return
	}
	if targetErr != nil {
		s.outcome(ctx, d, now, nil, false, "UnknownCapabilities", "No verified render target")
		reply(w, 200, 202)
		return
	}
	assigned, err := (assignment.Explicit{Reader: s.Reader}).Resolve(ctx, d)
	if err != nil {
		reason, msg := "ReferenceNotFound", "Referenced screen does not exist in this namespace"
		if errors.Is(err, assignment.ErrDiscoveryDeferred) || errors.Is(err, assignment.ErrPlaylistDeferred) {
			reason, msg = "UnsupportedFeature", "Playlists and automatic discovery require Phase 5"
		} else if !apierrors.IsNotFound(err) {
			reply(w, 503, 503)
			return
		}
		s.outcome(ctx, d, now, boolPtr(false), false, reason, msg)
		reply(w, 200, 202)
		return
	}
	if assigned.Spec.Source.HTTP == nil || (assigned.Spec.Source.HTTP.Method != "" && assigned.Spec.Source.HTTP.Method != "GET" && assigned.Spec.Source.HTTP.Method != "POST") {
		s.outcome(ctx, d, now, boolPtr(false), false, "UnsupportedFeature", "Assign an HTTP GET or POST screen; Job execution requires Phase 4")
		reply(w, 200, 202)
		return
	}
	var m artifact.Metadata
	var renderErr error
	if assigned.Spec.Source.HTTP.Method == "POST" {
		var snapshot render.Context
		// mutate returned the direct status snapshot, and this explicit overlay
		// keeps current request telemetry independent of informer propagation.
		snapshot, renderErr = render.Build(d.Name, target, controller.Observation(d.Status), render.Observation{Model: o.Model, FirmwareVersion: o.Firmware, Voltage: o.Voltage, RSSI: o.RSSI})
		if renderErr == nil {
			m, renderErr = s.POST.Image(ctx, assigned, d, snapshot)
		}
	} else {
		m, renderErr = s.GET.Image(ctx, assigned, target)
	}
	// Recheck identity, assignment, definition and target after the asynchronous
	// network call. Never issue an image selected for a superseded assignment.
	var current api.TrmnlDevice
	var currentScreen api.TrmnlScreen
	if s.Reader.Get(ctx, client.ObjectKeyFromObject(d), &current) != nil || current.UID != d.UID || !reflect.DeepEqual(current.Spec, d.Spec) || current.Generation != d.Generation || !current.DeletionTimestamp.IsZero() || current.Status.Display == nil || *current.Status.Display != (api.DisplayStatus{Width: target.Width, Height: target.Height, Palette: target.Palette}) {
		reply(w, 200, 202)
		return
	}
	if s.Reader.Get(ctx, client.ObjectKeyFromObject(assigned), &currentScreen) != nil || currentScreen.UID != assigned.UID || currentScreen.Generation != assigned.Generation || !reflect.DeepEqual(currentScreen.Spec, assigned.Spec) || !currentScreen.DeletionTimestamp.IsZero() {
		reply(w, 200, 202)
		return
	}
	// Secret contents do not change device generation. Re-resolve identity and
	// credentials after rendering, before issuing an immutable image.
	if err := s.Identity.Check(ctx, &current, d.Status.HardwareID); err != nil {
		if errors.Is(err, deviceidentity.ErrDuplicate) {
			reply(w, 200, 202)
		} else {
			s.identityError(w, err)
		}
		return
	}
	key, keyErr := s.key(ctx, &current)
	headers, _ := Headers(r.Header)
	if keyErr != nil || !equal(key, headers["access-token"]) {
		reply(w, 401, 401)
		return
	}
	reason, message := "ImageReady", "Validated HTTP image available"
	if renderErr != nil {
		reason, message = "RenderFailed", "HTTP rendering failed; check renderer availability and the PNG contract"
	}
	s.screenOutcome(ctx, assigned, renderErr == nil, reason, message)
	if m.ImageDigest == "" {
		s.outcome(ctx, d, now, boolPtr(true), false, reason, message)
		reply(w, 200, 202)
		return
	}
	if err := s.Repository.Issue(ctx, string(d.UID), m.ImageDigest, time.Now()); err != nil {
		reply(w, 503, 503)
		return
	}
	if renderErr != nil {
		reason, message = "StaleImageAvailable", "Renderer failed; serving the previous compatible image"
	}
	s.outcome(ctx, d, now, boolPtr(true), true, reason, message)
	jsonResponse(w, 200, DisplayResponse{Status: 0, ImageURL: s.deviceURL(d.Name) + "/images/" + m.ImageDigest + ".png", Filename: m.Filename, RefreshRate: s.Config.RefreshSeconds, ImageURLTimeout: 10})
}
func boolPtr(b bool) *bool { return &b }
func (s *Server) outcome(ctx context.Context, d *api.TrmnlDevice, seen metav1.Time, assigned *bool, ready bool, reason, message string) {
	_ = s.mutate(ctx, d, func(current *api.TrmnlDevice) {
		if current.Status.LastSeenAt == nil || !current.Status.LastSeenAt.Equal(&seen) {
			return
		}
		if assigned != nil {
			condition(current, "AssignmentReady", *assigned, "AssignmentResolved", "Same-namespace HTTP screen resolved")
			if !*assigned {
				condition(current, "AssignmentReady", false, reason, message)
			}
		}
		condition(current, "Ready", ready, reason, message)
		if assigned != nil && *assigned {
			renderReady := ready && reason != "StaleImageAvailable"
			renderReason := "ImageReady"
			if !renderReady {
				renderReason = "RenderFailed"
			}
			condition(current, "RenderReady", renderReady, renderReason, message)
		}
	})
}
func (s *Server) screenOutcome(ctx context.Context, screenObj *api.TrmnlScreen, ready bool, reason, message string) {
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current api.TrmnlScreen
		if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(screenObj), &current); err != nil {
			return err
		}
		rev, _ := screen.InputRevision(current.Spec)
		expected, _ := screen.InputRevision(screenObj.Spec)
		if current.UID != screenObj.UID || current.Generation != screenObj.Generation || rev != expected {
			return artifact.ErrObsolete
		}
		before := current.DeepCopy()
		state := metav1.ConditionFalse
		if ready {
			state = metav1.ConditionTrue
		}
		current.Status.ObservedGeneration = current.Generation
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "Ready", Status: state, Reason: reason, Message: message, ObservedGeneration: current.Generation})
		if reflect.DeepEqual(before.Status, current.Status) {
			return nil
		}
		return s.Client.Status().Patch(ctx, &current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
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

// Listener integrates serving and graceful shutdown with the manager lifecycle.
type Listener struct {
	Address string
	Handler http.Handler
}

func (l *Listener) NeedLeaderElection() bool { return false }
func (l *Listener) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", l.Address)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: l.Handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("BYOS listener: %w", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return err
		}
		return nil
	}
}
