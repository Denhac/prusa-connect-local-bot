package connect

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// Types is every event type the bot follows.
func Types() []string {
	return []string{
		event.TypeDeviceJobStarted,
		event.TypeDeviceJobSettled,
		event.TypeDeviceIssuesChanged,
		event.TypeDeviceStateChanged,
		event.TypeDevicePresenceChanged,
		event.TypeDeviceArrived,
		event.TypeDeviceDeparted,
		event.TypeDeviceRecordChanged,
	}
}

// windowMS is the coalescing window the bot asks for.
const windowMS = 1000

// Events implements domain.Events over bus.subscribe.
type Events struct {
	conn busapi.Opener
	log  *slog.Logger
}

// NewEvents builds the event reader over the module's session.
func NewEvents(conn busapi.Opener, log *slog.Logger) Events {
	return Events{conn: conn, log: log.With("component", "events")}
}

// Run subscribes until ctx ends. The cursor is kept in memory only: the
// server's own replay does not outlive it, so after a server restart the
// server answers with a resync and the caller reads the fleet again.
func (e Events) Run(ctx context.Context, handle func(context.Context, []domain.Event, bool)) error {
	cursor := ""
	wait := 2 * time.Second

	for {
		err := busapi.Subscribe(ctx, e.conn, busapi.SubscribeParams{
			Types: Types(), Resume: cursor, IntervalMS: windowMS,
		}, func(w busapi.Window) error {
			wait = 2 * time.Second
			cursor = w.Cursor

			batch := make([]domain.Event, 0, len(w.Events))
			for _, raw := range w.Events {
				if ev, ok := Decode(raw); ok {
					batch = append(batch, ev)
				}
			}

			handle(ctx, batch, w.Resync)

			return nil
		})

		if ctx.Err() != nil {
			return nil
		}

		e.log.Warn("the event subscription ended; subscribing again", "error", err, "in", wait)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}

		wait = min(wait*2, time.Minute)
	}
}

// payload is the fields the bot reads from the device events that have no
// type in the SDK: presence, arrival, departure, issues and the record.
type payload struct {
	DeviceID  string            `json:"device_id"`
	Connected *bool             `json:"connected"`
	Issues    []deviceapi.Issue `json:"issues"`
	Dialog    *deviceapi.Dialog `json:"dialog"`
	Name      string            `json:"name"`
}

// Decode turns one envelope into an event. An envelope of a type the bot does
// not follow, or one it cannot read, is skipped.
func Decode(raw json.RawMessage) (domain.Event, bool) {
	var env event.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return domain.Event{}, false
	}

	ev := domain.Event{At: env.Time}

	// A device's events name it as their subject, device:<id>. The job
	// events carry it nowhere else.
	if origin, err := event.ParseOrigin(env.Subject); err == nil && origin.Kind() == event.KindDevice {
		ev.Device = origin.ID()
	}

	var p payload
	_ = json.Unmarshal(env.Data, &p) // a job payload has none of these fields

	if ev.Device == "" {
		ev.Device = p.DeviceID
	}

	if ev.Device == "" {
		return domain.Event{}, false
	}

	switch env.Type {
	case event.TypeDeviceJobStarted:
		var j event.JobStarted
		if json.Unmarshal(env.Data, &j) != nil || j.JobID == "" {
			return domain.Event{}, false
		}

		ev.Kind = domain.EventJobStarted
		ev.Job = domain.Job{ID: j.JobID, File: shown(j.DisplayName, j.Path), Started: j.Started}
	case event.TypeDeviceJobSettled:
		var j event.JobSettled
		if json.Unmarshal(env.Data, &j) != nil || j.JobID == "" {
			return domain.Event{}, false
		}

		ev.Kind = domain.EventJobSettled
		ev.Job = domain.Job{
			ID: j.JobID, File: shown(j.DisplayName, j.Path), Started: j.Started, Ended: j.Ended,
			Outcome: j.Outcome, Progress: j.Progress,
			Printed: time.Duration(j.TimePrintingS) * time.Second, PrintedKnown: j.TimePrintingKnown,
			FilamentMM: j.FilamentUsedMM, FilamentKnown: j.FilamentUsedKnown,
		}
	case event.TypeDeviceIssuesChanged:
		ev.Kind, ev.Issues, ev.Dialog = domain.EventIssuesChanged, issuesOf(p.Issues), dialogOf(p.Dialog)
	case event.TypeDeviceStateChanged:
		var s event.StateChanged
		if json.Unmarshal(env.Data, &s) != nil {
			return domain.Event{}, false
		}

		ev.Kind, ev.State = domain.EventStateChanged, s.To
	case event.TypeDevicePresenceChanged:
		if p.Connected == nil {
			return domain.Event{}, false
		}

		ev.Kind, ev.Connected = domain.EventPresence, *p.Connected
	case event.TypeDeviceArrived:
		ev.Kind, ev.Connected = domain.EventArrived, true
	case event.TypeDeviceDeparted:
		ev.Kind, ev.Connected = domain.EventDeparted, false
	case event.TypeDeviceRecordChanged:
		ev.Kind, ev.Name = domain.EventRecordChanged, p.Name
	default:
		return domain.Event{}, false
	}

	return ev, true
}
