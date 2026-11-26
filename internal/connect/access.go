package connect

import (
	"context"
	"fmt"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
)

// Checker is the part of accessapi.Client the bot asks. A test fakes it.
type Checker interface {
	Check(ctx context.Context, params accessapi.CheckParams) (accessapi.Decision, error)
}

// Access implements domain.Access over access.check. It needs checks:
// [configure_resource] and check_absent_people: true in module.yaml, because
// it asks about people who are not on a request of the module's.
type Access struct {
	checker Checker
}

// NewAccess asks over the module's session.
func NewAccess(c Checker) Access { return Access{checker: c} }

// IsServerAdmin is whether the subject holds configure_resource on the auth
// resource kind: the server's own test for who administers its accounts.
func (a Access) IsServerAdmin(ctx context.Context, subject string) (bool, error) {
	d, err := a.checker.Check(ctx, accessapi.CheckParams{
		Subject: subject, Action: "configure_resource", ObjectType: "resource_kind", ObjectID: "auth",
	})
	if err != nil {
		return false, fmt.Errorf("access.check: %w", err)
	}

	return d.Allowed, nil
}
