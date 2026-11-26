// Package camera fetches stills from the Pi camera bots through their camera
// API, v1 (the sister project prusaConnectCamera, docs/api.md).
//
//	GET http://<pi>:8555/v1/cameras/{id}/still?max_age=&wait=
//
// The addresses come from the operator's cameras setting. They are held to the
// LAN when the connection is made, whatever a name resolves to: private and
// loopback addresses only, never a public one and never Tailscale's ranges,
// with no redirects followed.
package camera

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

const (
	// maxStill bounds an answer; a still is a few hundred KB.
	maxStill = 8 << 20
	// requestTimeout bounds one request, including a fresh still's wait.
	requestTimeout = 15 * time.Second
)

// tailscale are the ranges Tailscale gives out. The bot never uses them for a
// product path (sister project rule).
var tailscale = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// ErrRefusedAddress is a camera address outside the LAN.
var ErrRefusedAddress = errors.New("camera: the address is not on the local network")

// Client fetches stills for the printers it has addresses for.
type Client struct {
	urls map[string]*url.URL
	http *http.Client
}

// New checks the addresses, one per printer id, and builds the client.
func New(urls map[string]string) (*Client, error) {
	parsed := make(map[string]*url.URL, len(urls))

	for device, raw := range urls {
		u, err := Check(raw)
		if err != nil {
			return nil, fmt.Errorf("the camera for %s: %w", device, err)
		}

		parsed[device] = u
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: guard}

	return &Client{
		urls: parsed,
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Check reads one still address. It is http or https, names a host, carries
// no user and no password, and its address is not refused when it is written
// as one.
func Check(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)

	switch {
	case err != nil:
		return nil, fmt.Errorf("%q is not an address: %w", raw, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%q is not http or https", raw)
	case u.Hostname() == "":
		return nil, fmt.Errorf("%q names no host", raw)
	case u.User != nil:
		return nil, fmt.Errorf("%q carries a user or password", raw)
	}

	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowed(ip) {
		return nil, fmt.Errorf("%w: %s", ErrRefusedAddress, ip)
	}

	return u, nil
}

// guard refuses a connection to any address outside the LAN, after a name
// has been resolved.
func guard(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("camera: %w", err)
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("camera: %w", err)
	}

	if !allowed(ip) {
		return fmt.Errorf("%w: %s", ErrRefusedAddress, ip)
	}

	return nil
}

func allowed(ip netip.Addr) bool {
	ip = ip.Unmap()

	for _, p := range tailscale {
		if p.Contains(ip) {
			return false
		}
	}

	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// Has reports whether the printer has a camera address.
func (c *Client) Has(device string) bool {
	_, ok := c.urls[device]

	return ok
}

// Still fetches a JPEG. Fresh waits up to 10 s for one taken after the
// request; otherwise any still from the last minute does.
func (c *Client) Still(ctx context.Context, device string, fresh bool) ([]byte, error) {
	base, ok := c.urls[device]
	if !ok {
		return nil, domain.ErrNoCamera
	}

	u := *base
	q := u.Query()

	if fresh {
		q.Set("wait", "10")
		q.Set("max_age", "30")
	} else {
		q.Set("max_age", "60")
	}

	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("camera: %w", err)
	}

	req.Header.Set("Accept", "image/jpeg")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("camera: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusServiceUnavailable:
		return nil, domain.ErrCameraBusy
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("camera: %s answered %s", u.Host, resp.Status)
	case resp.Header.Get("Content-Type") != "image/jpeg":
		return nil, fmt.Errorf("camera: %s answered %q, not a JPEG", u.Host, resp.Header.Get("Content-Type"))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxStill+1))
	if err != nil {
		return nil, fmt.Errorf("camera: %w", err)
	}

	if len(body) > maxStill {
		return nil, fmt.Errorf("camera: %s answered more than %d bytes", u.Host, maxStill)
	}

	return body, nil
}
