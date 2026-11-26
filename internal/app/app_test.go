package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

type noFleet struct{}

func (noFleet) Printers(context.Context) ([]domain.Printer, error) { return nil, nil }

func (noFleet) Jobs(context.Context, string) (*domain.Job, *domain.Job, error) { return nil, nil, nil }

func (noFleet) History(context.Context, string, time.Time, int) ([]domain.Job, error) {
	return nil, nil
}

type noEvents struct{}

func (noEvents) Run(ctx context.Context, _ func(context.Context, []domain.Event, bool)) error {
	<-ctx.Done()

	return nil
}

// host plays the server's side of the wire against a module, over a pipe.
func host(t *testing.T) (*wirerpc.Conn, *module) {
	t.Helper()

	openAPI, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hostSide, childSide := net.Pipe()
	m := newModule(slog.New(slog.DiscardHandler), openAPI, noFleet{}, noEvents{}, time.Now)

	child, err := wirerpc.NewConn(childSide, wirerpc.Options{Role: wirerpc.RoleChild, Methods: m.methods()})
	if err != nil {
		t.Fatal(err)
	}

	server, err := wirerpc.NewConn(hostSide, wirerpc.Options{Role: wirerpc.RoleHost})
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = child.Serve(ctx) }()
	go func() { _ = server.Serve(ctx) }()

	return server, m
}

func call(t *testing.T, conn *wirerpc.Conn, method wirerpc.Method, params any) (json.RawMessage, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return conn.Call(ctx, method, params) //nolint:wrapcheck // the test reads the error itself.
}

func TestInfo(t *testing.T) {
	server, _ := host(t)

	raw, err := call(t, server, wirerpc.MethodInfo, nil)
	if err != nil {
		t.Fatal(err)
	}

	var info wirerpc.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}

	if info.Name != Name || info.Version != Version {
		t.Errorf("info is %s %s", info.Name, info.Version)
	}

	for _, method := range []wirerpc.Method{wirerpc.MethodConfigure, routesapi.MethodServe} {
		if !slices.Contains(info.Serves, method) {
			t.Errorf("info does not name %s in Serves: %v", method, info.Serves)
		}
	}
}

// TestConfigureIdle is a module installed with no settings: it starts, stays
// off Slack, and lists what it still needs.
func TestConfigureIdle(t *testing.T) {
	server, m := host(t)

	if _, err := call(t, server, wirerpc.MethodConfigure, wirerpc.Settings{Config: json.RawMessage(`{"allow_guests":false}`)}); err != nil {
		t.Fatalf("refused the defaults: %v", err)
	}

	h := m.health()
	if h.Slack.State != domain.SlackOff {
		t.Errorf("Slack is %s with no tokens", h.Slack.State)
	}

	if !slices.Equal(h.Missing, []string{"slack_bot_token", "slack_app_token", "status_channel"}) {
		t.Errorf("missing is %v", h.Missing)
	}

	if _, err := call(t, server, wirerpc.MethodShutdown, nil); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestConfigureRefuses(t *testing.T) {
	cases := map[string]string{
		"a token in the wrong field":     `{"slack_bot_token":"xapp-1"}`,
		"a key the module does not read": `{"surprise":true}`,
	}

	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			server, _ := host(t)

			if _, err := call(t, server, wirerpc.MethodConfigure, wirerpc.Settings{Config: json.RawMessage(config)}); err == nil {
				t.Errorf("took %s", config)
			}
		})
	}
}
