package domain

import (
	"context"
	"errors"
	"time"
)

// Membership is what the site's scheme says about one person.
type Membership struct {
	// Code is the member code.
	Code string
	// DisplayName is the member's name as the member system shows it.
	DisplayName string
	// Qualifies is whether the person may have an account now.
	Qualifies bool
	// Reason says why not, in words for the person.
	Reason string
	// Authorizations is what the member system lists, for later rules.
	Authorizations []string
}

// Verifier is the site's scheme. internal/verify implements it. An error
// means "cannot tell" and never "no".
type Verifier interface {
	// Check reads a Slack user's profile and then their member page.
	Check(ctx context.Context, slackID string) (Membership, error)
	// CheckCode reads a member page alone.
	CheckCode(ctx context.Context, code string) (Membership, error)
}

// The server's roles.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// Account is one account on the server.
type Account struct {
	ID          string
	Username    string
	DisplayName string
	Role        string
	Created     time.Time
}

// Subject is the account's principal, as access checks name it.
func (a Account) Subject() string { return "user:" + a.ID }

// ErrNameTaken is a username another account has.
var ErrNameTaken = errors.New("the username is taken")

// ErrAdminAccount is a change the bot refuses because the account holds admin.
var ErrAdminAccount = errors.New("the bot never changes an account that holds admin")

// Accounts is the server's accounts, signed in as the bot. internal/serverhttp
// implements it. Every call but MakeAdmin and Unadmin refuses an account that
// holds admin, and every call refuses the bot's own account.
type Accounts interface {
	List(ctx context.Context) ([]Account, error)
	// Create makes an operator account.
	Create(ctx context.Context, username, displayName, password string) (Account, error)
	SetPassword(ctx context.Context, id, password string) error
	// SetRole sets operator or viewer.
	SetRole(ctx context.Context, id, role string) error
	Delete(ctx context.Context, id string) error
	// DirectGrants are the grants written by hand on single printers for an
	// account, as "relation on object". The sweep only reports them.
	DirectGrants(ctx context.Context, id string) ([]string, error)
	// MakeAdmin gives an operator account the admin role. It is the one call
	// that writes admin, and it refuses an account that is not an operator.
	MakeAdmin(ctx context.Context, id string) error
	// Unadmin sets operator or viewer on an account that may hold admin. The
	// service calls it only for an account it made admin.
	Unadmin(ctx context.Context, id, role string) error
}

// Access asks the server who may do what. internal/connect implements it.
type Access interface {
	// IsServerAdmin is whether the subject may administer the server's
	// accounts, which is the server's own test for an admin.
	IsServerAdmin(ctx context.Context, subject string) (bool, error)
}

// Directory names chat users. internal/slack implements it.
type Directory interface {
	Name(ctx context.Context, user string) (string, error)
}

// MembersState is what the accounts service keeps in the state file.
type MembersState struct {
	// Members are the accounts the bot holds, by member code.
	Members map[string]*MemberRecord `json:"members,omitempty"`
	// Suspensions are by member code, and outlive an account.
	Suspensions map[string]*Suspension `json:"suspensions,omitempty"`
	// Admins are linked admin accounts, by chat user.
	Admins map[string]*AdminLink `json:"admins,omitempty"`
	// Journal is account changes begun and not yet finished.
	Journal []*JournalEntry `json:"journal,omitempty"`
	// Sweep is where the membership sweep stands.
	Sweep SweepState `json:"sweep,omitzero"`
}

// MemberRecord is one member's account.
type MemberRecord struct {
	Slack     string    `json:"slack"`
	AccountID string    `json:"account_id"`
	Username  string    `json:"username"`
	Via       string    `json:"via"` // created or claimed
	BasedOn   string    `json:"based_on,omitempty"`
	Since     time.Time `json:"since"`
	// Lapsed is when the account was turned off for no longer qualifying.
	Lapsed time.Time `json:"lapsed,omitzero"`
	// FailedChecks are the first and the latest of the checks in a row that
	// said the member does not qualify. Any check that qualifies clears them.
	FailedChecks []time.Time `json:"failed_checks,omitempty"`
	// Checked is when the sweep last read the member page, and Check what it
	// found, for lookup.
	Checked time.Time `json:"checked,omitzero"`
	Check   string    `json:"check,omitempty"`
	// Qualified is the last time a check found the member qualifying. A lapsed
	// account is deleted only once this, too, is delete_after old.
	Qualified time.Time `json:"qualified,omitzero"`
	// Elevated is when an admin made the account an admin with the admin
	// command, and ElevatedBy which admin. Only such an account can the bot
	// take admin from. Turning the account off ends it.
	Elevated   time.Time `json:"elevated,omitzero"`
	ElevatedBy string    `json:"elevated_by,omitempty"`
}

// turnedOn is a record whose account is an operator again, for a person a
// check has just found qualifying.
func (r *MemberRecord) turnedOn(now time.Time) {
	r.Lapsed, r.FailedChecks, r.Qualified = time.Time{}, nil, now
}

// Suspension is an admin's.
type Suspension struct {
	By     string    `json:"by"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// AdminLink is a chat user's own admin account on the server.
type AdminLink struct {
	Subject  string    `json:"subject"`
	Username string    `json:"username"`
	Linked   time.Time `json:"linked"`
}

// JournalEntry is one account change, written before it starts.
type JournalEntry struct {
	Op        string `json:"op"`
	Code      string `json:"code"`
	Slack     string `json:"slack,omitempty"`
	Username  string `json:"username,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	// OldAccountID is the account a replacement deletes.
	OldAccountID string    `json:"old_account_id,omitempty"`
	At           time.Time `json:"at"`
}

// The journal's operations.
const (
	opSignup  = "signup"
	opSuspend = "suspend"
	opClaim   = "claim"
	opElevate = "elevate"
)
