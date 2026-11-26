package camera

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

func TestCheck(t *testing.T) {
	cases := map[string]bool{
		"http://192.168.1.20:8555/v1/cameras/cam0/still": true,
		"http://10.0.0.5:8555/v1/cameras/cam0/still":     true,
		"http://printer-pi:8555/v1/cameras/cam0/still":   true, // held at dial time
		"https://172.16.0.9/v1/cameras/cam0/still":       true,
		"http://100.100.1.2:8555/v1/cameras/cam0/still":  false, // Tailscale
		"http://8.8.8.8/still":                           false,
		"ftp://10.0.0.5/still":                           false,
		"http://user:pw@10.0.0.5/still":                  false,
		"http:///still":                                  false,
	}

	for raw, ok := range cases {
		if _, err := Check(raw); (err == nil) != ok {
			t.Errorf("Check(%q) = %v", raw, err)
		}
	}
}

func TestGuard(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:80": true, "10.1.2.3:8555": true, "[fd00::1]:80": true,
		"100.64.0.1:80": false, "[fd7a:115c:a1e0::1]:80": false, "1.1.1.1:443": false, "[2001:db8::1]:80": false,
	}

	for address, ok := range cases {
		if err := guard("tcp", address, nil); (err == nil) != ok {
			t.Errorf("guard(%s) = %v", address, err)
		}
	}

	if allowed(netip.MustParseAddr("::ffff:100.64.0.1")) {
		t.Error("a mapped Tailscale address was allowed")
	}
}

func server(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()

	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)

	return s.URL + "/v1/cameras/cam0/still"
}

func TestStill(t *testing.T) {
	var query string

	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff})
	})

	c, err := New(map[string]string{"a": url})
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.Still(context.Background(), "a", true)
	if err != nil || len(got) != 3 {
		t.Fatalf("Still = %v, %v", got, err)
	}

	if query != "max_age=30&wait=10" {
		t.Errorf("a fresh still asked %q", query)
	}

	if _, err := c.Still(context.Background(), "a", false); err != nil || query != "max_age=60" {
		t.Errorf("a still asked %q: %v", query, err)
	}

	if !c.Has("a") || c.Has("b") {
		t.Error("Has is wrong")
	}

	if _, err := c.Still(context.Background(), "b", true); !errors.Is(err, domain.ErrNoCamera) {
		t.Errorf("no camera is %v", err)
	}
}

func TestStillRefusals(t *testing.T) {
	cases := map[string]struct {
		handler http.HandlerFunc
		want    error
	}{
		"live view": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
		}, domain.ErrCameraBusy},
		"not found": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, nil},
		"redirect": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://8.8.8.8/", http.StatusFound)
		}, nil},
		"not a JPEG": {func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		}, nil},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			client, err := New(map[string]string{"a": server(t, c.handler)})
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.Still(context.Background(), "a", true)
			if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
				t.Errorf("answered %v, want %v", err, c.want)
			}
		})
	}
}
