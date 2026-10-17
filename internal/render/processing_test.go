package render

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/dayflower/arprt/internal/imageprocessing"
)

func pngChunk(kind string, body []byte) []byte {
	b := make([]byte, len(body)+12)
	binary.BigEndian.PutUint32(b, uint32(len(body)))
	copy(b[4:], kind)
	copy(b[8:], body)
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[4:len(b)-4]))
	return b
}
func animated(b []byte, shared bool) []byte {
	out := append([]byte(nil), b[:33]...)
	out = append(out, pngChunk("acTL", []byte{0, 0, 0, 1, 0, 0, 0, 0})...)
	control := make([]byte, 26)
	copy(control[4:12], b[16:24])
	if shared {
		out = append(out, pngChunk("fcTL", control)...)
	}
	out = append(out, b[33:len(b)-12]...)
	if !shared {
		out = append(out, pngChunk("fcTL", control)...)
		// Later frame data is structurally checked, without decoding its pixels.
		out = append(out, pngChunk("fdAT", []byte{0, 0, 0, 1, 1})...)
	}
	return append(out, b[len(b)-12:]...)
}
func TestHTTPProcessingInputs(t *testing.T) {
	rgba := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	rgba.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	rgba.SetNRGBA(2, 1, color.NRGBA{G: 255, A: 255})
	var source bytes.Buffer
	if err := png.Encode(&source, rgba); err != nil {
		t.Fatal(err)
	}
	legacy := fixture(t)
	normalized, err := os.ReadFile("../../testdata/png/normalized-gray4.png")
	if err != nil {
		t.Fatal(err)
	}
	// A complete PNG above the old output budget remains valid input. The
	// compatible opaque bytes are preserved when under the output budget only.
	large := append([]byte(nil), source.Bytes()[:len(source.Bytes())-12]...)
	large = append(large, pngChunk("raNd", make([]byte, 100000))...)
	large = append(large, source.Bytes()[len(source.Bytes())-12:]...)
	for _, method := range []string{"GET", "POST"} {
		for name, b := range map[string][]byte{"rgba": source.Bytes(), "large": large, "legacy": legacy, "passthrough": normalized, "shared-apng": animated(source.Bytes(), true), "separate-apng": animated(source.Bytes(), false)} {
			t.Run(method+"/"+name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "image/apng; charset=binary")
					_, _ = w.Write(b)
				}))
				defer server.Close()
				input := Request{Target: og, Policy: imageprocessing.Policy{Fit: imageprocessing.Stretch, Dither: imageprocessing.None}}
				var executor Executor = HTTPGET{URL: server.URL}
				if method == "POST" {
					input.Context = &Context{Version: "v1", RenderTarget: og, Device: Device{Name: "synthetic"}}
					executor = HTTPPOST{URL: server.URL}
				}
				out, err := executor.Execute(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				expected, err := imageprocessing.Normalize(t.Context(), b, CurrentTarget(og).ImageTarget(), input.Policy)
				if err != nil || !bytes.Equal(expected, out.PNG) || out.RenderedAt.IsZero() {
					t.Fatal("processing policy or timestamp lost", err)
				}
				if err := CurrentTarget(og).ValidateOutput(t.Context(), out.PNG); err != nil {
					t.Fatal(err)
				}
				if name == "passthrough" && !bytes.Equal(b, out.PNG) {
					t.Fatal("compatible renderer bytes changed")
				}
				if name == "legacy" && !bytes.Equal(normalized, out.PNG) {
					t.Fatal("legacy gray samples changed")
				}
			})
		}
	}
}

func TestHTTPInputByteLimit(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(map[bool]string{false: "streamed", true: "declared"}[declared], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/png")
				if declared {
					w.Header().Set("Content-Length", "33554433")
				} else {
					w.(http.Flusher).Flush()
				}
				block := make([]byte, 32768)
				for i := 0; i < 1024; i++ {
					if _, err := w.Write(block); err != nil {
						return
					}
				}
				_, _ = w.Write([]byte{0})
			}))
			defer server.Close()
			if _, err := (HTTPGET{URL: server.URL}).Execute(context.Background(), Request{Target: og}); err == nil {
				t.Fatal("over-limit input accepted")
			}
		})
	}
}
