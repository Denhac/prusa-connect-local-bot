// Command slack-bot is a prusa-connect service module. It posts printer status
// to Slack and manages member accounts through the /prusaconnect command.
//
// The server starts this program, speaks to it over standard input and output,
// and restarts it when it fails. Standard output belongs to that protocol:
// nothing else may write to it. docs/design.md describes the whole module.
//
// Built on Prusa Connect by Prusa Research.
package main

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/denhac/prusa-connect-local-bot/internal/app"
)

// openAPI describes the module's routes, with every path relative to the
// mount. The host refuses a module with routes and no description.
//
//go:embed openapi.json
var openAPI []byte

func main() {
	if err := app.Run(openAPI); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", app.Name, err)
		os.Exit(1)
	}
}
