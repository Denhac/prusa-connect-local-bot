package domain

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is the account service (docs/design.md, "Accounts"): signup,
// password, whoami, link, suspend, unsuspend, lookup, admin and unadmin. One lock makes it
// the only writer of accounts. Each change is written to the journal before
// it reaches the server and finished on the next start if a crash cut it short.

// The service's limits.
const (
	actionsPerDay = 3
	linkFor       = 10 * time.Minute
	maxLinkTries  = 5
	confirmFor    = 10 * time.Minute
)

// AccountsConfig is what the service is built from.
type AccountsConfig struct {
	Verifier  Verifier
	Accounts  Accounts
	Access    Access
	Directory Directory
	// BotAccount is the bot's own username, which it never touches.
	BotAccount string
	// LinkURL is the module's /link page, to which ?for=<nonce> is added.
	LinkURL string
	Now     func() time.Time
	// Save writes the state; it is called with the lock held.
	Save func(MembersState) error
	Log  *slog.Logger
	// Sweep is how the membership sweep runs.
	Sweep SweepConfig
}

// AccountsService is the account half of the bot.
type AccountsService struct {
	cfg AccountsConfig
	log *slog.Logger

	mu sync.Mutex // the writer: held across every account change
	st MembersState

	pending sync.Mutex // guards the in-memory requests below, never across I/O
	asks    map[string]linkAsk
	codes   map[string]*linkCode
	tries   map[string]int
	confirm map[string]confirmation

	lastSweep *SweepResult // the last pass, for SweepStatus
	lastFull  time.Time    // when the last full pass began
}

type linkAsk struct {
	slack   string
	expires time.Time
}

type linkCode struct {
	slack, subject, username string
	expires                  time.Time
}

type confirmation struct {
	op, code, reason, admin string
	expires                 time.Time
}

// NewAccountsService picks up a saved state.
func NewAccountsService(cfg AccountsConfig, st MembersState) *AccountsService {
	if st.Members == nil {
		st.Members = map[string]*MemberRecord{}
	}

	if st.Suspensions == nil {
		st.Suspensions = map[string]*Suspension{}
	}

	if st.Admins == nil {
		st.Admins = map[string]*AdminLink{}
	}

	if st.Limits == nil {
		st.Limits = map[string][]time.Time{}
	}

	return &AccountsService{
		cfg: cfg, log: cfg.Log.With("component", "audit"), st: st,
		asks: map[string]linkAsk{}, codes: map[string]*linkCode{}, tries: map[string]int{},
		confirm:   map[string]confirmation{},
		lastSweep: st.Sweep.Result, lastFull: st.Sweep.Last,
	}
}

func (s *AccountsService) save() {
	if err := s.cfg.Save(s.st); err != nil {
		s.log.Error("could not save the member records", "error", err)
	}
}

// ----- helpers -------------------------------------------------------------

// passwordLength is how long a made password is: short enough to type from
// the window, and about 69 bits from the letters below, plenty for a server
// reached only on the space's own network.
const passwordLength = 12

// newPassword is passwordLength characters from crypto/rand, from letters and
// digits a person cannot mistake for one another.
func newPassword() (string, error) {
	const letters = "abcdefghijkmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ23456789"

	var b strings.Builder

	for range passwordLength {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", fmt.Errorf("make a password: %w", err)
		}

		b.WriteByte(letters[n.Int64()])
	}

	return b.String(), nil
}

func randomToken(n int) string {
	const letters = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	var b strings.Builder

	for range n {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			panic(err) // crypto/rand does not fail on a supported platform
		}

		b.WriteByte(letters[v.Int64()])
	}

	return b.String()
}

func find(accounts []Account, id string) *Account {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}

	return nil
}

func taken(accounts []Account) func(string) bool {
	return func(name string) bool {
		for _, a := range accounts {
			if strings.EqualFold(a.Username, name) {
				return true
			}
		}

		return false
	}
}

func (s *AccountsService) memberOf(slack string) (string, *MemberRecord) {
	for code, rec := range s.st.Members {
		if rec.Slack == slack {
			return code, rec
		}
	}

	return "", nil
}

// overLimit and count hold signups and resets to actionsPerDay per person.
func (s *AccountsService) overLimit(slack string) bool {
	now := s.cfg.Now()
	kept := s.st.Limits[slack][:0]

	for _, at := range s.st.Limits[slack] {
		if now.Sub(at) < 24*time.Hour {
			kept = append(kept, at)
		}
	}

	s.st.Limits[slack] = kept

	return len(kept) >= actionsPerDay
}

func (s *AccountsService) count(slack string) {
	s.st.Limits[slack] = append(s.st.Limits[slack], s.cfg.Now())
}

func (s *AccountsService) begin(e JournalEntry) {
	e.At = s.cfg.Now()
	s.st.Journal = append(s.st.Journal, &e)
	s.save()
}

func (s *AccountsService) end(op, code string) {
	kept := s.st.Journal[:0]

	for _, e := range s.st.Journal {
		if e.Op != op || e.Code != code {
			kept = append(kept, e)
		}
	}

	s.st.Journal = kept
}

// show updates a window, or, when it cannot, answers in a message. A secret
// never goes into that message: plain is what to say instead.
func show(ctx context.Context, ui UI, id string, m Modal, plain Message) {
	if id != "" && ui.UpdateModal(ctx, id, m) == nil {
		return
	}

	_ = ui.Respond(ctx, plain) // nothing else is left to tell the person
}

func plainOf(m Modal) Message { return Message{Lines: m.Lines, Fields: m.Fields} }

func window(title string, lines ...string) Modal { return Modal{Title: title, Lines: lines} }

// ----- signup --------------------------------------------------------------

type signupMeta struct {
	Code string `json:"code"`
	Old  string `json:"old,omitempty"`
}

// Signup makes a member's account.
func (s *AccountsService) Signup(ctx context.Context, r Request, ui UI) {
	id, _ := ui.OpenModal(ctx, window("Sign up", "Checking your membership…"))

	m, err := s.cfg.Verifier.Check(ctx, r.User)
	if err != nil {
		s.log.Warn("signup: cannot check membership", "slack", r.User, "error", err)
		m := window("Sign up", "The bot cannot check your membership right now. Try again later.")
		show(ctx, ui, id, m, plainOf(m))

		return
	}

	if !m.Qualifies {
		m := window("Sign up", m.Reason)
		show(ctx, ui, id, m, plainOf(m))

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if refusal, ok := s.refuse(m.Code, r.User); !ok {
		show(ctx, ui, id, refusal, plainOf(refusal))

		return
	}

	accounts, err := s.cfg.Accounts.List(ctx)
	if err != nil {
		s.log.Warn("signup: cannot list accounts", "error", err)
		m := window("Sign up", "The bot cannot reach the printer server right now. Try again later.")
		show(ctx, ui, id, m, plainOf(m))

		return
	}

	old := ""

	if rec := s.st.Members[m.Code]; rec != nil {
		acct := find(accounts, rec.AccountID)

		switch {
		case acct == nil:
			delete(s.st.Members, m.Code) // the account is gone; start again
		case acct.Role == RoleAdmin && !rec.Elevated.IsZero() && rec.Slack == r.User:
			w := window("Sign up", "You already have the account "+acct.Username+", and it is an admin account.")
			show(ctx, ui, id, w, plainOf(w))

			return
		case acct.Role == RoleAdmin:
			w := window("Sign up", "Your account "+acct.Username+" is an admin account, which is managed by hand.")
			show(ctx, ui, id, w, plainOf(w))

			return
		case rec.Slack == r.User && (rec.BasedOn == m.DisplayName || NormalizeUsername(m.DisplayName) == acct.Username):
			rec.BasedOn = m.DisplayName // a change that gives the same username needs no new account
			w := s.again(ctx, rec, acct)
			show(ctx, ui, id, w, plainOf(w))

			return
		case rec.Slack != r.User:
			// Member codes are unique to one person, so a second Slack user
			// carrying one is a fault in the member data. Touch nothing.
			s.log.Warn("signup: a member code already belongs to another Slack user; refused",
				"code", m.Code, "slack", r.User, "owner", rec.Slack)

			w := window("Sign up", "Your member code already belongs to another Slack user. Ask an admin to look into it.")
			show(ctx, ui, id, w, plainOf(w))

			return
		default:
			meta, _ := json.Marshal(signupMeta{Code: m.Code, Old: acct.ID})
			w := Modal{
				Title: "Sign up",
				Lines: []string{
					"Your denhac name is now " + m.DisplayName + ".",
					"Replace your account " + acct.Username + " with a new one under that name? " +
						"The old account and its sign-ins end, and prints it made stay recorded under the old name.",
				},
				Callback: "signup-replace", Submit: "Replace", Meta: string(meta),
			}
			show(ctx, ui, id, w, Message{Lines: []string{"Run " + SlashCommand + " signup again to replace your account."}})

			return
		}
	}

	s.choose(ctx, ui, id, r, m, old, accounts, "")
}

// again answers a member who already has an account.
func (s *AccountsService) again(ctx context.Context, rec *MemberRecord, acct *Account) Modal {
	if acct.Role == RoleOperator {
		return window("Sign up", "You already have the account "+acct.Username+".",
			"Use "+SlashCommand+" password to reset its password.")
	}

	if err := s.cfg.Accounts.SetRole(ctx, acct.ID, RoleOperator); err != nil {
		s.log.Warn("signup: could not turn an account back on", "account", acct.Username, "error", err)

		return window("Sign up", "The bot could not turn your account back on. Try again later.")
	}

	rec.turnedOn(s.cfg.Now())
	s.save()
	s.log.Info("account turned back on", "account", acct.Username, "slack", rec.Slack)

	return window("Sign up", "Your account "+acct.Username+" is on again. Sign in with your old password.",
		"If you have lost it, "+SlashCommand+" password sets a new one.")
}

// choose picks the username, asking the person when the obvious one is taken.
func (s *AccountsService) choose(ctx context.Context, ui UI, id string, r Request, m Membership, old string, accounts []Account, typed string) {
	name := typed
	if name == "" {
		name = NormalizeUsername(m.DisplayName)
		if name == "" && s.cfg.Directory != nil {
			if slackName, err := s.cfg.Directory.Name(ctx, r.User); err == nil {
				name = NormalizeUsername(slackName)
			}
		}
	}

	isTaken := taken(accounts)

	problem := ""

	switch {
	case typed != "" && !ValidUsername(typed):
		problem = "\"" + typed + "\" is not a username: use lower-case letters, digits, '.', '_' and '-', at most 32."
	case name == "":
		problem = "Choose a username."
	case Reserved(name, s.cfg.BotAccount):
		problem = "\"" + name + "\" is reserved."
	case isTaken(name):
		problem = "\"" + name + "\" is taken."
	}

	if problem == "" {
		s.create(ctx, ui, id, r, m, old, name)

		return
	}

	suggest := ""
	if name != "" && ValidUsername(name) {
		suggest = NextFree(name, isTaken)
	}

	meta, _ := json.Marshal(signupMeta{Code: m.Code, Old: old})
	w := Modal{
		Title:    "Sign up",
		Lines:    []string{problem, "Take the suggestion below, or type a username of your own."},
		Input:    &Input{ID: "username", Label: "Username", Initial: suggest, Hint: "Lower-case letters, digits, '.', '_' and '-'."},
		Callback: "signup-name", Submit: "Create", Meta: string(meta),
	}
	show(ctx, ui, id, w, Message{Lines: []string{problem + " Run " + SlashCommand + " signup again to choose another."}})
}

// create makes the account, then shows its password once.
func (s *AccountsService) create(ctx context.Context, ui UI, id string, r Request, m Membership, old, username string) {
	password, err := newPassword()
	if err != nil {
		w := window("Sign up", "The bot could not make a password. Try again.")
		show(ctx, ui, id, w, plainOf(w))

		return
	}

	s.begin(JournalEntry{Op: opSignup, Code: m.Code, Slack: r.User, Username: username, OldAccountID: old})

	acct, err := s.cfg.Accounts.Create(ctx, username, m.DisplayName, password)
	if err != nil {
		s.end(opSignup, m.Code)
		s.save()

		if errors.Is(err, ErrNameTaken) {
			accounts, listErr := s.cfg.Accounts.List(ctx)
			if listErr == nil {
				s.choose(ctx, ui, id, r, m, old, accounts, username)

				return
			}
		}

		s.log.Warn("signup: create failed", "username", username, "error", err)
		w := window("Sign up", "The printer server refused the new account. Try again later.")
		show(ctx, ui, id, w, plainOf(w))

		return
	}

	s.st.Members[m.Code] = &MemberRecord{
		Slack: r.User, AccountID: acct.ID, Username: acct.Username, Via: "created",
		BasedOn: m.DisplayName, Since: s.cfg.Now(), Qualified: s.cfg.Now(),
	}
	s.count(r.User)
	s.save()

	if old != "" {
		if err := s.cfg.Accounts.Delete(ctx, old); err != nil {
			s.log.Warn("signup: the replaced account is still there; the journal keeps trying", "account", old, "error", err)
			s.save()
		} else {
			s.end(opSignup, m.Code)
		}
	} else {
		s.end(opSignup, m.Code)
	}

	s.save()
	s.log.Info("account created", "account", acct.Username, "slack", r.User, "replaced", old != "")

	w := Modal{
		Title: "Your account",
		Lines: []string{
			"Your account on the printer server is ready. Copy the password now: it is not shown again.",
		},
		Fields: []Field{{Label: "Username", Value: acct.Username}, {Label: "Password", Value: password}},
	}
	show(ctx, ui, id, w, Message{Lines: []string{
		"Your account " + acct.Username + " is ready, but the window closed before the password could be shown.",
		"Run " + SlashCommand + " password to get a new one.",
	}})
}

// refuse answers a suspended member code, or a person over the limit.
func (s *AccountsService) refuse(code, slack string) (Modal, bool) {
	if _, suspended := s.st.Suspensions[code]; suspended {
		return window("Sign up", "Your account is suspended. Talk to an admin."), false
	}

	if s.overLimit(slack) {
		return window("Sign up", fmt.Sprintf("You have signed up or reset a password %d times today. Try again tomorrow.", actionsPerDay)), false
	}

	return Modal{}, true
}

// SignupSubmit is the window's answer: a typed username, or a replacement
// confirmed.
func (s *AccountsService) SignupSubmit(ctx context.Context, it Interaction, ui UI) {
	var meta signupMeta
	if json.Unmarshal([]byte(it.Meta), &meta) != nil || meta.Code == "" {
		return
	}

	m, err := s.cfg.Verifier.Check(ctx, it.User)
	if err != nil || !m.Qualifies || m.Code != meta.Code {
		w := window("Sign up", "The bot cannot confirm your membership right now. Try again later.")
		show(ctx, ui, it.ModalID, w, plainOf(w))

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if refusal, ok := s.refuse(m.Code, it.User); !ok {
		show(ctx, ui, it.ModalID, refusal, plainOf(refusal))

		return
	}

	accounts, err := s.cfg.Accounts.List(ctx)
	if err != nil {
		w := window("Sign up", "The bot cannot reach the printer server right now. Try again later.")
		show(ctx, ui, it.ModalID, w, plainOf(w))

		return
	}

	typed := strings.TrimSpace(it.Inputs["username"])
	if it.Callback == "signup-replace" {
		typed = ""
	}

	s.choose(ctx, ui, it.ModalID, Request{User: it.User}, m, meta.Old, accounts, typed)
}

// ----- password ------------------------------------------------------------

// Password sets a new password on the person's own account.
func (s *AccountsService) Password(ctx context.Context, r Request, ui UI) {
	id, _ := ui.OpenModal(ctx, window("New password", "Checking your membership…"))

	m, err := s.cfg.Verifier.Check(ctx, r.User)
	if err != nil {
		w := window("New password", "The bot cannot check your membership right now. Try again later.")
		show(ctx, ui, id, w, plainOf(w))

		return
	}

	if !m.Qualifies {
		w := window("New password", m.Reason)
		show(ctx, ui, id, w, plainOf(w))

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.st.Members[m.Code]
	if rec == nil || rec.Slack != r.User {
		w := window("New password", "You have no account the bot looks after.",
			SlashCommand+" signup makes one, and "+SlashCommand+" link claims one you already have.")
		show(ctx, ui, id, w, plainOf(w))

		return
	}

	if refusal, ok := s.refuse(m.Code, r.User); !ok {
		refusal.Title = "New password"
		show(ctx, ui, id, refusal, plainOf(refusal))

		return
	}

	accounts, err := s.cfg.Accounts.List(ctx)
	acct := find(accounts, rec.AccountID)

	switch {
	case err != nil:
		w := window("New password", "The bot cannot reach the printer server right now. Try again later.")
		show(ctx, ui, id, w, plainOf(w))

		return
	case acct == nil:
		w := window("New password", "Your account is gone from the printer server. "+SlashCommand+" signup makes a new one.")
		show(ctx, ui, id, w, plainOf(w))

		return
	case acct.Role == RoleAdmin:
		w := window("New password", "Your account is an admin account, and the bot never sets an admin's password.",
			"Change it in the printer server's web app, signed in as "+acct.Username+".")
		show(ctx, ui, id, w, plainOf(w))

		return
	}

	if acct.Role != RoleOperator {
		if err := s.cfg.Accounts.SetRole(ctx, acct.ID, RoleOperator); err != nil {
			w := window("New password", "The bot could not turn your account back on. Try again later.")
			show(ctx, ui, id, w, plainOf(w))

			return
		}

		rec.turnedOn(s.cfg.Now())
	}

	password, err := newPassword()
	if err == nil {
		err = s.cfg.Accounts.SetPassword(ctx, acct.ID, password)
	}

	if err != nil {
		s.log.Warn("password: reset failed", "account", acct.Username, "error", err)
		w := window("New password", "The printer server refused the new password. Try again later.")
		show(ctx, ui, id, w, plainOf(w))

		return
	}

	s.count(r.User)
	s.save()
	s.log.Info("password reset", "account", acct.Username, "slack", r.User)

	w := Modal{
		Title: "New password",
		Lines: []string{
			"Copy the password now: it is not shown again.",
			"Every sign-in, token and app pairing of the account has ended.",
		},
		Fields: []Field{{Label: "Username", Value: acct.Username}, {Label: "Password", Value: password}},
	}
	show(ctx, ui, id, w, Message{Lines: []string{
		"Your password was reset, but the window closed before it could be shown.",
		"Run " + SlashCommand + " password again for another.",
	}})
}

// ----- whoami --------------------------------------------------------------

// WhoAmI says what the bot knows about the person.
func (s *AccountsService) WhoAmI(ctx context.Context, r Request) Message {
	msg := Message{Title: "You"}

	m, err := s.cfg.Verifier.Check(ctx, r.User)

	switch {
	case err != nil:
		msg.Fields = append(msg.Fields, Field{Label: "Membership", Value: "cannot be checked right now"})
	case m.Qualifies:
		msg.Fields = append(msg.Fields, Field{Label: "Membership", Value: "you qualify for an account"})
	default:
		msg.Fields = append(msg.Fields, Field{Label: "Membership", Value: m.Reason})
	}

	s.mu.Lock()
	code, rec := s.memberOf(r.User)
	_, suspended := s.st.Suspensions[code]
	admin := s.st.Admins[r.User]
	s.mu.Unlock()

	switch {
	case rec == nil:
		msg.Fields = append(msg.Fields, Field{Label: "Account", Value: "none that the bot looks after"})
	default:
		state := "on"

		switch {
		case suspended:
			state = "suspended by an admin"
		case !rec.Lapsed.IsZero():
			state = "off since " + clock(rec.Lapsed, s.cfg.Now()) + ": membership lapsed"
		case !rec.Elevated.IsZero():
			state = "on, an admin"
		}

		msg.Fields = append(msg.Fields, Field{Label: "Account", Value: rec.Username + " (" + state + ")"})
	}

	if admin != nil {
		msg.Fields = append(msg.Fields, Field{Label: "Admin", Value: "linked to " + admin.Username})
	}

	return msg
}

// ----- link ----------------------------------------------------------------

// Link starts a link, or with a code finishes one.
func (s *AccountsService) Link(ctx context.Context, r Request, args []string, ui UI) Message {
	if len(args) == 0 {
		if s.cfg.LinkURL == "" {
			return say("Linking is off: the module's public_url is not set.")
		}

		nonce := randomToken(24)

		s.pending.Lock()
		s.asks[nonce] = linkAsk{slack: r.User, expires: s.cfg.Now().Add(linkFor)}
		s.pending.Unlock()

		err := ui.DM(ctx, Message{
			Lines: []string{
				"To link your Slack user to your account on the printer server, open this address in a browser where you are signed in to the printer server. It shows a code.",
				"Then type " + SlashCommand + " link <code> here within 10 minutes.",
			},
			Link: s.cfg.LinkURL + "?for=" + nonce,
		})
		if err != nil {
			return say("The bot could not send you a direct message.")
		}

		return say("I sent you a direct message with the link.")
	}

	code := strings.ToUpper(strings.ReplaceAll(strings.Join(args, ""), "-", ""))

	s.pending.Lock()
	defer s.pending.Unlock()

	if s.tries[r.User] >= maxLinkTries {
		return say("Too many wrong codes. Start again with " + SlashCommand + " link.")
	}

	lc, ok := s.codes[code]
	if !ok || lc.slack != r.User || s.cfg.Now().After(lc.expires) {
		s.tries[r.User]++

		return say("That code is not right, or it has expired.")
	}

	token := randomToken(16)
	s.confirm[token] = confirmation{op: "link", code: code, admin: r.User, expires: s.cfg.Now().Add(confirmFor)}

	return Message{
		Lines:   []string{"Link your Slack user to the printer-server account " + lc.username + "?"},
		Buttons: []Button{{Action: "link-confirm", Value: token, Label: "Link"}},
	}
}

// LinkPage answers the module's /link route for a signed-in person. It
// answers the HTTP status and the text of the page.
func (s *AccountsService) LinkPage(ctx context.Context, nonce, principal string) (int, string) {
	if !strings.HasPrefix(principal, "user:") {
		return 403, "Sign in to the printer server as yourself, then open this address again."
	}

	s.pending.Lock()
	ask, ok := s.asks[nonce]
	delete(s.asks, nonce)
	s.pending.Unlock()

	if !ok || s.cfg.Now().After(ask.expires) {
		return 404, "This address has expired or was used already. In Slack, type " + SlashCommand + " link for a new one."
	}

	username := strings.TrimPrefix(principal, "user:")

	if accounts, err := s.cfg.Accounts.List(context.WithoutCancel(ctx)); err == nil {
		if a := find(accounts, strings.TrimPrefix(principal, "user:")); a != nil {
			username = a.Username
		}
	}

	slackName := ask.slack
	if s.cfg.Directory != nil {
		if n, err := s.cfg.Directory.Name(ctx, ask.slack); err == nil && n != "" {
			slackName = n
		}
	}

	code := randomToken(8)

	s.pending.Lock()
	s.codes[code] = &linkCode{slack: ask.slack, subject: principal, username: username, expires: s.cfg.Now().Add(linkFor)}
	s.pending.Unlock()

	return 200, fmt.Sprintf("Your code: %s-%s\n\n"+
		"It links the Slack user @%s to your printer-server account %s.\n"+
		"In Slack, type %s link %s-%s within 10 minutes.\n\n"+
		"Only do this if you are @%s.\n", code[:4], code[4:], slackName, username, SlashCommand, code[:4], code[4:], slackName)
}

// LinkConfirm is the click on "Link".
func (s *AccountsService) LinkConfirm(ctx context.Context, it Interaction, ui UI) {
	s.pending.Lock()
	c, ok := s.confirm[it.Value]
	delete(s.confirm, it.Value)

	var lc *linkCode
	if ok && c.admin == it.User && !s.cfg.Now().After(c.expires) {
		lc = s.codes[c.code]
		delete(s.codes, c.code)
	}
	s.pending.Unlock()

	if lc == nil || lc.slack != it.User {
		_ = ui.Respond(ctx, say("That link has expired. Start again with "+SlashCommand+" link."))

		return
	}

	_ = ui.Respond(ctx, s.link(ctx, it.User, lc))
}

func (s *AccountsService) link(ctx context.Context, slack string, lc *linkCode) Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	accounts, err := s.cfg.Accounts.List(ctx)
	if err != nil {
		return say("The bot cannot reach the printer server right now. Try again later.")
	}

	acct := find(accounts, strings.TrimPrefix(lc.subject, "user:"))
	if acct == nil {
		return say("That account is gone from the printer server.")
	}

	if acct.Role == RoleAdmin {
		s.st.Admins[slack] = &AdminLink{Subject: lc.subject, Username: acct.Username, Linked: s.cfg.Now()}
		s.save()
		s.log.Info("admin linked", "slack", slack, "account", acct.Username)

		return say("Linked to the admin account " + acct.Username + ". The admin commands now work for you.")
	}

	m, err := s.cfg.Verifier.Check(ctx, slack)

	switch {
	case err != nil:
		return say("The bot cannot check your membership right now. Try again later.")
	case !m.Qualifies:
		return say(m.Reason)
	}

	if _, suspended := s.st.Suspensions[m.Code]; suspended {
		return say("Your account is suspended. Talk to an admin.")
	}

	for code, rec := range s.st.Members {
		if rec.AccountID == acct.ID && code != m.Code {
			return say("That account belongs to another member.")
		}
	}

	if rec := s.st.Members[m.Code]; rec != nil && rec.AccountID != acct.ID {
		return say("Your member code already has the account " + rec.Username + ".")
	}

	s.begin(JournalEntry{Op: opClaim, Code: m.Code, Slack: slack, AccountID: acct.ID})
	s.st.Members[m.Code] = &MemberRecord{
		Slack: slack, AccountID: acct.ID, Username: acct.Username, Via: "claimed",
		BasedOn: m.DisplayName, Since: s.cfg.Now(), Qualified: s.cfg.Now(),
	}
	s.save()

	if err := s.cfg.Accounts.SetRole(ctx, acct.ID, RoleOperator); err != nil {
		s.log.Warn("claim: could not set the role; the journal keeps trying", "account", acct.Username, "error", err)

		return say("Linked, but the bot could not turn the account on yet. It will try again.")
	}

	s.end(opClaim, m.Code)
	s.save()
	s.log.Info("account claimed", "slack", slack, "account", acct.Username)

	return say("Linked. The bot now looks after your account " + acct.Username + ".")
}

// ----- admins --------------------------------------------------------------

// admin answers whether the person is a linked admin the server confirms.
func (s *AccountsService) admin(ctx context.Context, slack string) (Message, bool) {
	s.mu.Lock()
	link := s.st.Admins[slack]
	s.mu.Unlock()

	if link == nil {
		return say("Admin commands need your admin account linked first: " + SlashCommand + " link."), false
	}

	ok, err := s.cfg.Access.IsServerAdmin(ctx, link.Subject)

	switch {
	case err != nil:
		return say("The bot cannot check your admin rights right now. Try again later."), false
	case !ok:
		return say("Your linked account " + link.Username + " is not an admin on the printer server."), false
	}

	return Message{}, true
}

var mention = regexp.MustCompile(`^<@([A-Z0-9]+)(\|[^>]*)?>$`)

// resolve finds the member code a person named: a Slack mention, a username
// or a member code.
func (s *AccountsService) resolve(who string) (string, bool) {
	if m := mention.FindStringSubmatch(who); m != nil {
		code, rec := s.memberOf(m[1])

		return code, rec != nil
	}

	for code, rec := range s.st.Members {
		if strings.EqualFold(rec.Username, who) {
			return code, true
		}
	}

	upper := strings.ToUpper(who)
	if _, ok := s.st.Members[upper]; ok {
		return upper, true
	}

	if _, ok := s.st.Suspensions[upper]; ok {
		return upper, true
	}

	return "", false
}

// Suspend asks an admin to confirm a suspension, or lists them.
func (s *AccountsService) Suspend(ctx context.Context, r Request, args []string) Message {
	if refusal, ok := s.admin(ctx, r.User); !ok {
		return refusal
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(args) == 0 {
		return s.suspensions()
	}

	code, ok := s.resolve(args[0])
	if !ok {
		return say("No member matches " + args[0] + ".")
	}

	name := code
	if rec := s.st.Members[code]; rec != nil {
		name = rec.Username
	}

	token := randomToken(16)

	s.pending.Lock()
	s.confirm[token] = confirmation{op: "suspend", code: code, reason: strings.Join(args[1:], " "), admin: r.User, expires: s.cfg.Now().Add(confirmFor)}
	s.pending.Unlock()

	return Message{
		Lines: []string{
			"Suspend " + name + "? The account turns viewer and stays so while their membership is active, until an admin lifts it.",
		},
		Buttons: []Button{{Action: "suspend-confirm", Value: token, Label: "Suspend", Danger: true}},
	}
}

func (s *AccountsService) suspensions() Message {
	if len(s.st.Suspensions) == 0 {
		return say("Nobody is suspended.")
	}

	codes := make([]string, 0, len(s.st.Suspensions))
	for code := range s.st.Suspensions {
		codes = append(codes, code)
	}

	sort.Strings(codes)

	msg := Message{Title: "Suspended"}

	for _, code := range codes {
		sus := s.st.Suspensions[code]
		name := code

		if rec := s.st.Members[code]; rec != nil {
			name = rec.Username
		}

		line := "since " + clock(sus.At, s.cfg.Now())
		if sus.Reason != "" {
			line += ": " + sus.Reason
		}

		msg.Fields = append(msg.Fields, Field{Label: name, Value: line})
	}

	return msg
}

// SuspendConfirm is the click on "Suspend".
func (s *AccountsService) SuspendConfirm(ctx context.Context, it Interaction, ui UI) {
	c, ok := s.takeConfirm(it, "suspend")
	if !ok {
		_ = ui.Respond(ctx, say("That request has expired. Run it again."))

		return
	}

	if refusal, ok := s.admin(ctx, it.User); !ok {
		_ = ui.Respond(ctx, refusal)

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.begin(JournalEntry{Op: opSuspend, Code: c.code, Slack: it.User})
	s.st.Suspensions[c.code] = &Suspension{By: it.User, Reason: c.reason, At: s.cfg.Now()}
	s.save()

	name := c.code

	if rec := s.st.Members[c.code]; rec != nil {
		name = rec.Username

		if err := s.setViewer(ctx, rec); err != nil && !errors.Is(err, ErrAdminAccount) {
			s.log.Warn("suspend: could not set viewer; the journal keeps trying", "account", rec.Username, "error", err)
			_ = ui.Respond(ctx, say("Suspended "+name+", but the bot could not change the account yet. It will try again."))

			return
		}
	}

	s.end(opSuspend, c.code)
	s.save()
	s.log.Info("suspended", "by", it.User, "member", name, "reason", c.reason)
	_ = ui.Respond(ctx, say("Suspended "+name+"."))
}

// Unsuspend asks an admin to confirm lifting a suspension.
func (s *AccountsService) Unsuspend(ctx context.Context, r Request, args []string) Message {
	if refusal, ok := s.admin(ctx, r.User); !ok {
		return refusal
	}

	if len(args) == 0 {
		return say("Unsuspend whom? " + SlashCommand + " unsuspend <@user|username|member code>")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	code, ok := s.resolve(args[0])
	if _, suspended := s.st.Suspensions[code]; !ok || !suspended {
		return say(args[0] + " is not suspended.")
	}

	token := randomToken(16)

	s.pending.Lock()
	s.confirm[token] = confirmation{op: "unsuspend", code: code, admin: r.User, expires: s.cfg.Now().Add(confirmFor)}
	s.pending.Unlock()

	return Message{
		Lines:   []string{"Lift the suspension of " + args[0] + "?"},
		Buttons: []Button{{Action: "unsuspend-confirm", Value: token, Label: "Lift it"}},
	}
}

// UnsuspendConfirm is the click on "Lift it".
func (s *AccountsService) UnsuspendConfirm(ctx context.Context, it Interaction, ui UI) {
	c, ok := s.takeConfirm(it, "unsuspend")
	if !ok {
		_ = ui.Respond(ctx, say("That request has expired. Run it again."))

		return
	}

	if refusal, ok := s.admin(ctx, it.User); !ok {
		_ = ui.Respond(ctx, refusal)

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.st.Members[c.code]
	if rec == nil {
		delete(s.st.Suspensions, c.code)
		s.save()
		s.log.Info("suspension lifted", "by", it.User, "member", c.code)
		_ = ui.Respond(ctx, say("Lifted. That member has no account now; they can sign up again."))

		return
	}

	m, err := s.cfg.Verifier.CheckCode(ctx, c.code)
	if err == nil && m.Qualifies {
		// The suspension goes only once the account is on again, so that an
		// account left a viewer is never taken for one demoted by hand.
		if err := s.cfg.Accounts.SetRole(ctx, rec.AccountID, RoleOperator); err != nil {
			s.log.Warn("unsuspend: could not set operator; the suspension stays", "account", rec.Username, "error", err)
			_ = ui.Respond(ctx, say("The bot could not turn "+rec.Username+" back on, so the suspension stays. Try again later."))

			return
		}

		delete(s.st.Suspensions, c.code)
		rec.turnedOn(s.cfg.Now())
		s.save()
		s.log.Info("suspension lifted", "by", it.User, "member", rec.Username)
		_ = ui.Respond(ctx, say("Lifted. "+rec.Username+" is an operator again."))

		return
	}

	delete(s.st.Suspensions, c.code)

	if rec.Lapsed.IsZero() {
		rec.Lapsed = s.cfg.Now()
	}

	s.save()

	why := "their membership does not qualify now"
	if err != nil {
		why = "the bot cannot check their membership right now. " + SlashCommand + " signup turns it on once they qualify"
	}

	s.log.Info("suspension lifted; the account stays a viewer", "by", it.User, "member", rec.Username, "why", why)
	_ = ui.Respond(ctx, say("Lifted. "+rec.Username+" stays a viewer: "+why+"."))
}

func (s *AccountsService) takeConfirm(it Interaction, op string) (confirmation, bool) {
	s.pending.Lock()
	defer s.pending.Unlock()

	c, ok := s.confirm[it.Value]
	delete(s.confirm, it.Value)

	return c, ok && c.op == op && c.admin == it.User && !s.cfg.Now().After(c.expires)
}

// Lookup shows what the bot knows about a member, to an admin.
func (s *AccountsService) Lookup(ctx context.Context, r Request, args []string) Message {
	if refusal, ok := s.admin(ctx, r.User); !ok {
		return refusal
	}

	if len(args) == 0 {
		return say("Look up whom? " + SlashCommand + " lookup <@user|username|member code>")
	}

	who := args[0]
	s.log.Info("lookup", "by", r.User, "for", who)

	accounts, listErr := s.cfg.Accounts.List(ctx)

	s.mu.Lock()
	code, ok := s.resolve(who)
	rec := s.st.Members[code]
	sus := s.st.Suspensions[code]
	s.mu.Unlock()

	if !ok {
		if listErr == nil {
			for _, a := range accounts {
				if strings.EqualFold(a.Username, who) {
					kind := "an account the bot does not look after"
					if a.Role == RoleAdmin {
						kind = "an admin account, managed by hand"
					}

					return say(a.Username + " is " + kind + ".")
				}
			}
		}

		return say("No member matches " + who + ".")
	}

	msg := Message{Title: "Member"}
	msg.Fields = append(msg.Fields, Field{Label: "Member code", Value: code})

	if rec != nil {
		slackName := rec.Slack
		if s.cfg.Directory != nil {
			if n, err := s.cfg.Directory.Name(ctx, rec.Slack); err == nil && n != "" {
				slackName = n + " (" + rec.Slack + ")"
			}
		}

		account := rec.Username
		if a := find(accounts, rec.AccountID); a != nil {
			account = a.Username + ", " + a.Role
			if a.DisplayName != "" {
				account += ", shown as " + a.DisplayName
			}
		} else if listErr == nil {
			account += " (gone from the server)"
		}

		now := s.cfg.Now()

		state := "on"
		if !rec.Lapsed.IsZero() {
			state = "lapsed since " + clock(rec.Lapsed, now)

			if after := s.cfg.Sweep.DeleteAfter; after > 0 {
				from := rec.Lapsed
				if rec.Qualified.After(from) {
					from = rec.Qualified
				}

				state += "; deleted from " + from.Add(after).Local().Format("2 Jan 2006") + " if they do not qualify again"
			}
		}

		check := "not yet checked by the sweep"
		if !rec.Checked.IsZero() {
			check = clock(rec.Checked, now) + ": " + rec.Check
			if len(rec.FailedChecks) > 0 {
				check += " (failing since " + clock(rec.FailedChecks[0], now) + ")"
			}
		}

		msg.Fields = append(msg.Fields,
			Field{Label: "Slack user", Value: slackName},
			Field{Label: "Account", Value: account},
			Field{Label: "State", Value: state},
			Field{Label: "Arrived", Value: rec.Via + " " + clock(rec.Since, now)},
			Field{Label: "Based on", Value: rec.BasedOn},
			Field{Label: "Last check", Value: check},
		)

		if !rec.Elevated.IsZero() {
			msg.Fields = append(msg.Fields, Field{Label: "Admin", Value: "made an admin by <@" + rec.ElevatedBy + "> " + clock(rec.Elevated, now)})
		}
	}

	if sus != nil {
		line := "by " + sus.By + " " + clock(sus.At, s.cfg.Now())
		if sus.Reason != "" {
			line += ": " + sus.Reason
		}

		msg.Fields = append(msg.Fields, Field{Label: "Suspended", Value: line})
	}

	return msg
}

// ----- recovery ------------------------------------------------------------

// Recover finishes the journal's entries after a start.
func (s *AccountsService) Recover(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.st.Journal) == 0 {
		return
	}

	accounts, err := s.cfg.Accounts.List(ctx)
	if err != nil {
		s.log.Warn("recover: cannot list accounts; the sweep tries again", "error", err)

		return
	}

	s.recoverLocked(ctx, accounts)
}

// recoverLocked finishes the journal's entries, with the lock held. The sweep
// calls it too, so an entry a failed call left open is tried again then.
func (s *AccountsService) recoverLocked(ctx context.Context, accounts []Account) {
	if len(s.st.Journal) == 0 {
		return
	}

	entries := append([]*JournalEntry(nil), s.st.Journal...)

	for _, e := range entries {
		switch e.Op {
		case opSignup:
			s.recoverSignup(ctx, e, accounts)
		case opSuspend:
			if rec := s.st.Members[e.Code]; rec != nil {
				_ = s.setViewer(ctx, rec) // an admin account made by hand refuses; nothing to do then
			}
		case opElevate:
			s.recoverElevate(e, accounts)
		case opClaim:
			if rec := s.st.Members[e.Code]; rec != nil {
				if _, suspended := s.st.Suspensions[e.Code]; !suspended {
					_ = s.cfg.Accounts.SetRole(ctx, rec.AccountID, RoleOperator)
				}
			}
		}

		s.end(e.Op, e.Code)
	}

	s.save()
}

// recoverSignup adopts the account a signup made before a crash, and never
// one that merely has the same name.
func (s *AccountsService) recoverSignup(ctx context.Context, e *JournalEntry, accounts []Account) {
	if rec := s.st.Members[e.Code]; rec != nil && rec.Username == e.Username {
		// The record was written; only the old account may be left.
		if e.OldAccountID != "" && find(accounts, e.OldAccountID) != nil {
			_ = s.cfg.Accounts.Delete(ctx, e.OldAccountID)
		}

		return
	}

	for _, a := range accounts {
		if a.Username != e.Username || a.Role != RoleOperator || a.Created.Before(e.At.Add(-time.Minute)) {
			continue
		}

		for _, rec := range s.st.Members {
			if rec.AccountID == a.ID {
				return
			}
		}

		s.st.Members[e.Code] = &MemberRecord{
			Slack: e.Slack, AccountID: a.ID, Username: a.Username, Via: "created", Since: a.Created,
		}

		if e.OldAccountID != "" {
			_ = s.cfg.Accounts.Delete(ctx, e.OldAccountID)
		}

		s.log.Info("recover: a signup cut short was finished; the member runs password for a password", "account", a.Username)

		return
	}

	s.log.Warn("recover: a signup cut short left no account to adopt", "username", e.Username)
}

// recoverElevate finishes an admin command cut short: an account the server
// now holds as admin is marked as made admin by the bot. One still an operator
// was never changed.
func (s *AccountsService) recoverElevate(e *JournalEntry, accounts []Account) {
	rec := s.st.Members[e.Code]
	if rec == nil || rec.AccountID != e.AccountID {
		return
	}

	if acct := find(accounts, e.AccountID); acct != nil && acct.Role == RoleAdmin && rec.Elevated.IsZero() {
		s.elevated(rec, acct, e.Slack)
		s.log.Info("recover: an admin command cut short was finished", "member", rec.Username, "by", e.Slack)
	}
}

// ----- admin and unadmin ---------------------------------------------------

// setViewer turns a member's account off. One the bot made admin loses admin
// through Unadmin and is no longer elevated; turning it on again makes it an
// operator, and only the admin command makes it an admin again.
func (s *AccountsService) setViewer(ctx context.Context, rec *MemberRecord) error {
	if rec.Elevated.IsZero() {
		return s.cfg.Accounts.SetRole(ctx, rec.AccountID, RoleViewer) //nolint:wrapcheck // the port's own error.
	}

	if err := s.cfg.Accounts.Unadmin(ctx, rec.AccountID, RoleViewer); err != nil {
		return err //nolint:wrapcheck // the port's own error.
	}

	s.unelevated(rec)

	return nil
}

// elevated marks a record as made admin by the bot, and links its Slack user
// as an admin unless they are linked to an admin account already.
func (s *AccountsService) elevated(rec *MemberRecord, acct *Account, by string) {
	rec.Elevated, rec.ElevatedBy = s.cfg.Now(), by

	if s.st.Admins[rec.Slack] == nil {
		s.st.Admins[rec.Slack] = &AdminLink{Subject: acct.Subject(), Username: acct.Username, Linked: s.cfg.Now()}
	}
}

// unelevated ends what elevated began, and the admin link only if it is to
// this account.
func (s *AccountsService) unelevated(rec *MemberRecord) {
	rec.Elevated, rec.ElevatedBy = time.Time{}, ""

	if l := s.st.Admins[rec.Slack]; l != nil && l.Subject == "user:"+rec.AccountID {
		delete(s.st.Admins, rec.Slack)
	}
}

// MakeAdmin asks an admin to confirm making a member's account an admin.
func (s *AccountsService) MakeAdmin(ctx context.Context, r Request, args []string) Message {
	if refusal, ok := s.admin(ctx, r.User); !ok {
		return refusal
	}

	if len(args) == 0 {
		return say("Make whom an admin? " + SlashCommand + " admin <@user|username|member code>")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	code, _ := s.resolve(args[0])

	rec := s.st.Members[code]
	if refusal, ok := s.mayElevate(args[0], code, rec); !ok {
		return refusal
	}

	token := randomToken(16)

	s.pending.Lock()
	s.confirm[token] = confirmation{op: "admin", code: code, admin: r.User, expires: s.cfg.Now().Add(confirmFor)}
	s.pending.Unlock()

	return Message{
		Lines: []string{
			"Make " + rec.Username + " an admin? An admin has full control of the printer server: every account, " +
				"printer and setting. " + SlashCommand + " unadmin takes it back.",
		},
		Buttons: []Button{{Action: "admin-confirm", Value: token, Label: "Make admin", Danger: true}},
	}
}

// mayElevate refuses a member whose account the admin command cannot take.
func (s *AccountsService) mayElevate(who, code string, rec *MemberRecord) (Message, bool) {
	_, suspended := s.st.Suspensions[code]

	switch {
	case rec == nil:
		return say(who + " has no account the bot looks after. They run " + SlashCommand + " signup first, or " +
			SlashCommand + " link to claim one."), false
	case suspended:
		return say(rec.Username + " is suspended. Lift the suspension first."), false
	case !rec.Lapsed.IsZero():
		return say(rec.Username + " is off: their membership lapsed."), false
	case !rec.Elevated.IsZero():
		return say(rec.Username + " is an admin already. " + SlashCommand + " unadmin takes it back."), false
	}

	return Message{}, true
}

// MakeAdminConfirm is the click on "Make admin".
func (s *AccountsService) MakeAdminConfirm(ctx context.Context, it Interaction, ui UI) {
	c, ok := s.takeConfirm(it, "admin")
	if !ok {
		_ = ui.Respond(ctx, say("That request has expired. Run it again."))

		return
	}

	if refusal, ok := s.admin(ctx, it.User); !ok {
		_ = ui.Respond(ctx, refusal)

		return
	}

	m, err := s.cfg.Verifier.CheckCode(ctx, c.code)

	switch {
	case err != nil:
		_ = ui.Respond(ctx, say("The bot cannot check that membership right now, so it made nobody an admin. Try again later."))

		return
	case !m.Qualifies:
		_ = ui.Respond(ctx, say("That member does not qualify now, so the bot made nobody an admin. "+m.Reason))

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.st.Members[c.code]
	if refusal, ok := s.mayElevate(c.code, c.code, rec); !ok {
		_ = ui.Respond(ctx, refusal)

		return
	}

	accounts, err := s.cfg.Accounts.List(ctx)
	acct := find(accounts, rec.AccountID)

	switch {
	case err != nil:
		_ = ui.Respond(ctx, say("The bot cannot reach the printer server right now. Try again later."))

		return
	case acct == nil:
		_ = ui.Respond(ctx, say(rec.Username+"'s account is gone from the printer server."))

		return
	case acct.Role != RoleOperator:
		_ = ui.Respond(ctx, say(rec.Username+" is "+acct.Role+" on the printer server, so the bot leaves it alone."))

		return
	}

	s.begin(JournalEntry{Op: opElevate, Code: c.code, Slack: it.User, AccountID: acct.ID})

	if err := s.cfg.Accounts.MakeAdmin(ctx, acct.ID); err != nil {
		s.log.Warn("admin: the server refused", "member", rec.Username, "error", err)
		_ = ui.Respond(ctx, say("The printer server refused to make "+rec.Username+" an admin. Try again later."))

		return // the journal entry stays; the next sweep reads what the server holds
	}

	s.elevated(rec, acct, it.User)
	s.end(opElevate, c.code)
	s.save()
	s.log.Info("made admin", "by", it.User, "member", rec.Username, "slack", rec.Slack)
	_ = ui.Respond(ctx, say(rec.Username+" is an admin now. Their Slack user can use the admin commands."))
}

// Unadmin asks an admin to confirm taking admin from an account the bot made
// admin.
func (s *AccountsService) Unadmin(ctx context.Context, r Request, args []string) Message {
	if refusal, ok := s.admin(ctx, r.User); !ok {
		return refusal
	}

	if len(args) == 0 {
		return say("Take admin from whom? " + SlashCommand + " unadmin <@user|username|member code>")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	code, _ := s.resolve(args[0])

	rec := s.st.Members[code]
	if rec == nil || rec.Elevated.IsZero() {
		return say(args[0] + " is not an admin the bot made. An admin made by hand is changed in the web app.")
	}

	token := randomToken(16)

	s.pending.Lock()
	s.confirm[token] = confirmation{op: "unadmin", code: code, admin: r.User, expires: s.cfg.Now().Add(confirmFor)}
	s.pending.Unlock()

	line := "Take admin from " + rec.Username + "? The account becomes an operator."
	if rec.Slack == r.User {
		line += " That is your own account: you lose the admin commands unless you are linked to another admin account."
	}

	return Message{
		Lines:   []string{line},
		Buttons: []Button{{Action: "unadmin-confirm", Value: token, Label: "Take admin away", Danger: true}},
	}
}

// UnadminConfirm is the click on "Take admin away".
func (s *AccountsService) UnadminConfirm(ctx context.Context, it Interaction, ui UI) {
	c, ok := s.takeConfirm(it, "unadmin")
	if !ok {
		_ = ui.Respond(ctx, say("That request has expired. Run it again."))

		return
	}

	if refusal, ok := s.admin(ctx, it.User); !ok {
		_ = ui.Respond(ctx, refusal)

		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.st.Members[c.code]
	if rec == nil || rec.Elevated.IsZero() {
		_ = ui.Respond(ctx, say("That account is not an admin the bot made any more."))

		return
	}

	if err := s.cfg.Accounts.Unadmin(ctx, rec.AccountID, RoleOperator); err != nil {
		s.log.Warn("unadmin: the server refused", "member", rec.Username, "error", err)
		_ = ui.Respond(ctx, say("The printer server refused to change "+rec.Username+". Try again later."))

		return
	}

	s.unelevated(rec)
	s.save()
	s.log.Info("admin taken away", "by", it.User, "member", rec.Username)
	_ = ui.Respond(ctx, say(rec.Username+" is an operator again."))
}
