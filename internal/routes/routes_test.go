package routes

import (
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
	handler := New(func() domain.Health { return health })

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
	handler := New(func() domain.Health { return domain.Health{} })

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
