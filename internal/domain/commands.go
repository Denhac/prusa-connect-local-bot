package domain

import (
	"slices"
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
	// Group is the heading help lists it under.
	Group string
	// Aliases are other words for it. Help names them on its line, and lists
	// no line of their own.
	Aliases []string
}

// SlashCommand is the command people type in Slack.
const SlashCommand = "/prusaconnect"

// The headings of the help text, in its order.
const (
	groupPrinters = "Printers (in #bots)"
	groupAccount  = "Your account on the printer server"
	groupAdmins   = "Admins"
	groupBot      = "This bot"
)

// Commands is every command, in the order help lists them. It is the one
// list: help is drawn from it, and a test fails when a command here has no
// handler or a handler has no entry here.
func Commands() []Command {
	const who = "<@user|username|member code>"

	return []Command{
		{Name: "status", Summary: "Tick printers and see what they are doing, or post it in #bots.", Who: InBots, Group: groupPrinters},
		{Name: "jobs", Summary: "Tick printers and see their last prints.", Who: InBots, Group: groupPrinters},
		{Name: "snap", Summary: "Tick printers and post a picture from each one's camera in #bots.", Who: InBots, Group: groupPrinters,
			Aliases: []string{"snapshot", "cam", "camera"}},
		{Name: "signup", Summary: "Make your account (denhac members with 3D Printer User).", Who: Anyone, Group: groupAccount},
		{Name: "password", Summary: "Set a new password on your account.", Who: Anyone, Group: groupAccount},
		{Name: "whoami", Summary: "What the bot knows about you.", Who: Anyone, Group: groupAccount},
		{Name: "link", Usage: "[code]", Summary: "Link your Slack user to an account you already have.", Who: Anyone, Group: groupAccount},
		{Name: "suspend", Usage: "[@user|username|member code] [reason]", Summary: "Turn an account off until an admin lifts it. Alone, lists the suspensions.", Who: Admins, Group: groupAdmins},
		{Name: "unsuspend", Usage: who, Summary: "Lift a suspension.", Who: Admins, Group: groupAdmins},
		{Name: "lookup", Usage: who, Summary: "Which member code, Slack user and account belong together.", Who: Admins, Group: groupAdmins},
		{Name: "admin", Usage: who, Summary: "Make a member's account an admin.", Who: Admins, Group: groupAdmins},
		{Name: "unadmin", Usage: who, Summary: "Take admin from an account the bot made an admin.", Who: Admins, Group: groupAdmins},
		{Name: "help", Summary: "This list.", Who: Anyone, Group: groupBot},
		{Name: "bot", Summary: "How the bot itself is doing.", Who: Anyone, Group: groupBot},
	}
}

// Lookup finds a command by its word or one of its aliases.
func Lookup(name string) (Command, bool) {
	for _, c := range Commands() {
		if c.Name == name || slices.Contains(c.Aliases, name) {
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

// Help is the help text, drawn from Commands and grouped by their headings.
// The admin commands are listed only for an admin.
func Help(admin bool) Message {
	var groups []Group

	for _, c := range Commands() {
		if c.Who == Admins && !admin {
			continue
		}

		usage := SlashCommand + " " + c.Name
		if c.Usage != "" {
			usage += " " + c.Usage
		}

		if len(groups) == 0 || groups[len(groups)-1].Heading != c.Group {
			groups = append(groups, Group{Heading: c.Group})
		}

		text := c.Summary
		if len(c.Aliases) > 0 {
			text += " Also: " + strings.Join(c.Aliases, ", ") + "."
		}

		last := &groups[len(groups)-1]
		last.Items = append(last.Items, Item{Code: usage, Text: text})
	}

	return Message{
		Title:  "Printer bot",
		Lines:  []string{"Type " + SlashCommand + " and a command. Only you see the answers, unless a command says it posts."},
		Groups: groups,
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
