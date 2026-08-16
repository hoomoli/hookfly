package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestListRepositoriesPreservesConfiguredCanonicalOrderIncludingEmptyRepositories(t *testing.T) {
	s := openTestStore(t)
	repositories, err := s.ListRepositories(context.Background(), []ConfiguredRepository{
		{Provider: "github", SourceID: "github-a", ID: "app", Name: "Application"},
		{Provider: "gitlab", SourceID: "gitlab-a", ID: "empty", Name: "Empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 || repositories[0].Provider != "github" || repositories[0].SourceID != "github-a" || repositories[1].ID != "empty" || repositories[1].Name != "Empty" {
		t.Fatalf("repositories = %#v", repositories)
	}
}

func TestListEventsUsesStablePaginationAndSameDeliveryStatusFilters(t *testing.T) {
	s := openTestStore(t)
	first := queryIngest(t, s, "event-a", time.UnixMilli(1000), []domain.NewDelivery{{TargetID: "one"}, {TargetID: "two"}})
	second := queryIngest(t, s, "event-b", time.UnixMilli(1000), []domain.NewDelivery{{TargetID: "three"}})
	querySetState(t, s, first, "one", "failed", "not_started")
	querySetState(t, s, first, "two", "enqueued", "running")
	querySetState(t, s, second, "three", "unknown", "locating")
	wantFirst, wantSecond := first, second
	if second > first {
		wantFirst, wantSecond = second, first
	}

	page, err := s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != wantFirst || page.HasPrevious || !page.HasNext || !page.Active {
		t.Fatalf("first page = %#v", page)
	}
	next, err := s.ListEvents(context.Background(), EventQuery{Page: 2, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || next.Items[0].ID != wantSecond || !next.HasPrevious || next.HasNext {
		t.Fatalf("second page = %#v", next)
	}

	transport, deployment := "failed", "running"
	filtered, err := s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 50, TransportStatus: &transport, DeploymentStatus: &deployment})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 0 {
		t.Fatalf("split fan-out statuses matched = %#v", filtered.Items)
	}
	transport, deployment = "unknown", "locating"
	filtered, err = s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 50, TransportStatus: &transport, DeploymentStatus: &deployment})
	if err != nil || filtered.Total != 1 || filtered.Items[0].ID != second {
		t.Fatalf("same delivery statuses = %#v/%v", filtered, err)
	}
}

func TestListEventsMaximumPageReturnsEmptyWithoutOverflow(t *testing.T) {
	s := openTestStore(t)
	firstID := queryIngest(t, s, "max-page", time.UnixMilli(1000), nil)
	page, err := s.ListEvents(context.Background(), EventQuery{Page: math.MaxInt, PageSize: 200})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 0 || !page.HasPrevious || page.HasNext || page.Active {
		t.Fatalf("maximum page returned first event %q: %#v", firstID, page)
	}
}

func TestListEventsSummariesUseOnlyCurrentAttempt(t *testing.T) {
	s := openTestStore(t)
	none := queryIngest(t, s, "none", time.UnixMilli(1000), nil)
	mixed := queryIngest(t, s, "mixed", time.UnixMilli(2000), []domain.NewDelivery{{TargetID: "one"}, {TargetID: "two"}})
	querySetState(t, s, mixed, "one", "unknown", "locating")
	querySetState(t, s, mixed, "two", "enqueued", "done")
	var currentAttempt string
	if err := s.db.QueryRow(`SELECT current_attempt_id FROM deliveries WHERE event_id = ? AND target_id = 'one'`, mixed).Scan(&currentAttempt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, created_at)
		SELECT 'historical', id, 'retry', 'enqueued', 'done', 1 FROM deliveries WHERE event_id = ? AND target_id = 'one'`, mixed); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]EventSummary{}
	for _, item := range page.Items {
		items[item.ID] = item
	}
	if got := items[none].DeliverySummary; got.Kind != "none" || got.Count != 0 || got.TransportStatus != nil || got.DeploymentStatus != nil {
		t.Fatalf("none summary = %#v", got)
	}
	got := items[mixed]
	if got.DeliverySummary.Kind != "mixed" || got.DeliverySummary.Count != 2 || got.DeliverySummary.TransportStatus != nil || got.DeliverySummary.DeploymentStatus != nil {
		t.Fatalf("mixed summary = %#v", got.DeliverySummary)
	}
	if !got.Active || !got.Attention {
		t.Fatalf("unknown+locating flags = active:%v attention:%v", got.Active, got.Attention)
	}
	_ = currentAttempt
}

func TestListEventsFiltersByTargetAndReturnsStablePerTargetSummaries(t *testing.T) {
	s := openTestStore(t)
	matched := queryIngest(t, s, "target-filter", time.UnixMilli(1000), []domain.NewDelivery{{TargetID: "z-target"}, {TargetID: "a-target"}})
	querySetState(t, s, matched, "a-target", "failed", "not_started")
	querySetState(t, s, matched, "z-target", "enqueued", "done")
	queryIngest(t, s, "other-target", time.UnixMilli(2000), []domain.NewDelivery{{TargetID: "other"}})

	target := "a-target"
	page, err := s.ListEvents(context.Background(), EventQuery{Target: &target, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != matched {
		t.Fatalf("target filter = %#v", page)
	}
	deliveries := page.Items[0].Deliveries
	if len(deliveries) != 2 || deliveries[0].TargetID != "a-target" || deliveries[1].TargetID != "z-target" {
		t.Fatalf("delivery order = %#v", deliveries)
	}
	if deliveries[0].ID == "" || deliveries[0].CurrentAttemptID == "" || deliveries[0].TransportStatus != "failed" || deliveries[0].DeploymentStatus != "not_started" || len(deliveries[0].AllowedOperations) != 1 || deliveries[0].AllowedOperations[0] != domain.OperationRetry {
		t.Fatalf("failed delivery = %#v", deliveries[0])
	}
	if deliveries[1].Active || deliveries[1].Attention || len(deliveries[1].AllowedOperations) != 0 {
		t.Fatalf("successful delivery = %#v", deliveries[1])
	}
}

func TestListEventsAppliesExactRepositoryAndOtherFilters(t *testing.T) {
	s := openTestStore(t)
	matchedCommand := canonicalCommand("gitlab", "gitlab-a", "app", "exact-matched")
	matchedCommand.CanonicalEvent.ExternalID = "pipeline-matched"
	matchedCommand.CanonicalEvent.Ref, matchedCommand.RuleID = "main", "rule-main"
	matched, err := s.Ingest(context.Background(), matchedCommand)
	if err != nil {
		t.Fatal(err)
	}
	unmatchedCommand := canonicalCommand("gitlab", "gitlab-a", "app", "exact-unmatched")
	unmatchedCommand.CanonicalEvent.ExternalID = "pipeline-unmatched"
	unmatchedCommand.CanonicalEvent.Ref, unmatchedCommand.RoutingResult = "main", domain.RoutingUnmatched
	if _, err := s.Ingest(context.Background(), unmatchedCommand); err != nil {
		t.Fatal(err)
	}
	otherCommand := canonicalCommand("gitlab", "gitlab-b", "app", "exact-other")
	otherCommand.CanonicalEvent.ExternalID = "pipeline-other"
	otherCommand.CanonicalEvent.Ref, otherCommand.RuleID = "dev", "rule-other"
	if _, err := s.Ingest(context.Background(), otherCommand); err != nil {
		t.Fatal(err)
	}

	unmatched := false
	source, repository, ref, rule := "gitlab-a", "app", "main", "rule-main"
	page, err := s.ListEvents(context.Background(), EventQuery{Source: &source, Repository: &repository, Unmatched: &unmatched, Ref: &ref, Rule: &rule, ID: matched.EventID, Page: 1, PageSize: 50})
	if err != nil || page.Total != 1 || page.Items[0].ID != matched.EventID {
		t.Fatalf("AND filters = %#v/%v", page, err)
	}
	page, err = s.ListEvents(context.Background(), EventQuery{ID: matched.EventID[:8], Page: 1, PageSize: 50})
	if err != nil || page.Total != 0 {
		t.Fatalf("partial ID matched = %#v/%v", page, err)
	}
	unmatched = true
	page, err = s.ListEvents(context.Background(), EventQuery{Source: &source, Repository: &repository, Unmatched: &unmatched, Page: 1, PageSize: 50})
	if err != nil || page.Total != 1 || page.Items[0].RoutingResult != "unmatched" {
		t.Fatalf("unmatched filter = %#v/%v", page, err)
	}
}

func TestListEventsRepositoryFiltersAndProjectionDoNotCrossSourcesOrProviders(t *testing.T) {
	s := openTestStore(t)
	for _, event := range []struct{ provider, source, delivery string }{
		{provider: "gitlab", source: "gitlab-a", delivery: "gitlab-a-event"},
		{provider: "gitlab", source: "gitlab-b", delivery: "gitlab-b-event"},
		{provider: "github", source: "github-a", delivery: "github-a-event"},
	} {
		command := canonicalCommand(event.provider, event.source, "app", event.delivery)
		command.CanonicalEvent.Ref = "main"
		command.CanonicalEvent.Status = "success"
		command.CanonicalEvent.Revision = "deadbeef"
		command.CanonicalEvent.CommitMessage = "fix checkout timeout"
		command.CanonicalEvent.ExternalID = "run-99"
		command.CanonicalEvent.Trigger = "push"
		if _, err := s.Ingest(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}

	source, repository := "gitlab-a", "app"
	page, err := s.ListEvents(context.Background(), EventQuery{Source: &source, Repository: &repository, Page: 1, PageSize: 50})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].SourceID != "gitlab-a" {
		t.Fatalf("repository query = %#v, %v", page, err)
	}
	item := page.Items[0]
	if item.Provider != "gitlab" || item.Repository != "app" || item.Ref == nil || *item.Ref != "main" || item.Status == nil || *item.Status != "success" || item.Revision == nil || *item.Revision != "deadbeef" || item.CommitMessage == nil || *item.CommitMessage != "fix checkout timeout" || item.ExternalID == nil || *item.ExternalID != "run-99" || item.Trigger == nil || *item.Trigger != "push" {
		t.Fatalf("canonical projection = %#v", item)
	}
	page, err = s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 50})
	if err != nil || page.Total != 3 {
		t.Fatalf("all histories = %#v/%v", page, err)
	}
}

func TestListEventsRequiresSourceAndRepositoryFiltersTogether(t *testing.T) {
	s := openTestStore(t)
	source, repository := "gitlab-a", "app"
	for _, query := range []EventQuery{
		{Source: &source, Page: 1, PageSize: 50},
		{Repository: &repository, Page: 1, PageSize: 50},
	} {
		if _, err := s.ListEvents(context.Background(), query); err == nil {
			t.Fatalf("ListEvents(%#v) error = nil", query)
		}
	}
}

func TestListEventsCollapsesPipelineUpdatesToNewestState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for index, status := range []string{"pending", "running", "success"} {
		command := canonicalCommand("gitlab", "gitlab-a", "app", fmt.Sprintf("delivery-%d", index))
		command.ReceivedAt = time.UnixMilli(int64(1000 + index))
		command.CanonicalEvent.Event = "pipeline"
		command.CanonicalEvent.ExternalID = "13952"
		command.CanonicalEvent.Status = status
		if _, err := s.Ingest(ctx, command); err != nil {
			t.Fatal(err)
		}
	}
	job := canonicalCommand("gitlab", "gitlab-a", "app", "job-delivery")
	job.ReceivedAt = time.UnixMilli(2000)
	job.CanonicalEvent.Event = "job"
	job.CanonicalEvent.ExternalID = "13952"
	job.CanonicalEvent.Status = "success"
	if _, err := s.Ingest(ctx, job); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListEvents(ctx, EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("collapsed page = %#v", page)
	}
	for _, item := range page.Items {
		if item.EventType == "pipeline" && (item.Status == nil || *item.Status != "success") {
			t.Fatalf("pipeline state = %#v", item)
		}
	}
}

func TestListEventsProjectsPushAndPipelineAsOneLifecycle(t *testing.T) {
	s := openTestStore(t)
	s.now = func() time.Time { return time.UnixMilli(1000).Add(time.Minute) }
	ctx := context.Background()
	push := canonicalCommand("github", "github-a", "app", "push-delivery")
	push.ReceivedAt = time.UnixMilli(1000)
	push.CanonicalEvent.Event = "push"
	push.CanonicalEvent.Status = ""
	push.CanonicalEvent.ExternalID = ""
	push.CanonicalEvent.Trigger = ""
	push.CanonicalEvent.Ref = "main"
	push.CanonicalEvent.Revision = "commit-1"
	pushResult, err := s.Ingest(ctx, push)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListEvents(ctx, EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != pushResult.EventID || page.Items[0].EventType != "push" || page.Items[0].Status == nil || *page.Items[0].Status != "waiting_for_pipeline" || !page.Active {
		t.Fatalf("waiting push lifecycle = %#v", page)
	}

	pipeline := canonicalCommand("github", "github-a", "app", "pipeline-delivery")
	pipeline.ReceivedAt = time.UnixMilli(2000)
	pipeline.CanonicalEvent.Ref = "main"
	pipeline.CanonicalEvent.Revision = "commit-1"
	pipeline.CanonicalEvent.ExternalID = "workflow-1"
	pipeline.CanonicalEvent.Trigger = "push"
	pipeline.CanonicalEvent.Status = "in_progress"
	pipelineResult, err := s.Ingest(ctx, pipeline)
	if err != nil {
		t.Fatal(err)
	}

	page, err = s.ListEvents(ctx, EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != pipelineResult.EventID || page.Items[0].EventType != "pipeline" || page.Items[0].Status == nil || *page.Items[0].Status != "in_progress" || !page.Active {
		t.Fatalf("running pipeline lifecycle = %#v", page)
	}
}

func TestListEventsStopsPollingWhenPushPipelineDoesNotArrive(t *testing.T) {
	s := openTestStore(t)
	s.now = func() time.Time { return time.UnixMilli(1000).Add(16 * time.Minute) }
	ctx := context.Background()
	push := canonicalCommand("github", "github-a", "app", "expired-push")
	push.ReceivedAt = time.UnixMilli(1000)
	push.CanonicalEvent.Event = "push"
	push.CanonicalEvent.Status = ""
	push.CanonicalEvent.ExternalID = ""
	push.CanonicalEvent.Trigger = ""
	push.CanonicalEvent.Ref = "main"
	push.CanonicalEvent.Revision = "expired-commit"
	pushResult, err := s.Ingest(ctx, push)
	if err != nil {
		t.Fatal(err)
	}

	page, err := s.ListEvents(ctx, EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != pushResult.EventID || page.Items[0].Status == nil || *page.Items[0].Status != "pipeline_not_received" || page.Items[0].Active || page.Active {
		t.Fatalf("expired push lifecycle = %#v", page)
	}
}

func TestListEventsDoesNotResolveANewerPushWithAnOlderPipeline(t *testing.T) {
	s := openTestStore(t)
	s.now = func() time.Time { return time.UnixMilli(2000).Add(time.Minute) }
	ctx := context.Background()
	pipeline := canonicalCommand("github", "github-a", "app", "older-pipeline")
	pipeline.ReceivedAt = time.UnixMilli(1000)
	pipeline.CanonicalEvent.Ref = "main"
	pipeline.CanonicalEvent.Revision = "reused-commit"
	pipeline.CanonicalEvent.ExternalID = "older-workflow"
	pipeline.CanonicalEvent.Trigger = "push"
	if _, err := s.Ingest(ctx, pipeline); err != nil {
		t.Fatal(err)
	}

	push := canonicalCommand("github", "github-a", "app", "newer-push")
	push.ReceivedAt = time.UnixMilli(2000)
	push.CanonicalEvent.Event = "push"
	push.CanonicalEvent.Status = ""
	push.CanonicalEvent.ExternalID = ""
	push.CanonicalEvent.Trigger = ""
	push.CanonicalEvent.Ref = "main"
	push.CanonicalEvent.Revision = "reused-commit"
	pushResult, err := s.Ingest(ctx, push)
	if err != nil {
		t.Fatal(err)
	}
	pipelineUpdate := pipeline
	pipelineUpdate.DeliveryID = "older-pipeline-update"
	pipelineUpdate.ReceivedAt = time.UnixMilli(3000)
	pipelineUpdate.CanonicalEvent.Status = "success"
	if _, err := s.Ingest(ctx, pipelineUpdate); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListEvents(ctx, EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	var waitingPush *EventSummary
	for index := range page.Items {
		if page.Items[index].ID == pushResult.EventID {
			waitingPush = &page.Items[index]
		}
	}
	if page.Total != 2 || waitingPush == nil || waitingPush.Status == nil || *waitingPush.Status != "waiting_for_pipeline" {
		t.Fatalf("newer push lifecycle = %#v", page)
	}
	detail, err := s.GetEventDetail(ctx, pushResult.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != pushResult.EventID || detail.EventType != "push" || detail.Status == nil || *detail.Status != "waiting_for_pipeline" {
		t.Fatalf("newer push detail = %#v", detail.EventSummary)
	}
}

func TestListEventsKeepsAPushWithItsOwnDelivery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	push := canonicalCommand("github", "github-a", "app", "deploying-push")
	push.ReceivedAt = time.UnixMilli(1000)
	push.CanonicalEvent.Event = "push"
	push.CanonicalEvent.Status = ""
	push.CanonicalEvent.ExternalID = ""
	push.CanonicalEvent.Trigger = ""
	push.CanonicalEvent.Ref = "main"
	push.CanonicalEvent.Revision = "deploying-commit"
	push.RoutingResult = domain.RoutingDeploy
	push.Deliveries = []domain.NewDelivery{{TargetID: "production"}}
	pushResult, err := s.Ingest(ctx, push)
	if err != nil {
		t.Fatal(err)
	}
	querySetState(t, s, pushResult.EventID, "production", "enqueued", "done")

	pipeline := canonicalCommand("github", "github-a", "app", "deploying-pipeline")
	pipeline.ReceivedAt = time.UnixMilli(2000)
	pipeline.CanonicalEvent.Ref = "main"
	pipeline.CanonicalEvent.Revision = "deploying-commit"
	pipeline.CanonicalEvent.ExternalID = "deploying-workflow"
	pipeline.CanonicalEvent.Trigger = "push"
	if _, err := s.Ingest(ctx, pipeline); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListEvents(ctx, EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 || page.Items[1].ID != pushResult.EventID || page.Items[1].Status != nil || page.Items[1].Active || len(page.Items[1].Deliveries) != 1 || page.Items[1].Deliveries[0].TargetID != "production" {
		t.Fatalf("push delivery lifecycle = %#v", page)
	}
	detail, err := s.GetEventDetail(ctx, pushResult.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != pushResult.EventID || len(detail.Deliveries) != 1 || detail.Deliveries[0].TargetID != "production" {
		t.Fatalf("push delivery detail = %#v", detail)
	}
	pipelineDetail, err := s.GetEventDetail(ctx, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pipelineDetail.SourceStates) != 1 || pipelineDetail.SourceStates[0].Status != "success" {
		t.Fatalf("pipeline incorrectly absorbed deployment push = %#v", pipelineDetail.SourceStates)
	}
}

func TestListEventsPollsAllNormalizedActivePipelineStatuses(t *testing.T) {
	for _, status := range []string{"created", "preparing", "scheduled", "pending", "queued", "waiting_for_resource", "requested", "running", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			s := openTestStore(t)
			command := canonicalCommand("gitlab", "gitlab-a", "app", "pipeline-"+status)
			command.CanonicalEvent.Status = status
			if _, err := s.Ingest(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			page, err := s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 50})
			if err != nil {
				t.Fatal(err)
			}
			if !page.Active || !page.Items[0].Active {
				t.Fatalf("status %q stopped polling: %#v", status, page)
			}
		})
	}
}

func TestGetEventDetailReturnsRedactedDecodedStableTimelines(t *testing.T) {
	s := openTestStore(t)
	command := ingestParams("detail", []domain.NewDelivery{{TargetID: "prod", TargetSnapshot: []byte(`{"base_url":"https://private","resource_id":"secret"}`)}})
	command.ReceivedAt = time.UnixMilli(1000)
	command.CanonicalEvent = domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-a", Repository: "app", Event: "pipeline", Ref: "main", Status: "success", ExternalID: "99", Trigger: "push", Revision: "deadbeef"}
	command.RuleID = "deploy-main"
	command.HeadersJSON = []byte(`{"Authorization":"Bearer secret","safe":"ok"}`)
	command.PayloadJSON = []byte(`{"nested":{"Api_Key":"hidden"},"items":[{"PASSWORD":"hidden"}]}`)
	command.RuleSnapshot = []byte(`{"SecretValue":"hidden","match":"main"}`)
	result, err := s.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	var deliveryID, initialID string
	if err := s.db.QueryRow(`SELECT id, current_attempt_id FROM deliveries WHERE event_id = ?`, result.EventID).Scan(&deliveryID, &initialID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE delivery_attempts SET transport_status='failed', deployment_status='not_started', request_json=?, response_json=?, request_at=1000, response_at=1001 WHERE id=?`,
		[]byte(`{"headers":{"X-Api-Key":"hidden"},"composeId":"must-not-leak"}`), []byte(`{"TOKEN":"hidden","ok":true,"error":"remote failure detail"}`), initialID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO delivery_attempts (id, delivery_id, kind, actor, transport_status, deployment_status, created_at)
		VALUES ('attempt-z', ?, 'retry', 'operator', 'unknown', 'locating', 2000), ('attempt-a', ?, 'retry', 'operator', 'failed', 'not_started', 2000)`, deliveryID, deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE deliveries SET current_attempt_id='attempt-z', transport_status='unknown', deployment_status='locating' WHERE id=?`, deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO audit_logs (id,event_id,delivery_id,attempt_id,actor,action,before_json,after_json,created_at)
		VALUES ('audit-z',?,?,?,'operator','manual_retry',?, ?,3000), ('audit-a',?,?,?,'operator','manual_retry','malformed',?,3000)`,
		result.EventID, deliveryID, initialID, []byte(`{"Authorization":"hidden"}`), []byte(`{"safe":true}`),
		result.EventID, deliveryID, initialID, []byte(`[{"Password":"hidden"}]`)); err != nil {
		t.Fatal(err)
	}

	detail, err := s.GetEventDetail(context.Background(), result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"Bearer secret", "hidden", "https://private", "must-not-leak", "remote failure detail"} {
		if contains(text, forbidden) {
			t.Fatalf("detail leaked %q: %s", forbidden, text)
		}
	}
	if len(detail.Deliveries) != 1 || detail.Deliveries[0].TargetID != "prod" || !detail.Deliveries[0].Active || !detail.Deliveries[0].Attention {
		t.Fatalf("delivery = %#v", detail.Deliveries)
	}
	if got := string(detail.Deliveries[0].TargetSnapshot); got != `{"base_url":"https://private","resource_id":"secret"}` {
		t.Fatalf("target snapshot = %s", got)
	}
	attempts := detail.Deliveries[0].Attempts
	if len(attempts) != 3 || attempts[0].ID != initialID || attempts[1].ID != "attempt-a" || attempts[2].ID != "attempt-z" || !attempts[2].Current {
		t.Fatalf("attempt order/current = %#v", attempts)
	}
	if len(detail.Activities) != 2 || detail.Activities[0].ID != "audit-a" || detail.Activities[1].ID != "audit-z" {
		t.Fatalf("activity order = %#v", detail.Activities)
	}
	if got, ok := detail.Activities[0].Before.(string); !ok || got != "[invalid JSON]" {
		t.Fatalf("malformed JSON fallback = %#v", detail.Activities[0].Before)
	}
}

func TestGetEventDetailBuildsCredentialSafeCurlFromVersionedDeploymentEvidence(t *testing.T) {
	// Break caught: losing reproducible deployment evidence, exposing a stored credential, or guessing from legacy snapshots.
	s := openTestStore(t)
	targetSnapshot := []byte(`{"binding_version":2,"id":"prod","type":"dokploy","resource_type":"compose","resource_id":"compose-1","connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`)
	command := ingestParams("deploy-curl", []domain.NewDelivery{{TargetID: "prod", TargetSnapshot: targetSnapshot}})
	result, err := s.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	var attemptID string
	if err := s.db.QueryRow(`SELECT current_attempt_id FROM deliveries WHERE event_id = ?`, result.EventID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	requestJSON := `{"version":1,"method":"POST","url":"https://dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1","description":"attempt_id=` + attemptID + `"}}`
	if _, err := s.db.Exec(`UPDATE delivery_attempts SET request_json = ?, request_at = 1000 WHERE id = ?`, requestJSON, attemptID); err != nil {
		t.Fatal(err)
	}

	detail, err := s.GetEventDetail(context.Background(), result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Deliveries) != 1 || len(detail.Deliveries[0].Attempts) != 1 {
		t.Fatalf("deliveries = %#v", detail.Deliveries)
	}
	want := "curl --request POST \\\n" +
		"  --url 'https://dokploy.example.invalid/api/compose.deploy' \\\n" +
		"  --header 'Content-Type: application/json' \\\n" +
		"  --header \"x-api-key: ${DOKPLOY_API_KEY}\" \\\n" +
		`  --data '{"composeId":"compose-1","description":"attempt_id=` + attemptID + `"}'`
	if got := detail.Deliveries[0].Attempts[0].DeployCommand; got == nil || *got != want {
		t.Fatalf("deploy command = %q\nwant           = %q", nullableValue(got), want)
	}
	encoded, err := json.Marshal(detail.Deliveries[0].Attempts[0].RequestSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"dokploy.example.invalid", "compose-1"} {
		if strings.Contains(string(encoded), hidden) {
			t.Fatalf("request snapshot exposed %q: %s", hidden, encoded)
		}
	}
}

func TestDeploymentCurlRejectsUntrustedOrLegacyEvidence(t *testing.T) {
	// Break caught: rendering a credential-bearing, altered, malformed, or inferred request as an executable command.
	validSnapshot := []byte(`{"binding_version":2,"id":"prod","type":"dokploy","resource_type":"compose","resource_id":"compose-1","connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`)
	validEvidence := `{"version":1,"method":"POST","url":"https://dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1"}}`
	for _, test := range []struct {
		raw      string
		snapshot []byte
		recorded bool
	}{
		{`{"composeId":"legacy"}`, validSnapshot, true},
		{`{"version":2,"method":"POST","url":"https://dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1"}}`, validSnapshot, true},
		{`{"version":1,"method":"GET","url":"https://dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1"}}`, validSnapshot, true},
		{`{"version":1,"method":"POST","url":"https://user@dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1"}}`, validSnapshot, true},
		{`{"version":1,"method":"POST","url":"https://dokploy.example.invalid/api/other","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1"}}`, validSnapshot, true},
		{`{"version":1,"method":"POST","url":"https://dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json","x-api-key":"stored-secret"},"body":{"composeId":"compose-1"}}`, validSnapshot, true},
		{`{"version":1,"method":"POST","url":"https://dokploy.example.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1","token":"stored-secret"}}`, validSnapshot, true},
		{`{"version":1`, validSnapshot, true},
		{validEvidence, validSnapshot, false},
		{validEvidence, []byte(`{"binding_version":2,"id":"prod","type":"dokploy","resource_type":"compose","resource_id":"compose-1","connection":{"id":"primary","base_url":"https://attacker.example.invalid"}}`), true},
		{validEvidence, []byte(`{"binding_version":2,"id":"prod","type":"dokploy","resource_type":"compose","resource_id":"compose-other","connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`), true},
		{validEvidence, []byte(`{"binding_version":2,"id":"prod","type":"other","resource_type":"compose","resource_id":"compose-1","connection":{"id":"primary","base_url":"https://dokploy.example.invalid"}}`), true},
	} {
		if got := deploymentCurl([]byte(test.raw), test.snapshot, test.recorded); got != nil {
			t.Errorf("deploymentCurl(%s) = %q", test.raw, *got)
		}
	}
}

func nullableValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestGetEventDetailReturnsCorrelatedPipelineStateHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	var latestID string
	for index, status := range []string{"pending", "running", "success"} {
		command := canonicalCommand("gitlab", "gitlab-a", "app", fmt.Sprintf("pipeline-update-%d", index))
		command.ReceivedAt = time.UnixMilli(int64(1000 + index))
		command.CanonicalEvent.ExternalID = "13952"
		command.CanonicalEvent.Status = status
		result, err := s.Ingest(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		latestID = result.EventID
	}

	detail, err := s.GetEventDetail(ctx, latestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.SourceStates) != 3 {
		t.Fatalf("source states = %#v", detail.SourceStates)
	}
	for index, status := range []string{"pending", "running", "success"} {
		if detail.SourceStates[index].Status != status || detail.SourceStates[index].EventID == "" {
			t.Fatalf("source state %d = %#v", index, detail.SourceStates[index])
		}
	}
}

func TestGetNonPushPipelineDetailDoesNotIncludePushWaitingState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	push := canonicalCommand("github", "github-a", "app", "manual-pipeline-push")
	push.ReceivedAt = time.UnixMilli(1000)
	push.CanonicalEvent.Event = "push"
	push.CanonicalEvent.Status = ""
	push.CanonicalEvent.ExternalID = ""
	push.CanonicalEvent.Trigger = ""
	push.CanonicalEvent.Ref = "main"
	push.CanonicalEvent.Revision = "manual-pipeline-commit"
	if _, err := s.Ingest(ctx, push); err != nil {
		t.Fatal(err)
	}

	pipeline := canonicalCommand("github", "github-a", "app", "manual-pipeline")
	pipeline.ReceivedAt = time.UnixMilli(2000)
	pipeline.CanonicalEvent.Ref = "main"
	pipeline.CanonicalEvent.Revision = "manual-pipeline-commit"
	pipeline.CanonicalEvent.ExternalID = "manual-workflow"
	pipeline.CanonicalEvent.Trigger = "manual"
	pipeline.CanonicalEvent.Status = "success"
	result, err := s.Ingest(ctx, pipeline)
	if err != nil {
		t.Fatal(err)
	}

	detail, err := s.GetEventDetail(ctx, result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.SourceStates) != 1 || detail.SourceStates[0].EventID != result.EventID || detail.SourceStates[0].Status != "success" {
		t.Fatalf("manual pipeline source states = %#v", detail.SourceStates)
	}
}

func TestGetPushDetailFollowsItsPipelineLifecycle(t *testing.T) {
	s := openTestStore(t)
	s.now = func() time.Time { return time.UnixMilli(1000).Add(time.Minute) }
	ctx := context.Background()
	push := canonicalCommand("github", "github-a", "app", "push-detail")
	push.ReceivedAt = time.UnixMilli(1000)
	push.CanonicalEvent.Event = "push"
	push.CanonicalEvent.Status = ""
	push.CanonicalEvent.ExternalID = ""
	push.CanonicalEvent.Trigger = ""
	push.CanonicalEvent.Ref = "main"
	push.CanonicalEvent.Revision = "commit-detail"
	pushResult, err := s.Ingest(ctx, push)
	if err != nil {
		t.Fatal(err)
	}

	waiting, err := s.GetEventDetail(ctx, pushResult.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.ID != pushResult.EventID || waiting.EventType != "push" || waiting.Status == nil || *waiting.Status != "waiting_for_pipeline" || !waiting.Active {
		t.Fatalf("waiting push detail = %#v", waiting.EventSummary)
	}

	var latestPipelineID string
	for index, status := range []string{"pending", "running"} {
		pipeline := canonicalCommand("github", "github-a", "app", fmt.Sprintf("pipeline-detail-%d", index))
		pipeline.ReceivedAt = time.UnixMilli(int64(2000 + index))
		pipeline.CanonicalEvent.Ref = "main"
		pipeline.CanonicalEvent.Revision = "commit-detail"
		pipeline.CanonicalEvent.ExternalID = "workflow-detail"
		pipeline.CanonicalEvent.Trigger = "push"
		pipeline.CanonicalEvent.Status = status
		result, err := s.Ingest(ctx, pipeline)
		if err != nil {
			t.Fatal(err)
		}
		latestPipelineID = result.EventID
	}

	detail, err := s.GetEventDetail(ctx, pushResult.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != latestPipelineID || detail.EventType != "pipeline" || detail.Status == nil || *detail.Status != "running" || !detail.Active {
		t.Fatalf("resolved pipeline detail = %#v", detail.EventSummary)
	}
	if len(detail.SourceStates) != 3 || detail.SourceStates[0].EventID != pushResult.EventID || detail.SourceStates[0].Status != "waiting_for_pipeline" || detail.SourceStates[1].Status != "pending" || detail.SourceStates[2].Status != "running" {
		t.Fatalf("push pipeline source states = %#v", detail.SourceStates)
	}
}

func TestGetPipelineDetailFollowsItsLatestState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	var firstPipelineID, latestPipelineID string
	for index, status := range []string{"pending", "running", "success"} {
		pipeline := canonicalCommand("gitlab", "gitlab-a", "app", fmt.Sprintf("follow-pipeline-%d", index))
		pipeline.ReceivedAt = time.UnixMilli(int64(1000 + index))
		pipeline.CanonicalEvent.Ref = "main"
		pipeline.CanonicalEvent.Revision = "follow-commit"
		pipeline.CanonicalEvent.ExternalID = "follow-workflow"
		pipeline.CanonicalEvent.Trigger = "push"
		pipeline.CanonicalEvent.Status = status
		result, err := s.Ingest(ctx, pipeline)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			firstPipelineID = result.EventID
		}
		latestPipelineID = result.EventID
	}

	detail, err := s.GetEventDetail(ctx, firstPipelineID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != latestPipelineID || detail.Status == nil || *detail.Status != "success" || detail.Active {
		t.Fatalf("latest pipeline detail = %#v", detail.EventSummary)
	}
}

func TestGetEventDetailRedactsGitLabPipelineVariablesWithoutChangingEvidence(t *testing.T) {
	// Break caught: exposing GitLab Pipeline Hook variable values or camel/hyphen API keys in the management projection.
	s := openTestStore(t)
	payload := []byte(`{
		"object_kind":"pipeline",
		"object_attributes":{
			"id":88,
			"ref":"main",
			"status":"success",
			"variables":[
				{"key":"PUBLIC_RELEASE_CHANNEL","value":"stable"},
				{"key":"DEPLOY_PASSWORD","value":"variable-secret-value"}
			]
		},
		"project":{"id":4,"name":"example"},
		"apiKey":"camel-secret-value",
		"api-key":"hyphen-secret-value",
		"ordinary":"visible"
	}`)
	command := ingestParams("pipeline-variable-redaction", nil)
	command.PayloadJSON = payload
	result, err := s.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}

	var stored []byte
	if err := s.db.QueryRow(`SELECT raw_payload_json FROM events WHERE id = ?`, result.EventID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !contains(string(stored), "variable-secret-value") || !contains(string(stored), "camel-secret-value") || !contains(string(stored), "hyphen-secret-value") {
		t.Fatalf("raw evidence was changed: %s", stored)
	}

	detail, err := s.GetEventDetail(context.Background(), result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(detail.Payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"stable", "variable-secret-value", "camel-secret-value", "hyphen-secret-value"} {
		if contains(string(encoded), forbidden) {
			t.Fatalf("management payload leaked %q: %s", forbidden, encoded)
		}
	}
	for _, retained := range []string{"PUBLIC_RELEASE_CHANNEL", "DEPLOY_PASSWORD", `"ordinary":"visible"`, `"ref":"main"`} {
		if !contains(string(encoded), retained) {
			t.Fatalf("management payload lost %q: %s", retained, encoded)
		}
	}
}

func TestDecodedRedactedNeverEchoesInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "malformed secret", raw: `{"Authorization":"Bearer secret"`},
		{name: "trailing remote URL", raw: `{"safe":true} remote error https://private.invalid`},
		{name: "plain remote error", raw: `Dokploy failed at https://private.invalid with api_key=secret`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, removeInfrastructure := range []bool{false, true} {
				got := decodedRedacted([]byte(test.raw), removeInfrastructure)
				if got != "[invalid JSON]" {
					t.Fatalf("decodedRedacted() = %#v", got)
				}
			}
		})
	}
}

func TestMissingEventStringsRemainNullInListAndDetail(t *testing.T) {
	s := openTestStore(t)
	command := ingestParams("null-fields", nil)
	command.CanonicalEvent = domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-a", Repository: "app", Event: "pipeline"}
	result, err := s.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE events SET ref = NULL, status = NULL WHERE id = ?`, result.EventID); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListEvents(context.Background(), EventQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetEventDetail(context.Background(), result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"list": page.Items[0], "detail": detail.EventSummary} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"ref", "status", "external_id", "trigger", "revision"} {
			if !strings.Contains(string(encoded), `"`+field+`":null`) {
				t.Fatalf("%s %s did not preserve null: %s", name, field, encoded)
			}
		}
	}
}

func TestEventDeliveriesClosesRowsAfterScanError(t *testing.T) {
	s := openTestStore(t)
	eventID := queryIngest(t, s, "delivery-scan-error", time.UnixMilli(1000), []domain.NewDelivery{{TargetID: "prod"}})
	if _, err := s.db.Exec(`UPDATE deliveries SET created_at = 'not-a-number' WHERE event_id = ?`, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEventDetail(context.Background(), eventID); err == nil || !strings.Contains(err.Error(), "scan event delivery") {
		t.Fatalf("GetEventDetail() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("Ready() after scan error = %v", err)
	}
}

func TestEventDeliveriesSortByTargetThenID(t *testing.T) {
	s := openTestStore(t)
	eventID := queryIngest(t, s, "delivery-order", time.UnixMilli(1000), []domain.NewDelivery{{TargetID: "z-target"}, {TargetID: "a-target"}})
	if _, err := s.db.Exec(`UPDATE deliveries SET created_at = CASE target_id WHEN 'z-target' THEN 1000 ELSE 2000 END WHERE event_id = ?`, eventID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetEventDetail(context.Background(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Deliveries) != 2 || detail.Deliveries[0].TargetID != "a-target" || detail.Deliveries[1].TargetID != "z-target" {
		t.Fatalf("delivery order = %#v", detail.Deliveries)
	}
}

func queryIngest(t *testing.T, s *Store, key string, at time.Time, deliveries []domain.NewDelivery) string {
	t.Helper()
	command := ingestParams(key, deliveries)
	command.ReceivedAt = at
	command.CanonicalEvent.Source = "gitlab-a"
	command.CanonicalEvent.Repository = "app"
	command.CanonicalEvent.ExternalID = key
	result, err := s.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	return result.EventID
}

func querySetState(t *testing.T, s *Store, eventID, target, transport, deployment string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE delivery_attempts SET transport_status=?, deployment_status=?
		WHERE id=(SELECT current_attempt_id FROM deliveries WHERE event_id=? AND target_id=?)`, transport, deployment, eventID, target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE deliveries SET transport_status=?, deployment_status=? WHERE event_id=? AND target_id=?`, transport, deployment, eventID, target); err != nil {
		t.Fatal(err)
	}
}

func contains(text, part string) bool {
	for index := 0; index+len(part) <= len(text); index++ {
		if text[index:index+len(part)] == part {
			return true
		}
	}
	return false
}
