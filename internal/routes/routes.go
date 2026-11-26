// Package routes is the module's HTTP surface on the server, reached at
// /api/v1/modules/<namespace>/... . The host authorizes every request before
// it arrives here: a GET needs view_resource on the module's instance.
//
// openapi.json at the repository root describes these routes, and the host
// refuses to start a module whose routes it does not describe.
package routes

import (
	"encoding/json"
	"net/http"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// New is the module's routes. health is read on every request.
func New(health func() domain.Health) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		_ = json.NewEncoder(w).Encode(health())
	})

	return mux
}
