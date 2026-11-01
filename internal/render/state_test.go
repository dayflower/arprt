package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestStateResultValuesAndPrivacy(t *testing.T) {
	for _, value := range []string{"null", "{}", "[]", "false", "0", `"cursor"`, `9007199254740993123456789`, `1e999999`} {
		t.Run(value, func(t *testing.T) {
			r, err := ParseStateResult([]byte(`{"version":"v1","nextState":` + value + `}`))
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Apply(json.RawMessage(`{"previous":1}`))
			if err != nil {
				t.Fatal(err)
			}
			want := value
			if value == "null" {
				want = ""
			}
			if string(got) != want {
				t.Fatal("state changed", string(got))
			}
		})
	}
	r, err := ParseStateResult([]byte(`{"version":"v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Apply(json.RawMessage(`false`))
	if err != nil || string(got) != "false" {
		t.Fatal("omission cleared state", err)
	}
	invalid := [][]byte{
		nil, {}, []byte(`null`), []byte(`[]`), []byte(`{}`), []byte(`{"version":null}`), []byte(`{"version":"v2"}`),
		[]byte(`{"version":"v1","other":"private-secret"}`), []byte(`{"version":"v1","version":"v1"}`),
		[]byte(`{"version":"v1","nextState":{"a":0,"\u0061":1}}`), []byte(`{"version":"v1","nextState":[{"a":0,"a":1}]}`),
		[]byte(`{"version":"v1","nextState":01}`), []byte(`{"version":"v1","nextState":NaN}`),
		[]byte(`{"version":"v1"} {}`), append([]byte(`{"version":"v1","nextState":"`), 0xff, '"', '}'),
	}
	for _, data := range invalid {
		if _, err := ParseStateResult(data); !errors.Is(err, ErrStateJSON) || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unsafe parser result", err)
		}
	}
}

func TestStateLimits(t *testing.T) {
	exact := `"` + strings.Repeat("x", MaxStateBytes-2) + `"`
	for _, tc := range []struct {
		state string
		valid bool
	}{{exact, true}, {`"` + strings.Repeat("x", MaxStateBytes-1) + `"`, false}, {strings.Repeat("[", MaxStateDepth) + "0" + strings.Repeat("]", MaxStateDepth), true}, {strings.Repeat("[", MaxStateDepth+1) + "0" + strings.Repeat("]", MaxStateDepth+1), false}} {
		_, err := ParseStateResult([]byte(`{"version":"v1","nextState":` + tc.state + `}`))
		if (err == nil) != tc.valid {
			t.Fatal("state limit", len(tc.state), err)
		}
	}
	base := `{"version":"v1"}`
	for _, n := range []int{MaxResultBytes, MaxResultBytes + 1} {
		_, err := ParseStateResult([]byte(base + strings.Repeat(" ", n-len(base))))
		if (err == nil) != (n == MaxResultBytes) {
			t.Fatal("envelope boundary", n, err)
		}
	}
	c := Context{Version: "v1", RenderTarget: Target{Width: 800, Height: 480, Palette: "gray4"}, Device: Device{Name: "fixture"}}
	raw, err := MarshalContext(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{MaxContextBytes, MaxContextBytes + 1} {
		c.Device.Name = strings.Repeat("x", n-len(raw)+len("fixture"))
		b, err := MarshalContext(c)
		if (err == nil) != (n == MaxContextBytes) {
			t.Fatal("context boundary", n, len(b), err)
		}
	}
	c.Device.Name = "fixture"
	c.RendererState = json.RawMessage("null")
	b, err := MarshalContext(c)
	if err != nil || bytes.Contains(b, []byte("rendererState")) {
		t.Fatal("null context state exposed", err)
	}
	c.RendererState = json.RawMessage(exact)
	c.Device.Name = strings.Repeat("<", MaxContextBytes/6)
	if _, err := MarshalContext(c); err == nil {
		t.Fatal("serialized context escaping bypassed limit")
	}
}

func TestStateContextFixture(t *testing.T) {
	b, err := os.ReadFile("../../testdata/render-context/render-context-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Context
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	got, err := MarshalContext(c)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		t.Fatal(err)
	}
	var roundtrip Context
	if err := json.Unmarshal(got, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if string(roundtrip.RendererState) != `{"cursor":9007199254740993}` {
		t.Fatal("large cursor changed")
	}
	if len(c.RendererState) == 0 {
		t.Fatal("fixture lacks state")
	}
}
