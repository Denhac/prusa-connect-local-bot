package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// elevate runs the admin command as admin for who and clicks the button. It
// answers what the bot said, the refusal when no button came.
func (w *world) elevate(admin, who string) string {
	msg := w.svc.MakeAdmin(context.Background(), Request{User: admin}, []string{who})
	if len(msg.Buttons) != 1 {
		return strings.Join(msg.Lines, " ")
	}

	ui := &fakeUI{}
	w.svc.MakeAdminConfirm(context.Background(), Interaction{User: admin, Action: "admin-confirm", Value: msg.Buttons[0].Value}, ui)

	return said(ui)
}

func (w *world) unadmin(admin, who string) string {
	msg := w.svc.Unadmin(context.Background(), Request{User: admin}, []string{who})
	if len(msg.Buttons) != 1 {
		return strings.Join(msg.Lines, " ")
	}

	ui := &fakeUI{}
	w.svc.UnadminConfirm(context.Background(), Interaction{User: admin, Action: "unadmin-confirm", Value: msg.Buttons[0].Value}, ui)

	return said(ui)
}

func said(ui *fakeUI) string {
	if len(ui.said) == 0 {
		return ""
	}

	return strings.Join(ui.said[len(ui.said)-1].Lines, " ")
}

// adminWorld is a world with a linked admin, UADMIN, and a member, ABCD, who
// qualifies and has an operator account.
func adminWorld(t *testing.T) *world {
	t.Helper()

	w := newWorld(t)
	linkAs(t, w, "UADMIN", "user:admin1")
	w.held("ABCD", RoleOperator)
	w.qualifies("ABCD")

	return w
}

// TestMakeAdmin makes a member's account an admin, and links their Slack user
// as an admin, so the admin commands work for them at once.
func TestMakeAdmin(t *testing.T) {
	w := adminWorld(t)

	if got := w.elevate("UADMIN", "abcd"); !strings.Contains(got, "is an admin now") {
		t.Fatalf("admin answered %q", got)
	}

	rec := w.saved.Members["ABCD"]
	if w.role("ABCD") != RoleAdmin || rec.Elevated.IsZero() || rec.ElevatedBy != "UADMIN" {
		t.Fatalf("after admin: role %s, record %+v", w.role("ABCD"), rec)
	}

	if l := w.saved.Admins["U-ABCD"]; l == nil || l.Subject != "user:acct-ABCD" {
		t.Errorf("the member's Slack user is not linked as an admin: %+v", l)
	}

	w.svc.cfg.Access.(fakeAccess)["user:acct-ABCD"] = true

	if got := w.svc.Lookup(context.Background(), Request{User: "U-ABCD"}, []string{"abcd"}); fieldOfMessage(got, "Admin") == "" {
		t.Errorf("the new admin's lookup shows no Admin field: %+v", got)
	}

	if len(w.svc.st.Journal) != 0 {
		t.Errorf("the journal kept %+v", w.svc.st.Journal)
	}
}

// TestMakeAdminRefusals makes nobody an admin who should not be one.
func TestMakeAdminRefusals(t *testing.T) {
	cases := map[string]struct {
		set  func(*world)
		who  string
		by   string
		want string
	}{
		"not an admin":      {by: "U-ABCD", who: "abcd", want: "admin"},
		"no such member":    {who: "nobody", want: "no account the bot looks after"},
		"an admin by hand":  {who: "boss", want: "no account the bot looks after"},
		"suspended":         {set: func(w *world) { w.svc.st.Suspensions["ABCD"] = &Suspension{At: start} }, want: "suspended"},
		"lapsed":            {set: func(w *world) { w.svc.st.Members["ABCD"].Lapsed = start }, want: "lapsed"},
		"does not qualify":  {set: func(w *world) { w.lapses("ABCD") }, want: "does not qualify"},
		"cannot tell":       {set: func(w *world) { w.verifier.codeErr = errors.New("down") }, want: "cannot check"},
		"viewer on server":  {set: func(w *world) { w.accounts.get("acct-ABCD").Role = RoleViewer }, want: "is viewer on the printer server"},
		"server refuses":    {set: func(w *world) { w.accounts.roleErr = errors.New("500") }, want: "refused"},
		"made admin before": {set: func(w *world) { w.svc.st.Members["ABCD"].Elevated = start }, want: "an admin already"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := adminWorld(t)
			if c.set != nil {
				c.set(w)
			}

			by := c.by
			if by == "" {
				by = "UADMIN"
			}

			who := c.who
			if who == "" {
				who = "abcd"
			}

			before := w.role("ABCD")

			if got := w.elevate(by, who); !strings.Contains(got, c.want) {
				t.Errorf("answered %q, want %q", got, c.want)
			}

			if w.role("ABCD") != before || (w.svc.st.Members["ABCD"].Elevated.IsZero() != (name != "made admin before")) {
				t.Errorf("the account changed: role %s, record %+v", w.role("ABCD"), w.svc.st.Members["ABCD"])
			}
		})
	}
}

// TestAdminConfirmBelongsToItsAdmin refuses a click by anyone but the admin
// who asked.
func TestAdminConfirmBelongsToItsAdmin(t *testing.T) {
	w := adminWorld(t)
	msg := w.svc.MakeAdmin(context.Background(), Request{User: "UADMIN"}, []string{"abcd"})

	ui := &fakeUI{}
	w.svc.MakeAdminConfirm(context.Background(), Interaction{User: "U-ABCD", Action: "admin-confirm", Value: msg.Buttons[0].Value}, ui)

	if w.role("ABCD") != RoleOperator || !strings.Contains(said(ui), "expired") {
		t.Errorf("a click by another user: role %s, said %q", w.role("ABCD"), said(ui))
	}
}

// TestUnadmin takes admin from an account the bot made admin, and from no
// other.
func TestUnadmin(t *testing.T) {
	w := adminWorld(t)
	w.elevate("UADMIN", "abcd")

	if got := w.unadmin("UADMIN", "boss"); !strings.Contains(got, "not an admin the bot made") {
		t.Errorf("unadmin of an admin by hand answered %q", got)
	}

	if got := w.unadmin("UADMIN", "abcd"); !strings.Contains(got, "operator again") {
		t.Fatalf("unadmin answered %q", got)
	}

	rec := w.saved.Members["ABCD"]
	if w.role("ABCD") != RoleOperator || !rec.Elevated.IsZero() || w.saved.Admins["U-ABCD"] != nil {
		t.Errorf("after unadmin: role %s, record %+v, link %+v", w.role("ABCD"), rec, w.saved.Admins["U-ABCD"])
	}

	if w.accounts.get("admin1").Role != RoleAdmin {
		t.Error("an admin made by hand was changed")
	}
}

// TestAdminKeepsAnExistingAdminLink leaves the link of a Slack user who has
// an admin account of their own, both when making them admin and after.
func TestAdminKeepsAnExistingAdminLink(t *testing.T) {
	w := adminWorld(t)
	w.svc.st.Members["ABCD"].Slack = "UADMIN"

	w.elevate("UADMIN", "abcd")

	if l := w.svc.st.Admins["UADMIN"]; l.Subject != "user:admin1" {
		t.Fatalf("the admin link was replaced: %+v", l)
	}

	w.unadmin("UADMIN", "abcd")

	if l := w.svc.st.Admins["UADMIN"]; l == nil || l.Subject != "user:admin1" {
		t.Errorf("unadmin took the admin's own link: %+v", l)
	}
}

// TestSuspendingAnElevatedAdmin turns the account to viewer and ends the
// elevation: lifting the suspension makes it an operator, not an admin.
func TestSuspendingAnElevatedAdmin(t *testing.T) {
	w := adminWorld(t)
	w.elevate("UADMIN", "abcd")

	msg := w.svc.Suspend(context.Background(), Request{User: "UADMIN"}, []string{"abcd", "safety"})
	w.svc.SuspendConfirm(context.Background(), Interaction{User: "UADMIN", Value: msg.Buttons[0].Value}, &fakeUI{})

	if w.role("ABCD") != RoleViewer || !w.svc.st.Members["ABCD"].Elevated.IsZero() || w.svc.st.Admins["U-ABCD"] != nil {
		t.Fatalf("after suspend: role %s, record %+v", w.role("ABCD"), w.svc.st.Members["ABCD"])
	}

	msg = w.svc.Unsuspend(context.Background(), Request{User: "UADMIN"}, []string{"abcd"})
	w.svc.UnsuspendConfirm(context.Background(), Interaction{User: "UADMIN", Value: msg.Buttons[0].Value}, &fakeUI{})

	if w.role("ABCD") != RoleOperator {
		t.Errorf("after unsuspend: role %s", w.role("ABCD"))
	}
}

// TestSweepAndElevatedAdmins keeps an admin the bot made, turns one off when
// the membership lapses, and takes in admin taken away on the server.
func TestSweepAndElevatedAdmins(t *testing.T) {
	w := adminWorld(t)
	w.held("EFGH", RoleOperator)
	w.qualifies("EFGH")
	w.elevate("UADMIN", "abcd")
	w.elevate("UADMIN", "efgh")

	w.sweep(t, time.Hour)

	if w.svc.st.Members["ABCD"] == nil || w.role("ABCD") != RoleAdmin {
		t.Fatal("the sweep let go of an admin the bot made")
	}

	w.lapses("ABCD")
	w.accounts.get("acct-EFGH").Role = RoleOperator // admin taken away in the web app

	w.sweep(t, 25*time.Hour)

	if rec := w.svc.st.Members["EFGH"]; rec == nil || !rec.Elevated.IsZero() || w.role("EFGH") != RoleOperator {
		t.Errorf("admin taken away by hand: %+v, role %s", rec, w.role("EFGH"))
	}

	w.sweep(t, 46*time.Hour)

	if rec := w.svc.st.Members["ABCD"]; rec.Lapsed.IsZero() || !rec.Elevated.IsZero() || w.role("ABCD") != RoleViewer {
		t.Errorf("a lapsed admin the bot made: %+v, role %s", rec, w.role("ABCD"))
	}
}

// TestPasswordAndSignupForAnElevatedAdmin never sets an admin's password, and
// tells an admin who signs up again that they have their account.
func TestPasswordAndSignupForAnElevatedAdmin(t *testing.T) {
	w := newWorld(t)
	linkAs(t, w, "UADMIN", "user:admin1")
	w.svc.Signup(context.Background(), Request{User: "U1"}, &fakeUI{})
	w.elevate("UADMIN", "ABCD")

	id := w.saved.Members["ABCD"].AccountID
	before := w.accounts.passwords[id]

	ui := &fakeUI{}
	w.svc.Password(context.Background(), Request{User: "U1"}, ui)

	if !strings.Contains(linesOf(ui.last()), "never sets an admin's password") || w.accounts.passwords[id] != before {
		t.Errorf("password for an admin: %+v", ui.last())
	}

	ui = &fakeUI{}
	w.svc.Signup(context.Background(), Request{User: "U1"}, ui)

	if !strings.Contains(linesOf(ui.last()), "already have") || w.accounts.get(id).Role != RoleAdmin {
		t.Errorf("signup for an admin: %+v, role %s", ui.last(), w.accounts.get(id).Role)
	}
}

// TestRecoverAnAdminCommand finishes an admin command cut short after the
// server made the account admin, and drops one the server never carried out.
func TestRecoverAnAdminCommand(t *testing.T) {
	w := adminWorld(t)
	w.held("EFGH", RoleOperator)

	w.accounts.get("acct-ABCD").Role = RoleAdmin // done before the crash
	w.svc.st.Journal = []*JournalEntry{
		{Op: opElevate, Code: "ABCD", Slack: "UADMIN", AccountID: "acct-ABCD", At: start},
		{Op: opElevate, Code: "EFGH", Slack: "UADMIN", AccountID: "acct-EFGH", At: start},
	}

	w.svc.Recover(context.Background())

	if rec := w.svc.st.Members["ABCD"]; rec.Elevated.IsZero() || rec.ElevatedBy != "UADMIN" {
		t.Errorf("the finished one: %+v", rec)
	}

	if rec := w.svc.st.Members["EFGH"]; !rec.Elevated.IsZero() || w.role("EFGH") != RoleOperator {
		t.Errorf("the one never carried out: %+v", rec)
	}

	if len(w.svc.st.Journal) != 0 {
		t.Errorf("the journal kept %+v", w.svc.st.Journal)
	}
}
