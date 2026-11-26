// Package verify is the site's scheme for who may have an account
// (docs/design.md, "Checking membership"): denhac's Slack profile fields, and
// the member page they point at.
//
//  1. The Slack profile's "Member" field says Yes.
//  2. Its "Member Code URL" field links to https://denhac.org/member/<code>.
//     The bot takes the code out and builds the address itself.
//  3. That page shows Membership Status ACTIVE and lists the authorization
//     "3D Printer User".
//
// It is the only package that knows the scheme. An error means "cannot tell",
// never "no", so nothing is refused or taken away because a page could not be
// read.
package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// Config says where the membership facts are. These are the module's verify
// setting.
type Config struct {
	MemberField   string `json:"member_field"`
	CodeField     string `json:"code_field"`
	MemberURL     string `json:"member_url"`
	Authorization string `json:"authorization"`
}

// Defaults are denhac's, as of 2026-11-25.
func Defaults() Config {
	return Config{
		MemberField:   "Member",
		CodeField:     "Member Code URL",
		MemberURL:     "https://denhac.org/member/",
		Authorization: "3D Printer User",
	}
}

// Profiles reads a Slack user's custom profile fields, by label.
// internal/slack implements it.
type Profiles interface {
	Fields(ctx context.Context, user string) (map[string]string, error)
}

const (
	// cacheFor matches denhac's own cache (max-age=600 on its member pages).
	cacheFor = 10 * time.Minute
	// pace is the least time between two page requests.
	pace         = 2 * time.Second
	pageTimeout  = 15 * time.Second
	maxPageBytes = 1 << 20
	userAgent    = "prusa-connect-local-bot (+https://github.com/denhac/prusa-connect-local-bot)"
	statusActive = "ACTIVE"
)

// ErrPage is a member page the bot cannot read: the site is down, or its
// layout changed.
var ErrPage = errors.New("verify: the member page cannot be read")

var codeShape = regexp.MustCompile(`^[A-Za-z0-9]{2,16}$`)

// Verifier implements domain.Verifier.
type Verifier struct {
	cfg      Config
	profiles Profiles
	base     *url.URL
	http     *http.Client
	now      func() time.Time
	pace     time.Duration

	mu    sync.Mutex
	last  time.Time
	cache map[string]cached
}

type cached struct {
	m  domain.Membership
	at time.Time
}

// New builds a verifier. The member address must be https.
func New(cfg Config, profiles Profiles, now func() time.Time) (*Verifier, error) {
	base, err := url.Parse(cfg.MemberURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("verify: member_url %q is not an https address", cfg.MemberURL)
	}

	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}

	host := base.Host

	return &Verifier{
		cfg: cfg, profiles: profiles, base: base, now: now, pace: pace, cache: map[string]cached{},
		http: &http.Client{
			Timeout: pageTimeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if req.URL.Scheme != "https" || req.URL.Host != host {
					return errors.New("verify: a redirect off the member site")
				}

				return nil
			},
		},
	}, nil
}

// Check reads the Slack profile, then the member page.
func (v *Verifier) Check(ctx context.Context, slack string) (domain.Membership, error) {
	fields, err := v.profiles.Fields(ctx, slack)
	if err != nil {
		return domain.Membership{}, fmt.Errorf("verify: the Slack profile: %w", err)
	}

	if !strings.EqualFold(strings.TrimSpace(fields[v.cfg.MemberField]), "yes") {
		return domain.Membership{Reason: "Your Slack profile does not say you are a denhac member."}, nil
	}

	code := v.codeOf(fields[v.cfg.CodeField])
	if code == "" {
		return domain.Membership{Reason: "Your Slack profile has no member code link the bot can read."}, nil
	}

	return v.CheckCode(ctx, code)
}

// codeOf takes the code out of a link to the member site, and nothing else.
func (v *Verifier) codeOf(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !strings.EqualFold(u.Host, v.base.Host) || !strings.HasPrefix(u.Path, v.base.Path) {
		return ""
	}

	code := strings.Trim(strings.TrimPrefix(u.Path, v.base.Path), "/")
	if !codeShape.MatchString(code) {
		return ""
	}

	return strings.ToUpper(code)
}

// CheckCode reads one member page.
func (v *Verifier) CheckCode(ctx context.Context, code string) (domain.Membership, error) {
	code = strings.ToUpper(code)
	if !codeShape.MatchString(code) {
		return domain.Membership{Code: code, Reason: "That is not a member code."}, nil
	}

	v.mu.Lock()
	if c, ok := v.cache[code]; ok && v.now().Sub(c.at) < cacheFor {
		v.mu.Unlock()

		return c.m, nil
	}
	v.mu.Unlock()

	page, found, err := v.fetch(ctx, code)
	if err != nil {
		return domain.Membership{}, err
	}

	m := domain.Membership{Code: code}

	if !found {
		m.Reason = "denhac's member system does not know your member code."
	} else {
		parsed, err := Parse(page)
		if err != nil {
			return domain.Membership{}, err
		}

		m.DisplayName, m.Authorizations = parsed.DisplayName, parsed.Authorizations

		switch {
		case !strings.EqualFold(parsed.Status, statusActive):
			m.Reason = "denhac's member system shows your membership as " + strings.ToLower(parsed.Status) + "."
		case !slices.ContainsFunc(parsed.Authorizations, func(a string) bool { return strings.EqualFold(a, v.cfg.Authorization) }):
			m.Reason = "You need the " + v.cfg.Authorization + " authorization from denhac first."
		default:
			m.Qualifies = true
		}
	}

	v.mu.Lock()
	v.cache[code] = cached{m: m, at: v.now()}
	v.mu.Unlock()

	return m, nil
}

// fetch reads a member page, at most one request every two seconds.
func (v *Verifier) fetch(ctx context.Context, code string) (io.Reader, bool, error) {
	v.mu.Lock()
	wait := v.last.Add(v.pace).Sub(v.now())
	v.last = v.now().Add(max(wait, 0))
	v.mu.Unlock()

	if wait > 0 {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err() //nolint:wrapcheck // the caller's own cancellation.
		case <-time.After(wait):
		}
	}

	page := v.base.JoinPath(code)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page.String(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrPage, err)
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrPage, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("%w: the page answered %s", ErrPage, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrPage, err)
	}

	return strings.NewReader(string(body)), true, nil
}

// Page is what a member page says.
type Page struct {
	DisplayName    string
	Status         string
	Authorizations []string
}

// Parse reads a member page: the div.page-content, its "User Information"
// list and its "Active Authorizations" list. A page without the status is a
// changed layout, and an error.
func Parse(r io.Reader) (Page, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return Page{}, fmt.Errorf("%w: %v", ErrPage, err)
	}

	content := findDiv(doc, "page-content")
	if content == nil {
		return Page{}, fmt.Errorf("%w: it has no page-content block; the layout may have changed", ErrPage)
	}

	var (
		p       Page
		section string
	)

	for n := content.FirstChild; n != nil; n = n.NextSibling {
		if n.Type != html.ElementNode {
			continue
		}

		switch n.Data {
		case "h2":
			section = strings.ToLower(strings.TrimSpace(text(n)))
		case "ul":
			for li := n.FirstChild; li != nil; li = li.NextSibling {
				if li.Type != html.ElementNode || li.Data != "li" {
					continue
				}

				item := strings.Join(strings.Fields(text(li)), " ")

				switch section {
				case "user information":
					label, value, ok := strings.Cut(item, ":")
					if !ok {
						continue
					}

					switch strings.ToLower(strings.TrimSpace(label)) {
					case "display name":
						p.DisplayName = strings.TrimSpace(value)
					case "membership status":
						p.Status = strings.TrimSpace(value)
					}
				case "active authorizations":
					if item != "" {
						p.Authorizations = append(p.Authorizations, item)
					}
				}
			}
		}
	}

	if p.Status == "" {
		return Page{}, fmt.Errorf("%w: it shows no membership status; the layout may have changed", ErrPage)
	}

	return p, nil
}

func findDiv(n *html.Node, class string) *html.Node {
	if n.Type == html.ElementNode && n.Data == "div" {
		for _, a := range n.Attr {
			if a.Key == "class" && slices.Contains(strings.Fields(a.Val), class) {
				return n
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findDiv(c, class); found != nil {
			return found
		}
	}

	return nil
}

func text(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}

	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(text(c))
	}

	return b.String()
}
