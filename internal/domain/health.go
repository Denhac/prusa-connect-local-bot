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

// NotifyStatus is how the status messages are doing.
type NotifyStatus struct {
	// Running is whether the module is following the event stream.
	Running bool `json:"running"`
	// LastEvent is when the last event was applied.
	LastEvent time.Time `json:"last_event,omitzero"`
	// Detail says why it is not running, or what went wrong last.
	Detail string `json:"detail,omitempty"`
}

// Health is how the module itself is doing. GET /status answers it as JSON and
// the bot command draws it.
type Health struct {
	Version    string       `json:"version"`
	SDKVersion string       `json:"sdk_version"`
	Started    time.Time    `json:"started"`
	Slack      SlackStatus  `json:"slack"`
	Notify     NotifyStatus `json:"notify"`
	// Missing names the settings the module still needs.
	Missing []string `json:"missing,omitempty"`
}

// fleetTimeout bounds a fleet read made for a command, so an answer comes
// before the person has given up.
const fleetTimeout = 5 * time.Second

// BotStatus is the answer to the bot command: the module's health and what it
// can see of the fleet.
func BotStatus(ctx context.Context, health Health, fleet Fleet, now time.Time) Message {
	fields := []Field{
		{Label: "Version", Value: health.Version + " (SDK " + health.SDKVersion + ")"},
		{Label: "Running for", Value: since(now, health.Started)},
		{Label: "Slack", Value: slackLine(health.Slack, now)},
		{Label: "Status messages", Value: notifyLine(health.Notify, now)},
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

func notifyLine(n NotifyStatus, now time.Time) string {
	if !n.Running {
		if n.Detail != "" {
			return "off: " + n.Detail
		}

		return "off"
	}

	line := "on"
	if !n.LastEvent.IsZero() {
		line += ", last event " + since(now, n.LastEvent) + " ago"
	}

	if n.Detail != "" {
		line += " (" + n.Detail + ")"
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
