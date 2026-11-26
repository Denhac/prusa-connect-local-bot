// Package domain is what the bot does, written without Slack, the server's
// SDK, HTTP or files. It decides which commands exist, who may run them, and
// what each answers. Adapters in the other internal packages connect it to the
// outside, through the interfaces (ports) declared here.
//
// internal/arch holds this package to the standard library.
package domain

// Message is an answer or a post, in a form any chat platform can draw.
// internal/slack is the one place that turns it into Slack's Block Kit.
//
// Every string here is plain text. The renderer escapes it, so text that came
// from a printer or a person cannot become markup.
type Message struct {
	// Title is the heading, if any.
	Title string
	// Lines are paragraphs, in order.
	Lines []string
	// Fields are label and value pairs, drawn as a grid.
	Fields []Field
	// Footer is small text at the end, if any.
	Footer string
	// Link is an address drawn as a link after the lines, if any.
	Link string
	// Buttons are clicks the person can make on the message.
	Buttons []Button
}

// Button is one click on a message. Action says what it does, and Value is
// handed back with the click.
type Button struct {
	Action string
	Value  string
	Label  string
	// Danger draws it as a destructive action.
	Danger bool
}

// Field is one label and its value.
type Field struct {
	Label string
	Value string
}

// Plain is the message as one line of text, for notifications and for
// clients that draw no blocks.
func (m Message) Plain() string {
	switch {
	case m.Title != "" && len(m.Lines) > 0:
		return m.Title + ": " + m.Lines[0]
	case m.Title != "":
		return m.Title
	case len(m.Lines) > 0:
		return m.Lines[0]
	}

	return ""
}
