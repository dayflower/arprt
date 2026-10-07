package render

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
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
	b, e := os.ReadFile("../../testdata/phase0/target-gray4.png")
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
	indexed, err := os.ReadFile("../../testdata/phase2/target-indexed-gray4.png")
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
		if q.Get("width") != "800" || len(q["width"]) != 1 || q.Get("height") != "480" || q.Get("palette") != "gray4" || len(q["key"]) != 2 {
			t.Errorf("query not normalized: %v", q)
		}
		if r.Method != "GET" || r.Header.Get("Accept") != "image/png" || r.Header.Get("Access-Token") != "" || r.ContentLength > 0 {
			t.Error("GET privacy contract violated")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	}))
	defer server.Close()
	out, err := (HTTPGET{URL: server.URL + "?key=a&key=b&width=1&width=2&palette=bw"}).Execute(context.Background(), Request{Target: og})
	if err != nil || !bytes.Equal(out.PNG, b) {
		t.Fatal(err)
	}
	for _, mode := range []string{"redirect", "status", "type", "large", "slow"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, server.URL, 302)
				case "status":
					w.WriteHeader(500)
				case "type":
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write(b)
				case "large":
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(bytes.Repeat([]byte{0}, 90000))
				case "slow":
					<-r.Context().Done()
				}
			}))
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, err := (HTTPGET{URL: s.URL + "?secret=operator-value"}).Execute(ctx, Request{Target: og})
			if err == nil {
				t.Fatal("invalid response accepted")
			}
			if strings.Contains(err.Error(), "operator-value") {
				t.Fatal("URL leaked through error")
			}
		})
	}
}
