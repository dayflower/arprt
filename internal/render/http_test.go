package render

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/dayflower/arprt/internal/imageprocessing"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var og = Target{Width: 800, Height: 480, Palette: "gray4"}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("../../testdata/png/target-gray4.png")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func alterChunk(b []byte, kind string, change func([]byte)) []byte {
	b = append([]byte(nil), b...)
	for pos := 8; pos < len(b); {
		n := int(binary.BigEndian.Uint32(b[pos : pos+4]))
		if string(b[pos+4:pos+8]) == kind {
			change(b[pos+8 : pos+8+n])
			binary.BigEndian.PutUint32(b[pos+8+n:], crc32.ChecksumIEEE(b[pos+4:pos+8+n]))
			return b
		}
		pos += n + 12
	}
	return b
}
func TestPNGContract(t *testing.T) {
	b := fixture(t)
	if err := ValidatePNG(b, og); err != nil {
		t.Fatal(err)
	}
	indexed, err := os.ReadFile("../../testdata/png/target-indexed-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePNG(indexed, og); err != nil {
		t.Fatal(err)
	}
	badCRC := append([]byte(nil), b...)
	badCRC[29] ^= 1
	for name, v := range map[string][]byte{
		"truncated": b[:40], "trailing": append(append([]byte(nil), b...), 0), "crc": badCRC, "oversized": bytes.Repeat([]byte{0}, 90000),
		"8bit": alterChunk(b, "IHDR", func(v []byte) { v[8] = 8 }), "rgb": alterChunk(b, "IHDR", func(v []byte) { v[9] = 2 }), "alpha": alterChunk(b, "IHDR", func(v []byte) { v[9] = 4 }), "interlaced": alterChunk(b, "IHDR", func(v []byte) { v[12] = 1 }),
		"wrong-size": alterChunk(b, "IHDR", func(v []byte) { binary.BigEndian.PutUint32(v, 801) }), "bad-pixels": alterChunk(b, "IDAT", func(v []byte) { v[len(v)/2] ^= 1 }),
		"bad-palette": alterChunk(indexed, "PLTE", func(v []byte) { v[3] = 84 }),
	} {
		t.Run(name, func(t *testing.T) {
			if ValidatePNG(v, og) == nil {
				t.Fatal("invalid PNG accepted")
			}
		})
	}
	if ValidatePNG(b, Target{Width: 799, Height: 480, Palette: "gray4"}) == nil {
		t.Fatal("mismatched target accepted")
	}
}
func TestHTTPGETContract(t *testing.T) {
	b := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("width") != "800" || len(q["width"]) != 1 || q.Get("height") != "480" || q.Get("palette") != "gray4" || q.Get("rotation") != "270" || len(q["rotation"]) != 1 || len(q["key"]) != 2 {
			t.Errorf("query not normalized: %v", q)
		}
		if r.Method != "GET" || r.Header.Get("Accept") != "image/png" || r.Header.Get("Access-Token") != "" || r.ContentLength > 0 {
			t.Error("GET privacy contract violated")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer server.Close()
	target := og
	target.Rotation = 270
	out, err := (HTTPGET{URL: server.URL + "?key=a&key=b&width=1&width=2&palette=bw&rotation=90&rotation=180"}).Execute(context.Background(), Request{Output: CurrentTarget(target).Output, Target: target})
	expected, normErr := imageprocessing.Normalize(t.Context(), b, CurrentTarget(target).ImageTarget(), imageprocessing.Policy{})
	if err != nil || normErr != nil || !bytes.Equal(out.PNG, expected) || bytes.Equal(out.PNG, b) == imageprocessing.Gray4Native4 {
		t.Fatal(err)
	}
}

func TestHTTPFailures(t *testing.T) {
	b := fixture(t)
	for _, method := range []string{"GET", "POST"} {
		for _, mode := range []string{"redirect", "status", "type", "large", "slow", "malformed"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch mode {
					case "redirect":
						http.Redirect(w, r, "/redirected", http.StatusFound)
					case "status":
						w.WriteHeader(500)
					case "type":
						w.Header().Set("Content-Type", "text/plain")
						_, _ = w.Write(b)
					case "large":
						w.Header().Set("Content-Type", "image/png")
						_, _ = w.Write(bytes.Repeat([]byte{0}, 90000))
					case "slow":
						_, _ = io.Copy(io.Discard, r.Body)
						<-r.Context().Done()
					case "malformed":
						w.Header().Set("Content-Type", "image/png")
						_, _ = w.Write(b[:40])
					}
				}))
				defer s.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				var executor Executor = HTTPGET{URL: s.URL + "?secret=operator-value"}
				input := Request{Output: CurrentTarget(og).Output, Target: og}
				if method == "POST" {
					executor = HTTPPOST{URL: s.URL + "?secret=operator-value"}
					input.Context = &Context{Version: "v1", RenderTarget: og, Device: Device{Name: "test"}}
				}
				_, err := executor.Execute(ctx, input)
				if err == nil {
					t.Fatal("invalid response accepted")
				}
				if strings.Contains(err.Error(), "operator-value") {
					t.Fatal("URL leaked through error")
				}
			})
		}
	}
}

func TestHTTPPOSTContextContract(t *testing.T) {
	b := fixture(t)
	contextJSON, err := os.ReadFile("../../testdata/render-context/render-context-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Context
	if err := json.Unmarshal(contextJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var got Context
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(got)
		expected, _ := json.Marshal(snapshot)
		if !bytes.Equal(encoded, expected) || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "image/png" {
			t.Error("POST did not send complete context and content negotiation")
		}
		q := r.URL.Query()
		if q.Get("width") != "800" || len(q["width"]) != 1 || q.Get("height") != "480" || q.Get("palette") != "gray4" || q.Get("rotation") != "90" || len(q["rotation"]) != 1 || len(q["key"]) != 2 {
			t.Error("query target diverged from context or removed custom values")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer s.Close()
	executor := HTTPPOST{URL: s.URL + "?width=1&width=2&height=3&palette=bw&rotation=180&rotation=270&key=a&key=b"}
	if _, err := executor.Execute(context.Background(), Request{Output: CurrentTarget(snapshot.RenderTarget).Output, Target: snapshot.RenderTarget, Context: &snapshot}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []Request{{Target: og}, {Target: Target{Width: 799, Height: 480, Palette: "gray4"}, Context: &snapshot}, {Target: og, Context: &Context{Version: "v2", RenderTarget: og, Device: Device{Name: "device"}}}} {
		if _, err := executor.Execute(context.Background(), invalid); err == nil {
			t.Fatal("invalid POST context accepted")
		}
	}
	if calls != 1 {
		t.Fatal("invalid context reached renderer")
	}
}

func TestHTTPPOSTExcludesRendererState(t *testing.T) {
	png := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if bytes.Contains(data, []byte("rendererState")) || bytes.Contains(data, []byte("private-cursor")) {
			t.Error("Job state leaked to HTTP renderer")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer server.Close()
	c := Context{Version: "v1", RenderTarget: og, Device: Device{Name: "fixture"}, RendererState: json.RawMessage(`{"cursor":"private-cursor"}`)}
	if _, err := (HTTPPOST{URL: server.URL}).Execute(t.Context(), Request{Output: CurrentTarget(og).Output, Target: og, Context: &c}); err != nil {
		t.Fatal(err)
	}
	if len(c.RendererState) == 0 {
		t.Fatal("HTTP execution mutated original Job snapshot")
	}
}
