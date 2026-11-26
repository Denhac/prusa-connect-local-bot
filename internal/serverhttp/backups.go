package serverhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// Backups implements domain.Backups over /api/v1/server/backups: the server's
// own consistent backup of its configuration, its databases (the accounts
// among them) and every module's data directory, this bot's state file
// included. The server keeps them beside its configuration on the Pi.
type Backups struct {
	session *Session
}

// NewBackups reads and makes the server's backups through a signed-in session.
func NewBackups(s *Session) *Backups { return &Backups{session: s} }

type manifest struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Files     []struct {
		Size int64 `json:"size"`
	} `json:"files"`
}

func (m manifest) domain() domain.Backup {
	b := domain.Backup{ID: m.ID, Created: m.CreatedAt, Files: len(m.Files)}
	for _, f := range m.Files {
		b.Size += f.Size
	}

	return b
}

// Create makes a backup and answers it once the server has verified it.
func (b *Backups) Create(ctx context.Context) (domain.Backup, error) {
	resp, err := b.session.Do(ctx, http.MethodPost, "/api/v1/server/backups", nil, "application/json")
	if err != nil {
		return domain.Backup{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
	case http.StatusNotFound:
		return domain.Backup{}, domain.ErrBackupsOff
	default:
		return domain.Backup{}, refusal("make a backup", resp)
	}

	var m manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&m); err != nil {
		return domain.Backup{}, fmt.Errorf("serverhttp: the backup: %w", err)
	}

	return m.domain(), nil
}

// List is the backups the server keeps, as it answers them.
func (b *Backups) List(ctx context.Context) ([]domain.Backup, error) {
	resp, err := b.session.Do(ctx, http.MethodGet, "/api/v1/server/backups", nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, domain.ErrBackupsOff
	default:
		return nil, refusal("list the backups", resp)
	}

	var list []manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("serverhttp: the backups: %w", err)
	}

	out := make([]domain.Backup, 0, len(list))
	for _, m := range list {
		out = append(out, m.domain())
	}

	return out, nil
}
