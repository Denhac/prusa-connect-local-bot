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
	// Link is an address drawn as a link after the fields, if any.
	Link string
	// Inputs are the fields, in order.
	Inputs []Input
	// Callback names what a submit is for. Empty is a window with no submit.
	Callback string
	Submit   string
	// Meta is handed back with the submit.
	Meta string
}

// Input is one field in a window: a line of text, or with Choices, boxes to
// tick, one or more.
type Input struct {
	ID      string
	Label   string
	Initial string
	Hint    string
	// Choices make the field a set of boxes to tick.
	Choices []Choice
	// Optional lets the window be sent with the field left empty.
	Optional bool
}

// Choice is one box to tick. Value comes back in Interaction.Picked.
type Choice struct {
	Value  string
	Label  string
	Detail string
}

// Interaction is a click on a button or the submit of a window.
type Interaction struct {
	User string
	// Action and Value are a button's.
	Action string
	Value  string
	// Callback, Meta, Inputs, Picked and ModalID are a window's: the text
	// typed into each field, and the choices ticked in each field, by its ID.
	Callback string
	Meta     string
	Inputs   map[string]string
	Picked   map[string][]string
	ModalID  string
}
