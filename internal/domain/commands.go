package domain

import (
	"strings"
)

// Who is who may run a command.
type Who int

// The audiences, from the widest. Member and Admin arrive with the phases that
// need them (docs/design.md, "Phases and milestones").
const (
	// Anyone in the workspace who is not refused as a guest.
	Anyone Who = iota
)

// String names the audience in help text.
func (w Who) String() string {
	switch w {
	case Anyone:
		return "anyone"
	}

	return "nobody"
}

// Command is one word after /prusaconnect.
type Command struct {
	// Name is the word, in lower case.
	Name string
	// Usage is what follows the word, such as "[printer]", or empty.
	Usage string
	// Summary is one line for the help text.
	Summary string
	// Who may run it.
	Who Who
}

// SlashCommand is the command people type in Slack.
const SlashCommand = "/prusaconnect"

// Commands is every command, in the order help lists them. It is the one
// list: help is drawn from it, and a test fails when a command here has no
// handler or a handler has no entry here.
func Commands() []Command {
	return []Command{
		{Name: "help", Summary: "What this bot can do.", Who: Anyone},
		{Name: "bot", Summary: "How the bot itself is doing.", Who: Anyone},
	}
}

// Lookup finds a command by its word.
func Lookup(name string) (Command, bool) {
	for _, c := range Commands() {
		if c.Name == name {
			return c, true
		}
	}

	return Command{}, false
}

// Parse splits the text typed after the slash command into the command's word
// and its arguments. No text at all is help.
func Parse(text string) (name string, args []string) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return "help", nil
	}

	return strings.ToLower(words[0]), words[1:]
}

// Help is the help text, drawn from Commands.
func Help() Message {
	lines := make([]string, 0, len(Commands()))

	for _, c := range Commands() {
		usage := SlashCommand + " " + c.Name
		if c.Usage != "" {
			usage += " " + c.Usage
		}

		lines = append(lines, usage+": "+c.Summary)
	}

	return Message{
		Title:  "Printer bot",
		Lines:  lines,
		Footer: "Built on Prusa Connect by Prusa Research.",
	}
}

// Unknown answers a word that is not a command.
func Unknown(name string) Message {
	return Message{
		Lines: []string{
			"There is no command \"" + name + "\".",
			"Type " + SlashCommand + " help to see what this bot can do.",
		},
	}
}
