package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SlackState is where the connection to Slack stands.
type SlackState string

// The states of the Slack connection.
const (
	// SlackOff is a module that has not been given both Slack tokens.
	SlackOff SlackState = "not configured"
	// SlackConnecting is a connection being opened, or opened again.
	SlackConnecting SlackState = "connecting"
	// SlackConnected is a working connection.
	SlackConnected SlackState = "connected"
	// SlackRetrying is a connection that failed and will be tried again.
	SlackRetrying SlackState = "retrying"
	// SlackAuth is a token Slack refused. Nothing is retried until the
	// settings change.
	SlackAuth SlackState = "token refused"
)

// SlackStatus is the Slack connection as the module last saw it.
type SlackStatus struct {
	State  SlackState `json:"state"`
	Since  time.Time  `json:"since"`
	Detail string     `json:"detail,omitempty"`
	// Team is the workspace the bot token belongs to.
	Team string `json:"team,omitempty"`
}

// Health is how the module itself is doing. GET /status answers it as JSON and
// the bot command draws it.
type Health struct {
	Version    string      `json:"version"`
	SDKVersion string      `json:"sdk_version"`
	Started    time.Time   `json:"started"`
	Slack      SlackStatus `json:"slack"`
	// Missing names the settings the module still needs.
	Missing []string `json:"missing,omitempty"`
}

// Printer is one printer as the bot needs it.
type Printer struct {
	ID        string
	Name      string
	Model     string
	Connected bool
}

// Fleet reads the printers the module may see. internal/connect implements it
// over the server's wire.
type Fleet interface {
	Printers(ctx context.Context) ([]Printer, error)
}

// fleetTimeout bounds a fleet read made for a command, so an answer comes
// before Slack's user has given up.
const fleetTimeout = 3 * time.Second

// BotStatus is the answer to the bot command: the module's health and what it
// can see of the fleet.
func BotStatus(ctx context.Context, health Health, fleet Fleet, now time.Time) Message {
	fields := []Field{
		{Label: "Version", Value: health.Version + " (SDK " + health.SDKVersion + ")"},
		{Label: "Running for", Value: since(now, health.Started)},
		{Label: "Slack", Value: slackLine(health.Slack, now)},
		{Label: "Printers", Value: printersLine(ctx, fleet)},
	}

	if len(health.Missing) > 0 {
		fields = append(fields, Field{Label: "Settings missing", Value: strings.Join(health.Missing, ", ")})
	}

	return Message{
		Title:  "Printer bot",
		Fields: fields,
		Footer: "Built on Prusa Connect by Prusa Research.",
	}
}

func slackLine(s SlackStatus, now time.Time) string {
	line := string(s.State) + " for " + since(now, s.Since)
	if s.Detail != "" {
		line += " (" + s.Detail + ")"
	}

	return line
}

func printersLine(ctx context.Context, fleet Fleet) string {
	ctx, cancel := context.WithTimeout(ctx, fleetTimeout)
	defer cancel()

	printers, err := fleet.Printers(ctx)
	if err != nil {
		return "cannot read the printers right now"
	}

	online := 0

	for _, p := range printers {
		if p.Connected {
			online++
		}
	}

	return fmt.Sprintf("%d visible, %d online", len(printers), online)
}

// since says how long ago a moment was, to the minute.
func since(now, then time.Time) string {
	if then.IsZero() {
		return "unknown"
	}

	d := now.Sub(then).Round(time.Minute)

	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}

	return fmt.Sprintf("%d days", int(d.Hours())/24)
}
