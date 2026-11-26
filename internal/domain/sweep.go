package domain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// This file is the membership sweep (docs/design.md, "The sweep"). It checks
// each member's page now and then, turns the account of a former member to
// viewer, deletes it once it has been lapsed for delete_after, and puts right
// what was changed by hand on the server. It never sends anyone a message.
//
// A pass reads the member pages without the writer's lock, which can take
// minutes, and then takes the lock to apply what it found against a fresh
// account list. Its own changes need no journal: each is written to the
// record first, and the next pass puts the account in step with the record.

// SweepConfig is how the sweep runs.
type SweepConfig struct {
	// Interval is how often every member is checked. Zero turns the sweep off.
	Interval time.Duration
	// MaxChanges is the most accounts turned off, and the most deleted, in
	// any 24 hours. Zero changes none and only reports.
	MaxChanges int
	// DeleteAfter is how long an account stays lapsed before it is deleted.
	DeleteAfter time.Duration
}

// DefaultSweepConfig is module.yaml's defaults: daily, 5, 90 days.
func DefaultSweepConfig() SweepConfig {
	return SweepConfig{Interval: 24 * time.Hour, MaxChanges: 5, DeleteAfter: 90 * 24 * time.Hour}
}

// The sweep's timing.
const (
	// recheckAfter is how far apart the two failed checks that turn an
	// account off must be, so that one bad day at denhac turns nobody off.
	recheckAfter = 20 * time.Hour
	// limitWindow is the span MaxChanges counts over.
	limitWindow = 24 * time.Hour
	// sweepFirst is the first look after a start, and sweepTick the next ones.
	// A look does only what is due, so most do nothing.
	sweepFirst = 2 * time.Minute
	sweepTick  = time.Hour
	// breakerMin is the fewest members a full pass must check before "nobody
	// qualifies" is taken for a broken page rather than believed.
	breakerMin = 4
)

// The check results kept on a record.
const (
	checkQualifies = "qualifies"
	checkFails     = "does not qualify"
	checkUnknown   = "cannot tell"
)

// byTheServer is the Suspension.By of an account an admin set to viewer on
// the server instead of with suspend.
const byTheServer = "an admin on the server"

// SweepState is kept in the state file.
type SweepState struct {
	// Last is when the last full pass began.
	Last time.Time `json:"last,omitzero"`
	// Offs and Deletes are when accounts were turned off and deleted, for
	// MaxChanges.
	Offs    []time.Time `json:"offs,omitempty"`
	Deletes []time.Time `json:"deletes,omitempty"`
	// Result is what the last pass did.
	Result *SweepResult `json:"result,omitempty"`
}

// SweepResult is what one pass did.
type SweepResult struct {
	At time.Time `json:"at"`
	// Full is a pass over every member. Other passes check again only the
	// members with a failed check, and delete what is due.
	Full       bool `json:"full"`
	Checked    int  `json:"checked"`
	Qualify    int  `json:"qualify"`
	Fail       int  `json:"fail"`
	CannotTell int  `json:"cannot_tell"`
	TurnedOff  int  `json:"turned_off"`
	Deleted    int  `json:"deleted"`
	// Held are changes due and left for later by MaxChanges.
	Held int `json:"held,omitempty"`
	// ByHand are changes made on the server that the pass took in or undid.
	ByHand int `json:"by_hand,omitempty"`
	// Drift are non-admin accounts the bot does not look after.
	Drift int `json:"drift,omitempty"`
	// Problem is what kept the pass from doing all it was due to: an account
	// list it could not read, or checks it did not believe.
	Problem string `json:"problem,omitempty"`
}

// errBreaker stands in for a check the breaker did not believe.
var errBreaker = errors.New("no member checked qualifies, so the page or the authorization's name may have changed")

// RunSweeps runs the sweep until ctx ends.
func (s *AccountsService) RunSweeps(ctx context.Context) {
	if s.cfg.Sweep.Interval <= 0 {
		s.log.Info("the membership sweep is off: sweep_interval is 0")

		return
	}

	timer := time.NewTimer(sweepFirst)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		s.SweepOnce(ctx)
		timer.Reset(sweepTick)
	}
}

type checked struct {
	m   Membership
	err error
	at  time.Time
}

// SweepOnce does what is due now: a full pass every Interval, a second
// check of members with one failed check once recheckAfter has passed, the
// deletions that are due, and the journal. It answers what it did, and a zero
// result when nothing was due.
func (s *AccountsService) SweepOnce(ctx context.Context) SweepResult {
	now := s.cfg.Now()

	s.mu.Lock()
	full := s.cfg.Sweep.Interval > 0 && now.Sub(s.st.Sweep.Last) >= s.cfg.Sweep.Interval
	codes := s.dueChecks(now, full)
	due := full || len(codes) > 0 || len(s.st.Journal) > 0 || s.deletesDue(now)
	s.mu.Unlock()

	if !due {
		return SweepResult{}
	}

	res := SweepResult{At: now, Full: full}
	checks := s.checkAll(ctx, codes, &res)

	if ctx.Err() != nil {
		return SweepResult{} // stopping: nothing is applied, and the pass runs again after the start
	}

	if full && res.Checked >= breakerMin && res.Qualify == 0 && res.Fail > 0 {
		for code, c := range checks {
			if c.err == nil {
				c.err = errBreaker
				checks[code] = c
			}
		}

		res.CannotTell += res.Fail
		res.Fail = 0
		res.Problem = errBreaker.Error() + "; no check was counted against anyone"
		s.log.Warn("sweep: "+res.Problem, "checked", res.Checked)
	}

	s.mu.Lock()
	held := s.apply(ctx, now, full, checks, &res)
	lastFull := s.st.Sweep.Last
	s.mu.Unlock()

	if full {
		s.reportGrants(ctx, held)
	}

	s.pending.Lock()
	s.lastSweep, s.lastFull = &res, lastFull
	s.pending.Unlock()

	s.log.Info("sweep", "full", res.Full, "checked", res.Checked, "qualify", res.Qualify, "fail", res.Fail,
		"cannot_tell", res.CannotTell, "turned_off", res.TurnedOff, "deleted", res.Deleted, "held", res.Held,
		"by_hand", res.ByHand, "drift", res.Drift, "problem", res.Problem)

	return res
}

// dueChecks are the member codes to read now, oldest check first.
func (s *AccountsService) dueChecks(now time.Time, full bool) []string {
	var codes []string

	for code, rec := range s.st.Members {
		if full || (len(rec.FailedChecks) > 0 && now.Sub(rec.Checked) >= recheckAfter) {
			codes = append(codes, code)
		}
	}

	sort.Slice(codes, func(i, j int) bool {
		a, b := s.st.Members[codes[i]], s.st.Members[codes[j]]
		if !a.Checked.Equal(b.Checked) {
			return a.Checked.Before(b.Checked)
		}

		return codes[i] < codes[j]
	})

	return codes
}

// checkAll reads each member page, without the lock.
func (s *AccountsService) checkAll(ctx context.Context, codes []string, res *SweepResult) map[string]checked {
	out := make(map[string]checked, len(codes))

	for _, code := range codes {
		if ctx.Err() != nil {
			break
		}

		m, err := s.cfg.Verifier.CheckCode(ctx, code)
		out[code] = checked{m: m, err: err, at: s.cfg.Now()}

		switch {
		case err != nil:
			res.CannotTell++
		case m.Qualifies:
			res.Qualify++
		default:
			res.Fail++
		}
	}

	res.Checked = len(out)

	return out
}

// apply makes the pass's changes, with the lock held, and answers the member
// accounts whose grants a full pass reports.
func (s *AccountsService) apply(ctx context.Context, now time.Time, full bool, checks map[string]checked, res *SweepResult) []Account {
	accounts, err := s.cfg.Accounts.List(ctx)
	if err == nil && !slices.ContainsFunc(accounts, func(a Account) bool { return strings.EqualFold(a.Username, s.cfg.BotAccount) }) {
		err = errors.New("the account list does not hold the bot's own account, so it cannot be trusted")
	}

	if err != nil {
		res.Problem = "cannot read the accounts: " + err.Error()
		s.log.Warn("sweep: cannot read the accounts; nothing was applied", "error", err)

		return nil
	}

	s.recoverLocked(ctx, accounts)
	s.record(checks)

	if full {
		s.takeInHandChanges(now, accounts, res)
	}

	s.keepOff(ctx, accounts, res)
	s.turnOff(ctx, now, accounts, res)
	s.deleteLapsed(ctx, now, accounts, res)

	var held []Account

	if full {
		res.Drift = s.drift(accounts)
		s.st.Sweep.Last = now

		for _, code := range sortedCodes(s.st.Members) {
			held = append(held, Account{ID: s.st.Members[code].AccountID, Username: s.st.Members[code].Username})
		}
	}

	s.st.Sweep.Result = res
	s.save()

	return held
}

// record writes each check on its member's record.
func (s *AccountsService) record(checks map[string]checked) {
	for code, c := range checks {
		rec := s.st.Members[code]
		if rec == nil {
			continue // deleted while the pages were read
		}

		rec.Checked = c.at

		switch {
		case c.err != nil:
			rec.Check = checkUnknown + ": " + c.err.Error()
		case c.m.Qualifies:
			rec.Check, rec.FailedChecks, rec.Qualified = checkQualifies, nil, c.at
		default:
			rec.Check = checkFails + ": " + c.m.Reason

			first := c.at
			if len(rec.FailedChecks) > 0 {
				first = rec.FailedChecks[0]
			}

			rec.FailedChecks = []time.Time{first, c.at}
		}
	}
}

// journalOpen is whether a change to this member is begun and not finished.
func (s *AccountsService) journalOpen(code string) bool {
	return slices.ContainsFunc(s.st.Journal, func(e *JournalEntry) bool { return e.Code == code })
}

// takeInHandChanges reads what an admin changed on the server.
func (s *AccountsService) takeInHandChanges(now time.Time, accounts []Account, res *SweepResult) {
	for _, code := range sortedCodes(s.st.Members) {
		rec := s.st.Members[code]
		if s.journalOpen(code) {
			continue
		}

		acct := find(accounts, rec.AccountID)
		_, suspended := s.st.Suspensions[code]

		if acct != nil && !rec.Elevated.IsZero() && acct.Role != RoleAdmin {
			s.unelevated(rec)
			res.ByHand++
			s.log.Info("sweep: admin was taken from the account on the server", "member", rec.Username, "role", acct.Role)
		}

		switch {
		case acct == nil:
			delete(s.st.Members, code)
			res.ByHand++
			s.log.Info("sweep: the account is gone from the server; the bot forgets it", "member", rec.Username)
		case acct.Role == RoleAdmin && !rec.Elevated.IsZero():
			// made an admin with the admin command: still the bot's
		case acct.Role == RoleAdmin:
			delete(s.st.Members, code)
			res.ByHand++
			s.log.Info("sweep: the account was made an admin on the server; it is managed by hand from now on",
				"member", rec.Username)
		case acct.Role == RoleViewer && rec.Lapsed.IsZero() && !suspended:
			s.st.Suspensions[code] = &Suspension{By: byTheServer, Reason: "set to viewer on the server", At: now}
			res.ByHand++
			s.log.Info("sweep: the account was set to viewer on the server; it is kept as a suspension",
				"member", rec.Username)
		}
	}
}

// keepOff sets back to viewer an account that is an operator while it is
// lapsed or suspended: one restored by hand, or one a failed call left. That
// includes an admin the bot made, whose admin a failed call left.
func (s *AccountsService) keepOff(ctx context.Context, accounts []Account, res *SweepResult) {
	for _, code := range sortedCodes(s.st.Members) {
		rec := s.st.Members[code]
		_, suspended := s.st.Suspensions[code]

		acct := find(accounts, rec.AccountID)
		if acct == nil || (rec.Lapsed.IsZero() && !suspended) || s.journalOpen(code) {
			continue
		}

		if acct.Role != RoleOperator && (acct.Role != RoleAdmin || rec.Elevated.IsZero()) {
			continue
		}

		if err := s.setViewer(ctx, rec); err != nil {
			s.log.Warn("sweep: could not set an account back to viewer; the next pass tries again",
				"member", rec.Username, "error", err)

			continue
		}

		acct.Role = RoleViewer
		res.ByHand++
		s.log.Info("sweep: an operator account that should be off was set back to viewer",
			"member", rec.Username, "suspended", suspended, "lapsed", !rec.Lapsed.IsZero())
	}
}

// turnOff lapses the members with two failed checks recheckAfter apart,
// oldest first, within MaxChanges.
func (s *AccountsService) turnOff(ctx context.Context, now time.Time, accounts []Account, res *SweepResult) {
	var codes []string

	for code, rec := range s.st.Members {
		if rec.Lapsed.IsZero() && len(rec.FailedChecks) == 2 && rec.FailedChecks[1].Sub(rec.FailedChecks[0]) >= recheckAfter {
			codes = append(codes, code)
		}
	}

	sort.Slice(codes, func(i, j int) bool {
		a, b := s.st.Members[codes[i]].FailedChecks[0], s.st.Members[codes[j]].FailedChecks[0]
		if !a.Equal(b) {
			return a.Before(b)
		}

		return codes[i] < codes[j]
	})

	for _, code := range codes {
		rec := s.st.Members[code]

		acct := find(accounts, rec.AccountID)
		if acct == nil || (acct.Role == RoleAdmin && rec.Elevated.IsZero()) || s.journalOpen(code) {
			continue
		}

		change := acct.Role == RoleOperator || acct.Role == RoleAdmin
		if change && !s.allow(&s.st.Sweep.Offs, now) {
			res.Held++

			continue
		}

		failedSince := rec.FailedChecks[0]
		rec.Lapsed, rec.FailedChecks = now, nil
		s.save() // the record first: the next pass sets the role if this call fails

		if change {
			if err := s.setViewer(ctx, rec); err != nil {
				s.log.Warn("sweep: could not turn a lapsed account off; the next pass tries again",
					"member", rec.Username, "error", err)

				continue
			}

			acct.Role = RoleViewer
			s.st.Sweep.Offs = append(s.st.Sweep.Offs, now)
		}

		res.TurnedOff++
		s.log.Info("sweep: membership lapsed; the account is a viewer", "member", rec.Username,
			"failing_since", failedSince, "check", rec.Check)
	}
}

// deletesDue is whether an account has been lapsed long enough to delete.
func (s *AccountsService) deletesDue(now time.Time) bool {
	for _, rec := range s.st.Members {
		if s.deleteDue(rec, now) {
			return true
		}
	}

	return false
}

// deleteDue is an account lapsed for DeleteAfter, whose member has not
// qualified at a check for as long.
func (s *AccountsService) deleteDue(rec *MemberRecord, now time.Time) bool {
	after := s.cfg.Sweep.DeleteAfter

	return after > 0 && !rec.Lapsed.IsZero() && now.Sub(rec.Lapsed) >= after && now.Sub(rec.Qualified) >= after
}

// deleteLapsed deletes the accounts that are due, oldest lapse first, within
// MaxChanges. A suspension record stays.
func (s *AccountsService) deleteLapsed(ctx context.Context, now time.Time, accounts []Account, res *SweepResult) {
	var codes []string

	for code, rec := range s.st.Members {
		if s.deleteDue(rec, now) && !s.journalOpen(code) {
			codes = append(codes, code)
		}
	}

	sort.Slice(codes, func(i, j int) bool {
		a, b := s.st.Members[codes[i]].Lapsed, s.st.Members[codes[j]].Lapsed
		if !a.Equal(b) {
			return a.Before(b)
		}

		return codes[i] < codes[j]
	})

	for _, code := range codes {
		rec := s.st.Members[code]

		acct := find(accounts, rec.AccountID)
		if acct != nil && acct.Role == RoleAdmin {
			continue // made an admin by hand: the next full pass lets it go
		}

		if !s.allow(&s.st.Sweep.Deletes, now) {
			res.Held++

			continue
		}

		if acct != nil {
			if err := s.cfg.Accounts.Delete(ctx, acct.ID); err != nil {
				s.log.Warn("sweep: could not delete a lapsed account; the next pass tries again",
					"member", rec.Username, "error", err)

				continue
			}
		}

		delete(s.st.Members, code)
		s.st.Sweep.Deletes = append(s.st.Sweep.Deletes, now)
		res.Deleted++
		s.log.Info("sweep: a lapsed account was deleted", "member", rec.Username, "lapsed", rec.Lapsed,
			"suspension_kept", s.st.Suspensions[code] != nil)
	}
}

// allow is whether one more change fits in MaxChanges over limitWindow. It
// drops the times that have left the window.
func (s *AccountsService) allow(times *[]time.Time, now time.Time) bool {
	kept := (*times)[:0]

	for _, at := range *times {
		if now.Sub(at) < limitWindow {
			kept = append(kept, at)
		}
	}

	*times = kept

	return len(kept) < s.cfg.Sweep.MaxChanges
}

// drift logs the non-admin accounts the bot does not look after, and counts
// them. They are never touched.
func (s *AccountsService) drift(accounts []Account) int {
	held := map[string]bool{}
	for _, rec := range s.st.Members {
		held[rec.AccountID] = true
	}

	var names []string

	for _, a := range accounts {
		if a.Role != RoleAdmin && !strings.EqualFold(a.Username, s.cfg.BotAccount) && !held[a.ID] {
			names = append(names, a.Username)
		}
	}

	sort.Strings(names)

	if len(names) > 0 {
		shown := names
		if len(shown) > 20 {
			shown = shown[:20]
		}

		s.log.Info("sweep: accounts the bot does not look after, left alone (drift); link claims one",
			"count", len(names), "usernames", strings.Join(shown, ", "))
	}

	return len(names)
}

// reportGrants logs grants written by hand on single printers for member
// accounts, without the lock. They are never touched.
func (s *AccountsService) reportGrants(ctx context.Context, held []Account) {
	for _, a := range held {
		grants, err := s.cfg.Accounts.DirectGrants(ctx, a.ID)
		if err != nil {
			s.log.Warn("sweep: cannot read the grants on single printers; skipped this time", "error", err)

			return
		}

		if len(grants) > 0 {
			s.log.Info("sweep: a member account has grants written by hand on single printers, left alone",
				"member", a.Username, "grants", strings.Join(grants, "; "))
		}
	}
}

func sortedCodes(members map[string]*MemberRecord) []string {
	codes := make([]string, 0, len(members))
	for code := range members {
		codes = append(codes, code)
	}

	sort.Strings(codes)

	return codes
}

// SweepStatus is the sweep for the bot command and GET /status.
func (s *AccountsService) SweepStatus() string {
	cfg := s.cfg.Sweep
	if cfg.Interval <= 0 {
		return "off: sweep_interval is 0"
	}

	s.pending.Lock()
	last, full := s.lastSweep, s.lastFull
	s.pending.Unlock()

	now := s.cfg.Now()

	if last == nil {
		return "not run since the start; the first look is a few minutes after it"
	}

	line := fmt.Sprintf("last pass %s ago: %d checked (%d qualify, %d do not, %d cannot tell), %d turned off, %d deleted",
		since(now, last.At), last.Checked, last.Qualify, last.Fail, last.CannotTell, last.TurnedOff, last.Deleted)

	if last.Held > 0 {
		line += fmt.Sprintf(", %d held by sweep_max_changes", last.Held)
	}

	if last.Problem != "" {
		line += "; " + last.Problem
	}

	if !full.IsZero() {
		if next := full.Add(cfg.Interval); next.After(now) {
			line += "; next full sweep in " + since(next, now)
		} else {
			line += "; next full sweep within the hour"
		}
	}

	return line
}
