package connect

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
)

// pages answers device.list from a fixed number of devices, as the server
// pages them.
type pages struct {
	total int
	err   error
	calls int
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
