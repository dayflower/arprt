package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBasePathConfiguration(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	for input, want := range map[string]string{"": "/", "/": "/", "/dashboard": "/dashboard/", "/dashboard/": "/dashboard/", "/ops/dashboard": "/ops/dashboard/", "/A_1.~-/": "/A_1.~-/"} {
		c := s.config
		c.BasePath = input
		got, err := NewServer(c, s.reader, s.store)
		if err != nil || got.config.BasePath != want {
			t.Fatalf("base path %q: %v, %v", input, got, err)
		}
	}
	for _, input := range []string{"dashboard", "https://example.test/dashboard/", "//", "//example.test/", "/a//", "/a//b", "/a/./b", "/a/../b", "/.", "/..", "/a?b", "/a#b", "/a%2fb", "/a\\b", "/a b", "/日本語", "/a\n"} {
		c := s.config
		c.BasePath = input
		if _, err := NewServer(c, s.reader, s.store); err == nil {
			t.Errorf("accepted base path %q", input)
		}
	}
}

func TestBasePathRouting(t *testing.T) {
	for _, basePath := range []string{"/", "/dashboard/", "/ops/dashboard/"} {
		t.Run(basePath, func(t *testing.T) {
			original, repo, reader, device, screen := fixture(t)
			record := recordFixture(t, repo, reader, device, screen)
			config := original.config
			config.BasePath = basePath
			s, err := NewServer(config, reader, repo)
			if err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range []string{"", "assets/dashboard.js", "assets/dashboard.css", "assets/mark.svg", "api/devices?limit=1"} {
				req := httptest.NewRequest(http.MethodGet, basePath+endpoint, nil)
				path := req.URL.Path
				w := httptest.NewRecorder()
				s.ServeHTTP(w, req)
				if w.Code != 200 {
					t.Fatalf("%s: %d", endpoint, w.Code)
				}
				if req.URL.Path != path {
					t.Fatal("mutated caller request")
				}
				req.Method = http.MethodPost
				w = httptest.NewRecorder()
				s.ServeHTTP(w, req)
				if w.Code != 405 || w.Header().Get("Allow") != "GET" {
					t.Fatal("accepted write", endpoint, w.Code)
				}
			}
			html := get(t, s, basePath).Body.String()
			for _, reference := range []string{`href="assets/dashboard.css"`, `src="assets/dashboard.js"`, `href="./"`} {
				if !strings.Contains(html, reference) {
					t.Fatal("missing relative reference", reference)
				}
			}
			var page DevicePage
			if err := json.Unmarshal(get(t, s, basePath+"api/devices").Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			want := basePath + "api/devices/desk/preview/" + record.ImageDigest + ".png"
			if len(page.Devices) != 1 || page.Devices[0].Image == nil || page.Devices[0].Image.PreviewURL != want {
				t.Fatalf("incorrect preview URL: %+v", page)
			}
			if w := get(t, s, want); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
				t.Fatal("preview failed", w.Code)
			}
			for _, endpoint := range []string{"unknown", "assets/", "assets/../server.go", "assets/%64ashboard.js", "api/%64evices", "api//devices"} {
				if w := get(t, s, basePath+endpoint); w.Code != 404 {
					t.Fatal("accepted unknown path", endpoint, w.Code)
				}
			}
			if basePath == "/" {
				return
			}
			bare := strings.TrimSuffix(basePath, "/")
			w := get(t, s, bare)
			if w.Code != 308 || w.Header().Get("Location") != basePath {
				t.Fatal("missing canonical redirect", w.Code, w.Header())
			}
			if w := get(t, s, bare+"?q=1"); w.Code != 400 {
				t.Fatal("redirect accepted query", w.Code)
			}
			w = httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, bare, nil))
			if w.Code != 405 {
				t.Fatal("redirect accepted write", w.Code)
			}
			for _, path := range []string{"/", "/api/devices", "/assets/dashboard.js", bare + "-other/", bare + "%2f", basePath + "../api/devices"} {
				if w := get(t, s, path); w.Code != 404 {
					t.Fatal("escaped base path", path, w.Code)
				}
			}
		})
	}
}
