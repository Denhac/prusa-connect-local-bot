package domain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"
)

// Request is one command as a person typed it.
type Request struct {
	// User is the chat platform's ID of the person.
	User string
	// Channel is where they typed it.
	Channel string
	// Text is what followed the slash command.
	Text string
}

// handler answers one command. A handler that answers through the UI itself
// returns an empty message.
type handler func(ctx context.Context, r Request, args []string, ui UI) Message

// BotConfig is what the command handler is built from.
type BotConfig struct {
	Fleet  Fleet
	Stills Stills
	// Chat posts into the channel, for snap and status --here. Nil before
	// Slack is set up.
	Chat   Chat
	Health func() Health
	Now    func() time.Time
	// StatusChannel is #bots, where the member commands answer.
	StatusChannel string
	// Accounts is the account half, nil while account features are off.
	Accounts *AccountsService
	// Log takes what went wrong behind a short answer. Nil logs nothing.
	Log *slog.Logger
}

// Bot answers commands. It holds the ports it reads, and nothing of Slack.
type Bot struct {
	cfg BotConfig
}

// NewBot builds the command handler.
func NewBot(cfg BotConfig) *Bot {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}

	return &Bot{cfg: cfg}
}

// The time limits of the commands that read the fleet or a camera.
const (
	stillTimeout = 20 * time.Second
	jobsSince    = 30 * 24 * time.Hour
	jobsShown    = 10
)

// handlers is the code behind each command in Commands.
func (b *Bot) handlers() map[string]handler {
	plain := func(f func(context.Context, Request, []string) Message) handler {
		return func(ctx context.Context, r Request, args []string, _ UI) Message { return f(ctx, r, args) }
	}

	return map[string]handler{
		"help": plain(func(context.Context, Request, []string) Message { return Help() }),
		"bot": plain(func(ctx context.Context, _ Request, _ []string) Message {
			return BotStatus(ctx, b.cfg.Health(), b.cfg.Fleet, b.cfg.Now())
		}),
		"status": plain(b.status),
		"jobs":   plain(b.jobs),
		"snap":   plain(b.snap),
		"signup": b.accounts(func(ctx context.Context, a *AccountsService, r Request, _ []string, ui UI) Message {
			a.Signup(ctx, r, ui)

			return Message{}
		}),
		"password": b.accounts(func(ctx context.Context, a *AccountsService, r Request, _ []string, ui UI) Message {
			a.Password(ctx, r, ui)

			return Message{}
		}),
		"whoami": b.accounts(func(ctx context.Context, a *AccountsService, r Request, _ []string, _ UI) Message {
			return a.WhoAmI(ctx, r)
		}),
		"link": b.accounts(func(ctx context.Context, a *AccountsService, r Request, args []string, ui UI) Message {
			return a.Link(ctx, r, args, ui)
		}),
		"suspend": b.accounts(func(ctx context.Context, a *AccountsService, r Request, args []string, _ UI) Message {
			return a.Suspend(ctx, r, args)
		}),
		"unsuspend": b.accounts(func(ctx context.Context, a *AccountsService, r Request, args []string, _ UI) Message {
			return a.Unsuspend(ctx, r, args)
		}),
		"lookup": b.accounts(func(ctx context.Context, a *AccountsService, r Request, args []string, _ UI) Message {
			return a.Lookup(ctx, r, args)
		}),
		"admin": b.accounts(func(ctx context.Context, a *AccountsService, r Request, args []string, _ UI) Message {
			return a.MakeAdmin(ctx, r, args)
		}),
		"unadmin": b.accounts(func(ctx context.Context, a *AccountsService, r Request, args []string, _ UI) Message {
			return a.Unadmin(ctx, r, args)
		}),
	}
}

// accounts wraps a command of the account half, which is off without the
// bot's sign-in.
func (b *Bot) accounts(f func(context.Context, *AccountsService, Request, []string, UI) Message) handler {
	return func(ctx context.Context, r Request, args []string, ui UI) Message {
		if b.cfg.Accounts == nil {
			return say("Account features are off: the bot has no sign-in to the printer server yet.")
		}

		return f(ctx, b.cfg.Accounts, r, args, ui)
	}
}

// Interact answers a click or a window's submit.
func (b *Bot) Interact(ctx context.Context, it Interaction, ui UI) {
	a := b.cfg.Accounts
	if a == nil {
		return
	}

	switch {
	case it.Action == "link-confirm":
		a.LinkConfirm(ctx, it, ui)
	case it.Action == "suspend-confirm":
		a.SuspendConfirm(ctx, it, ui)
	case it.Action == "unsuspend-confirm":
		a.UnsuspendConfirm(ctx, it, ui)
	case it.Action == "admin-confirm":
		a.MakeAdminConfirm(ctx, it, ui)
	case it.Action == "unadmin-confirm":
		a.UnadminConfirm(ctx, it, ui)
	case it.Callback == "signup-name" || it.Callback == "signup-replace":
		a.SignupSubmit(ctx, it, ui)
	}
}

// Handle answers one command through its UI.
func (b *Bot) Handle(ctx context.Context, r Request, ui UI) {
	if m := b.answer(ctx, r, ui); len(m.Lines) > 0 || len(m.Fields) > 0 || m.Title != "" {
		_ = ui.Respond(ctx, m) // a failed answer has nowhere else to go
	}
}

func (b *Bot) answer(ctx context.Context, r Request, ui UI) Message {
	name, args := Parse(r.Text)

	c, known := Lookup(name)
	if !known {
		return Unknown(name)
	}

	if c.Who == InBots {
		switch {
		case b.cfg.StatusChannel == "":
			return say("The bot has no status channel set yet, so " + SlashCommand + " " + name + " is off.")
		case r.Channel != b.cfg.StatusChannel:
			return say("Use " + SlashCommand + " " + name + " in #bots, the members' channel.")
		}
	}

	run, ok := b.handlers()[name]
	if !ok {
		return Unknown(name)
	}

	return run(ctx, r, args, ui)
}

// printers reads the fleet for a command, sorted by name.
func (b *Bot) printers(ctx context.Context) ([]Printer, error) {
	ctx, cancel := context.WithTimeout(ctx, fleetTimeout)
	defer cancel()

	printers, err := b.cfg.Fleet.Printers(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller says it in words.
	}

	sort.Slice(printers, func(i, j int) bool {
		return strings.ToLower(printers[i].Label()) < strings.ToLower(printers[j].Label())
	})

	return printers, nil
}

// pick finds the printer a person named: by its name, its id, or the start of
// its name when only one starts that way.
func pick(printers []Printer, name string) (Printer, Message, bool) {
	want := strings.ToLower(name)

	for _, p := range printers {
		if strings.ToLower(p.Label()) == want || p.ID == name {
			return p, Message{}, true
		}
	}

	var starts []Printer

	for _, p := range printers {
		if strings.HasPrefix(strings.ToLower(p.Label()), want) {
			starts = append(starts, p)
		}
	}

	if len(starts) == 1 {
		return starts[0], Message{}, true
	}

	names := make([]string, 0, len(printers))
	for _, p := range printers {
		names = append(names, p.Label())
	}

	return Printer{}, say("There is no printer \""+name+"\".", "The printers are: "+strings.Join(names, ", ")+"."), false
}

func (b *Bot) status(ctx context.Context, r Request, args []string) Message {
	here := slices.Contains(args, "--here")
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--here" })

	printers, err := b.printers(ctx)
	if err != nil {
		return say("The bot cannot read the printers right now. Try again in a moment.")
	}

	if len(args) > 0 {
		p, refusal, ok := pick(printers, strings.Join(args, " "))
		if !ok {
			return refusal
		}

		printers = []Printer{p}
	}

	if len(printers) == 0 {
		return say("The bot can see no printers.")
	}

	msg := Message{Title: "Printers"}

	for _, p := range printers {
		msg.Fields = append(msg.Fields, Field{Label: p.Label(), Value: b.statusLine(ctx, p)})
	}

	if !here || b.cfg.Chat == nil {
		return msg
	}

	if _, err := b.cfg.Chat.Post(ctx, r.Channel, msg); err != nil {
		return say("The bot could not post in the channel.")
	}

	return say("Posted.")
}

// statusLine is one printer in a word or two: its state, its print and its
// heaters.
func (b *Bot) statusLine(ctx context.Context, p Printer) string {
	if !p.Connected {
		return "offline"
	}

	parts := []string{strings.ToLower(p.State)}
	if p.State == "" {
		parts[0] = "online"
	}

	if current, _, err := b.cfg.Fleet.Jobs(ctx, p.ID); err == nil && current != nil {
		job := fmt.Sprintf("%.0f%% of %s", current.Progress, fileName(*current))
		if current.TimeRemaining > 0 {
			job += ", " + span(current.TimeRemaining) + " to go"
		}

		parts = append(parts, job)
	}

	if p.Dialog != nil && p.Dialog.Title != "" {
		parts = append(parts, "asking: "+p.Dialog.Title)
	}

	parts = append(parts, "nozzle "+temp(p.Nozzle), "bed "+temp(p.Bed))

	return strings.Join(parts, " · ")
}

func (b *Bot) jobs(ctx context.Context, _ Request, args []string) Message {
	printers, err := b.printers(ctx)
	if err != nil {
		return say("The bot cannot read the printers right now. Try again in a moment.")
	}

	if len(args) > 0 {
		p, refusal, ok := pick(printers, strings.Join(args, " "))
		if !ok {
			return refusal
		}

		printers = []Printer{p}
	}

	now := b.cfg.Now()

	type ended struct {
		printer string
		job     Job
	}

	var all []ended

	for _, p := range printers {
		history, err := b.cfg.Fleet.History(ctx, p.ID, now.Add(-jobsSince), jobsShown)
		if err != nil {
			continue
		}

		for _, j := range history {
			all = append(all, ended{p.Label(), j})
		}
	}

	if len(all) == 0 {
		return say("No prints ended in the last 30 days.")
	}

	sort.Slice(all, func(i, j int) bool { return all[i].job.Ended.After(all[j].job.Ended) })

	msg := Message{Title: "Last prints"}

	for _, e := range all[:min(len(all), jobsShown)] {
		line := outcomeIcon(e.job.Outcome) + " " + e.printer + " · " + fileName(e.job)
		if d := took(e.job); d > 0 {
			line += " · " + span(d)
		}

		msg.Lines = append(msg.Lines, line+" · "+clock(e.job.Ended, now))
	}

	return msg
}

func (b *Bot) snap(ctx context.Context, r Request, args []string) Message {
	if b.cfg.Stills == nil || b.cfg.Chat == nil {
		return say("No cameras are set up.")
	}

	printers, err := b.printers(ctx)
	if err != nil {
		return say("The bot cannot read the printers right now. Try again in a moment.")
	}

	var withCamera []Printer

	for _, p := range printers {
		if b.cfg.Stills.Has(p.ID) {
			withCamera = append(withCamera, p)
		}
	}

	var p Printer

	switch {
	case len(args) > 0:
		found, refusal, ok := pick(printers, strings.Join(args, " "))
		if !ok {
			return refusal
		}

		p = found
	case len(withCamera) == 1:
		p = withCamera[0]
	default:
		names := make([]string, 0, len(withCamera))
		for _, c := range withCamera {
			names = append(names, c.Label())
		}

		if len(names) == 0 {
			return say("No cameras are set up.")
		}

		return say("Which printer? These have a camera: " + strings.Join(names, ", ") + ".")
	}

	ctx, cancel := context.WithTimeout(ctx, stillTimeout)
	defer cancel()

	jpeg, err := b.cfg.Stills.Still(ctx, p.ID, true)

	switch {
	case errors.Is(err, ErrNoCamera):
		return say(p.Label() + " has no camera set up.")
	case errors.Is(err, ErrCameraBusy):
		return say(p.Label() + "'s camera has no recent picture. Someone may be watching live view; try again later.")
	case errors.Is(err, ErrNoPicture):
		return say(p.Label() + "'s camera has not sent a picture yet.")
	case err != nil:
		b.cfg.Log.Warn("could not get a still", "printer", p.ID, "error", err)

		return say("The bot could not get a picture from " + p.Label() + "'s camera.")
	}

	title := p.Label() + " at " + clock(b.cfg.Now(), b.cfg.Now())
	if err := b.cfg.Chat.Upload(ctx, r.Channel, "", jpeg, title); err != nil {
		b.cfg.Log.Warn("could not post a still", "printer", p.ID, "bytes", len(jpeg), "error", err)

		if errors.Is(err, ErrNotInChannel) {
			return say("The bot is not in this channel, so it cannot post the picture. " +
				"Invite it with /invite and the bot's name, then try again.")
		}

		return say("The bot could not post the picture.")
	}

	return say("Posted.")
}
