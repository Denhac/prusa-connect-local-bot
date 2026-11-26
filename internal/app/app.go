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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"

	"github.com/denhac/prusa-connect-local-bot/internal/camera"
	"github.com/denhac/prusa-connect-local-bot/internal/connect"
	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/routes"
	"github.com/denhac/prusa-connect-local-bot/internal/serverhttp"
	"github.com/denhac/prusa-connect-local-bot/internal/settings"
	slackbot "github.com/denhac/prusa-connect-local-bot/internal/slack"
	"github.com/denhac/prusa-connect-local-bot/internal/store"
	"github.com/denhac/prusa-connect-local-bot/internal/verify"
)

// What the module reports to the host. Name is the manifest's name, and the
// version is the manifest's version: manifest_test.go holds them together.
const (
	Name    = "slack-bot"
	Version = extension.Version("0.3.2")
)

// Namespace is where the server mounts the module's routes: the module path
// github.com/denhac/prusa-connect-local-bot, as the server spells it.
const Namespace = "com.github.aux.denhac.prusa-connect-local-bot"

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

	m := newModule(log, openAPI, connect.NewFleet(deviceapi.NewClient(conn)), connect.NewEvents(conn, log),
		connect.NewAccess(accessapi.NewClient(conn)), time.Now)
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
	events  domain.Events
	now     func() time.Time
	started time.Time

	mu          sync.Mutex
	configured  bool
	values      settings.Values
	slack       *slackbot.Runner
	notify      *notifyLoop
	notifyOff   string
	accounts    *domain.AccountsService
	accountsOff string
	access      domain.Access
	session     *serverhttp.Session
	cancel      context.CancelFunc
	done        chan struct{}
}

func newModule(
	log *slog.Logger, openAPI []byte, fleet domain.Fleet, events domain.Events, access domain.Access, now func() time.Time,
) *module {
	return &module{log: log, openAPI: openAPI, fleet: fleet, events: events, access: access, now: now, started: now()}
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
		routesapi.MethodServe:   routesapi.Serve(routes.New(m.health, m.linkPage, routesapi.PrincipalOf)),
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

	session, err := serverhttp.New(values.ServerURL, values.AdminUsername, values.AdminPassword, m.now)
	if err != nil {
		return fmt.Errorf("server_url: %w", err)
	}

	m.session = session

	var sources domain.CombinedStills

	if len(values.Cameras) > 0 {
		cameras, err := camera.New(values.Cameras)
		if err != nil {
			return fmt.Errorf("cameras: %w", err)
		}

		sources.Pi = cameras
	}

	if !values.AdminPassword.Empty() {
		sources.Server = serverhttp.NewCameras(session, m.now)
	}

	var stills domain.Stills
	if sources.Pi != nil || sources.Server != nil {
		stills = sources
	}

	var (
		state     *store.Store
		saved     store.State
		stateFail string
	)

	if grants.Data == "" {
		stateFail = "the server granted no data directory; approve the module's data request"
	} else if state, saved, err = store.Open(grants.Data); err != nil {
		m.log.Error("cannot read the state file; status messages and accounts stay off", "error", err)
		stateFail = "the state file cannot be read: " + err.Error()
		state = nil
	}

	if !values.SlackReady() {
		m.notifyOff, m.accountsOff = "the Slack tokens are not set", "the Slack tokens are not set"

		return nil
	}

	slackConfig := slackbot.Config{
		BotToken:    values.SlackBotToken,
		AppToken:    values.SlackAppToken,
		AllowGuests: values.AllowGuests,
	}
	poster := slackbot.NewPoster(slackConfig, m.log, m.now)
	profiles := slackbot.NewProfiles(slackConfig, m.log, m.now)

	accounts, err := m.newAccounts(values, session, profiles, state, saved, stateFail)
	if err != nil {
		return err
	}

	bot := domain.NewBot(domain.BotConfig{
		Fleet: m.fleet, Stills: stills, Chat: poster, Health: m.health, Now: m.now,
		StatusChannel: values.StatusChannel, Accounts: accounts,
		Log: m.log.With("component", "bot"),
	})
	runner := slackbot.New(slackConfig, bot, m.log, m.now)

	m.notify, m.notifyOff = m.newNotify(values, state, saved, stateFail, poster, stills)
	m.accounts = accounts

	// The connections outlive this call and end when the module stops.
	background, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	m.slack, m.cancel, m.done = runner, cancel, done

	var wg sync.WaitGroup

	wg.Go(func() { _ = runner.Run(background) }) // Run logs its own end.

	if m.notify != nil {
		wg.Go(func() { m.notify.run(background) })
	}

	if accounts != nil {
		wg.Go(func() { accounts.Recover(background) })
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	return nil
}

// newAccounts builds the account service, or says why it stays off. Only a
// setting of the wrong shape is an error.
func (m *module) newAccounts(
	values settings.Values, session *serverhttp.Session, profiles *slackbot.Profiles,
	state *store.Store, saved store.State, stateFail string,
) (*domain.AccountsService, error) {
	switch {
	case values.AdminPassword.Empty():
		m.accountsOff = "admin_password is not set"

		return nil, nil
	case state == nil:
		m.accountsOff = stateFail

		return nil, nil
	}

	verifier, err := verify.New(values.Verify, profiles, m.now)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}

	link := ""
	if values.PublicURL != "" {
		link = strings.TrimRight(values.PublicURL, "/") + "/api/v1/modules/" + Namespace + "/link"
	}

	return domain.NewAccountsService(domain.AccountsConfig{
		Verifier: verifier, Accounts: serverhttp.NewAccounts(session, values.AdminUsername),
		Access: m.access, Directory: profiles, BotAccount: values.AdminUsername, LinkURL: link,
		Now: m.now, Save: state.SaveAccounts, Log: m.log,
	}, saved.Accounts), nil
}

// newNotify builds the status-message loop, or says why it stays off.
func (m *module) newNotify(
	values settings.Values, state *store.Store, saved store.State, stateFail string,
	chat domain.Chat, stills domain.Stills,
) (*notifyLoop, string) {
	switch {
	case values.StatusChannel == "":
		return nil, "status_channel is not set"
	case state == nil:
		return nil, stateFail
	}

	cfg := domain.DefaultNotifyConfig()
	if len(values.IssueReasons) > 0 {
		cfg.IssueReasons = values.IssueReasons
	}

	return newNotifyLoop(m.log, state, saved.Status, cfg, m.fleet, m.events, chat, stills,
		values.StatusChannel, string(Version), m.now), ""
}

// linkPage answers the module's /link route.
func (m *module) linkPage(ctx context.Context, nonce, principal string) (int, string) {
	m.mu.Lock()
	accounts := m.accounts
	m.mu.Unlock()

	if accounts == nil {
		return 503, "Account features are off on this server."
	}

	return accounts.LinkPage(ctx, nonce, principal)
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

	if m.notify != nil {
		h.Notify = m.notify.Status()
	} else {
		h.Notify.Detail = m.notifyOff
	}

	h.Accounts = "on"
	if m.accounts == nil {
		h.Accounts = "off: " + m.accountsOff
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
	cancel, done, session := m.cancel, m.done, m.session
	m.cancel, m.session = nil, nil
	m.mu.Unlock()

	if session != nil {
		ctx, end := context.WithTimeout(context.Background(), time.Second)
		session.Close(ctx) // sign out, so the server holds no session for a stopped module
		end()
	}

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
