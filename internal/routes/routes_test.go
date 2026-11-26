package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

func TestStatus(t *testing.T) {
	health := domain.Health{
		Version: "0.1.0", Started: time.Unix(0, 0).UTC(),
		Slack:   domain.SlackStatus{State: domain.SlackOff},
		Missing: []string{"slack_bot_token"},
	}
	handler := New(func() domain.Health { return health }, noLink, func(*http.Request) string { return "" })

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))

	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("answered %d %q", w.Code, w.Header().Get("Content-Type"))
	}

	var got domain.Health
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if got.Version != "0.1.0" || got.Slack.State != domain.SlackOff || len(got.Missing) != 1 {
		t.Errorf("answered %+v", got)
	}
}

func TestOnlyStatus(t *testing.T) {
	handler := New(func() domain.Health { return domain.Health{} }, noLink, func(*http.Request) string { return "" })

	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/status", nil),
		httptest.NewRequest(http.MethodGet, "/anything", nil),
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code == http.StatusOK {
			t.Errorf("%s %s answered 200", r.Method, r.URL.Path)
		}
	}
}

func noLink(context.Context, string, string) (int, string) { return 503, "off" }

func TestLink(t *testing.T) {
	var asked [2]string

	link := func(_ context.Context, nonce, principal string) (int, string) {
		asked = [2]string{nonce, principal}

		return 200, "Your code: ABCD-EFGH"
	}
	handler := New(func() domain.Health { return domain.Health{} }, link, func(*http.Request) string { return "user:42" })

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/link?for=NONCE", nil))

	if w.Code != 200 || w.Body.String() != "Your code: ABCD-EFGH" || asked != [2]string{"NONCE", "user:42"} {
		t.Errorf("answered %d %q, asked %v", w.Code, w.Body.String(), asked)
	}

	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("a link code may be cached")
	}
}
