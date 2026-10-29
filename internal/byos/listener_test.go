package byos

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func startListener(t *testing.T, handler http.Handler) string {
	t.Helper()
	conn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	l := &Listener{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- l.serve(ctx, conn) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(17 * time.Second):
			t.Error("listener did not shut down")
		}
	})
	return "http://" + conn.Addr().String()
}

func TestListenerTimeouts(t *testing.T) {
	srv := (&Listener{}).httpServer(t.Context())
	if srv.WriteTimeout != 15*time.Second || srv.ReadHeaderTimeout != 3*time.Second || srv.ReadTimeout != 5*time.Second || srv.IdleTimeout != 30*time.Second || srv.MaxHeaderBytes != 16<<10 {
		t.Fatalf("unexpected listener limits: %+v", srv)
	}
}

// Real elapsed time and TCP writes exercise net/http's socket deadline. A
// ResponseRecorder or a scaled timeout would not exercise the 15-second limit.
func TestListenerDelayedDisplay(t *testing.T) {
	t.Parallel()
	for _, delay := range []time.Duration{10 * time.Second, 13 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			t.Parallel()
			url := startListener(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(delay):
					reply(w, http.StatusOK, 202)
				case <-r.Context().Done():
				}
			}))
			client := &http.Client{Timeout: 20 * time.Second}
			defer client.CloseIdleConnections()
			started := time.Now()
			resp, err := client.Get(url + devicePath + "/api/display")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || string(body) != `{"status":202}` || time.Since(started) < delay {
				t.Fatalf("delayed response: status=%d body=%q error=%v elapsed=%s", resp.StatusCode, body, err, time.Since(started))
			}
		})
	}
}

type canceledReader struct {
	client.Reader
	entered chan struct{}
	done    chan error
}

func (r canceledReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	close(r.entered)
	<-ctx.Done()
	r.done <- ctx.Err()
	return ctx.Err()
}

func TestListenerRequestCancellation(t *testing.T) {
	t.Parallel()
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "request-deadline", true: "client-disconnect"}[disconnect], func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestServer(t, "http://unused.invalid")
			entered := make(chan struct{})
			done := make(chan error, 1)
			s.Reader = canceledReader{Reader: s.Reader, entered: entered, done: done}
			url := startListener(t, s)
			ctx, cancel := context.WithTimeout(t.Context(), 16*time.Second)
			defer cancel()
			if disconnect {
				go func() {
					select {
					case <-entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+devicePath+"/api/display", nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: 16 * time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			if !disconnect && (err != nil || resp.StatusCode != http.StatusServiceUnavailable) {
				t.Fatalf("expired application request: response=%v error=%v", resp, err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("request work was not canceled")
				}
			case <-time.After(time.Second):
				t.Fatal("request work outlived its deadline or disconnect")
			}
		})
	}
}

func TestListenerExpiredWrite(t *testing.T) {
	t.Parallel()
	written := make(chan error, 1)
	url := startListener(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(16 * time.Second):
			reply(w, http.StatusOK, 202)
			written <- http.NewResponseController(w).Flush()
		case <-r.Context().Done():
			written <- r.Context().Err()
		}
	}))
	client := &http.Client{Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Get(url + devicePath + "/api/display")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("response beyond the write deadline was delivered")
	}
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("expired socket write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("delayed handler did not finish")
	}
}

func TestListenerAuthenticatedDisplay(t *testing.T) {
	s, _, _ := newTestServer(t, "http://unused.invalid")
	url := startListener(t, s)
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	for _, test := range []struct {
		token string
		code  int
	}{
		{"wrong", http.StatusUnauthorized},
		{token, http.StatusOK},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+devicePath+"/api/display", nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range auth() {
			req.Header.Set(k, v)
		}
		req.Header.Set("Access-Token", test.token)
		// An unresolved model returns the ordinary pending response without DNS
		// or renderer execution, after the real authentication path.
		req.Header.Set("Model", "unreviewed")
		req.Header.Del("Width")
		req.Header.Del("Height")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result DisplayResponse
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil || resp.StatusCode != test.code || (test.code == http.StatusOK && result.Status != 202) {
			t.Fatalf("authenticated response: status=%d result=%+v error=%v", resp.StatusCode, result, err)
		}
	}
}
