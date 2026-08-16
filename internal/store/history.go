package store

import (
	"context"
	"errors"
	"fmt"
)

var ErrHistoryActive = errors.New("history has active work")

type ClearHistoryResult struct {
	Deleted int `json:"deleted"`
}

func (s *Store) ClearHistory(ctx context.Context) (ClearHistoryResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClearHistoryResult{}, fmt.Errorf("begin clear history: %w", err)
	}
	defer tx.Rollback()

	active, err := countActive(ctx, tx)
	if err != nil {
		return ClearHistoryResult{}, err
	}
	queued, err := countQueued(ctx, tx)
	if err != nil {
		return ClearHistoryResult{}, err
	}
	if active > 0 || queued > 0 {
		return ClearHistoryResult{}, ErrHistoryActive
	}

	deleted, err := tx.ExecContext(ctx, "DELETE FROM events")
	if err != nil {
		return ClearHistoryResult{}, fmt.Errorf("delete event history: %w", err)
	}
	count, err := deleted.RowsAffected()
	if err != nil {
		return ClearHistoryResult{}, fmt.Errorf("inspect event history delete: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ClearHistoryResult{}, fmt.Errorf("commit clear history: %w", err)
	}
	return ClearHistoryResult{Deleted: int(count)}, nil
}
