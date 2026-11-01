package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Limits apply to encoded UTF-8 JSON bytes, including whitespace.
const (
	MaxContextBytes = 64 << 10
	MaxResultBytes  = 48 << 10
	MaxStateBytes   = 32 << 10
	MaxStateDepth   = 32
)

var ErrStateJSON = errors.New("invalid renderer state JSON")

// StateResult distinguishes omission (keep), null (clear), and every JSON value.
// RawMessage preserves large integers and renderer-owned number representations.
type StateResult struct {
	NextState json.RawMessage `json:"nextState,omitempty"`
}

// ValidateJSON rejects duplicate keys, invalid UTF-8, trailing values and excessive
// container depth. Errors never include parser input or renderer-owned values.
func ValidateJSON(data []byte, limit, depth int) error {
	if len(data) == 0 || len(data) > limit || !utf8.Valid(data) {
		return ErrStateJSON
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := jsonValue(dec, 0, depth); err != nil {
		return ErrStateJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrStateJSON
	}
	return nil
}

func jsonValue(dec *json.Decoder, level, maxDepth int) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if level >= maxDepth {
		return ErrStateJSON
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return ErrStateJSON
			}
			keys[name] = true
			if err := jsonValue(dec, level+1, maxDepth); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := jsonValue(dec, level+1, maxDepth); err != nil {
				return err
			}
		}
	default:
		return ErrStateJSON
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	if delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
		return ErrStateJSON
	}
	return nil
}

func ValidateState(data json.RawMessage) error {
	return ValidateJSON(data, MaxStateBytes, MaxStateDepth)
}

// ParseStateResult requires an existing, bounded result file. File absence is
// represented by StateResult{} by the collector, never by an empty byte slice.
func ParseStateResult(data []byte) (StateResult, error) {
	if err := ValidateJSON(data, MaxResultBytes, MaxStateDepth+1); err != nil {
		return StateResult{}, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return StateResult{}, ErrStateJSON
	}
	var version string
	if err := json.Unmarshal(envelope["version"], &version); err != nil || version != "v1" {
		return StateResult{}, ErrStateJSON
	}
	for key := range envelope {
		if key != "version" && key != "nextState" {
			return StateResult{}, ErrStateJSON
		}
	}
	next := envelope["nextState"]
	if len(next) > 0 {
		if err := ValidateState(next); err != nil {
			return StateResult{}, err
		}
	}
	return StateResult{NextState: bytes.Clone(next)}, nil
}

func (r StateResult) Apply(current json.RawMessage) (json.RawMessage, error) {
	if len(current) > 0 {
		if err := ValidateState(current); err != nil {
			return nil, err
		}
	}
	next := r.NextState
	if len(next) == 0 {
		next = current
	} else if err := ValidateState(next); err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(next), []byte("null")) {
		return nil, nil
	}
	var compact bytes.Buffer
	if len(next) == 0 {
		return nil, nil
	}
	if err := json.Compact(&compact, next); err != nil {
		return nil, ErrStateJSON
	}
	return compact.Bytes(), nil
}

// MarshalContext checks the final serialization, including escaped characters.
// Attach state only to an opted-in Job snapshot; HTTP POST removes it explicitly.
func MarshalContext(c Context) ([]byte, error) {
	if len(c.RendererState) > 0 {
		if err := ValidateState(c.RendererState); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(c.RendererState), []byte("null")) {
			c.RendererState = nil
		}
	}
	data, err := json.Marshal(c)
	if err != nil || len(data) > MaxContextBytes {
		return nil, errors.New("invalid or oversized renderer context")
	}
	return data, nil
}
