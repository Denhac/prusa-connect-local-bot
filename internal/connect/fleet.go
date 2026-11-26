// Package connect reads the prusa-connect server over the module wire, through
// the server's Go SDK. It is the only package besides internal/app that
// imports the SDK's api packages (internal/arch checks this), so a change in
// the SDK lands here.
//
// Every call here is the module's own work: it names no principal, so the
// server answers for the module's instance and the grants the operator gave it.
package connect

import (
	"context"
	"fmt"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// Devices is the part of deviceapi.Client the fleet reads. A test fakes it.
type Devices interface {
	List(ctx context.Context, params deviceapi.ListParams) (deviceapi.Page, error)
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
			out = append(out, domain.Printer{ID: d.ID, Name: d.Name, Model: d.Model, Connected: d.Connected})
		}

		if len(got.Items) < deviceapi.MaxPageLimit || len(out) >= got.Total {
			return out, nil
		}
	}

	return out, nil
}
