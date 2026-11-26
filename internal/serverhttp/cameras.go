package serverhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

const (
	// listFor is how long the camera list is reused.
	listFor = 5 * time.Minute
	// staleAfter is the oldest picture the bot posts. The Pis push one every
	// 15 s, and stop while someone watches live view.
	staleAfter = 10 * time.Minute
	// maxSnapshot bounds one picture.
	maxSnapshot = 8 << 20
)

// Cameras implements domain.Stills over the server's own cameras:
// GET /api/v1/cameras to find each printer's camera, and
// GET /api/v1/cameras/{camera}/snapshot for its newest picture.
type Cameras struct {
	session *Session
	now     func() time.Time

	mu       sync.Mutex
	byDevice map[string]string
	fetched  time.Time
}

// NewCameras reads cameras through a signed-in session.
func NewCameras(s *Session, now func() time.Time) *Cameras {
	return &Cameras{session: s, now: now, byDevice: map[string]string{}}
}

// Has reports whether the server has a camera for the printer. The list is
// read again when it is older than listFor.
func (c *Cameras) Has(device string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, _ := c.camera(ctx, device, false)

	return name != ""
}

// camera is the server's camera for a printer, reading the list when it is
// old or when fresh asks.
func (c *Cameras) camera(ctx context.Context, device string, fresh bool) (string, error) {
	c.mu.Lock()
	old := fresh || c.now().Sub(c.fetched) > listFor
	c.mu.Unlock()

	if old {
		if err := c.refresh(ctx); err != nil {
			c.mu.Lock()
			defer c.mu.Unlock()

			return c.byDevice[device], err // the last list is still the best answer
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.byDevice[device], nil
}

func (c *Cameras) refresh(ctx context.Context) error {
	resp, err := c.session.Do(ctx, http.MethodGet, "/api/v1/cameras", nil, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("serverhttp: GET /api/v1/cameras answered %s %s", resp.Status, problemCode(resp.Body))
	}

	var list struct {
		Items []struct {
			Name     string `json:"name"`
			DeviceID string `json:"device_id"`
			Snapshot bool   `json:"snapshot"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return fmt.Errorf("serverhttp: the camera list: %w", err)
	}

	byDevice := map[string]string{}

	for _, cam := range list.Items {
		if cam.DeviceID != "" && cam.Snapshot {
			if _, taken := byDevice[cam.DeviceID]; !taken {
				byDevice[cam.DeviceID] = cam.Name // a printer's first camera
			}
		}
	}

	c.mu.Lock()
	c.byDevice, c.fetched = byDevice, c.now()
	c.mu.Unlock()

	return nil
}

// Still is the newest picture from the printer's camera on the server. The
// server holds no fresher one to wait for, so fresh changes nothing here.
func (c *Cameras) Still(ctx context.Context, device string, _ bool) ([]byte, error) {
	name, err := c.camera(ctx, device, false)
	if name == "" {
		if err != nil {
			return nil, err
		}

		return nil, domain.ErrNoCamera
	}

	resp, err := c.session.Do(ctx, http.MethodGet, "/api/v1/cameras/"+url.PathEscape(name)+"/snapshot", nil, "image/jpeg")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		return nil, domain.ErrNoPicture
	case http.StatusNotFound:
		c.mu.Lock()
		c.fetched = time.Time{} // the camera moved or went: read the list again next time
		c.mu.Unlock()

		return nil, domain.ErrNoCamera
	default:
		return nil, fmt.Errorf("serverhttp: the snapshot of %s answered %s %s", name, resp.Status, problemCode(resp.Body))
	}

	if modified, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil && c.now().Sub(modified) > staleAfter {
		return nil, domain.ErrCameraBusy
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSnapshot+1))
	if err != nil {
		return nil, fmt.Errorf("serverhttp: %w", err)
	}

	if len(body) > maxSnapshot {
		return nil, fmt.Errorf("serverhttp: the snapshot of %s is over %d bytes", name, maxSnapshot)
	}

	return body, nil
}
