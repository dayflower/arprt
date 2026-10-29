// Command byos-delay probes delayed display transport in an isolated environment.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dayflower/arprt/internal/byos"
)

func main() {
	address := flag.String("listen", "127.0.0.1:8081", "HTTP listen address; use only an isolated test route")
	delay := flag.Duration("delay", 10*time.Second, "delay before forwarding a display request (0s to 27s; 15s or longer is a negative transport probe)")
	path := flag.String("display-path", "/arprt/devices/arprt/delay-probe/api/display", "exact display path received after any proxy prefix stripping")
	upstream := flag.String("upstream", "", "optional disposable BYOS controller origin; preserve the incoming path")
	refresh := flag.Int("refresh-seconds", 0, "optional successful display polling interval override for an isolated device test (0 disables)")
	flag.Parse()
	if *delay < 0 || *delay > 27*time.Second || *path == "" || !byos.CleanPath(*path) || *refresh < 0 || *refresh > 3600 || (*refresh != 0 && *upstream == "") {
		log.Fatal("invalid delay or display path")
	}
	var next http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != *path || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "14")
		_, _ = fmt.Fprint(w, `{"status":202}`)
	})
	if *upstream != "" {
		u, err := url.Parse(*upstream)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			log.Fatal("upstream must be an HTTP(S) origin without credentials, path, query or fragment")
		}
		u.Path = ""
		proxy := httputil.NewSingleHostReverseProxy(u)
		if *refresh != 0 {
			proxy.ModifyResponse = pollingOverride(*path, *refresh)
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			// Do not expose upstream addresses or request headers in probe logs.
			http.Error(w, "probe upstream unavailable", http.StatusServiceUnavailable)
		}
		next = proxy
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	l := byos.Listener{Address: *address, Handler: delayed(next, *path, *delay)}
	log.Printf("isolated transport probe: delay=%s upstream_enabled=%t", *delay, *upstream != "")
	if err := l.Start(ctx); err != nil {
		log.Fatal("probe listener stopped with an error")
	}
}

// pollingOverride affects only successful display JSON on the probe path. It
// leaves pairing, pending, authentication failures and image bytes untouched.
func pollingOverride(path string, seconds int) func(*http.Response) error {
	return func(response *http.Response) error {
		if response.Request.URL.Path != path || response.StatusCode != http.StatusOK {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		response.Body.Close()
		if err != nil || len(body) > 64<<10 {
			return fmt.Errorf("invalid probe display response")
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(body, &value); err != nil {
			return fmt.Errorf("invalid probe display JSON")
		}
		if string(value["status"]) == "0" {
			value["refresh_rate"] = json.RawMessage(fmt.Sprint(seconds))
			body, err = json.Marshal(value)
			if err != nil {
				return err
			}
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		response.Header.Set("Content-Length", fmt.Sprint(len(body)))
		return nil
	}
}

func delayed(next http.Handler, path string, delay time.Duration) http.Handler {
	slots := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "probe busy", http.StatusServiceUnavailable)
			return
		}
		// WriteTimeout limits socket writes; it does not cancel handler work.
		// Bound both the delay and any upstream work independently. Negative
		// transport probes can outlive the 15-second socket deadline; production
		// BYOS requests retain their separate 13-second application deadline.
		ctx, cancel := context.WithTimeout(r.Context(), 28*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		if r.URL.Path == path && r.Method == http.MethodGet {
			started := time.Now()
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				log.Printf("display delay finished: elapsed=%s", time.Since(started))
			case <-ctx.Done():
				log.Printf("display delay canceled: elapsed=%s", time.Since(started))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
