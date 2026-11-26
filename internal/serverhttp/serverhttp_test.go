package serverhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/secret"
)

func TestCheckURL(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:8080":                true,
		"http://localhost:8080/":               true,
		"https://printserver.example.org:8080": true,
		"http://10.0.0.5:8080":                 false, // the password would cross the LAN in the clear
		"https://100.101.102.103:8080":         false, // Tailscale
		"https://box.tail1234.ts.net":          false,
		"https://user:pw@example.org":          false,
		"ftp://127.0.0.1":                      false,
		"":                                     false,
	}

	for raw, ok := range cases {
		if _, err := CheckURL(raw); (err == nil) != ok {
			t.Errorf("CheckURL(%q) = %v", raw, err)
		}
	}
}

// fakeServer is the sign-in and the two camera routes, as 0.1.0-beta.2
// answers them.
type fakeServer struct {
	mu       sync.Mutex
	logins   int
	logouts  int
	expires  time.Time
	revoked  bool // answer the next camera call 401, as after a password change
	modified time.Time
	status   int
}

func (f *fakeServer) start(t *testing.T) *httptest.Server {
	t.Helper()

	signedIn := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok" }

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)

		f.mu.Lock()
		defer f.mu.Unlock()

		if body["username"] != "slackbot" || body["password"] != "right" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"core.auth/LOGIN_FAILED"}`))

			return
		}

		f.logins++
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "tok", HttpOnly: true})
		_ = json.NewEncoder(w).Encode(map[string]any{"expires_at": f.expires})
	})
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.logouts++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/cameras", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		revoked := f.revoked
		f.revoked = false
		f.mu.Unlock()

		if !signedIn(r) || revoked {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = w.Write([]byte(`{"items":[
			{"name":"blue-cam","type":"generic","device_id":"dev-a","snapshot":true},
			{"name":"spare","type":"generic","device_id":"","snapshot":true},
			{"name":"stream-only","device_id":"dev-b","snapshot":false}]}`))
	})
	mux.HandleFunc("GET /api/v1/cameras/{camera}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if !signedIn(r) || r.PathValue("camera") != "blue-cam" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		f.mu.Lock()
		status, modified := f.status, f.modified
		f.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)

			return
		}

		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff})
	})

	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)

	return s
}

func newCameras(t *testing.T, f *fakeServer, password string) (*Session, *Cameras) {
	t.Helper()

	now := time.Now()
	f.expires, f.modified = now.Add(7*24*time.Hour), now.Add(-20*time.Second)

	s, err := New(f.start(t).URL, "slackbot", secret.New(password), time.Now)
	if err != nil {
		t.Fatal(err)
	}

	return s, NewCameras(s, time.Now)
}

func TestStill(t *testing.T) {
	f := &fakeServer{}
	s, c := newCameras(t, f, "right")
	ctx := context.Background()

	if !c.Has("dev-a") || c.Has("dev-b") || c.Has("") {
		t.Fatal("the camera list was read wrong")
	}

	jpeg, err := c.Still(ctx, "dev-a", true)
	if err != nil || len(jpeg) != 3 {
		t.Fatalf("Still = %v, %v", jpeg, err)
	}

	if _, err := c.Still(ctx, "dev-b", true); !errors.Is(err, domain.ErrNoCamera) {
		t.Errorf("a printer without a snapshot camera: %v", err)
	}

	if f.logins != 1 {
		t.Errorf("signed in %d times, want once", f.logins)
	}

	s.Close(ctx)

	if f.logouts != 1 {
		t.Errorf("signed out %d times", f.logouts)
	}
}

func TestStillRefusals(t *testing.T) {
	f := &fakeServer{}
	_, c := newCameras(t, f, "right")
	ctx := context.Background()

	f.modified = time.Now().Add(-time.Hour)
	if _, err := c.Still(ctx, "dev-a", false); !errors.Is(err, domain.ErrCameraBusy) {
		t.Errorf("an hour-old picture: %v", err)
	}

	f.modified, f.status = time.Now(), http.StatusNoContent
	if _, err := c.Still(ctx, "dev-a", false); !errors.Is(err, domain.ErrNoPicture) {
		t.Errorf("204: %v", err)
	}

	f.status = http.StatusBadGateway
	if _, err := c.Still(ctx, "dev-a", false); err == nil {
		t.Error("502 gave a picture")
	}
}

// TestSignsInAgainAfterA401 is a session the server ended, for example when
// someone changed the bot's password and then set it back.
func TestSignsInAgainAfterA401(t *testing.T) {
	f := &fakeServer{}
	_, c := newCameras(t, f, "right")

	c.Has("dev-a")
	f.revoked = true
	c.fetched = time.Time{}

	if !c.Has("dev-a") {
		t.Fatal("the camera was lost after a 401")
	}

	if f.logins != 2 {
		t.Errorf("signed in %d times, want twice", f.logins)
	}
}

func TestRenewsBeforeExpiry(t *testing.T) {
	f := &fakeServer{}
	s, c := newCameras(t, f, "right")
	f.expires = time.Now().Add(30 * time.Minute) // inside renewBefore

	c.Has("dev-a")
	c.fetched = time.Time{}
	c.Has("dev-a")

	if f.logins != 2 {
		t.Errorf("a session about to expire was used again (%d sign-ins)", f.logins)
	}

	_ = s
}

func TestWrongPassword(t *testing.T) {
	f := &fakeServer{}
	s, c := newCameras(t, f, "wrong")

	if _, err := c.Still(context.Background(), "dev-a", false); !errors.Is(err, ErrSignIn) {
		t.Errorf("a wrong password: %v", err)
	}

	if !errors.Is(s.Last(), ErrSignIn) {
		t.Errorf("Last is %v", s.Last())
	}
}

func TestNoPassword(t *testing.T) {
	s, err := New("http://127.0.0.1:1", "slackbot", secret.Secret{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Do(context.Background(), http.MethodGet, "/x", nil, "*/*"); !errors.Is(err, ErrOff) {
		t.Errorf("no password: %v", err)
	}
}
