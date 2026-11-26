package domain

import (
	"context"
	"time"
)

// EventKind is what happened.
type EventKind string

// The events the bot reacts to. internal/connect turns the server's event
// types into these.
const (
	EventJobStarted    EventKind = "job.started"
	EventJobSettled    EventKind = "job.settled"
	EventIssuesChanged EventKind = "issues.changed"
	EventStateChanged  EventKind = "state.changed"
	EventPresence      EventKind = "presence.changed"
	EventArrived       EventKind = "arrived"
	EventDeparted      EventKind = "departed"
	EventRecordChanged EventKind = "record.changed"
)

// Event is one thing that happened to one printer. Only the fields of its
// kind are set.
type Event struct {
	Kind   EventKind
	Device string
	At     time.Time

	// Job is the print, for the two job kinds.
	Job Job
	// Issues and Dialog are the whole list, for EventIssuesChanged.
	Issues []Issue
	Dialog *Dialog
	// Connected is whether the printer is reachable, for EventPresence,
	// EventArrived (true) and EventDeparted (false).
	Connected bool
	// State is the machine's new word, for EventStateChanged.
	State string
	// Name is the printer's name, for EventRecordChanged.
	Name string
}

// Events is the server's event stream. internal/connect implements it.
type Events interface {
	// Run calls handle with each batch until ctx ends. resync is true when
	// continuity is gone, and the caller then reads the fleet again.
	Run(ctx context.Context, handle func(ctx context.Context, batch []Event, resync bool)) error
}
