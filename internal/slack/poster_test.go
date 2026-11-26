package slack

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/secret"
)

// webAPI is chat.postMessage and chat.update, keeping what they were sent,
// and refusing the first call with a rate limit when limited is set.
type webAPI struct {
	mu      sync.Mutex
	calls   []map[string]string
	limited bool
}

func (a *webAPI) server(t *testing.T) *httptest.Server {
	t.Helper()

	answer := func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		a.mu.Lock()
		defer a.mu.Unlock()

		if a.limited {
			a.limited = false

			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		call := map[string]string{"path": r.URL.Path}
		for k := range r.Form {
			call[k] = r.Form.Get(k)
		}

		a.calls = append(a.calls, call)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": r.Form.Get("channel"), "ts": "1700000000.0001"})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat.postMessage", answer)
	mux.HandleFunc("/api/chat.update", answer)

	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)

	return s
}

func newPoster(t *testing.T, a *webAPI) *Poster {
	t.Helper()

	return NewPoster(Config{BotToken: secret.New("xoxb-test"), APIURL: a.server(t).URL + "/api/"},
		slog.New(slog.DiscardHandler), time.Now)
}

func TestPostReplyUpdate(t *testing.T) {
	a := &webAPI{}
	p := newPoster(t, a)
	ctx := context.Background()
	m := domain.Message{Lines: []string{"▶ MK4S-1 started bracket.bgcode"}}

	ts, err := p.Post(ctx, "C1", m)
	if err != nil || ts != "1700000000.0001" {
		t.Fatalf("Post = %q, %v", ts, err)
	}

	if _, err := p.Reply(ctx, "C1", ts, m); err != nil {
		t.Fatal(err)
	}

	if err := p.Update(ctx, "C1", ts, m); err != nil {
		t.Fatal(err)
	}

	if len(a.calls) != 3 {
		t.Fatalf("Slack got %d calls", len(a.calls))
	}

	if a.calls[0]["text"] != m.Plain() || a.calls[0]["blocks"] == "" || a.calls[0]["thread_ts"] != "" {
		t.Errorf("post sent %v", a.calls[0])
	}

	if a.calls[1]["thread_ts"] != ts {
		t.Errorf("reply sent %v", a.calls[1])
	}

	if a.calls[2]["path"] != "/api/chat.update" || a.calls[2]["ts"] != ts {
		t.Errorf("update sent %v", a.calls[2])
	}
}

// TestPacing keeps two messages to one channel about a second apart, and
// lets another channel go at once.
func TestPacing(t *testing.T) {
	p := newPoster(t, &webAPI{})
	ctx := context.Background()
	m := domain.Message{Lines: []string{"x"}}

	began := time.Now()

	for _, channel := range []string{"C1", "C2", "C1"} {
		if _, err := p.Post(ctx, channel, m); err != nil {
			t.Fatal(err)
		}
	}

	if took := time.Since(began); took < spacing || took > spacing+time.Second {
		t.Errorf("three posts over two channels took %v, want about %v", took, spacing)
	}
}

func TestRetriesOnceAfterARateLimit(t *testing.T) {
	a := &webAPI{limited: true}
	p := newPoster(t, a)

	if _, err := p.Post(context.Background(), "C1", domain.Message{Lines: []string{"x"}}); err != nil {
		t.Fatalf("Post after a rate limit: %v", err)
	}

	if len(a.calls) != 1 {
		t.Errorf("Slack got %d calls that went through", len(a.calls))
	}
}

func TestWaitHonoursTheContext(t *testing.T) {
	p := newPoster(t, &webAPI{})
	p.next["C1"] = time.Now().Add(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := p.Post(ctx, "C1", domain.Message{Lines: []string{"x"}}); err == nil {
		t.Error("posted although the context ended first")
	}
}
