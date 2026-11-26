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

// accountServer is the account routes of 0.1.0-beta.2, behind a sign-in.
type accountServer struct {
	mu        sync.Mutex
	accounts  map[string]map[string]any
	passwords map[string]string
	calls     []string
	fail5xx   int
}

func (f *accountServer) start(t *testing.T) *Accounts {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "tok"})
		_ = json.NewEncoder(w).Encode(map[string]any{"expires_at": time.Now().Add(7 * 24 * time.Hour)})
	})

	record := func(r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
	}

	mux.HandleFunc("GET /api/v1/auth/accounts", func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{}
		for _, a := range f.accounts {
			items = append(items, a)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	})
	mux.HandleFunc("POST /api/v1/auth/accounts", func(w http.ResponseWriter, r *http.Request) {
		record(r)

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)

		for _, a := range f.accounts {
			if a["username"] == body["username"] {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"core.validation/ACCOUNT_EXISTS"}`))

				return
			}
		}

		if body["role"] != "operator" {
			t.Errorf("created with role %q", body["role"])
		}

		a := map[string]any{"id": "new1", "username": body["username"], "display_name": body["display_name"],
			"created_at": time.Now(), "subject": "user:new1"}
		f.accounts["new1"] = a
		f.passwords["new1"] = body["password"]

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
	})
	mux.HandleFunc("POST /api/v1/auth/accounts/{id}/password", func(w http.ResponseWriter, r *http.Request) {
		record(r)

		if f.fail5xx > 0 {
			f.fail5xx--
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.passwords[r.PathValue("id")] = body["new_password"]

		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /api/v1/auth/accounts/{id}/role", func(w http.ResponseWriter, r *http.Request) {
		record(r)

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.accounts[r.PathValue("id")]["role"] = body["role"]

		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v1/auth/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		delete(f.accounts, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)

	session, err := New(s.URL, "slackbot", secret.New("right"), time.Now)
	if err != nil {
		t.Fatal(err)
	}

	return NewAccounts(session, "slackbot")
}

func newAccountServer() *accountServer {
	return &accountServer{
		passwords: map[string]string{},
		accounts: map[string]map[string]any{
			"m1":  {"id": "m1", "username": "member", "role": "operator"},
			"a1":  {"id": "a1", "username": "boss", "role": "admin"},
			"bot": {"id": "bot", "username": "slackbot", "role": "admin"},
		},
	}
}

func TestCreateAndList(t *testing.T) {
	f := newAccountServer()
	a := f.start(t)
	ctx := context.Background()

	made, err := a.Create(ctx, "example.user", "Example User", "pw-123456789")
	if err != nil || made.ID != "new1" || made.Role != domain.RoleOperator || f.passwords["new1"] != "pw-123456789" {
		t.Fatalf("Create = %+v, %v", made, err)
	}

	if _, err := a.Create(ctx, "member", "x", "pw-123456789"); !errors.Is(err, domain.ErrNameTaken) {
		t.Errorf("a taken name: %v", err)
	}

	list, err := a.List(ctx)
	if err != nil || len(list) != 4 {
		t.Errorf("List = %v, %v", list, err)
	}
}

// TestNeverTouchesAdmins refuses every change to an admin or the bot itself,
// before anything is sent.
func TestNeverTouchesAdmins(t *testing.T) {
	f := newAccountServer()
	a := f.start(t)
	ctx := context.Background()

	for _, id := range []string{"a1", "bot"} {
		for name, err := range map[string]error{
			"password": a.SetPassword(ctx, id, "pw-123456789"),
			"role":     a.SetRole(ctx, id, domain.RoleViewer),
			"delete":   a.Delete(ctx, id),
		} {
			if !errors.Is(err, domain.ErrAdminAccount) {
				t.Errorf("%s of %s: %v", name, id, err)
			}
		}
	}

	if err := a.SetRole(ctx, "m1", domain.RoleAdmin); err == nil {
		t.Error("the bot wrote admin")
	}

	if len(f.calls) != 0 {
		t.Errorf("the server got %v", f.calls)
	}
}

func TestChanges(t *testing.T) {
	f := newAccountServer()
	a := f.start(t)
	ctx := context.Background()

	if err := a.SetRole(ctx, "m1", domain.RoleViewer); err != nil || f.accounts["m1"]["role"] != "viewer" {
		t.Errorf("SetRole: %v, %v", err, f.accounts["m1"])
	}

	f.fail5xx = 1
	if err := a.SetPassword(ctx, "m1", "pw-new-123456"); err != nil || f.passwords["m1"] != "pw-new-123456" {
		t.Errorf("SetPassword after a 500: %v", err)
	}

	if err := a.Delete(ctx, "m1"); err != nil || f.accounts["m1"] != nil {
		t.Errorf("Delete: %v", err)
	}

	if err := a.Delete(ctx, "m1"); err != nil {
		t.Errorf("deleting a deleted account: %v", err)
	}
}
