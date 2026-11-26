package domain

import (
	"context"
	"errors"
	"time"
)

// Printer is one printer as the bot needs it.
type Printer struct {
	ID        string
	Name      string
	Model     string
	Connected bool
	// State is the machine's own word for what it is doing, such as IDLE,
	// PRINTING or ATTENTION.
	State string
	// Issues is what the server found wrong with the printer, worst first.
	Issues []Issue
	// Dialog is the question on the printer's screen, while it waits for a person.
	Dialog *Dialog
	Nozzle Temp
	Bed    Temp
}

// Label is how a person knows the printer: its name, or its id when it has none.
func (p Printer) Label() string {
	if p.Name != "" {
		return p.Name
	}

	return p.ID
}

// Temp is one heater's reading.
type Temp struct {
	Now    float64
	Target float64
	Known  bool
}

// Issue is one thing the server found wrong with a printer.
type Issue struct {
	// Reason is the server's token, such as awaiting_operator.
	Reason   string
	Severity string
	// Detail is the machine's own words.
	Detail string
}

// Dialog is what a printer's screen asks while it waits for a person.
type Dialog struct {
	Title   string
	Text    string
	Buttons []string
}

// Job is one print.
type Job struct {
	ID string
	// File is the name the printer shows for the file.
	File string
	// Progress is the last percentage reported.
	Progress      float64
	TimeRemaining time.Duration
	Started       time.Time
	// Ended is zero while the print runs.
	Ended time.Time
	// Outcome is finished, failed, cancelled or unknown, and empty while the
	// print runs.
	Outcome string
	// Printed is how long the printer says it printed, when it says.
	Printed      time.Duration
	PrintedKnown bool
	// FilamentMM is how much filament the printer says it used, when it says.
	FilamentMM    float64
	FilamentKnown bool
}

// The outcomes the server gives an ended print.
const (
	OutcomeFinished  = "finished"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
	OutcomeUnknown   = "unknown"
)

// Fleet reads the printers the module may see. internal/connect implements
// it over the server's wire.
type Fleet interface {
	Printers(ctx context.Context) ([]Printer, error)
	// Jobs is the print on the bed and the one before it, either of which may
	// be nil.
	Jobs(ctx context.Context, device string) (current, last *Job, err error)
	// History is the prints of one printer that ended since a moment, newest
	// first, at most limit of them.
	History(ctx context.Context, device string, since time.Time, limit int) ([]Job, error)
}

// Stills fetches pictures from the printers' cameras. internal/camera
// implements it over the Pi camera API.
type Stills interface {
	// Has reports whether a camera is set up for the printer.
	Has(device string) bool
	// Still is a JPEG from the printer's camera. Fresh asks for one taken
	// after the request.
	Still(ctx context.Context, device string, fresh bool) ([]byte, error)
}

// The refusals a Stills answers with.
var (
	ErrNoCamera = errors.New("no camera is set up for this printer")
	// ErrCameraBusy is a camera with no recent picture, which is what live
	// view does: the camera stops taking stills while someone watches.
	ErrCameraBusy = errors.New("the camera has no recent picture: someone may be watching live view")
	// ErrNoPicture is a camera that has not sent a picture yet.
	ErrNoPicture = errors.New("the camera has not sent a picture yet")
)

// CombinedStills takes pictures from a printer's own Pi when one is set up
// for it, and from the server's camera otherwise (docs/design.md, "Camera
// stills"). Either may be nil.
type CombinedStills struct {
	// Pi is the Pi camera API, which can wait for a fresh picture.
	Pi Stills
	// Server is the server's camera snapshots, found by the printer.
	Server Stills
}

// Has reports whether either source has a camera for the printer.
func (c CombinedStills) Has(device string) bool {
	return (c.Pi != nil && c.Pi.Has(device)) || (c.Server != nil && c.Server.Has(device))
}

// Still asks the Pi first when it has the printer, then the server.
func (c CombinedStills) Still(ctx context.Context, device string, fresh bool) ([]byte, error) {
	if c.Pi != nil && c.Pi.Has(device) {
		jpeg, err := c.Pi.Still(ctx, device, fresh)
		if err == nil || c.Server == nil || !c.Server.Has(device) {
			return jpeg, err
		}
	}

	if c.Server != nil {
		return c.Server.Still(ctx, device, fresh)
	}

	return nil, ErrNoCamera
}

// Chat posts into channels. internal/slack implements it.
type Chat interface {
	// Post starts a message in a channel and answers its id.
	Post(ctx context.Context, channel string, m Message) (string, error)
	// Reply answers in a message's thread.
	Reply(ctx context.Context, channel, thread string, m Message) (string, error)
	// Update replaces a message.
	Update(ctx context.Context, channel, id string, m Message) error
	// Upload shares a JPEG in a channel, in a thread when thread is set.
	Upload(ctx context.Context, channel, thread string, jpeg []byte, title string) error
}

// ErrNotInChannel is a channel the bot cannot post in until someone invites it.
var ErrNotInChannel = errors.New("the bot is not in the channel")
