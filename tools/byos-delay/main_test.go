package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDelayedCancellation(t *testing.T) {
	called := false
	handler := delayed(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}), "/api/display", time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/display", nil))
	if called {
		t.Fatal("disconnected request reached the upstream")
	}
}

func TestPollingOverride(t *testing.T) {
	for _, test := range []struct {
		path string
		code int
		body string
		want string
	}{
		{"/api/display", 200, `{"status":0,"refresh_rate":300,"filename":"fixture.png"}`, `{"filename":"fixture.png","refresh_rate":5,"status":0}`},
		{"/api/display", 200, `{"status":202}`, `{"status":202}`},
		{"/api/display", 401, `{"status":401}`, `{"status":401}`},
		{"/images/fixture.png", 200, "unchanged image bytes", "unchanged image bytes"},
		{"/api/setup", 200, `{"api_key":"synthetic-token"}`, `{"api_key":"synthetic-token"}`},
	} {
		response := &http.Response{StatusCode: test.code, Header: make(http.Header),
			Request: httptest.NewRequest(http.MethodGet, test.path, nil), Body: io.NopCloser(strings.NewReader(test.body))}
		if err := pollingOverride("/api/display", 5)(response); err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != test.want {
			t.Fatalf("polling override: got=%q want=%q error=%v", body, test.want, err)
		}
	}
}

func TestDelayedForwarding(t *testing.T) {
	const path = "/arprt/devices/arprt/delay-probe/api/display"
	const delay = 25 * time.Millisecond
	started := time.Now()
	handler := delayed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Header.Get("Access-Token") != "synthetic-token" {
			t.Error("proxy path or authentication header changed")
		}
		deadline, ok := r.Context().Deadline()
		if !ok || deadline.After(started.Add(29*time.Second)) || deadline.Before(started.Add(27*time.Second)) {
			t.Error("upstream work has no bounded deadline")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}), path, delay)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	req.Header.Set("Access-Token", "synthetic-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || time.Since(started) < delay {
		t.Fatal("delay or upstream authentication response was lost")
	}
}

func TestOtherRoutesBypassDelay(t *testing.T) {
	for _, path := range []string{"/api/setup", "/images/fixture.png"} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		called := false
		handler := delayed(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		}), "/api/display", time.Hour)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		cancel()
		if !called {
			t.Fatal("non-display route was delayed")
		}
	}
}
