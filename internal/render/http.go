package render

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dayflower/arprt/internal/imageprocessing"
	"github.com/dayflower/arprt/internal/observe"
)

// MaxPNGBytes is retained only for legacy persisted PNG validation.
const MaxPNGBytes = 89999
const HTTPTimeout = 10 * time.Second

// HTTPGET never copies device headers or context into the renderer request.
type HTTPGET struct {
	URL    string
	Client *http.Client
}

// HTTPPOST sends only the versioned, allowlisted context; it never forwards
// inbound device headers. Both transports use the same bounded PNG execution.
type HTTPPOST struct {
	URL    string
	Client *http.Client
}

func TargetURL(raw string, target Target) (string, error) {
	if err := target.Validate(); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("invalid renderer URL")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("invalid renderer query")
	}
	q.Set("width", strconv.Itoa(int(target.Width)))
	q.Set("height", strconv.Itoa(int(target.Height)))
	q.Set("palette", target.Palette)
	q.Set("rotation", strconv.Itoa(int(target.Rotation)))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (h HTTPGET) Execute(ctx context.Context, input Request) (Output, error) {
	if input.Context != nil {
		return Output{}, errors.New("GET cannot receive device context")
	}
	return executeHTTP(ctx, h.URL, h.Client, http.MethodGet, input, nil)
}

func (h HTTPPOST) Execute(ctx context.Context, input Request) (Output, error) {
	if input.Context == nil || input.Context.Version != "v1" || input.Context.Device.Name == "" || input.Context.RenderTarget != input.Target {
		return Output{}, errors.New("POST requires Render Context v1 with the same target as the query")
	}
	snapshot := *input.Context
	snapshot.RendererState = nil
	body, err := MarshalContext(snapshot)
	if err != nil {
		return Output{}, errors.New("invalid renderer context")
	}
	return executeHTTP(ctx, h.URL, h.Client, http.MethodPost, input, body)
}

func executeHTTP(ctx context.Context, rawURL string, supplied *http.Client, method string, input Request, body []byte) (output Output, resultErr error) {
	started := time.Now()
	defer func() { observe.Render(method, started, resultErr) }()
	if err := input.ResolvedTarget().Validate(); err != nil {
		return Output{}, err
	}
	raw, err := TargetURL(rawURL, input.Target)
	if err != nil {
		return Output{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, raw, bytes.NewReader(body))
	if err != nil {
		return Output{}, errors.New("invalid renderer request")
	}
	req.Header.Set("Accept", "image/png")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	c := http.Client{Timeout: HTTPTimeout}
	if supplied != nil {
		c = *supplied
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return Output{}, errors.New("renderer request failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Output{}, errors.New("renderer must return HTTP 200 without redirects")
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (media != "image/png" && media != "image/apng") {
		return Output{}, errors.New("renderer must return Content-Type image/png or image/apng")
	}
	if resp.ContentLength > imageprocessing.MaxInputBytes {
		return Output{}, errors.New("renderer image exceeds byte limit")
	}
	b, err := imageprocessing.ReadInput(ctx, resp.Body)
	if err != nil {
		return Output{}, errors.New("renderer image read failed")
	}
	renderedAt := time.Now().UTC()
	normalized, err := ProcessPNG(ctx, b, input.ResolvedTarget(), input.Policy)
	if err != nil {
		return Output{}, err
	}
	return Output{PNG: normalized, RenderedAt: renderedAt}, nil
}
