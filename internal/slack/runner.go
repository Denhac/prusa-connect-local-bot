package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/secret"
)

// ErrAuth is a token Slack refused. Running again with the same settings would
// be refused again, so the caller stops until the settings change.
var ErrAuth = errors.New("slack refused the token")

// Handler answers commands, clicks and window submits.
type Handler interface {
	Handle(ctx context.Context, r domain.Request, ui domain.UI)
	Interact(ctx context.Context, it domain.Interaction, ui domain.UI)
}

// Config is what the runner needs from the settings.
type Config struct {
	BotToken    secret.Secret
	AppToken    secret.Secret
	AllowGuests bool
	// APIURL replaces https://slack.com/api/ in tests.
	APIURL string
}

const (
	// maxCommands bounds the commands answered at once. More are refused
	// with "busy" rather than queued.
	maxCommands = 8
	// commandTimeout bounds one answer. response_url itself lasts 30 minutes.
	commandTimeout = 25 * time.Second
	// guestCacheFor is how long a users.info answer is reused.
	guestCacheFor = 10 * time.Minute
	// retryFirst and retryMost bound the wait between connection attempts.
	retryFirst = 5 * time.Second
	retryMost  = 5 * time.Minute
)

// Runner holds the Socket Mode connection and answers commands on it.
type Runner struct {
	cfg     Config
	handler Handler
	log     *slog.Logger
	now     func() time.Time
	http    *http.Client

	mu     sync.Mutex
	status domain.SlackStatus
	team   string
	guests map[string]guestAnswer

	slots chan struct{}
	wg    sync.WaitGroup
}

type guestAnswer struct {
	refused bool
	at      time.Time
}

// New builds a runner. Nothing connects until Run.
func New(cfg Config, handler Handler, log *slog.Logger, now func() time.Time) *Runner {
	r := &Runner{
		cfg:     cfg,
		handler: handler,
		log:     log.With("component", "slack"),
		now:     now,
		http:    &http.Client{Timeout: 15 * time.Second},
		guests:  map[string]guestAnswer{},
		slots:   make(chan struct{}, maxCommands),
	}
	r.set(domain.SlackConnecting, "")

	return r
}

// Status is the connection as the runner last saw it.
func (r *Runner) Status() domain.SlackStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.status
}

func (r *Runner) set(state domain.SlackState, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status.State != state {
		r.status.Since = r.now()
	}

	r.status.State, r.status.Detail = state, detail
	r.status.Team = r.team
}

// Run connects and answers commands until ctx ends. It reconnects by itself,
// waiting longer after each failure, and returns ErrAuth when Slack refuses a
// token. It waits for the commands in flight before it returns.
func (r *Runner) Run(ctx context.Context) error {
	defer r.wg.Wait()

	wait := retryFirst

	for {
		err := r.connectOnce(ctx)

		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrAuth):
			r.set(domain.SlackAuth, "check the Slack tokens in the settings")
			r.log.Error("Slack refused a token; not retrying until the settings change", "error", err)

			return err
		}

		r.set(domain.SlackRetrying, err.Error())
		r.log.Warn("the Slack connection ended; retrying", "error", err, "in", wait)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}

		wait = min(wait*2, retryMost)
	}
}

// connectOnce checks the bot token, opens Socket Mode and serves it until the
// connection fails or ctx ends.
func (r *Runner) connectOnce(ctx context.Context) error {
	api := r.api()

	auth, err := api.AuthTestContext(ctx)
	if err != nil {
		return classify("auth.test", err)
	}

	r.mu.Lock()
	r.team = auth.TeamID
	r.mu.Unlock()
	r.log.Info("the bot token belongs to a workspace", "team", auth.Team, "team_id", auth.TeamID, "bot_user", auth.UserID)

	client := socketmode.New(api, socketmode.OptionLog(slog.NewLogLogger(r.log.Handler(), slog.LevelDebug)))
	done := make(chan error, 1)

	go func() { done <- client.RunContext(ctx) }()

	for {
		select {
		case <-ctx.Done():
			<-done

			return nil
		case err := <-done:
			return classify("socket mode", err)
		case ev, ok := <-client.Events:
			if !ok {
				return errors.New("socket mode closed its events")
			}

			r.dispatch(ctx, client, api, ev)
		}
	}
}

func (r *Runner) api() *slack.Client {
	options := []slack.Option{
		slack.OptionAppLevelToken(r.cfg.AppToken.Reveal()),
		slack.OptionLog(slog.NewLogLogger(r.log.Handler(), slog.LevelDebug)),
	}
	if r.cfg.APIURL != "" {
		options = append(options, slack.OptionAPIURL(r.cfg.APIURL))
	}

	return slack.New(r.cfg.BotToken.Reveal(), options...)
}

// classify turns Slack's refusal of a token into ErrAuth.
func classify(step string, err error) error {
	if err == nil {
		return fmt.Errorf("%s ended", step)
	}

	var slackErr slack.SlackErrorResponse
	if errors.As(err, &slackErr) {
		switch slackErr.Err {
		case "invalid_auth", "not_authed", "account_inactive", "token_revoked", "not_allowed_token_type":
			return fmt.Errorf("%w: %s answered %s", ErrAuth, step, slackErr.Err)
		}
	}

	switch err.Error() {
	case "invalid_auth", "not_authed", "account_inactive", "token_revoked", "not_allowed_token_type":
		return fmt.Errorf("%w: %s answered %s", ErrAuth, step, err)
	}

	return fmt.Errorf("%s: %w", step, err)
}

// dispatch handles one Socket Mode event. Every request is acknowledged at
// once, inside Slack's 3 seconds; the answer follows through response_url.
func (r *Runner) dispatch(ctx context.Context, client *socketmode.Client, api *slack.Client, ev socketmode.Event) {
	switch ev.Type {
	case socketmode.EventTypeConnecting:
		r.set(domain.SlackConnecting, "")
	case socketmode.EventTypeConnected:
		r.set(domain.SlackConnected, "")
		r.log.Info("connected to Slack over Socket Mode")
	case socketmode.EventTypeConnectionError:
		r.set(domain.SlackRetrying, fmt.Sprint(ev.Data))
	case socketmode.EventTypeDisconnect:
		r.set(domain.SlackConnecting, "Slack asked to reconnect")
	case socketmode.EventTypeSlashCommand:
		cmd, ok := ev.Data.(slack.SlashCommand)
		r.ack(client, ev)

		if ok {
			r.start(ctx, api, cmd)
		}
	case socketmode.EventTypeInteractive:
		cb, ok := ev.Data.(slack.InteractionCallback)
		if ok && cb.Type == slack.InteractionTypeViewSubmission && ev.Request != nil {
			// Keep the window open, showing that the bot is at work; the
			// answer replaces it.
			if err := client.Ack(*ev.Request, slack.NewUpdateViewSubmissionResponse(working())); err != nil {
				r.log.Warn("could not acknowledge a window's submit", "error", err)
			}
		} else {
			r.ack(client, ev)
		}

		if ok && (cb.Type == slack.InteractionTypeViewSubmission || cb.Type == slack.InteractionTypeBlockActions) {
			r.interact(ctx, api, cb)
		}
	case socketmode.EventTypeEventsAPI:
		r.ack(client, ev)
	}
}

func (r *Runner) ack(client *socketmode.Client, ev socketmode.Event) {
	if ev.Request == nil {
		return
	}

	if err := client.Ack(*ev.Request); err != nil {
		r.log.Warn("could not acknowledge a Slack request", "type", ev.Type, "error", err)
	}
}

// work runs one command or click in its own goroutine, within the bound.
func (r *Runner) work(ctx context.Context, u ui, run func(context.Context)) {
	select {
	case r.slots <- struct{}{}:
	default:
		_ = u.Respond(ctx, domain.Message{Lines: []string{"The bot is busy. Try again in a moment."}})

		return
	}

	r.wg.Go(func() {
		defer func() { <-r.slots }()

		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
		defer cancel()

		run(ctx)
	})
}

// start answers one command.
func (r *Runner) start(ctx context.Context, api *slack.Client, cmd slack.SlashCommand) {
	u := ui{api: api, http: r.http, responseURL: cmd.ResponseURL, trigger: cmd.TriggerID, user: cmd.UserID}

	r.work(ctx, u, func(ctx context.Context) {
		if refusal, ok := r.answer(ctx, api, cmd.TeamID, cmd.UserID, cmd.Text); !ok {
			_ = u.Respond(ctx, refusal)

			return
		}

		r.handler.Handle(ctx, domain.Request{User: cmd.UserID, Channel: cmd.ChannelID, Text: cmd.Text}, u)
	})
}

// interact answers a click or a window's submit.
func (r *Runner) interact(ctx context.Context, api *slack.Client, cb slack.InteractionCallback) {
	u := ui{api: api, http: r.http, responseURL: cb.ResponseURL, trigger: cb.TriggerID, user: cb.User.ID, replace: true}
	it := domain.Interaction{User: cb.User.ID}

	switch cb.Type {
	case slack.InteractionTypeBlockActions:
		if len(cb.ActionCallback.BlockActions) == 0 {
			return
		}

		it.Action, it.Value = cb.ActionCallback.BlockActions[0].ActionID, cb.ActionCallback.BlockActions[0].Value
	case slack.InteractionTypeViewSubmission:
		it.Callback, it.Meta, it.ModalID = cb.View.CallbackID, cb.View.PrivateMetadata, cb.View.ID
		it.Inputs, it.Picked = map[string]string{}, map[string][]string{}

		if cb.View.State != nil {
			for _, actions := range cb.View.State.Values {
				for id, action := range actions {
					switch action.Type {
					case slack.ActionType(slack.METCheckboxGroups), slack.ActionType(slack.MultiOptTypeStatic):
						for _, o := range action.SelectedOptions {
							it.Picked[id] = append(it.Picked[id], o.Value)
						}
					default:
						it.Inputs[id] = action.Value
					}
				}
			}
		}
	}

	r.work(ctx, u, func(ctx context.Context) {
		if refusal, ok := r.answer(ctx, api, cb.Team.ID, cb.User.ID, "click"); !ok {
			_ = u.Respond(ctx, refusal)

			return
		}

		r.handler.Interact(ctx, it, u)
	})
}

// answer checks who is asking. Only the command's first word is logged,
// never its arguments.
func (r *Runner) answer(ctx context.Context, api *slack.Client, teamID, user, text string) (domain.Message, bool) {
	name, _ := domain.Parse(text)
	log := r.log.With("user", user, "command", name)

	r.mu.Lock()
	team := r.team
	r.mu.Unlock()

	if teamID != team {
		log.Warn("refused a request from another workspace", "team_id", teamID)

		return domain.Message{Lines: []string{"This bot answers only in its own workspace."}}, false
	}

	refused, err := r.guest(ctx, api, user)
	if err != nil {
		log.Warn("could not read the Slack user", "error", err)

		return domain.Message{Lines: []string{"The bot cannot check your Slack account right now. Try again later."}}, false
	}

	if refused {
		log.Info("refused a request from a guest")

		return domain.Message{Lines: []string{"Guest accounts cannot use this bot."}}, false
	}

	log.Info("answering")

	return domain.Message{}, true
}

// guest reports whether the user is refused for being a guest, or for being a
// deactivated account. Answers are kept for a while.
func (r *Runner) guest(ctx context.Context, api *slack.Client, user string) (bool, error) {
	r.mu.Lock()
	held, ok := r.guests[user]
	r.mu.Unlock()

	if ok && r.now().Sub(held.at) < guestCacheFor {
		return held.refused, nil
	}

	info, err := api.GetUserInfoContext(ctx, user)
	if err != nil {
		return false, fmt.Errorf("users.info: %w", err)
	}

	refused := info.Deleted || ((info.IsRestricted || info.IsUltraRestricted) && !r.cfg.AllowGuests)

	r.mu.Lock()
	r.guests[user] = guestAnswer{refused: refused, at: r.now()}
	r.mu.Unlock()

	return refused, nil
}

// respond answers a command through its response_url, which only the person
// who typed it sees, and which Slack keeps in no channel's history.
func (r *Runner) respond(ctx context.Context, url string, m domain.Message) {
	msg := &slack.WebhookMessage{
		ResponseType: slack.ResponseTypeEphemeral,
		Text:         m.Plain(),
		Blocks:       &slack.Blocks{BlockSet: Render(m)},
	}

	if err := slack.PostWebhookCustomHTTPContext(ctx, url, r.http, msg); err != nil {
		r.log.Warn("could not answer a command", "error", err)
	}
}
