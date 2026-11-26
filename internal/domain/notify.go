package domain

import (
	"slices"
	"sort"
	"strconv"
	"time"
)

// This file is the status-message reducer: events, fleet snapshots and the
// clock go in, and what to post comes out. It does no I/O, so its tests are
// table tests with a fake clock (docs/design.md, "Status to #bots").

// ActionKind is what to do in the status channel.
type ActionKind string

// The actions.
const (
	// ActPost starts a message, and its id is kept under the action's thread key.
	ActPost ActionKind = "post"
	// ActReply answers in the thread kept under the key. With no such thread,
	// it is posted as a message of its own.
	ActReply ActionKind = "reply"
	// ActUpdate replaces the message kept under the key.
	ActUpdate ActionKind = "update"
)

// Action is one thing to post.
type Action struct {
	Kind ActionKind
	// Thread is the key the message is kept under (ActPost) or found by.
	Thread  string
	Message Message
	// StillOf names a printer whose camera still goes into the thread after
	// the message, when it has a camera.
	StillOf string
}

// NotifyConfig is what an operator may tune.
type NotifyConfig struct {
	// IssueReasons are the server's issue reasons that mean "needs a person".
	IssueReasons []string
	// AttentionAfter is how long a printer must keep waiting before it is
	// posted, so a question answered at once posts nothing.
	AttentionAfter time.Duration
	// OfflineAfter is how long a printer must stay away during a print.
	OfflineAfter time.Duration
	// KeepJobs is how many prints the state remembers.
	KeepJobs int
}

// DefaultNotifyConfig is the design's defaults.
func DefaultNotifyConfig() NotifyConfig {
	return NotifyConfig{
		IssueReasons:   []string{"awaiting_operator"},
		AttentionAfter: 30 * time.Second,
		OfflineAfter:   2 * time.Minute,
		KeepJobs:       200,
	}
}

// NotifyState is what the reducer must remember across restarts. It is plain
// data, kept in the state file.
type NotifyState struct {
	Jobs      map[string]*JobThread `json:"jobs,omitempty"`
	Attention map[string]*Pending   `json:"attention,omitempty"`
	Offline   map[string]*Pending   `json:"offline,omitempty"`
}

// JobThread is one print the channel has heard about.
type JobThread struct {
	Device  string    `json:"device"`
	File    string    `json:"file,omitempty"`
	Started time.Time `json:"started"`
	Posted  bool      `json:"posted"`
	Settled bool      `json:"settled"`
}

// Pending is a printer waiting for a person, or away during a print: noticed
// at Since, and posted once it has lasted long enough.
type Pending struct {
	Since  time.Time `json:"since"`
	Posted bool      `json:"posted"`
	Title  string    `json:"title,omitempty"`
	Text   string    `json:"text,omitempty"`
	// Job is the print a printer went away during.
	Job string `json:"job,omitempty"`
}

// Snapshot is the fleet as read on a rebuild.
type Snapshot struct {
	Printers []Printer
	// Current is each printer's print on the bed, if any.
	Current map[string]*Job
	// Ended is each printer's prints that ended since the last event applied.
	Ended map[string][]Job
}

// stateAttention is the machine's word for a printer waiting for a person.
const stateAttention = "ATTENTION"

// view is what the reducer knows of a printer. It is rebuilt from the fleet,
// so it is not kept in the state file.
type view struct {
	name      string
	connected bool
	state     string
	issues    []Issue
	dialog    *Dialog
}

// Notifier is the reducer. It is not safe for concurrent use: one goroutine
// owns it.
type Notifier struct {
	cfg      NotifyConfig
	st       NotifyState
	printers map[string]*view
}

// NewNotifier picks up where a saved state left off.
func NewNotifier(cfg NotifyConfig, st NotifyState) *Notifier {
	if st.Jobs == nil {
		st.Jobs = map[string]*JobThread{}
	}

	if st.Attention == nil {
		st.Attention = map[string]*Pending{}
	}

	if st.Offline == nil {
		st.Offline = map[string]*Pending{}
	}

	return &Notifier{cfg: cfg, st: st, printers: map[string]*view{}}
}

// State is what to save. The caller encodes it before the next call.
func (n *Notifier) State() NotifyState { return n.st }

// JobKey is the thread key of a print.
func JobKey(job string) string { return "job/" + job }

func attentionKey(device string, since time.Time) string {
	return "attention/" + device + "/" + strconv.FormatInt(since.Unix(), 10)
}

func (n *Notifier) printer(device string) *view {
	p, ok := n.printers[device]
	if !ok {
		p = &view{connected: true}
		n.printers[device] = p
	}

	return p
}

func (n *Notifier) name(device string) string {
	if p, ok := n.printers[device]; ok && p.name != "" {
		return p.name
	}

	return device
}

// Apply takes one event.
func (n *Notifier) Apply(ev Event, now time.Time) []Action {
	p := n.printer(ev.Device)

	switch ev.Kind {
	case EventJobStarted:
		return n.started(ev.Device, ev.Job)
	case EventJobSettled:
		return n.settled(ev.Device, ev.Job, false)
	case EventIssuesChanged:
		p.issues, p.dialog = ev.Issues, ev.Dialog

		return n.attention(ev.Device, now)
	case EventStateChanged:
		p.state = ev.State

		return n.attention(ev.Device, now)
	case EventPresence, EventArrived, EventDeparted:
		p.connected = ev.Connected

		return n.presence(ev.Device, now)
	case EventRecordChanged:
		if ev.Name != "" {
			p.name = ev.Name
		}
	}

	return nil
}

// Rebuild takes the fleet as read now, on a start or after continuity was
// lost, and posts what changed while the bot was not reading.
func (n *Notifier) Rebuild(snap Snapshot, now time.Time) []Action {
	for _, pr := range snap.Printers {
		p := n.printer(pr.ID)
		p.name, p.connected, p.state, p.issues, p.dialog = pr.Name, pr.Connected, pr.State, pr.Issues, pr.Dialog
	}

	var out []Action

	// Prints the channel heard start, and that ended meanwhile.
	ended := map[string]Job{}

	for _, device := range sortedKeys(snap.Ended) {
		for _, j := range snap.Ended[device] {
			ended[j.ID] = j
		}
	}

	for _, id := range sortedKeys(n.st.Jobs) {
		t := n.st.Jobs[id]
		if j, ok := ended[id]; ok && t.Posted && !t.Settled {
			out = append(out, n.settled(t.Device, j, false)...)
		}
	}

	// Prints that started and ended meanwhile.
	for _, device := range sortedKeys(snap.Ended) {
		for _, j := range snap.Ended[device] {
			if _, known := n.st.Jobs[j.ID]; !known {
				out = append(out, n.settled(device, j, true)...)
			}
		}
	}

	// Prints running now that the channel has not heard of.
	for _, device := range sortedKeys(snap.Current) {
		if j := snap.Current[device]; j != nil {
			out = append(out, n.started(device, *j)...)
		}
	}

	for _, pr := range snap.Printers {
		out = append(out, n.attention(pr.ID, now)...)
		out = append(out, n.presence(pr.ID, now)...)
	}

	return out
}

// Tick posts what has waited long enough.
func (n *Notifier) Tick(now time.Time) []Action {
	var out []Action

	for _, device := range sortedKeys(n.st.Attention) {
		pend := n.st.Attention[device]
		if pend.Posted || now.Sub(pend.Since) < n.cfg.AttentionAfter {
			continue
		}

		pend.Posted = true
		out = append(out, Action{
			Kind: ActPost, Thread: attentionKey(device, pend.Since), StillOf: device,
			Message: attentionMessage(n.name(device), pend),
		})
	}

	for _, device := range sortedKeys(n.st.Offline) {
		pend := n.st.Offline[device]
		if pend.Posted || now.Sub(pend.Since) < n.cfg.OfflineAfter {
			continue
		}

		if t, ok := n.st.Jobs[pend.Job]; !ok || t.Settled {
			delete(n.st.Offline, device)

			continue
		}

		pend.Posted = true
		out = append(out, Action{
			Kind: ActReply, Thread: JobKey(pend.Job),
			Message: Message{Lines: []string{"⚡ " + n.name(device) + " went offline during the print."}},
		})
	}

	return out
}

func (n *Notifier) started(device string, j Job) []Action {
	if t, ok := n.st.Jobs[j.ID]; ok && t.Posted {
		return nil
	}

	n.st.Jobs[j.ID] = &JobThread{Device: device, File: j.File, Started: j.Started, Posted: true}
	n.prune()

	lines := []string{"▶ " + n.name(device) + " started " + fileName(j)}
	if j.TimeRemaining > 0 {
		lines = append(lines, "About "+span(j.TimeRemaining)+" to go.")
	}

	return []Action{{Kind: ActPost, Thread: JobKey(j.ID), Message: Message{Lines: lines}}}
}

// settled closes a print. gap is a print that started and ended while the bot
// was not reading.
func (n *Notifier) settled(device string, j Job, gap bool) []Action {
	if pend, ok := n.st.Offline[device]; ok && pend.Job == j.ID {
		delete(n.st.Offline, device)
	}

	t, ok := n.st.Jobs[j.ID]
	if ok && t.Settled {
		return nil
	}

	name := n.name(device)

	if ok && t.Posted {
		t.Settled = true

		if j.File == "" {
			j.File = t.File
		}

		return []Action{
			{Kind: ActReply, Thread: JobKey(j.ID), Message: Message{Lines: []string{endLine(j)}}, StillOf: device},
			{Kind: ActUpdate, Thread: JobKey(j.ID), Message: Message{Lines: []string{
				outcomeIcon(j.Outcome) + " " + name + " " + outcomeVerb(j.Outcome) + " " + fileName(j),
			}}},
		}
	}

	n.st.Jobs[j.ID] = &JobThread{Device: device, File: j.File, Started: j.Started, Posted: true, Settled: true}
	n.prune()

	lines := []string{outcomeIcon(j.Outcome) + " " + name + " " + outcomeVerb(j.Outcome) + " " + fileName(j)}
	if d := took(j); d > 0 {
		lines[0] += " (" + span(d) + ")"
	}

	if gap {
		lines = append(lines, "This print ran while the bot was not watching.")
	}

	return []Action{{Kind: ActPost, Thread: JobKey(j.ID), Message: Message{Lines: lines}, StillOf: device}}
}

// attention starts or ends a "needs a person" for one printer.
func (n *Notifier) attention(device string, now time.Time) []Action {
	needs, title, text := n.needsPerson(device)
	pend, waiting := n.st.Attention[device]

	switch {
	case needs && !waiting:
		n.st.Attention[device] = &Pending{Since: now, Title: title, Text: text}
	case needs && !pend.Posted:
		pend.Title, pend.Text = title, text
	case !needs && waiting:
		delete(n.st.Attention, device)

		if pend.Posted {
			return []Action{{
				Kind: ActReply, Thread: attentionKey(device, pend.Since),
				Message: Message{Lines: []string{"✅ Answered."}},
			}}
		}
	}

	return nil
}

func (n *Notifier) needsPerson(device string) (bool, string, string) {
	p := n.printer(device)
	needs := p.state == stateAttention
	detail := ""

	for _, issue := range p.issues {
		if slices.Contains(n.cfg.IssueReasons, issue.Reason) {
			needs = true

			if detail == "" {
				detail = issue.Detail
			}
		}
	}

	if !needs {
		return false, "", ""
	}

	if p.dialog != nil && p.dialog.Title != "" {
		return true, p.dialog.Title, p.dialog.Text
	}

	if detail != "" {
		return true, detail, ""
	}

	return true, "the printer is waiting", ""
}

func attentionMessage(name string, pend *Pending) Message {
	lines := []string{"⚠ " + name + " needs a person: " + pend.Title}
	if pend.Text != "" {
		lines = append(lines, pend.Text)
	}

	return Message{Lines: lines}
}

// presence starts or ends "away during a print" for one printer.
func (n *Notifier) presence(device string, now time.Time) []Action {
	p := n.printer(device)
	pend, away := n.st.Offline[device]

	if !p.connected {
		if job := n.openJob(device); job != "" && !away {
			n.st.Offline[device] = &Pending{Since: now, Job: job}
		}

		return nil
	}

	if away {
		delete(n.st.Offline, device)

		if pend.Posted {
			return []Action{{
				Kind: ActReply, Thread: JobKey(pend.Job),
				Message: Message{Lines: []string{"🔌 " + n.name(device) + " is back online."}},
			}}
		}
	}

	return nil
}

// openJob is the newest print the channel posted for a printer and has not
// closed.
func (n *Notifier) openJob(device string) string {
	best, at := "", time.Time{}

	for id, t := range n.st.Jobs {
		if t.Device == device && t.Posted && !t.Settled && (best == "" || t.Started.After(at)) {
			best, at = id, t.Started
		}
	}

	return best
}

// prune forgets the oldest closed prints beyond KeepJobs.
func (n *Notifier) prune() {
	if len(n.st.Jobs) <= n.cfg.KeepJobs {
		return
	}

	type aged struct {
		id string
		at time.Time
	}

	var closed []aged

	for id, t := range n.st.Jobs {
		if t.Settled {
			closed = append(closed, aged{id, t.Started})
		}
	}

	sort.Slice(closed, func(i, j int) bool { return closed[i].at.Before(closed[j].at) })

	for _, c := range closed {
		if len(n.st.Jobs) <= n.cfg.KeepJobs {
			return
		}

		delete(n.st.Jobs, c.id)
	}
}

// Forgotten names the thread keys the state no longer refers to, so the
// caller can drop their message ids too.
func (n *Notifier) Forgotten(keys []string) []string {
	var out []string

	for _, k := range keys {
		switch {
		case len(k) > 4 && k[:4] == "job/":
			if _, ok := n.st.Jobs[k[4:]]; !ok {
				out = append(out, k)
			}
		case len(k) > 10 && k[:10] == "attention/":
			live := false

			for device, pend := range n.st.Attention {
				if attentionKey(device, pend.Since) == k {
					live = true
				}
			}

			if !live {
				out = append(out, k)
			}
		}
	}

	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

// startGap is how long the module must have been away before its start is
// worth a message. A settings change restarts it within seconds.
const startGap = 2 * time.Minute

// StartMessage is what the module posts when it starts, and whether to post
// it: after a gap, on a new version, or the first time. lastAlive is when it
// last wrote its heartbeat, and zero the first time.
func StartMessage(version, lastVersion string, lastAlive, now time.Time) (Message, bool) {
	gap := !lastAlive.IsZero() && now.Sub(lastAlive) > startGap

	if !gap && !lastAlive.IsZero() && version == lastVersion {
		return Message{}, false
	}

	lines := []string{"🤖 Printer bot " + version + " started."}
	if gap {
		// lastAlive reaches the disk only every few minutes, so after a crash
		// it is when the bot was last seen, not the moment it stopped.
		lines = append(lines, "It stopped some time after "+clock(lastAlive, now)+", and started again at "+clock(now, now)+".")
	}

	return Message{Lines: lines}, true
}
