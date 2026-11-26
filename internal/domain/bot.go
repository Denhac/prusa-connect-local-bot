package domain

import (
	"context"
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

// handler answers one command.
type handler func(ctx context.Context, r Request, args []string) Message

// Bot answers commands. It holds the ports it reads, and nothing of Slack.
type Bot struct {
	fleet  Fleet
	health func() Health
	now    func() time.Time
}

// NewBot builds the command handler over its ports.
func NewBot(fleet Fleet, health func() Health, now func() time.Time) *Bot {
	return &Bot{fleet: fleet, health: health, now: now}
}

// handlers is the code behind each command in Commands.
func (b *Bot) handlers() map[string]handler {
	return map[string]handler{
		"help": func(context.Context, Request, []string) Message { return Help() },
		"bot": func(ctx context.Context, _ Request, _ []string) Message {
			return BotStatus(ctx, b.health(), b.fleet, b.now())
		},
	}
}

// Handle answers one command.
func (b *Bot) Handle(ctx context.Context, r Request) Message {
	name, args := Parse(r.Text)

	if _, known := Lookup(name); !known {
		return Unknown(name)
	}

	run, ok := b.handlers()[name]
	if !ok {
		return Unknown(name)
	}

	return run(ctx, r, args)
}
