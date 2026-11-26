// Package routes is the module's HTTP surface on the server, reached at
// /api/v1/modules/<namespace>/... . The host authorizes every request before
// it arrives here: a GET needs view_resource on the module's instance.
//
// openapi.json at the repository root describes these routes, and the host
// refuses to start a module whose routes it does not describe.
package routes

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// LinkPage answers GET /link: the status and the page's text, for a
// signed-in principal and the nonce in ?for=.
type LinkPage func(ctx context.Context, nonce, principal string) (int, string)

// New is the module's routes. health is read on every request. principalOf
// is who the host authorized for a request (routesapi.PrincipalOf).
func New(health func() domain.Health, link LinkPage, principalOf func(*http.Request) string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /link", func(w http.ResponseWriter, r *http.Request) {
		status, page := link(r.Context(), r.URL.Query().Get("for"), principalOf(r))

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(page))
	})

	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		_ = json.NewEncoder(w).Encode(health())
	})

	return mux
}
