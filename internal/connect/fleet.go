// Package connect reads the prusa-connect server over the module wire, through
// the server's Go SDK. It is the only package besides internal/app that
// imports the SDK (internal/arch checks this), so a change in the SDK lands
// here, and docs/compat.md lists what it relies on.
//
// Every call here is the module's own work: it names no principal, so the
// server answers for the module's instance and the grants the operator gave it.
package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// Devices is the part of deviceapi.Client the fleet reads. A test fakes it.
type Devices interface {
	List(ctx context.Context, params deviceapi.ListParams) (deviceapi.Page, error)
	Jobs(ctx context.Context, params deviceapi.JobsParams) (deviceapi.Jobs, error)
	JobHistory(ctx context.Context, params deviceapi.JobHistoryParams) (deviceapi.JobPage, error)
}

// Fleet implements domain.Fleet over the server's devices.
type Fleet struct {
	devices Devices
}

// NewFleet builds the fleet over a device client.
func NewFleet(devices Devices) Fleet { return Fleet{devices: devices} }

// maxPages bounds a listing, so a server that keeps answering full pages
// cannot hold a command forever: 50 pages of 200 is 10,000 printers.
const maxPages = 50

// The state paths the bot reads from a printer's state tree.
const (
	pathState        = "/state"
	pathNozzle       = "/tools/0/nozzle_temp"
	pathNozzleTarget = "/tools/0/nozzle_target_temp"
	pathBed          = "/temperature/bed"
	pathBedTarget    = "/temperature/bed_target"
)

// Printers lists every printer the module may see, page by page.
func (f Fleet) Printers(ctx context.Context) ([]domain.Printer, error) {
	var out []domain.Printer

	for page := range maxPages {
		got, err := f.devices.List(ctx, deviceapi.ListParams{
			Limit:  deviceapi.MaxPageLimit,
			Offset: page * deviceapi.MaxPageLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("device.list: %w", err)
		}

		for _, d := range got.Items {
			out = append(out, printerOf(d))
		}

		if len(got.Items) < deviceapi.MaxPageLimit || len(out) >= got.Total {
			return out, nil
		}
	}

	return out, nil
}

func printerOf(d deviceapi.Device) domain.Printer {
	p := domain.Printer{
		ID: d.ID, Name: d.Name, Model: d.Model, Connected: d.Connected,
		Issues: issuesOf(d.Issues), Dialog: dialogOf(d.Dialog),
	}

	_ = json.Unmarshal(d.State[pathState], &p.State) // absent or not text: no state word

	p.Nozzle = tempOf(d.State[pathNozzle], d.State[pathNozzleTarget])
	p.Bed = tempOf(d.State[pathBed], d.State[pathBedTarget])

	return p
}

func tempOf(now, target json.RawMessage) domain.Temp {
	var t domain.Temp

	if json.Unmarshal(now, &t.Now) == nil {
		t.Known = true
	}

	_ = json.Unmarshal(target, &t.Target) // absent: no target

	return t
}

func issuesOf(in []deviceapi.Issue) []domain.Issue {
	out := make([]domain.Issue, 0, len(in))
	for _, i := range in {
		out = append(out, domain.Issue{Reason: i.Reason, Severity: i.Severity, Detail: i.Detail})
	}

	return out
}

func dialogOf(d *deviceapi.Dialog) *domain.Dialog {
	if d == nil {
		return nil
	}

	return &domain.Dialog{Title: d.Title, Text: d.Text, Buttons: d.Buttons}
}

// Jobs is one printer's print on the bed and the one before it.
func (f Fleet) Jobs(ctx context.Context, device string) (*domain.Job, *domain.Job, error) {
	got, err := f.devices.Jobs(ctx, deviceapi.JobsParams{ID: device})
	if err != nil {
		return nil, nil, fmt.Errorf("device.jobs: %w", err)
	}

	return jobPointer(got.Current), jobPointer(got.Last), nil
}

// History is one printer's prints that ended since a moment, newest first.
func (f Fleet) History(ctx context.Context, device string, since time.Time, limit int) ([]domain.Job, error) {
	page, err := f.devices.JobHistory(ctx, deviceapi.JobHistoryParams{
		ID: device, From: since, Limit: min(max(limit, 1), deviceapi.MaxPageLimit),
	})
	if err != nil {
		return nil, fmt.Errorf("device.job_history: %w", err)
	}

	out := make([]domain.Job, 0, len(page.Items))
	for _, j := range page.Items {
		out = append(out, jobOf(j))
	}

	return out, nil
}

func jobPointer(j *deviceapi.Job) *domain.Job {
	if j == nil {
		return nil
	}

	out := jobOf(*j)

	return &out
}

func jobOf(j deviceapi.Job) domain.Job {
	out := domain.Job{
		ID:            j.ID,
		File:          shown(j.DisplayName, j.Path),
		Progress:      j.Progress,
		TimeRemaining: time.Duration(j.TimeRemaining) * time.Second,
		Started:       j.Started,
		Outcome:       j.Outcome,
	}

	if j.Ended != nil {
		out.Ended = *j.Ended
	}

	return out
}

// shown is the name a printer shows for a file, or the file's own name.
func shown(display, file string) string {
	if display != "" {
		return display
	}

	if file == "" {
		return ""
	}

	return path.Base(file)
}
