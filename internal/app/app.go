// Package app builds the module from its parts and runs it under the host.
//
// The host launches this process, reads one handshake line from standard
// output, and then speaks the module wire over standard input and output. It
// calls plugin.info, then plugin.configure with the settings, and from then on
// pings, routes requests and finally asks the module to stop.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"

	"github.com/denhac/prusa-connect-local-bot/internal/connect"
	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/routes"
	"github.com/denhac/prusa-connect-local-bot/internal/settings"
	slackbot "github.com/denhac/prusa-connect-local-bot/internal/slack"
)

// What the module reports to the host. Name is the manifest's name, and the
// version is the manifest's version: manifest_test.go holds them together.
const (
	Name    = "slack-bot"
	Version = extension.Version("0.1.0")
)

// sdkModule is the SDK's Go module path, read back from the build information.
const sdkModule = "github.com/prusa3d/prusa-connect/sdk"

// stopWait bounds how long a stop waits for the Slack connection to close. The
// host kills the process 5 s after it asks the module to stop.
const stopWait = 2 * time.Second

// Run serves the module wire until the host closes it or the process is told
// to stop. openAPI is the document describing the module's routes.
func Run(openAPI []byte) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The host copies standard error into its own log, line by line, under
	// its own timestamp, so the module writes none of its own.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}

			return a
		},
	})).With("module", Name)

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return fmt.Errorf("render the handshake: %w", err)
	}

	if _, err = os.Stdout.WriteString(line); err != nil {
		return fmt.Errorf("write the handshake: %w", err)
	}

	// From here standard output belongs to the wire. Anything else that
	// writes to os.Stdout, such as a stray print in a library, goes to
	// standard error instead, where the host logs it.
	wireOut := os.Stdout
	os.Stdout = os.Stderr

	serves := wirerpc.Methods{}

	conn, err := wirerpc.NewConn(stdio{out: wireOut}, wirerpc.Options{
		Role: wirerpc.RoleChild, Methods: serves, Logger: log.With("component", "wire"),
	})
	if err != nil {
		return fmt.Errorf("build the session: %w", err)
	}

	m := newModule(log, openAPI, connect.NewFleet(deviceapi.NewClient(conn)), time.Now)
	maps.Copy(serves, m.methods())

	log.Info("serving on stdio", "version", Version, "sdk", sdkVersion(), "pid", os.Getpid())

	err = conn.Serve(ctx)
	m.stop()

	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("serve: %w", err)
	}

	log.Info("stopped")

	return nil
}

// module is everything the process holds.
type module struct {
	log     *slog.Logger
	openAPI []byte
	fleet   domain.Fleet
	now     func() time.Time
	started time.Time

	mu         sync.Mutex
	configured bool
	values     settings.Values
	slack      *slackbot.Runner
	cancel     context.CancelFunc
	done       chan struct{}
}

func newModule(log *slog.Logger, openAPI []byte, fleet domain.Fleet, now func() time.Time) *module {
	return &module{log: log, openAPI: openAPI, fleet: fleet, now: now, started: now()}
}

// methods is what the module answers on the wire.
func (m *module) methods() wirerpc.Methods {
	return wirerpc.Methods{
		wirerpc.MethodInfo: func(context.Context, wirerpc.Call) (any, error) {
			return routesapi.Describe(wirerpc.Info{
				Name:     Name,
				Version:  Version,
				Contract: wirerpc.Contract,
				Serves:   []wirerpc.Method{wirerpc.MethodConfigure},
			}, m.openAPI)
		},
		wirerpc.MethodShutdown: func(context.Context, wirerpc.Call) (any, error) {
			m.log.Info("the host asked the module to stop")
			m.stop()

			return nil, nil
		},
		wirerpc.MethodConfigure: wirerpc.ConfigureGranted(m.configure),
		routesapi.MethodServe:   routesapi.Serve(routes.New(m.health)),
	}
}

// configure takes the settings, which the host sends once after plugin.info.
// A value of the wrong shape refuses the start, and the operator reads why on
// the instance. Settings that are only missing leave the module running idle.
func (m *module) configure(ctx context.Context, sent settings.Sent, grants wirerpc.Grants) error {
	values, err := settings.Read(sent)
	if err != nil {
		return err //nolint:wrapcheck // the sentence the operator reads.
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.configured {
		// The host restarts a module whose settings change, so this does not
		// happen. If it ever does, the running connection is kept.
		m.log.Warn("settings arrived a second time; keeping the first")

		return nil
	}

	m.configured, m.values = true, values

	if grants.Data != "" {
		m.log.Info("the host granted a data directory", "path", grants.Data)
	}

	if missing := values.Missing(); len(missing) > 0 {
		m.log.Warn("settings are missing; the parts that need them stay off", "missing", missing)
	}

	if !values.SlackReady() {
		return nil
	}

	runner := slackbot.New(slackbot.Config{
		BotToken:    values.SlackBotToken,
		AppToken:    values.SlackAppToken,
		AllowGuests: values.AllowGuests,
	}, domain.NewBot(m.fleet, m.health, m.now), m.log, m.now)

	// The connection outlives this call and ends when the module stops.
	background, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	m.slack, m.cancel, m.done = runner, cancel, done

	go func() {
		defer close(done)

		_ = runner.Run(background) // Run logs its own end.
	}()

	return nil
}

// health is how the module is doing, for GET /status and the bot command.
func (m *module) health() domain.Health {
	m.mu.Lock()
	defer m.mu.Unlock()

	h := domain.Health{
		Version:    string(Version),
		SDKVersion: sdkVersion(),
		Started:    m.started,
		Slack:      domain.SlackStatus{State: domain.SlackOff, Since: m.started},
	}

	if m.slack != nil {
		h.Slack = m.slack.Status()
	}

	if m.configured {
		h.Missing = m.values.Missing()
	}

	return h
}

// stop ends the Slack connection, waiting at most stopWait. It runs once,
// whichever of plugin.shutdown, a signal and the end of the wire comes first.
func (m *module) stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel = nil
	m.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()

	select {
	case <-done:
	case <-time.After(stopWait):
		m.log.Warn("the Slack connection did not close in time")
	}
}

// sdkVersion is the SDK version this binary was built with.
func sdkVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	for _, dep := range info.Deps {
		if dep.Path == sdkModule {
			return dep.Version
		}
	}

	return "unknown"
}

// stdio is the channel the host handed over: standard input, and the standard
// output saved before Run pointed os.Stdout elsewhere. Close shuts the reading
// half, which ends a session whose host has gone.
type stdio struct {
	out *os.File
}

func (stdio) Read(p []byte) (int, error)    { return os.Stdin.Read(p) }
func (s stdio) Write(p []byte) (int, error) { return s.out.Write(p) }
func (stdio) Close() error                  { return os.Stdin.Close() }
