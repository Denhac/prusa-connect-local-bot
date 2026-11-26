package domain

import (
	"strings"
)

// Who is who may run a command.
type Who int

// The audiences, from the widest. Members and admins arrive with phase 2
// (docs/design.md, "Phases and milestones").
const (
	// Anyone in the workspace who is not refused as a guest.
	Anyone Who = iota
	// InBots is anyone who types it in the status channel, #bots. Only
	// members can see that channel, so until the member check of phase 2
	// this is how printer status stays with members.
	InBots
	// Admins are people linked to an admin account the server confirms.
	Admins
)

// String names the audience in help text.
func (w Who) String() string {
	switch w {
	case Anyone:
		return "anyone"
	case InBots:
		return "in #bots"
	case Admins:
		return "admins"
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
		{Name: "status", Usage: "[printer] [--here]", Summary: "What the printers are doing. --here posts it in the channel.", Who: InBots},
		{Name: "jobs", Usage: "[printer]", Summary: "The last prints.", Who: InBots},
		{Name: "snap", Usage: "[printer]", Summary: "A picture from a printer's camera, posted in the channel.", Who: InBots},
		{Name: "signup", Summary: "Make your account on the printer server (denhac members with 3D Printer User).", Who: Anyone},
		{Name: "password", Summary: "Set a new password on your account.", Who: Anyone},
		{Name: "whoami", Summary: "What the bot knows about you.", Who: Anyone},
		{Name: "link", Usage: "[code]", Summary: "Link your Slack user to an account you already have on the printer server.", Who: Anyone},
		{Name: "bot", Summary: "How the bot itself is doing.", Who: Anyone},
		{Name: "suspend", Usage: "[@user|username|member code] [reason]", Summary: "Turn an account off until an admin lifts it. Alone, lists the suspensions.", Who: Admins},
		{Name: "unsuspend", Usage: "<@user|username|member code>", Summary: "Lift a suspension.", Who: Admins},
		{Name: "lookup", Usage: "<@user|username|member code>", Summary: "Which member code, Slack user and account belong together.", Who: Admins},
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

		line := usage + ": " + c.Summary
		if c.Who != Anyone {
			line += " (" + c.Who.String() + ")"
		}

		lines = append(lines, line)
	}

	return Message{
		Title:  "Printer bot",
		Lines:  lines,
		Footer: "Built on Prusa Connect by Prusa Research.",
	}
}

// Unknown answers a word that is not a command.
func Unknown(name string) Message {
	return say(
		"There is no command \""+name+"\".",
		"Type "+SlashCommand+" help to see what this bot can do.",
	)
}

// say is a message of plain lines.
func say(lines ...string) Message { return Message{Lines: lines} }
