package store

import (
	"context"
	"github.com/hoomoli/hookfly/internal/domain"
	"testing"
	"time"
)

func TestPipelineStatisticsReceiveTimeAndRetention(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	for i, status := range []string{"created", "pending", "pending", "running", "success"} {
		_, err := s.Ingest(ctx, domain.IngestCommand{DeliveryID: status + time.Duration(i).String(), ReceivedAt: start.Add(time.Duration(i) * time.Second), RoutingResult: domain.RoutingRecordOnly, CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "source", Repository: "app", Event: "pipeline", ExternalID: "42", Status: status}})
		if err != nil {
			t.Fatal(err)
		}
	}
	// A delivery retry with the same delivery identity must not add observations.
	_, err := s.Ingest(ctx, domain.IngestCommand{DeliveryID: "created0s", ReceivedAt: start.Add(10 * time.Second), RoutingResult: domain.RoutingRecordOnly, CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "source", Repository: "app", Event: "pipeline", ExternalID: "42", Status: "created"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearHistory(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := s.PipelineStatistics(ctx, "source", "app", start.Add(-time.Second), start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("runs=%+v", result.Runs)
	}
	run := result.Runs[0]
	if !run.Complete || run.TotalMS == nil || *run.TotalMS != 4000 || run.WaitingMS != 2000 || run.RunningMS != 1000 {
		t.Fatalf("run=%+v", run)
	}
}

func TestPipelineStatisticsMissingBoundariesAndRestart(t *testing.T) {
	for _, statuses := range [][]string{{"running", "success"}, {"pending", "running", "success", "running", "failed"}, {"pending", "running", "pending", "failed"}} {
		t.Run(statuses[0]+time.Duration(len(statuses)).String(), func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			start := time.Now().Add(-time.Hour)
			for i, status := range statuses {
				_, err := s.Ingest(ctx, domain.IngestCommand{DeliveryID: time.Duration(i).String(), ReceivedAt: start.Add(time.Duration(i) * time.Second), RoutingResult: domain.RoutingRecordOnly, CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "source", Repository: "app", Event: "pipeline", ExternalID: "42", Status: status}})
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.PipelineStatistics(ctx, "source", "app", start.Add(-time.Second), start.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Runs) != 1 || result.Runs[0].Complete || result.Runs[0].TotalMS != nil {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}
