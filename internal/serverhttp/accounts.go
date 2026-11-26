package serverhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// Accounts implements domain.Accounts over /api/v1/auth/accounts.
//
// Before it changes an account it reads it again. It refuses the bot's own
// account always, and one that holds admin in every call but Unadmin. Only
// MakeAdmin writes admin, and only on an account that is an operator now.
type Accounts struct {
	session *Session
	bot     string
}

// NewAccounts manages accounts through a signed-in session. bot is the bot's
// own username.
func NewAccounts(s *Session, bot string) *Accounts { return &Accounts{session: s, bot: bot} }

type account struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

func (a account) domain() domain.Account {
	return domain.Account{ID: a.ID, Username: a.Username, DisplayName: a.DisplayName, Role: a.Role, Created: a.CreatedAt}
}

// List is every local account.
func (a *Accounts) List(ctx context.Context) ([]domain.Account, error) {
	resp, err := a.session.Do(ctx, http.MethodGet, "/api/v1/auth/accounts", nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, refusal("list accounts", resp)
	}

	var list struct {
		Items []account `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("serverhttp: the account list: %w", err)
	}

	out := make([]domain.Account, 0, len(list.Items))
	for _, x := range list.Items {
		out = append(out, x.domain())
	}

	return out, nil
}

// Create makes an operator account.
func (a *Accounts) Create(ctx context.Context, username, displayName, password string) (domain.Account, error) {
	body, err := json.Marshal(map[string]string{
		"username": username, "display_name": displayName, "password": password, "role": domain.RoleOperator,
	})
	if err != nil {
		return domain.Account{}, fmt.Errorf("serverhttp: %w", err)
	}

	resp, err := a.session.Do(ctx, http.MethodPost, "/api/v1/auth/accounts", body, "application/json")
	if err != nil {
		return domain.Account{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusConflict:
		return domain.Account{}, domain.ErrNameTaken
	default:
		return domain.Account{}, refusal("create an account", resp)
	}

	var made account
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&made); err != nil {
		return domain.Account{}, fmt.Errorf("serverhttp: the new account: %w", err)
	}

	if made.Role == "" {
		made.Role = domain.RoleOperator // the answer may leave the role out; it was written with it
	}

	return made.domain(), nil
}

// SetPassword resets a password. A 5xx is tried again: the server may have
// changed the password and failed to end the old sessions.
func (a *Accounts) SetPassword(ctx context.Context, id, password string) error {
	if err := a.guard(ctx, id); err != nil {
		return err
	}

	body, err := json.Marshal(map[string]string{"new_password": password})
	if err != nil {
		return fmt.Errorf("serverhttp: %w", err)
	}

	var last error

	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err() //nolint:wrapcheck // the caller's own cancellation.
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		resp, err := a.session.Do(ctx, http.MethodPost, "/api/v1/auth/accounts/"+url.PathEscape(id)+"/password", body, "application/json")
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusNoContent {
			_ = resp.Body.Close()

			return nil
		}

		last = refusal("set a password", resp)
		_ = resp.Body.Close()

		if resp.StatusCode < 500 {
			return last
		}
	}

	return last
}

// SetRole sets operator or viewer, never admin.
func (a *Accounts) SetRole(ctx context.Context, id, role string) error {
	if role != domain.RoleOperator && role != domain.RoleViewer {
		return fmt.Errorf("serverhttp: the bot sets only operator or viewer, not %q", role)
	}

	if err := a.guard(ctx, id); err != nil {
		return err
	}

	return a.putRole(ctx, id, role)
}

// MakeAdmin gives an operator account the admin role. It is the one call that
// writes admin, and it refuses an account that is not an operator now, as well
// as the bot's own.
func (a *Accounts) MakeAdmin(ctx context.Context, id string) error {
	acct, err := a.read(ctx, id)
	if err != nil {
		return err
	}

	if strings.EqualFold(acct.Username, a.bot) || acct.Role != domain.RoleOperator {
		return fmt.Errorf("serverhttp: the bot makes only an operator account an admin, and %s is %q", acct.Username, acct.Role)
	}

	return a.putRole(ctx, id, domain.RoleAdmin)
}

// Unadmin sets operator or viewer on an account that may hold admin, never
// the bot's own. The account service calls it only for an account it made
// admin.
func (a *Accounts) Unadmin(ctx context.Context, id, role string) error {
	if role != domain.RoleOperator && role != domain.RoleViewer {
		return fmt.Errorf("serverhttp: the bot sets only operator or viewer, not %q", role)
	}

	acct, err := a.read(ctx, id)
	if err != nil {
		return err
	}

	if strings.EqualFold(acct.Username, a.bot) {
		return domain.ErrAdminAccount
	}

	return a.putRole(ctx, id, role)
}

func (a *Accounts) putRole(ctx context.Context, id, role string) error {
	body, err := json.Marshal(map[string]string{"role": role})
	if err != nil {
		return fmt.Errorf("serverhttp: %w", err)
	}

	resp, err := a.session.Do(ctx, http.MethodPut, "/api/v1/auth/accounts/"+url.PathEscape(id)+"/role", body, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return refusal("set a role", resp)
	}

	return nil
}

// Delete removes an account. One already gone is not an error.
func (a *Accounts) Delete(ctx context.Context, id string) error {
	if err := a.guard(ctx, id); err != nil {
		if errors.Is(err, errGone) {
			return nil
		}

		return err
	}

	resp, err := a.session.Do(ctx, http.MethodDelete, "/api/v1/auth/accounts/"+url.PathEscape(id), nil, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return refusal("delete an account", resp)
	}

	return nil
}

// DirectGrants are the grants written on single printers for an account,
// read from /api/v1/grants/subject/user:<id>/direct, as "relation on object".
// An org role is not among them: it is written on the org.
func (a *Accounts) DirectGrants(ctx context.Context, id string) ([]string, error) {
	resp, err := a.session.Do(ctx, http.MethodGet,
		"/api/v1/grants/subject/"+url.PathEscape("user:"+id)+"/direct", nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, refusal("read grants", resp)
	}

	var list struct {
		Items []struct {
			Object   string `json:"object"`
			Relation string `json:"relation"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("serverhttp: the grants: %w", err)
	}

	out := make([]string, 0, len(list.Items))
	for _, g := range list.Items {
		out = append(out, g.Relation+" on "+g.Object)
	}

	return out, nil
}

var errGone = errors.New("serverhttp: no account has that id")

// guard reads the account again, and refuses one that holds admin or is the
// bot's own.
func (a *Accounts) guard(ctx context.Context, id string) error {
	x, err := a.read(ctx, id)
	if err != nil {
		return err
	}

	if x.Role == domain.RoleAdmin || strings.EqualFold(x.Username, a.bot) {
		return domain.ErrAdminAccount
	}

	return nil
}

// read is one account as the server holds it now.
func (a *Accounts) read(ctx context.Context, id string) (domain.Account, error) {
	accounts, err := a.List(ctx)
	if err != nil {
		return domain.Account{}, err
	}

	for _, x := range accounts {
		if x.ID == id {
			return x, nil
		}
	}

	return domain.Account{}, errGone
}

// refusal is a non-success answer as an error: the status and the problem
// code, never the body.
func refusal(what string, resp *http.Response) error {
	return fmt.Errorf("serverhttp: %s: the server answered %s %s", what, resp.Status, problemCode(resp.Body))
}
