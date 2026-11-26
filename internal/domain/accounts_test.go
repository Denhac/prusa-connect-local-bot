package domain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeVerifier answers from fixed memberships by Slack user and by code.
// byCode and codeErr are what the sweep reads, ahead of bySlack.
type fakeVerifier struct {
	bySlack map[string]Membership
	err     error
	byCode  map[string]Membership
	codeErr error
	reads   int
}

func (v *fakeVerifier) Check(_ context.Context, slack string) (Membership, error) {
	if v.err != nil {
		return Membership{}, v.err
	}

	if m, ok := v.bySlack[slack]; ok {
		return m, nil
	}

	return Membership{Reason: "Your Slack profile does not say you are a denhac member."}, nil
}

func (v *fakeVerifier) CheckCode(_ context.Context, code string) (Membership, error) {
	v.reads++

	if v.codeErr != nil {
		return Membership{}, v.codeErr
	}

	if m, ok := v.byCode[code]; ok {
		return m, nil
	}

	for _, m := range v.bySlack {
		if m.Code == code {
			return m, nil
		}
	}

	return Membership{Code: code}, nil
}

// fakeAccounts is the server's account list.
type fakeAccounts struct {
	list      []Account
	next      int
	passwords map[string]string
	createErr error
	listErr   error
	roleErr   error
	grants    map[string][]string
	deletes   []string
}

func (a *fakeAccounts) List(context.Context) ([]Account, error) {
	if a.listErr != nil {
		return nil, a.listErr
	}

	return append([]Account(nil), a.list...), nil
}

func (a *fakeAccounts) DirectGrants(_ context.Context, id string) ([]string, error) {
	return a.grants[id], nil
}

func (a *fakeAccounts) Create(_ context.Context, username, display, password string) (Account, error) {
	if a.createErr != nil {
		return Account{}, a.createErr
	}

	for _, x := range a.list {
		if x.Username == username {
			return Account{}, ErrNameTaken
		}
	}

	a.next++
	acct := Account{ID: "id" + string(rune('0'+a.next)), Username: username, DisplayName: display, Role: RoleOperator, Created: start}
	a.list = append(a.list, acct)
	a.passwords[acct.ID] = password

	return acct, nil
}

func (a *fakeAccounts) get(id string) *Account { return find(a.list, id) }

func (a *fakeAccounts) SetPassword(_ context.Context, id, password string) error {
	if a.get(id) == nil {
		return errors.New("no such account")
	}

	a.passwords[id] = password

	return nil
}

func (a *fakeAccounts) SetRole(_ context.Context, id, role string) error {
	acct := a.get(id)

	switch {
	case a.roleErr != nil:
		return a.roleErr
	case acct == nil:
		return errors.New("no such account")
	case acct.Role == RoleAdmin:
		return ErrAdminAccount
	}

	acct.Role = role

	return nil
}

// MakeAdmin and Unadmin hold to what internal/serverhttp holds to.
func (a *fakeAccounts) MakeAdmin(_ context.Context, id string) error {
	acct := a.get(id)

	switch {
	case a.roleErr != nil:
		return a.roleErr
	case acct == nil || acct.Role != RoleOperator || acct.Username == "slackbot":
		return errors.New("not an operator account")
	}

	acct.Role = RoleAdmin

	return nil
}

func (a *fakeAccounts) Unadmin(_ context.Context, id, role string) error {
	acct := a.get(id)

	switch {
	case a.roleErr != nil:
		return a.roleErr
	case acct == nil || acct.Username == "slackbot" || role == RoleAdmin:
		return errors.New("refused")
	}

	acct.Role = role

	return nil
}

func (a *fakeAccounts) Delete(_ context.Context, id string) error {
	for i, x := range a.list {
		if x.ID == id {
			if x.Role == RoleAdmin {
				return ErrAdminAccount
			}

			a.list = append(a.list[:i], a.list[i+1:]...)
			a.deletes = append(a.deletes, id)

			return nil
		}
	}

	return nil
}

type fakeAccess map[string]bool

func (f fakeAccess) IsServerAdmin(_ context.Context, subject string) (bool, error) {
	return f[subject], nil
}

type names map[string]string

func (n names) Name(_ context.Context, user string) (string, error) { return n[user], nil }

type world struct {
	svc      *AccountsService
	accounts *fakeAccounts
	verifier *fakeVerifier
	saved    MembersState
	saves    int
	clock    time.Time
}

// at moves the world's clock to start+d.
func (w *world) at(d time.Duration) { w.clock = start.Add(d) }

func member(code, name string) Membership {
	return Membership{Code: code, DisplayName: name, Qualifies: true, Authorizations: []string{"3D Printer User"}}
}

func newWorld(t *testing.T) *world {
	t.Helper()

	w := &world{
		clock: start,
		accounts: &fakeAccounts{passwords: map[string]string{}, list: []Account{
			{ID: "admin1", Username: "boss", Role: RoleAdmin},
			{ID: "bot", Username: "slackbot", Role: RoleAdmin},
		}},
		verifier: &fakeVerifier{bySlack: map[string]Membership{
			"U1": member("ABCD", "Example User"),
			"U2": member("EFGH", "boss"),
		}},
	}

	w.svc = NewAccountsService(AccountsConfig{
		Verifier: w.verifier, Accounts: w.accounts, Access: fakeAccess{"user:admin1": true},
		Directory: names{"U1": "example", "UADMIN": "Boss"}, BotAccount: "slackbot",
		LinkURL: "https://printserver.example/api/v1/modules/x/link", Now: func() time.Time { return w.clock },
		AccountURL: "https://printserver.example/settings/account",
		Save:       func(st MembersState) error { w.saved = st; w.saves++; return nil },
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sweep:      DefaultSweepConfig(),
	}, MembersState{})

	return w
}

func fieldOf(m Modal, label string) string {
	for _, f := range m.Fields {
		if f.Label == label {
			return f.Value
		}
	}

	return ""
}

func linesOf(m Modal) string { return strings.Join(m.Lines, " ") }

func TestSignup(t *testing.T) {
	w := newWorld(t)
	ui := &fakeUI{}

	w.svc.Signup(context.Background(), Request{User: "U1"}, ui)

	got := ui.last()
	if fieldOf(got, "Username") != "example.user" || strings.Count(fieldOf(got, "Password"), "-") != 2 ||
		!strings.Contains(linesOf(got), "change it") || got.Link != "https://printserver.example/settings/account" {
		t.Fatalf("signup showed %+v", got)
	}

	if len(ui.said) != 0 {
		t.Errorf("a password window was also answered in a message: %+v", ui.said)
	}

	rec := w.saved.Members["ABCD"]
	if rec == nil || rec.Slack != "U1" || rec.Username != "example.user" || rec.BasedOn != "Example User" || len(w.saved.Journal) != 0 {
		t.Errorf("saved %+v, journal %v", rec, w.saved.Journal)
	}

	if acct := w.accounts.get(rec.AccountID); acct == nil || acct.Role != RoleOperator || w.accounts.passwords[acct.ID] != fieldOf(got, "Password") {
		t.Errorf("the server holds %+v", acct)
	}

	// Again: nothing new.
	again := &fakeUI{}
	w.svc.Signup(context.Background(), Request{User: "U1"}, again)

	if !strings.Contains(linesOf(again.last()), "already have the account example.user") {
		t.Errorf("a second signup said %+v", again.last())
	}
}

func TestSignupRefusals(t *testing.T) {
	cases := map[string]struct {
		user  string
		setup func(*world)
		want  string
	}{
		"not a member": {user: "U9", want: "does not say you are a denhac member"},
		"cannot tell":  {user: "U1", setup: func(w *world) { w.verifier.err = errors.New("denhac is down") }, want: "cannot check your membership"},
		"suspended":    {user: "U1", setup: func(w *world) { w.svc.st.Suspensions["ABCD"] = &Suspension{} }, want: "suspended"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			if c.setup != nil {
				c.setup(w)
			}

			ui := &fakeUI{}
			w.svc.Signup(context.Background(), Request{User: c.user}, ui)

			if !strings.Contains(linesOf(ui.last()), c.want) {
				t.Errorf("said %+v, want %q", ui.last(), c.want)
			}

			if len(w.accounts.list) != 2 {
				t.Errorf("an account was made")
			}
		})
	}
}

// TestSignupNameTaken offers the next free name, and takes a typed one.
func TestSignupNameTaken(t *testing.T) {
	w := newWorld(t)
	w.accounts.list = append(w.accounts.list, Account{ID: "x", Username: "example.user", Role: RoleOperator})
	ui := &fakeUI{}

	w.svc.Signup(context.Background(), Request{User: "U1"}, ui)

	ask := ui.last()
	if ask.Callback != "signup-name" || ask.Input == nil || ask.Input.Initial != "example.user-2" ||
		!strings.Contains(linesOf(ask), `"example.user" is taken`) {
		t.Fatalf("asked %+v", ask)
	}

	// A typed name that is not a username, then one that is.
	for _, typed := range []string{"Not Valid!", "admin-two", "my.name"} {
		w.svc.SignupSubmit(context.Background(), Interaction{User: "U1", Callback: "signup-name", Meta: ask.Meta,
			Inputs: map[string]string{"username": typed}, ModalID: "V1"}, ui)
	}

	if got := ui.last(); fieldOf(got, "Username") != "my.name" {
		t.Errorf("the typed name made %+v", got)
	}

	if w.saved.Members["ABCD"].Username != "my.name" {
		t.Errorf("saved %+v", w.saved.Members["ABCD"])
	}
}

// TestSignupRename is a member whose denhac name changed: asked, then replaced.
func TestSignupRename(t *testing.T) {
	w := newWorld(t)
	w.svc.Signup(context.Background(), Request{User: "U1"}, &fakeUI{})
	old := w.saved.Members["ABCD"].AccountID

	w.verifier.bySlack["U1"] = member("ABCD", "Renamed Person")
	ui := &fakeUI{}
	w.svc.Signup(context.Background(), Request{User: "U1"}, ui)

	if ui.last().Callback != "signup-replace" {
		t.Fatalf("a rename was not asked: %+v", ui.last())
	}

	if w.accounts.get(old) == nil {
		t.Fatal("the old account went before the person confirmed")
	}

	w.svc.SignupSubmit(context.Background(), Interaction{User: "U1", Callback: "signup-replace", Meta: ui.last().Meta, ModalID: "V1"}, ui)

	rec := w.saved.Members["ABCD"]
	if fieldOf(ui.last(), "Username") != "renamed.person" || rec.Username != "renamed.person" || w.accounts.get(old) != nil {
		t.Errorf("after the replacement: %+v, %+v, old there: %v", ui.last(), rec, w.accounts.get(old) != nil)
	}
}

// TestSignupAnotherSlackUser is a second Slack user with the same member
// code. Codes are unique to one person, so it is a fault: refused, and the
// account is left alone.
func TestSignupAnotherSlackUser(t *testing.T) {
	w := newWorld(t)
	w.svc.Signup(context.Background(), Request{User: "U1"}, &fakeUI{})
	before := *w.saved.Members["ABCD"]

	w.verifier.bySlack["U3"] = member("ABCD", "Example User")
	ui := &fakeUI{}
	w.svc.Signup(context.Background(), Request{User: "U3"}, ui)

	if rec := w.saved.Members["ABCD"]; rec.Slack != "U1" || rec.AccountID != before.AccountID || w.accounts.get(before.AccountID) == nil {
		t.Errorf("the account changed: %+v", rec)
	}

	if !strings.Contains(linesOf(ui.last()), "already belongs to another Slack user") {
		t.Errorf("said %+v", ui.last())
	}
}

func TestSignupClosedWindowNeverLeaksThePassword(t *testing.T) {
	w := newWorld(t)
	ui := &fakeUI{updateErr: errors.New("view not found")}

	w.svc.Signup(context.Background(), Request{User: "U1"}, ui)

	password := w.accounts.passwords[w.saved.Members["ABCD"].AccountID]
	for _, m := range ui.said {
		if strings.Contains(strings.Join(m.Lines, " ")+fmtFields(m.Fields), password) {
			t.Fatal("the password went into a message")
		}
	}

	if len(ui.said) == 0 || !strings.Contains(strings.Join(ui.said[len(ui.said)-1].Lines, " "), "password to get a new one") {
		t.Errorf("said %+v", ui.said)
	}
}

func fmtFields(fields []Field) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.Label + f.Value)
	}

	return b.String()
}

func TestPassword(t *testing.T) {
	w := newWorld(t)
	w.svc.Signup(context.Background(), Request{User: "U1"}, &fakeUI{})
	id := w.saved.Members["ABCD"].AccountID
	first := w.accounts.passwords[id]

	w.accounts.get(id).Role = RoleViewer // lapsed meanwhile
	w.svc.st.Members["ABCD"].Lapsed = start

	ui := &fakeUI{}
	w.svc.Password(context.Background(), Request{User: "U1"}, ui)

	got := fieldOf(ui.last(), "Password")
	if strings.Count(got, "-") != 2 || strings.Count(first, "-") != 2 || !strings.Contains(linesOf(ui.last()), "change it") {
		t.Errorf("passwords %q and %q are not three words, or the window does not ask to change it", first, got)
	}

	if got == "" || got == first || w.accounts.passwords[id] != got || w.accounts.get(id).Role != RoleOperator {
		t.Errorf("password showed %+v; the account is %+v", ui.last(), w.accounts.get(id))
	}

	if !w.saved.Members["ABCD"].Lapsed.IsZero() {
		t.Error("the account is still marked lapsed")
	}

	other := &fakeUI{}
	w.svc.Password(context.Background(), Request{User: "U2"}, other)

	if !strings.Contains(linesOf(other.last()), "no account the bot looks after") {
		t.Errorf("a member with no account was answered %+v", other.last())
	}
}

func linkAs(t *testing.T, w *world, slack, principal string) Message {
	t.Helper()

	ui := &fakeUI{}
	w.svc.Link(context.Background(), Request{User: slack}, nil, ui)

	if len(ui.dms) != 1 || !strings.Contains(ui.dms[0].Link, "?for=") {
		t.Fatalf("link sent %+v", ui.dms)
	}

	nonce := ui.dms[0].Link[strings.Index(ui.dms[0].Link, "?for=")+5:]

	status, page := w.svc.LinkPage(context.Background(), nonce, principal)
	if status != 200 {
		t.Fatalf("the page answered %d %s", status, page)
	}

	code := page[strings.Index(page, ": ")+2 : strings.Index(page, ": ")+11]

	if again, _ := w.svc.LinkPage(context.Background(), nonce, principal); again != 404 {
		t.Error("a link address worked twice")
	}

	confirm := w.svc.Link(context.Background(), Request{User: slack}, []string{code}, ui)
	if len(confirm.Buttons) != 1 {
		t.Fatalf("link %s said %+v", code, confirm)
	}

	clicked := &fakeUI{}
	w.svc.LinkConfirm(context.Background(), Interaction{User: slack, Action: "link-confirm", Value: confirm.Buttons[0].Value}, clicked)

	return clicked.said[len(clicked.said)-1]
}

func TestLinkAnAdmin(t *testing.T) {
	w := newWorld(t)

	if got := linkAs(t, w, "UADMIN", "user:admin1"); !strings.Contains(strings.Join(got.Lines, " "), "admin account boss") {
		t.Fatalf("linking an admin said %+v", got)
	}

	if _, ok := w.svc.admin(context.Background(), "UADMIN"); !ok {
		t.Error("a linked admin is not recognised")
	}

	if _, ok := w.svc.admin(context.Background(), "U1"); ok {
		t.Error("an unlinked person is an admin")
	}
}

// TestClaimAnOldAccount is a member's account made before the bot.
func TestClaimAnOldAccount(t *testing.T) {
	w := newWorld(t)
	w.accounts.list = append(w.accounts.list, Account{ID: "old", Username: "olduser", Role: RoleViewer})

	if got := linkAs(t, w, "U1", "user:old"); !strings.Contains(strings.Join(got.Lines, " "), "looks after your account olduser") {
		t.Fatalf("claiming said %+v", got)
	}

	if rec := w.saved.Members["ABCD"]; rec == nil || rec.Via != "claimed" || w.accounts.get("old").Role != RoleOperator {
		t.Errorf("claimed %+v, role %s", rec, w.accounts.get("old").Role)
	}
}

func TestLinkRefusals(t *testing.T) {
	w := newWorld(t)

	if status, _ := w.svc.LinkPage(context.Background(), "nope", "user:x"); status != 404 {
		t.Errorf("an unknown address answered %d", status)
	}

	if status, _ := w.svc.LinkPage(context.Background(), "nope", "service_account:x"); status != 403 {
		t.Errorf("a service account answered %d", status)
	}

	for range maxLinkTries {
		w.svc.Link(context.Background(), Request{User: "U1"}, []string{"WRONG123"}, &fakeUI{})
	}

	if got := w.svc.Link(context.Background(), Request{User: "U1"}, []string{"WRONG123"}, &fakeUI{}); !strings.Contains(got.Lines[0], "Too many") {
		t.Errorf("after %d wrong codes: %+v", maxLinkTries, got)
	}
}

// TestSuspension holds while the member still qualifies, and lifts.
func TestSuspension(t *testing.T) {
	w := newWorld(t)
	linkAs(t, w, "UADMIN", "user:admin1")
	w.svc.Signup(context.Background(), Request{User: "U1"}, &fakeUI{})
	id := w.saved.Members["ABCD"].AccountID

	if got := w.svc.Suspend(context.Background(), Request{User: "U1"}, []string{"example.user"}); !strings.Contains(got.Lines[0], "linked first") {
		t.Errorf("a member could suspend: %+v", got)
	}

	ask := w.svc.Suspend(context.Background(), Request{User: "UADMIN"}, []string{"<@U1|example>", "safety", "issue"})
	if len(ask.Buttons) != 1 || !ask.Buttons[0].Danger {
		t.Fatalf("suspend asked %+v", ask)
	}

	w.svc.SuspendConfirm(context.Background(), Interaction{User: "UADMIN", Action: "suspend-confirm", Value: ask.Buttons[0].Value}, &fakeUI{})

	if w.accounts.get(id).Role != RoleViewer || w.saved.Suspensions["ABCD"].Reason != "safety issue" {
		t.Fatalf("after suspend: %s, %+v", w.accounts.get(id).Role, w.saved.Suspensions["ABCD"])
	}

	for _, run := range []func(*fakeUI){
		func(ui *fakeUI) { w.svc.Signup(context.Background(), Request{User: "U1"}, ui) },
		func(ui *fakeUI) { w.svc.Password(context.Background(), Request{User: "U1"}, ui) },
	} {
		ui := &fakeUI{}
		run(ui)

		if !strings.Contains(linesOf(ui.last()), "suspended") {
			t.Errorf("a suspended member was answered %+v", ui.last())
		}
	}

	lift := w.svc.Unsuspend(context.Background(), Request{User: "UADMIN"}, []string{"ABCD"})
	w.svc.UnsuspendConfirm(context.Background(), Interaction{User: "UADMIN", Action: "unsuspend-confirm", Value: lift.Buttons[0].Value}, &fakeUI{})

	if w.accounts.get(id).Role != RoleOperator || w.saved.Suspensions["ABCD"] != nil {
		t.Errorf("after unsuspend: %s, %+v", w.accounts.get(id).Role, w.saved.Suspensions)
	}
}

func TestConfirmationsAreTheirAdmins(t *testing.T) {
	w := newWorld(t)
	linkAs(t, w, "UADMIN", "user:admin1")
	w.svc.Signup(context.Background(), Request{User: "U1"}, &fakeUI{})

	ask := w.svc.Suspend(context.Background(), Request{User: "UADMIN"}, []string{"example.user"})

	ui := &fakeUI{}
	w.svc.SuspendConfirm(context.Background(), Interaction{User: "U1", Action: "suspend-confirm", Value: ask.Buttons[0].Value}, ui)

	if w.saved.Suspensions["ABCD"] != nil {
		t.Error("someone else's click suspended a member")
	}
}

func TestLookup(t *testing.T) {
	w := newWorld(t)
	linkAs(t, w, "UADMIN", "user:admin1")
	w.svc.Signup(context.Background(), Request{User: "U1"}, &fakeUI{})

	for _, who := range []string{"<@U1|example>", "example.user", "abcd"} {
		got := w.svc.Lookup(context.Background(), Request{User: "UADMIN"}, []string{who})
		if fieldOf(Modal{Fields: got.Fields}, "Member code") != "ABCD" || !strings.Contains(fieldOf(Modal{Fields: got.Fields}, "Slack user"), "example (U1)") {
			t.Errorf("lookup %s answered %+v", who, got)
		}
	}

	if got := w.svc.Lookup(context.Background(), Request{User: "UADMIN"}, []string{"boss"}); !strings.Contains(got.Lines[0], "admin account") {
		t.Errorf("lookup of an admin answered %+v", got)
	}
}

// TestRecoverAdoptsOnlyTheBotsOwnAccount is a crash between creating an
// account and recording it.
func TestRecoverAdoptsOnlyTheBotsOwnAccount(t *testing.T) {
	w := newWorld(t)
	w.svc.st.Journal = []*JournalEntry{
		{Op: opSignup, Code: "ABCD", Slack: "U1", Username: "made.by.bot", At: start},
		{Op: opSignup, Code: "EFGH", Slack: "U2", Username: "handmade", At: start},
	}
	w.accounts.list = append(w.accounts.list,
		Account{ID: "n1", Username: "made.by.bot", Role: RoleOperator, Created: start.Add(time.Second)},
		Account{ID: "n2", Username: "handmade", Role: RoleOperator, Created: start.Add(-time.Hour)}, // older than the entry
	)

	w.svc.Recover(context.Background())

	if rec := w.saved.Members["ABCD"]; rec == nil || rec.AccountID != "n1" {
		t.Errorf("the bot's own account was not adopted: %+v", rec)
	}

	if rec := w.saved.Members["EFGH"]; rec != nil {
		t.Errorf("an older account of the same name was adopted: %+v", rec)
	}

	if len(w.saved.Journal) != 0 {
		t.Errorf("the journal still holds %v", w.saved.Journal)
	}
}

func TestUsernames(t *testing.T) {
	cases := map[string]string{
		"Example User": "example.user", "Jane_DOE": "jane_doe", "  ..x..  ": "x", "Ünïcødé": "ncd",
		"🎉": "", strings.Repeat("a", 40): strings.Repeat("a", 32),
	}
	for in, want := range cases {
		if got := NormalizeUsername(in); got != want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", in, got, want)
		}
	}

	taken := map[string]bool{"x-2": true, "x-3": true}
	if got := NextFree("x", func(n string) bool { return taken[n] }); got != "x-4" {
		t.Errorf("NextFree = %q", got)
	}

	if got := NextFree(strings.Repeat("a", 32), func(string) bool { return false }); len(got) > 32 || !strings.HasSuffix(got, "-2") {
		t.Errorf("NextFree of a long name = %q", got)
	}

	for _, name := range []string{"admin", "administrator", "root", "slackbot", "SlackBot"} {
		if !Reserved(name, "slackbot") {
			t.Errorf("%s is not reserved", name)
		}
	}
}
