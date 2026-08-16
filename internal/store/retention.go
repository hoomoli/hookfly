package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// PruneResult records the terminal events removed by one retention transaction.
type PruneResult struct {
	Deleted  int
	EventIDs []string
}

// Prune removes only terminal events, first within each repository group and then across all groups.
func (s *Store) Prune(ctx context.Context, repositoryLimits map[RepositoryKey]int, defaultLimit, globalLimit int) (PruneResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, fmt.Errorf("begin retention prune: %w", err)
	}
	defer tx.Rollback()

	groups, err := retentionGroups(ctx, tx)
	if err != nil {
		return PruneResult{}, err
	}
	result := PruneResult{}
	for _, group := range groups {
		limit := defaultLimit
		if configured, found := repositoryLimits[RepositoryKey{SourceID: group.sourceID, RepositoryID: group.repositoryID}]; found {
			limit = configured
		}
		if limit <= 0 {
			continue
		}
		ids, err := terminalEventIDsAfter(ctx, tx, &group, limit)
		if err != nil {
			return PruneResult{}, err
		}
		if err := deleteRetentionEvents(ctx, tx, &result, ids); err != nil {
			return PruneResult{}, err
		}
	}
	if globalLimit > 0 {
		ids, err := terminalEventIDsAfter(ctx, tx, nil, globalLimit)
		if err != nil {
			return PruneResult{}, err
		}
		if err := deleteRetentionEvents(ctx, tx, &result, ids); err != nil {
			return PruneResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PruneResult{}, fmt.Errorf("commit retention prune: %w", err)
	}
	return result, nil
}

func deleteRetentionEvents(ctx context.Context, tx *sql.Tx, result *PruneResult, eventIDs []string) error {
	for _, eventID := range eventIDs {
		deleted, err := tx.ExecContext(ctx, "DELETE FROM events WHERE id = ?", eventID)
		if err != nil {
			return fmt.Errorf("delete retained event: %w", err)
		}
		count, err := deleted.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect retained event delete: %w", err)
		}
		if count == 1 {
			result.Deleted++
			result.EventIDs = append(result.EventIDs, eventID)
		}
	}
	return nil
}

type retentionGroup struct {
	sourceID     string
	repositoryID string
}

func retentionGroups(ctx context.Context, tx *sql.Tx) ([]retentionGroup, error) {
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT source_id, repository_id FROM events")
	if err != nil {
		return nil, fmt.Errorf("list retention repository groups: %w", err)
	}
	defer rows.Close()
	groups := make([]retentionGroup, 0)
	for rows.Next() {
		var group retentionGroup
		if err := rows.Scan(&group.sourceID, &group.repositoryID); err != nil {
			return nil, fmt.Errorf("scan retention repository group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retention repository groups: %w", err)
	}
	sort.Slice(groups, func(i, j int) bool {
		return retentionGroupLess(groups[i], groups[j])
	})
	return groups, nil
}

func retentionGroupLess(left, right retentionGroup) bool {
	if left.sourceID != right.sourceID {
		return left.sourceID < right.sourceID
	}
	return left.repositoryID < right.repositoryID
}

func terminalEventIDsAfter(ctx context.Context, tx *sql.Tx, group *retentionGroup, limit int) ([]string, error) {
	where := ""
	args := make([]any, 0, 4)
	if group == nil {
		// The global pass intentionally has no repository predicate.
	} else {
		where = "AND e.source_id = ? AND e.repository_id = ?"
		args = append(args, group.sourceID, group.repositoryID)
	}
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id
		FROM events e
		WHERE 1 = 1 `+where+`
		  AND e.routing_result <> 'deferred'
		  AND NOT EXISTS (
		      SELECT 1
		      FROM deliveries d
		      JOIN delivery_attempts a ON a.id = d.current_attempt_id AND a.delivery_id = d.id
		      WHERE d.event_id = e.id
		        AND (a.transport_status IN ('pending', 'sending') OR a.deployment_status IN ('locating', 'running', 'unrecognized'))
		  )
		ORDER BY e.received_at DESC, e.id DESC
		LIMIT -1 OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("select terminal retention events: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan terminal retention event: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate terminal retention events: %w", err)
	}
	return ids, nil
}
