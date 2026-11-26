package domain

import "context"

// UI is how a command talks back to the person who typed it. internal/slack
// implements it for each command and each click.
type UI interface {
	// Respond answers the person alone. After a click, it replaces the
	// message that was clicked.
	Respond(ctx context.Context, m Message) error
	// OpenModal opens a window. It works only within a few seconds of the
	// command or click, so a handler opens it before anything slow.
	OpenModal(ctx context.Context, m Modal) (string, error)
	// UpdateModal replaces an open window.
	UpdateModal(ctx context.Context, id string, m Modal) error
	// DM sends the person a direct message.
	DM(ctx context.Context, m Message) error
}

// Modal is a window over the chat. A password is shown only in one, because
// a window is never kept in any history.
type Modal struct {
	// Title is short: Slack takes at most 24 characters.
	Title  string
	Lines  []string
	Fields []Field
	// Input is one text field, if any.
	Input *Input
	// Callback names what a submit is for. Empty is a window with no submit.
	Callback string
	Submit   string
	// Meta is handed back with the submit.
	Meta string
}

// Input is one text field in a window.
type Input struct {
	ID      string
	Label   string
	Initial string
	Hint    string
}

// Interaction is a click on a button or the submit of a window.
type Interaction struct {
	User string
	// Action and Value are a button's.
	Action string
	Value  string
	// Callback, Meta, Inputs and ModalID are a window's.
	Callback string
	Meta     string
	Inputs   map[string]string
	ModalID  string
}
