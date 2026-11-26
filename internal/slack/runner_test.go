package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/secret"
)

const team = "T0TEAM"

// echo answers every command with its text, so a test sees what reached it.
type echo struct{}

func (echo) Handle(_ context.Context, r domain.Request) domain.Message {
	return domain.Message{Lines: []string{"handled " + r.Text}}
}

// fakeSlack is the Web API calls the runner makes, and a response_url that
// keeps what it was sent.
type fakeSlack struct {
	mu       sync.Mutex
	users    map[string]map[string]any
	answers  []slack.WebhookMessage
	userHits int
}

func (f *fakeSlack) server(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/users.info", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		f.mu.Lock()
		f.userHits++
		user, ok := f.users[r.Form.Get("user")]
		f.mu.Unlock()

		if !ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "user_not_found"})

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "user": user})
	})
	mux.HandleFunc("/respond", func(w http.ResponseWriter, r *http.Request) {
		var msg slack.WebhookMessage

		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Errorf("response_url got %s: %v", body, err)
		}

		f.mu.Lock()
		f.answers = append(f.answers, msg)
		f.mu.Unlock()
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func newRunner(t *testing.T, f *fakeSlack, allowGuests bool) (*Runner, *slack.Client, *httptest.Server) {
	t.Helper()

	server := f.server(t)
	r := New(Config{
		BotToken: secret.New("xoxb-test"), AppToken: secret.New("xapp-test"),
		AllowGuests: allowGuests, APIURL: server.URL + "/api/",
	}, echo{}, slog.New(slog.DiscardHandler), time.Now)
	r.team = team

	return r, r.api(), server
}

func command(server *httptest.Server, user, teamID, text string) slack.SlashCommand {
	return slack.SlashCommand{TeamID: teamID, UserID: user, Text: text, ResponseURL: server.URL + "/respond"}
}

func TestAnswer(t *testing.T) {
	f := &fakeSlack{users: map[string]map[string]any{
		"U1":     {"id": "U1"},
		"UGUEST": {"id": "UGUEST", "is_restricted": true},
		"UGONE":  {"id": "UGONE", "deleted": true},
	}}

	cases := map[string]struct {
		user, team string
		guests     bool
		want       string
	}{
		"a member":           {user: "U1", team: team, want: "handled bot"},
		"another workspace":  {user: "U1", team: "T0OTHER", want: "only in its own workspace"},
		"a guest":            {user: "UGUEST", team: team, want: "Guest accounts cannot"},
		"a guest, allowed":   {user: "UGUEST", team: team, guests: true, want: "handled bot"},
		"a deactivated user": {user: "UGONE", team: team, guests: true, want: "Guest accounts cannot"},
		"an unknown user":    {user: "UNOPE", team: team, want: "cannot check your Slack account"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, api, server := newRunner(t, f, c.guests)

			got := r.answer(context.Background(), api, command(server, c.user, c.team, "bot"))
			if text := strings.Join(got.Lines, " "); !strings.Contains(text, c.want) {
				t.Errorf("answered %q, want %q", text, c.want)
			}
		})
	}
}

func TestGuestAnswerIsCached(t *testing.T) {
	f := &fakeSlack{users: map[string]map[string]any{"U1": {"id": "U1"}}}
	r, api, server := newRunner(t, f, false)

	for range 3 {
		r.answer(context.Background(), api, command(server, "U1", team, "help"))
	}

	if f.userHits != 1 {
		t.Errorf("users.info was asked %d times, want 1", f.userHits)
	}
}

func TestRespondIsEphemeral(t *testing.T) {
	f := &fakeSlack{users: map[string]map[string]any{"U1": {"id": "U1"}}}
	r, api, server := newRunner(t, f, false)

	r.start(context.Background(), api, command(server, "U1", team, "help"))
	r.wg.Wait()

	if len(f.answers) != 1 {
		t.Fatalf("response_url got %d answers", len(f.answers))
	}

	if got := f.answers[0]; got.ResponseType != slack.ResponseTypeEphemeral || got.Text != "handled help" {
		t.Errorf("answered %+v", got)
	}
}

func TestClassify(t *testing.T) {
	if err := classify("auth.test", errors.New("invalid_auth")); !errors.Is(err, ErrAuth) {
		t.Errorf("invalid_auth is %v", err)
	}

	if err := classify("auth.test", slack.SlackErrorResponse{Err: "token_revoked"}); !errors.Is(err, ErrAuth) {
		t.Errorf("token_revoked is %v", err)
	}

	if err := classify("socket mode", errors.New("dial tcp: timeout")); errors.Is(err, ErrAuth) {
		t.Errorf("a network error is %v", err)
	}
}

// TestRunStopsOnARefusedToken runs the whole loop against a Slack that refuses
// the token: it must stop, say so, and not retry.
func TestRunStopsOnARefusedToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth.test", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	r := New(Config{BotToken: secret.New("xoxb-x"), AppToken: secret.New("xapp-x"), APIURL: server.URL + "/api/"},
		echo{}, slog.New(slog.DiscardHandler), time.Now)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := r.Run(ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("Run returned %v", err)
	}

	if r.Status().State != domain.SlackAuth {
		t.Errorf("status is %+v", r.Status())
	}
}
