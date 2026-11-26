package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const day = 24 * time.Hour

// held gives the world a member the bot looks after, with an account of the
// given role.
func (w *world) held(code, role string) *MemberRecord {
	id := "acct-" + code
	w.accounts.list = append(w.accounts.list, Account{ID: id, Username: strings.ToLower(code), Role: role, Created: start})
	rec := &MemberRecord{Slack: "U-" + code, AccountID: id, Username: strings.ToLower(code), Via: "created", Since: start}
	w.svc.st.Members[code] = rec

	return rec
}

func (w *world) lapses(codes ...string) {
	if w.verifier.byCode == nil {
		w.verifier.byCode = map[string]Membership{}
	}

	for _, code := range codes {
		w.verifier.byCode[code] = Membership{Code: code, Reason: "denhac shows the membership as inactive."}
	}
}

func (w *world) qualifies(codes ...string) {
	if w.verifier.byCode == nil {
		w.verifier.byCode = map[string]Membership{}
	}

	for _, code := range codes {
		w.verifier.byCode[code] = member(code, code)
	}
}

func (w *world) role(code string) string {
	rec := w.svc.st.Members[code]
	if rec == nil {
		return "no record"
	}

	if a := w.accounts.get(rec.AccountID); a != nil {
		return a.Role
	}

	return "no account"
}

func (w *world) sweep(t *testing.T, d time.Duration) SweepResult {
	t.Helper()
	w.at(d)

	return w.svc.SweepOnce(context.Background())
}

// TestSweepLapsesOnTheSecondFailedCheck turns an account off only on a second
// failed check at least 20 h after the first, and never on one alone.
func TestSweepLapsesOnTheSecondFailedCheck(t *testing.T) {
	w := newWorld(t)
	w.held("ABCD", RoleOperator)
	w.lapses("ABCD")

	if res := w.sweep(t, time.Hour); !res.Full || res.Fail != 1 || res.TurnedOff != 0 || w.role("ABCD") != RoleOperator {
		t.Fatalf("the first pass: %+v, role %s", res, w.role("ABCD"))
	}

	if res := w.sweep(t, 11*time.Hour); res != (SweepResult{}) {
		t.Fatalf("a pass 10 h on did something: %+v", res)
	}

	res := w.sweep(t, 21*time.Hour)
	if res.Full || res.Checked != 1 || res.TurnedOff != 1 || w.role("ABCD") != RoleViewer {
		t.Fatalf("the recheck 20 h on: %+v, role %s", res, w.role("ABCD"))
	}

	rec := w.saved.Members["ABCD"]
	if !rec.Lapsed.Equal(start.Add(21*time.Hour)) || len(rec.FailedChecks) != 0 || !strings.HasPrefix(rec.Check, checkFails) {
		t.Errorf("the record after lapsing: %+v", rec)
	}

	// The next full pass checks it again, and leaves it off.
	if res := w.sweep(t, 25*time.Hour); !res.Full || res.TurnedOff != 0 || w.role("ABCD") != RoleViewer {
		t.Errorf("the next full pass: %+v, role %s", res, w.role("ABCD"))
	}
}

// TestSweepAQualifyingCheckClears forgets a failed check once a later one
// qualifies, and never turns a lapsed account back on by itself.
func TestSweepAQualifyingCheckClears(t *testing.T) {
	w := newWorld(t)
	w.held("ABCD", RoleOperator)
	gone := w.held("GONE", RoleViewer)
	gone.Lapsed = start

	w.lapses("ABCD")
	w.qualifies("GONE")
	w.sweep(t, time.Hour)

	w.qualifies("ABCD")
	w.sweep(t, 21*time.Hour)

	if rec := w.svc.st.Members["ABCD"]; len(rec.FailedChecks) != 0 || !rec.Lapsed.IsZero() || w.role("ABCD") != RoleOperator {
		t.Errorf("a member who qualifies again: %+v, role %s", rec, w.role("ABCD"))
	}

	if rec := w.svc.st.Members["GONE"]; rec.Lapsed.IsZero() || w.role("GONE") != RoleViewer || !rec.Qualified.Equal(start.Add(time.Hour)) {
		t.Errorf("a lapsed member who qualifies: %+v, role %s; signup turns it on, not the sweep", rec, w.role("GONE"))
	}
}

// TestSweepCannotTellChangesNothing leaves every record as it was when the
// member pages cannot be read.
func TestSweepCannotTellChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.held("ABCD", RoleOperator)
	w.verifier.codeErr = errors.New("denhac.org did not answer")

	for _, d := range []time.Duration{time.Hour, 25 * time.Hour, 49 * time.Hour} {
		if res := w.sweep(t, d); res.CannotTell != 1 || res.Fail != 0 || res.TurnedOff != 0 {
			t.Errorf("pass at %v: %+v", d, res)
		}
	}

	if rec := w.svc.st.Members["ABCD"]; len(rec.FailedChecks) != 0 || w.role("ABCD") != RoleOperator ||
		!strings.HasPrefix(rec.Check, checkUnknown) {
		t.Errorf("after three passes that could not tell: %+v", rec)
	}
}

// TestSweepDeletesAfterDeleteAfter deletes a lapsed account once it has been
// lapsed for delete_after and its member has not qualified for as long, and
// keeps a suspension.
func TestSweepDeletesAfterDeleteAfter(t *testing.T) {
	w := newWorld(t)
	old := w.held("OLDX", RoleViewer)
	old.Lapsed = start
	w.svc.st.Suspensions["OLDX"] = &Suspension{By: "UADMIN", At: start}

	back := w.held("BACK", RoleViewer)
	back.Lapsed = start

	w.lapses("OLDX")
	w.qualifies("BACK")

	// BACK qualifies at this pass, though it does not run signup.
	if res := w.sweep(t, 30*day); res.Deleted != 0 {
		t.Fatalf("deleted before delete_after: %+v", res)
	}

	w.lapses("BACK")

	res := w.sweep(t, 90*day)
	if res.Deleted != 1 || w.svc.st.Members["OLDX"] != nil || w.svc.st.Suspensions["OLDX"] == nil {
		t.Fatalf("at 90 days: %+v; the suspension must stay", res)
	}

	if len(w.accounts.deletes) != 1 || w.accounts.deletes[0] != "acct-OLDX" {
		t.Errorf("deleted %v", w.accounts.deletes)
	}

	if w.svc.st.Members["BACK"] == nil {
		t.Error("BACK was deleted 60 days after it last qualified")
	}

	if res := w.sweep(t, 120*day+time.Hour); res.Deleted != 1 || w.svc.st.Members["BACK"] != nil {
		t.Errorf("BACK 90 days after it last qualified: %+v", res)
	}
}

// TestSweepLimit turns off at most MaxChanges accounts in any 24 h, oldest
// failure first, and the rest on later days.
func TestSweepLimit(t *testing.T) {
	w := newWorld(t)
	w.held("KEEP", RoleOperator)
	w.qualifies("KEEP")

	for i := range 7 {
		code := fmt.Sprintf("LAP%d", i)
		w.held(code, RoleOperator)
		w.lapses(code)
	}

	w.sweep(t, time.Hour)

	res := w.sweep(t, 21*time.Hour)
	if res.TurnedOff != 5 || res.Held != 2 {
		t.Fatalf("the first day: %+v", res)
	}

	res = w.sweep(t, 25*time.Hour)
	if res.TurnedOff != 0 || res.Held != 2 {
		t.Fatalf("within the same 24 h: %+v", res)
	}

	res = w.sweep(t, 45*time.Hour+time.Minute)
	if res.TurnedOff != 2 {
		t.Fatalf("the next day: %+v", res)
	}

	if w.role("KEEP") != RoleOperator {
		t.Error("a qualifying member was turned off")
	}
}

// TestSweepReportOnly changes nothing with MaxChanges 0.
func TestSweepReportOnly(t *testing.T) {
	w := newWorld(t)
	w.svc.cfg.Sweep.MaxChanges = 0
	w.held("ABCD", RoleOperator)
	w.lapses("ABCD")

	w.sweep(t, time.Hour)

	if res := w.sweep(t, 21*time.Hour); res.TurnedOff != 0 || res.Held != 1 || w.role("ABCD") != RoleOperator {
		t.Errorf("report only: %+v, role %s", res, w.role("ABCD"))
	}
}

// TestSweepBreaker believes no "does not qualify" from a full pass in which
// nobody qualifies: that is a page or an authorization that changed.
func TestSweepBreaker(t *testing.T) {
	w := newWorld(t)

	for _, code := range []string{"AAAA", "BBBB", "CCCC", "DDDD"} {
		w.held(code, RoleOperator)
		w.lapses(code)
	}

	res := w.sweep(t, time.Hour)
	if res.Fail != 0 || res.CannotTell != 4 || res.Problem == "" {
		t.Fatalf("a pass where nobody qualifies: %+v", res)
	}

	for code, rec := range w.svc.st.Members {
		if len(rec.FailedChecks) != 0 {
			t.Errorf("%s has a failed check counted: %+v", code, rec)
		}
	}
}

// TestSweepHandChanges takes in what an admin changed on the server.
func TestSweepHandChanges(t *testing.T) {
	w := newWorld(t)

	for _, code := range []string{"GONE", "BOSS", "DOWN", "BACK", "OKAY"} {
		w.held(code, RoleOperator)
	}

	w.qualifies("GONE", "BOSS", "DOWN", "BACK", "OKAY")
	w.svc.st.Members["BACK"].Lapsed = start // lapsed, then set to operator by hand

	w.accounts.list = deleteAccount(w.accounts.list, "acct-GONE")
	w.accounts.get("acct-BOSS").Role = RoleAdmin
	w.accounts.get("acct-DOWN").Role = RoleViewer

	res := w.sweep(t, time.Hour)
	if res.ByHand != 4 {
		t.Errorf("by hand: %+v", res)
	}

	if w.svc.st.Members["GONE"] != nil || w.svc.st.Members["BOSS"] != nil {
		t.Error("a record of an account deleted or made admin on the server is kept")
	}

	if sus := w.svc.st.Suspensions["DOWN"]; sus == nil || sus.By != byTheServer || w.role("DOWN") != RoleViewer {
		t.Errorf("set to viewer by hand: %+v, role %s", sus, w.role("DOWN"))
	}

	if w.role("BACK") != RoleViewer || w.role("OKAY") != RoleOperator {
		t.Errorf("BACK is %s and OKAY is %s", w.role("BACK"), w.role("OKAY"))
	}

	if w.accounts.get("acct-BOSS").Role != RoleAdmin {
		t.Error("the sweep changed an admin account")
	}
}

func deleteAccount(list []Account, id string) []Account {
	out := list[:0]

	for _, a := range list {
		if a.ID != id {
			out = append(out, a)
		}
	}

	return out
}

// TestSweepDrift counts accounts the bot does not look after, and touches none.
func TestSweepDrift(t *testing.T) {
	w := newWorld(t)
	w.held("ABCD", RoleOperator)
	w.qualifies("ABCD")
	w.accounts.list = append(w.accounts.list, Account{ID: "x1", Username: "by.hand", Role: RoleOperator})
	w.accounts.grants = map[string][]string{"acct-ABCD": {"operator on device:xl"}}

	res := w.sweep(t, time.Hour)
	if res.Drift != 1 || w.accounts.get("x1").Role != RoleOperator {
		t.Errorf("drift: %+v", res)
	}
}

// TestSweepNeedsATrustedAccountList changes nothing when the account list
// cannot be read, or does not hold the bot's own account, and tries the full
// pass again at the next look.
func TestSweepNeedsATrustedAccountList(t *testing.T) {
	for name, broken := range map[string]func(*fakeAccounts){
		"list fails":     func(a *fakeAccounts) { a.listErr = errors.New("503") },
		"bot is missing": func(a *fakeAccounts) { a.list = deleteAccount(a.list, "bot") },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.held("ABCD", RoleOperator)
			w.lapses("ABCD")
			broken(w.accounts)

			res := w.sweep(t, time.Hour)
			if res.Problem == "" || len(w.svc.st.Members["ABCD"].FailedChecks) != 0 || !w.svc.st.Sweep.Last.IsZero() {
				t.Errorf("%+v; the record is %+v", res, w.svc.st.Members["ABCD"])
			}

			if res := w.sweep(t, 2*time.Hour); !res.Full {
				t.Errorf("the next look is not a full pass: %+v", res)
			}
		})
	}
}

// TestSweepRetriesARoleChange sets the role at a later pass when the call
// that turned an account off failed.
func TestSweepRetriesARoleChange(t *testing.T) {
	w := newWorld(t)
	w.held("ABCD", RoleOperator)
	w.lapses("ABCD")
	w.sweep(t, time.Hour)

	w.accounts.roleErr = errors.New("500")
	w.sweep(t, 21*time.Hour)

	if w.svc.st.Members["ABCD"].Lapsed.IsZero() || w.role("ABCD") != RoleOperator {
		t.Fatal("the record should be lapsed with the account still an operator")
	}

	w.accounts.roleErr = nil

	if res := w.sweep(t, 25*time.Hour); w.role("ABCD") != RoleViewer || res.ByHand != 1 {
		t.Errorf("the next pass left it an operator: %+v", res)
	}
}

// TestSweepOff does nothing with interval 0.
func TestSweepOff(t *testing.T) {
	w := newWorld(t)
	w.svc.cfg.Sweep.Interval = 0
	w.held("ABCD", RoleOperator)
	w.lapses("ABCD")

	if res := w.sweep(t, 30*day); res.Checked != 0 || w.verifier.reads != 0 {
		t.Errorf("an off sweep checked: %+v", res)
	}

	if got := w.svc.SweepStatus(); !strings.HasPrefix(got, "off") {
		t.Errorf("status %q", got)
	}

	w.svc.RunSweeps(context.Background()) // returns at once
}

// TestSweepStatus says what the last pass did and when the next full one is.
func TestSweepStatus(t *testing.T) {
	w := newWorld(t)

	if got := w.svc.SweepStatus(); !strings.Contains(got, "not run") {
		t.Errorf("before a pass: %q", got)
	}

	w.held("ABCD", RoleOperator)
	w.qualifies("ABCD")
	w.sweep(t, time.Hour)
	w.at(3 * time.Hour)

	got := w.svc.SweepStatus()
	for _, want := range []string{"last pass 2 h", "1 checked (1 qualify", "next full sweep in 22 h"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q does not say %q", got, want)
		}
	}
}

// TestLookupShowsTheLastCheck shows the sweep's last result to an admin.
func TestLookupShowsTheLastCheck(t *testing.T) {
	w := newWorld(t)
	linkAs(t, w, "UADMIN", "user:admin1")
	w.held("ABCD", RoleOperator)
	w.lapses("ABCD")
	w.sweep(t, time.Hour)

	got := w.svc.Lookup(context.Background(), Request{User: "UADMIN"}, []string{"abcd"})
	if check := fieldOfMessage(got, "Last check"); !strings.Contains(check, "does not qualify") || !strings.Contains(check, "failing since") {
		t.Errorf("Last check is %q", check)
	}
}

func fieldOfMessage(m Message, label string) string {
	for _, f := range m.Fields {
		if f.Label == label {
			return f.Value
		}
	}

	return ""
}

// TestUnsuspendKeepsTheSuspensionUntilTheAccountIsOn keeps the suspension
// when the account cannot be turned back on, so a later sweep never takes the
// viewer account for one demoted by hand.
func TestUnsuspendKeepsTheSuspensionUntilTheAccountIsOn(t *testing.T) {
	w := newWorld(t)
	linkAs(t, w, "UADMIN", "user:admin1")
	w.held("ABCD", RoleViewer)
	w.qualifies("ABCD")
	w.svc.st.Suspensions["ABCD"] = &Suspension{By: "UADMIN", At: start}

	ask := func() *fakeUI {
		msg := w.svc.Unsuspend(context.Background(), Request{User: "UADMIN"}, []string{"abcd"})
		ui := &fakeUI{}
		w.svc.UnsuspendConfirm(context.Background(), Interaction{User: "UADMIN", Value: msg.Buttons[0].Value}, ui)

		return ui
	}

	w.accounts.roleErr = errors.New("500")
	ask()

	if w.svc.st.Suspensions["ABCD"] == nil {
		t.Fatal("the suspension went while the account stayed a viewer")
	}

	w.accounts.roleErr = nil
	ask()

	if w.svc.st.Suspensions["ABCD"] != nil || w.role("ABCD") != RoleOperator {
		t.Errorf("after a lift that worked: %+v, role %s", w.svc.st.Suspensions["ABCD"], w.role("ABCD"))
	}
}
