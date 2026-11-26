package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// pages answers device.list from a fixed number of devices, as the server
// pages them, and the job calls from fixed answers.
type pages struct {
	total   int
	err     error
	calls   int
	jobs    deviceapi.Jobs
	history deviceapi.JobPage
	asked   deviceapi.JobHistoryParams
}

func (p *pages) List(_ context.Context, params deviceapi.ListParams) (deviceapi.Page, error) {
	p.calls++
	if p.err != nil {
		return deviceapi.Page{}, p.err
	}

	if params.Principal != "" {
		return deviceapi.Page{}, errors.New("the fleet must read as the module itself")
	}

	var items []deviceapi.Device

	for i := params.Offset; i < min(params.Offset+params.Limit, p.total); i++ {
		items = append(items, deviceapi.Device{ID: fmt.Sprint(i), Connected: i%2 == 0})
	}

	return deviceapi.Page{Items: items, Total: p.total, Limit: params.Limit, Offset: params.Offset}, nil
}

func (p *pages) Jobs(context.Context, deviceapi.JobsParams) (deviceapi.Jobs, error) {
	return p.jobs, p.err
}

func (p *pages) JobHistory(_ context.Context, params deviceapi.JobHistoryParams) (deviceapi.JobPage, error) {
	p.asked = params

	return p.history, p.err
}

func TestPrintersPages(t *testing.T) {
	for _, total := range []int{0, 1, deviceapi.MaxPageLimit, deviceapi.MaxPageLimit + 1, 450} {
		devices := &pages{total: total}

		got, err := NewFleet(devices).Printers(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != total {
			t.Errorf("%d devices listed as %d", total, len(got))
		}

		if want := total/deviceapi.MaxPageLimit + 1; devices.calls > want {
			t.Errorf("%d devices took %d calls, want at most %d", total, devices.calls, want)
		}
	}
}

func TestPrintersError(t *testing.T) {
	if _, err := NewFleet(&pages{err: deviceapi.ErrNotReady}).Printers(context.Background()); !errors.Is(err, deviceapi.ErrNotReady) {
		t.Errorf("error is %v", err)
	}
}

// TestPrinterOf reads a printer's state tree as an MK4S on 0.1.0-beta.2
// reports it.
func TestPrinterOf(t *testing.T) {
	state := map[string]json.RawMessage{
		"/state":                      json.RawMessage(`"PRINTING"`),
		"/speed":                      json.RawMessage(`100`),
		"/temperature/bed":            json.RawMessage(`59.8`),
		"/temperature/bed_target":     json.RawMessage(`60.0`),
		"/tools/0/nozzle_temp":        json.RawMessage(`214.6`),
		"/tools/0/nozzle_target_temp": json.RawMessage(`215`),
	}

	p := printerOf(deviceapi.Device{
		ID: "a", Name: "MK4S-1", Connected: true, State: state,
		Issues: []deviceapi.Issue{{Reason: "awaiting_operator", Severity: "error"}},
		Dialog: &deviceapi.Dialog{Title: "Filament runout"},
	})

	switch {
	case p.State != "PRINTING", !p.Nozzle.Known || p.Nozzle.Target != 215, p.Bed.Now != 59.8:
		t.Errorf("printer is %+v", p)
	case len(p.Issues) != 1 || p.Dialog == nil || p.Dialog.Title != "Filament runout":
		t.Errorf("issues are %+v %+v", p.Issues, p.Dialog)
	}

	if bare := printerOf(deviceapi.Device{ID: "b"}); bare.State != "" || bare.Nozzle.Known || bare.Dialog != nil {
		t.Errorf("a printer with no state is %+v", bare)
	}
}

func TestJobsAndHistory(t *testing.T) {
	ended := time.Date(2026, 11, 25, 21, 0, 0, 0, time.UTC)
	devices := &pages{
		jobs: deviceapi.Jobs{Current: &deviceapi.Job{ID: "1", Path: "/usb/part.bgcode", Progress: 45, TimeRemaining: 4200}},
		history: deviceapi.JobPage{Items: []deviceapi.Job{
			{ID: "0", DisplayName: "Shown name", Outcome: "failed", Ended: &ended},
		}},
	}
	fleet := NewFleet(devices)

	current, last, err := fleet.Jobs(context.Background(), "a")
	if err != nil || last != nil || current == nil || current.File != "part.bgcode" || current.TimeRemaining != 70*time.Minute {
		t.Errorf("jobs are %+v %+v %v", current, last, err)
	}

	since := ended.Add(-time.Hour)

	history, err := fleet.History(context.Background(), "a", since, 10)
	if err != nil || len(history) != 1 || history[0].File != "Shown name" || !history[0].Ended.Equal(ended) {
		t.Errorf("history is %+v %v", history, err)
	}

	if devices.asked.ID != "a" || !devices.asked.From.Equal(since) || devices.asked.Limit != 10 {
		t.Errorf("history asked %+v", devices.asked)
	}
}
