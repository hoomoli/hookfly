package store

import (
	"context"
	"sort"
	"time"
)

type PipelineRun struct {
	Provider         string    `json:"provider"`
	Source           string    `json:"source"`
	Repository       string    `json:"repository"`
	PipelineID       string    `json:"pipeline_id"`
	Ref              string    `json:"ref"`
	Status           string    `json:"status"`
	FirstReceived    time.Time `json:"first_received"`
	LastReceived     time.Time `json:"last_received"`
	InitializationMS int64     `json:"initialization_ms"`
	WaitingMS        int64     `json:"waiting_ms"`
	RunningMS        int64     `json:"running_ms"`
	TotalMS          *int64    `json:"total_ms"`
	Complete         bool      `json:"complete"`
	Restarts         int       `json:"restarts"`
}

type PipelineStatistics struct {
	ServerNow     time.Time     `json:"server_now"`
	RetentionDays int           `json:"retention_days"`
	Runs          []PipelineRun `json:"runs"`
	Active        bool          `json:"active"`
}

func pipelinePhase(status string) string {
	switch status {
	case "created", "preparing", "scheduled":
		return "initialization"
	case "pending", "queued", "waiting_for_resource", "requested":
		return "waiting"
	case "running", "in_progress":
		return "running"
	case "success", "failed", "failure", "canceled", "cancelled", "skipped", "neutral", "timed_out", "action_required":
		return "terminal"
	default:
		return "unknown"
	}
}

// PipelineStatistics uses durable receive times, independent of event retention.
// Restart gaps and missing boundaries are excluded from duration percentiles.
func (s *Store) PipelineStatistics(ctx context.Context, source, repository string, from, to time.Time) (PipelineStatistics, error) {
	result := PipelineStatistics{ServerNow: s.now().UTC(), RetentionDays: 90, Runs: []PipelineRun{}}
	rows, err := s.db.QueryContext(ctx, `SELECT provider,source_id,repository_id,external_id,ref,status,received_at FROM pipeline_observations
 WHERE received_at >= ? AND received_at < ? AND (? = '' OR source_id = ?) AND (? = '' OR repository_id = ?)
 ORDER BY received_at,event_id`, result.ServerNow.AddDate(0, 0, -90).UnixMilli(), to.UnixMilli(), source, source, repository, repository)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	type identity struct{ provider, source, repository, pipeline string }
	type state struct {
		run                      PipelineRun
		phase                    string
		knownStart, ran, invalid bool
	}
	states := map[identity]*state{}
	boundaries := map[identity]time.Time{}
	for rows.Next() {
		var key identity
		var ref, status string
		var at int64
		if err := rows.Scan(&key.provider, &key.source, &key.repository, &key.pipeline, &ref, &status, &at); err != nil {
			return result, err
		}
		received := time.UnixMilli(at).UTC()
		phase := pipelinePhase(status)
		current := states[key]
		if current == nil {
			current = &state{run: PipelineRun{Provider: key.provider, Source: key.source, Repository: key.repository, PipelineID: key.pipeline, Ref: ref, FirstReceived: received}, knownStart: phase == "initialization" || phase == "waiting"}
			states[key] = current
		} else if phase != current.phase {
			if current.phase == "running" && (phase == "initialization" || phase == "waiting") {
				current.invalid = true
			}
			delta := received.Sub(boundaries[key]).Milliseconds()
			if delta == 0 {
				current.invalid = true
			}
			switch current.phase {
			case "initialization":
				current.run.InitializationMS += delta
			case "waiting":
				current.run.WaitingMS += delta
			case "running":
				current.run.RunningMS += delta
			case "terminal":
				current.run.Restarts++
				current.invalid = true
			}
		} else {
			// Keep the original boundary for repeated notifications of the same phase.
			if phase == "terminal" && current.run.Status != status {
				current.invalid = true
			}
			current.run.Status = status
			current.run.LastReceived = received
			continue
		}
		if phase == "unknown" {
			current.invalid = true
		}
		if phase == "running" {
			current.ran = true
		}
		boundaries[key] = received
		current.phase = phase
		current.run.Status = status
		current.run.LastReceived = received
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	for key, current := range states {
		run := current.run
		if run.FirstReceived.Before(from) {
			continue
		}
		run.Complete = current.knownStart && current.ran && current.phase == "terminal" && !current.invalid
		if run.Complete {
			total := run.InitializationMS + run.WaitingMS + run.RunningMS
			run.TotalMS = &total
		}
		if current.phase != "terminal" && current.phase != "unknown" {
			result.Active = true
			end := result.ServerNow
			if to.Before(end) {
				end = to
			}
			delta := end.Sub(boundaries[key]).Milliseconds()
			if delta > 0 {
				switch current.phase {
				case "initialization":
					run.InitializationMS += delta
				case "waiting":
					run.WaitingMS += delta
				case "running":
					run.RunningMS += delta
				}
			}
		}
		result.Runs = append(result.Runs, run)
	}
	sort.Slice(result.Runs, func(i, j int) bool { return result.Runs[i].FirstReceived.After(result.Runs[j].FirstReceived) })
	return result, nil
}
