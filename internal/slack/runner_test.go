package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (echo) Handle(ctx context.Context, r domain.Request, u domain.UI) {
	_ = u.Respond(ctx, domain.Message{Lines: []string{"handled " + r.Text}})
}

func (echo) Interact(ctx context.Context, it domain.Interaction, u domain.UI) {
	_ = u.Respond(ctx, domain.Message{Lines: []string{"clicked " + it.Action}})
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
			before := len(f.answers)

			r.start(context.Background(), api, command(server, c.user, c.team, "bot"))
			r.wg.Wait()

			if len(f.answers) != before+1 {
				t.Fatalf("response_url got %d answers", len(f.answers)-before)
			}

			if text := f.answers[len(f.answers)-1].Text; !strings.Contains(text, c.want) {
				t.Errorf("answered %q, want %q", text, c.want)
			}
		})
	}
}

func TestGuestAnswerIsCached(t *testing.T) {
	f := &fakeSlack{users: map[string]map[string]any{"U1": {"id": "U1"}}}
	r, api, _ := newRunner(t, f, false)

	for range 3 {
		r.answer(context.Background(), api, team, "U1", "help")
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

func TestModalView(t *testing.T) {
	v := modalView(domain.Modal{
		Title: "A title that is far too long for Slack", Lines: []string{"Pick one."},
		Inputs:   []domain.Input{{ID: "username", Label: "Username", Initial: "example.user-2"}},
		Callback: "signup-name", Submit: "Create", Meta: `{"code":"ABCD"}`,
	})

	if v.Title.Text != "A title that is far too " || v.Submit == nil || v.CallbackID != "signup-name" || v.PrivateMetadata != `{"code":"ABCD"}` {
		t.Errorf("view is %+v", v)
	}

	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(encoded), `"initial_value":"example.user-2"`) || !strings.Contains(string(encoded), `"block_id":"username"`) {
		t.Errorf("the input went out as %s", encoded)
	}

	if bare := modalView(domain.Modal{Title: "Done", Lines: []string{"x"}}); bare.Submit != nil {
		t.Error("a window with no callback has a submit button")
	}
}

// TestModalChoices draws up to 10 choices as checkboxes with their details,
// more as a menu that picks several, and an optional field as optional.
func TestModalChoices(t *testing.T) {
	few := []domain.Choice{{Value: "a", Label: "MK4S-1", Detail: "printing"}, {Value: "b", Label: "XL"}}

	encoded, err := json.Marshal(modalView(domain.Modal{Title: "Printer status", Inputs: []domain.Input{
		{ID: "printers", Label: "Printers", Choices: few},
		{ID: "post", Label: "Share", Optional: true, Choices: []domain.Choice{{Value: "post", Label: "Post it"}}},
	}}))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{`"type":"checkboxes"`, `"action_id":"printers"`, `"value":"a"`, `"text":"printing"`, `"optional":true`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("the window lacks %s: %s", want, encoded)
		}
	}

	var many []domain.Choice
	for i := range 12 {
		many = append(many, domain.Choice{Value: fmt.Sprint(i), Label: fmt.Sprint("printer ", i), Detail: "idle"})
	}

	encoded, _ = json.Marshal(modalView(domain.Modal{Inputs: []domain.Input{{ID: "printers", Label: "Printers", Choices: many}}}))
	if !strings.Contains(string(encoded), `"type":"multi_static_select"`) || strings.Contains(string(encoded), `"description"`) {
		t.Errorf("12 choices went out as %s", encoded)
	}
}

func TestRenderButtonsAndLink(t *testing.T) {
	encoded, err := json.Marshal(Render(domain.Message{
		Lines:   []string{"Suspend example.user?"},
		Link:    "https://printserver.example/link?for=A&B",
		Buttons: []domain.Button{{Action: "suspend-confirm", Value: "tok", Label: "Suspend", Danger: true}},
	}))
	if err != nil {
		t.Fatal(err)
	}

	out := string(encoded)
	for _, want := range []string{`"action_id":"suspend-confirm"`, `"value":"tok"`, `"style":"danger"`, "\\u003chttps://printserver.example/link?for=A\\u0026amp;B\\u003e"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}
