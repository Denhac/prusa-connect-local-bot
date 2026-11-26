package verify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "denhac", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestParse(t *testing.T) {
	p, err := Parse(strings.NewReader(fixture(t, "active.html")))
	if err != nil {
		t.Fatal(err)
	}

	if p.DisplayName != "exampleuser" || p.Status != "ACTIVE" ||
		!slices.Equal(p.Authorizations, []string{"3D Printer User", "Multi Tool 3D Printer User"}) {
		t.Errorf("parsed %+v", p)
	}

	if p, err := Parse(strings.NewReader(fixture(t, "no-authorizations-list.html"))); err != nil || len(p.Authorizations) != 0 {
		t.Errorf("a page with no list: %+v, %v", p, err)
	}

	if _, err := Parse(strings.NewReader(fixture(t, "changed-layout.html"))); !errors.Is(err, ErrPage) {
		t.Errorf("a changed layout: %v", err)
	}
}

type profiles map[string]map[string]string

func (p profiles) Fields(_ context.Context, user string) (map[string]string, error) {
	f, ok := p[user]
	if !ok {
		return nil, errors.New("user_not_found")
	}

	return f, nil
}

// site serves the fixtures as member pages, counting requests.
func site(t *testing.T, pages map[string]string, hits *int) *Verifier {
	t.Helper()

	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++

		code := strings.TrimPrefix(r.URL.Path, "/member/")
		name, ok := pages[code]

		switch {
		case name == "500":
			w.WriteHeader(http.StatusInternalServerError)
		case !ok:
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(fixture(t, name)))
		}
	}))
	t.Cleanup(s.Close)

	cfg := Defaults()
	cfg.MemberURL = s.URL + "/member/"

	v, err := New(cfg, profiles{
		"U1":         {"Member": "Yes", "Member Code URL": s.URL + "/member/ok01"},
		"Ulapsed":    {"Member": "Yes", "Member Code URL": s.URL + "/member/OFF1"},
		"Unoauth":    {"Member": "Yes", "Member Code URL": s.URL + "/member/NA01"},
		"Ugone":      {"Member": "Yes", "Member Code URL": s.URL + "/member/GONE"},
		"Udown":      {"Member": "Yes", "Member Code URL": s.URL + "/member/DOWN"},
		"Unot":       {"Member": "No", "Member Code URL": s.URL + "/member/OK01"},
		"Uelsewhere": {"Member": "Yes", "Member Code URL": "https://evil.example/member/OK01"},
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	v.http = s.Client()
	v.http.Timeout = pageTimeout
	v.pace = 0 // TestCachesAndPaces sets it back

	return v
}

func TestCheck(t *testing.T) {
	hits := 0
	v := site(t, map[string]string{
		"OK01": "active.html", "OFF1": "inactive.html", "NA01": "no-authorization.html", "DOWN": "500",
	}, &hits)
	v.last = time.Time{}

	cases := map[string]struct {
		qualifies bool
		reason    string
		err       bool
	}{
		"U1":         {qualifies: true},
		"Ulapsed":    {reason: "membership as inactive"},
		"Unoauth":    {reason: "3D Printer User authorization"},
		"Ugone":      {reason: "does not know your member code"},
		"Unot":       {reason: "does not say you are a denhac member"},
		"Uelsewhere": {reason: "no member code link"},
		"Udown":      {err: true},
		"Unknown":    {err: true},
	}

	for user, want := range cases {
		m, err := v.Check(context.Background(), user)

		switch {
		case want.err && err == nil:
			t.Errorf("%s: no error, %+v", user, m)
		case !want.err && err != nil:
			t.Errorf("%s: %v", user, err)
		case !want.err && (m.Qualifies != want.qualifies || !strings.Contains(m.Reason, want.reason)):
			t.Errorf("%s: %+v, want %+v", user, m, want)
		}
	}

	m, _ := v.Check(context.Background(), "U1")
	if m.Code != "OK01" || m.DisplayName != "exampleuser" {
		t.Errorf("U1 is %+v", m)
	}
}

func TestCachesAndPaces(t *testing.T) {
	hits := 0
	v := site(t, map[string]string{"OK01": "active.html", "OFF1": "inactive.html"}, &hits)
	v.pace = pace

	began := time.Now()

	for range 3 {
		if _, err := v.CheckCode(context.Background(), "ok01"); err != nil {
			t.Fatal(err)
		}
	}

	if hits != 1 {
		t.Errorf("three checks of one code made %d requests", hits)
	}

	if _, err := v.CheckCode(context.Background(), "OFF1"); err != nil {
		t.Fatal(err)
	}

	if took := time.Since(began); took < pace {
		t.Errorf("two pages were fetched %v apart, want at least %v", took, pace)
	}
}

func TestNewRefusesPlainHTTP(t *testing.T) {
	cfg := Defaults()
	cfg.MemberURL = "http://denhac.org/member/"

	if _, err := New(cfg, profiles{}, time.Now); err == nil {
		t.Error("an http member address was taken")
	}
}
