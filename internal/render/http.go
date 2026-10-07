package render

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const MaxPNGBytes = 89999
const HTTPTimeout = 10 * time.Second

// HTTPGET never copies device headers or context into the renderer request.
type HTTPGET struct {
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
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (h HTTPGET) Execute(ctx context.Context, input Request) (Output, error) {
	if input.Context != nil {
		return Output{}, errors.New("GET cannot receive device context")
	}
	raw, err := TargetURL(h.URL, input.Target)
	if err != nil {
		return Output{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return Output{}, errors.New("invalid renderer request")
	}
	req.Header.Set("Accept", "image/png")
	c := http.Client{Timeout: HTTPTimeout}
	if h.Client != nil {
		c = *h.Client
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
	if err != nil || media != "image/png" {
		return Output{}, errors.New("renderer must return Content-Type image/png")
	}
	if resp.ContentLength > MaxPNGBytes {
		return Output{}, errors.New("renderer image exceeds byte limit")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxPNGBytes+1))
	if err != nil {
		return Output{}, errors.New("renderer image read failed")
	}
	if err := ValidatePNG(b, input.Target); err != nil {
		return Output{}, err
	}
	return Output{PNG: b}, nil
}
