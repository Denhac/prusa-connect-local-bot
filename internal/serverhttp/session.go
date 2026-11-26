// Package serverhttp is the prusa-connect server's HTTP API, signed in as the
// bot's own account (docs/design.md, "The bot's admin account, plainly").
//
// The module wire has no camera read and no account calls, so the bot signs
// in like a person: POST /api/v1/auth/login, then the session token as
// Authorization: Bearer. The token is renewed before it expires and once
// after a 401. It is the only package that knows the HTTP API, and the only
// one that sees the password.
package serverhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/secret"
)

// sessionCookie is the cookie the server sets on a sign-in. Its value is the
// session token, which the server also takes as a bearer token.
const sessionCookie = "pcl_session"

// renewBefore is how long before its expiry a session is replaced.
const renewBefore = time.Hour

// Refusals a caller may branch on.
var (
	// ErrSignIn is the bot's account refused: a wrong name or password, or a
	// server holding too many failed attempts from this address.
	ErrSignIn = errors.New("serverhttp: the server refused the bot's sign-in")
	// ErrAddress is a server address the bot will not send a password to.
	ErrAddress = errors.New("serverhttp: refused server address")
	// ErrOff is a session with no password configured.
	ErrOff = errors.New("serverhttp: no password is set for the bot's account")
)

// tailscale are the ranges Tailscale gives out. The bot never uses them for a
// product path (sister project rule).
var tailscale = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// Session is the bot's sign-in to the server. It is safe for concurrent use.
type Session struct {
	base     *url.URL
	username string
	password secret.Secret
	http     *http.Client
	now      func() time.Time

	mu      sync.Mutex
	token   secret.Secret
	expires time.Time
	last    error
}

// CheckURL reads the server address. Plain http is taken only to loopback,
// because the password crosses it. A Tailscale address is refused.
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))

	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %q is not an address: %v", ErrAddress, raw, err)
	case u.Scheme != "http" && u.Scheme != "https", u.Host == "", u.User != nil, u.RawQuery != "":
		return nil, fmt.Errorf("%w: %q is not an http(s) address with a host and nothing else", ErrAddress, raw)
	case strings.HasSuffix(strings.ToLower(u.Hostname()), ".ts.net"):
		return nil, fmt.Errorf("%w: %q is a Tailscale name", ErrAddress, raw)
	}

	ip, ipErr := netip.ParseAddr(u.Hostname())
	if ipErr == nil {
		for _, p := range tailscale {
			if p.Contains(ip.Unmap()) {
				return nil, fmt.Errorf("%w: %q is a Tailscale address", ErrAddress, raw)
			}
		}
	}

	loopback := u.Hostname() == "localhost" || (ipErr == nil && ip.IsLoopback())
	if u.Scheme == "http" && !loopback {
		return nil, fmt.Errorf("%w: %q is plain http to another machine, which would send the password in the clear", ErrAddress, raw)
	}

	return u, nil
}

// New builds the session. Nothing is sent until the first call.
func New(rawURL, username string, password secret.Secret, now func() time.Time) (*Session, error) {
	base, err := CheckURL(rawURL)
	if err != nil {
		return nil, err
	}

	return &Session{
		base: base, username: username, password: password, now: now,
		http: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // a password never follows a redirect
			},
			Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext},
		},
	}, nil
}

// Last is the most recent sign-in failure, or nil.
func (s *Session) Last() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.last
}

// Do sends one request signed in, and signs in again once if the server
// answers 401. The caller closes the body.
func (s *Session) Do(ctx context.Context, method, path string, body []byte, accept string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		token, err := s.session(ctx)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, s.base.String()+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("serverhttp: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token.Reveal())
		req.Header.Set("Accept", accept)

		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := s.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("serverhttp: %s %s: %w", method, path, err)
		}

		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}

		_ = resp.Body.Close()
		s.forget(token)
	}
}

// session answers a token that is good for at least renewBefore, signing in
// when there is none.
func (s *Session) session(ctx context.Context) (secret.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.token.Empty() && s.now().Add(renewBefore).Before(s.expires) {
		return s.token, nil
	}

	if s.password.Empty() {
		return secret.Secret{}, ErrOff
	}

	token, expires, err := s.login(ctx)
	s.last = err

	if err != nil {
		return secret.Secret{}, err
	}

	s.token, s.expires = token, expires

	return token, nil
}

func (s *Session) forget(token secret.Secret) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token.Reveal() == token.Reveal() {
		s.token, s.expires = secret.Secret{}, time.Time{}
	}
}

func (s *Session) login(ctx context.Context) (secret.Secret, time.Time, error) {
	body, err := json.Marshal(map[string]string{"username": s.username, "password": s.password.Reveal()})
	if err != nil {
		return secret.Secret{}, time.Time{}, fmt.Errorf("serverhttp: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base.String()+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return secret.Secret{}, time.Time{}, fmt.Errorf("serverhttp: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return secret.Secret{}, time.Time{}, fmt.Errorf("serverhttp: sign in: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return secret.Secret{}, time.Time{}, fmt.Errorf("%w: the server answered %s %s", ErrSignIn, resp.Status, problemCode(resp.Body))
	}

	var answer struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&answer); err != nil {
		return secret.Secret{}, time.Time{}, fmt.Errorf("serverhttp: the sign-in answer: %w", err)
	}

	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return secret.New(c.Value), answer.ExpiresAt, nil
		}
	}

	return secret.Secret{}, time.Time{}, fmt.Errorf("serverhttp: the sign-in answer set no %s", sessionCookie)
}

// Close signs out, ending the session on the server.
func (s *Session) Close(ctx context.Context) {
	s.mu.Lock()
	token := s.token
	s.token, s.expires = secret.Secret{}, time.Time{}
	s.mu.Unlock()

	if token.Empty() {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base.String()+"/api/v1/auth/logout", nil)
	if err != nil {
		return
	}

	req.Header.Set("Authorization", "Bearer "+token.Reveal())

	if resp, err := s.http.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// problemCode reads the code of an RFC 9457 problem document, for a log line.
func problemCode(body io.Reader) string {
	var p struct {
		Code string `json:"code"`
	}

	_ = json.NewDecoder(io.LimitReader(body, 1<<16)).Decode(&p) // not a problem document: no code

	return p.Code
}
