package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestListNotificationsReturnsTargetFactsInStableCursorOrder(t *testing.T) {
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), domain.IngestCommand{
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-a", Repository: "app", Event: "pipeline", Status: "success"},
		DeliveryID:     "provider-1", ReceivedAt: time.UnixMilli(1000), RoutingResult: domain.RoutingDeploy,
		Deliveries: []domain.NewDelivery{{TargetID: "api"}, {TargetID: "worker"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT id,current_attempt_id,target_id FROM deliveries WHERE event_id=? ORDER BY target_id`, result.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type delivery struct{ id, attempt, target string }
	var deliveries []delivery
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.id, &d.attempt, &d.target); err != nil {
			t.Fatal(err)
		}
		deliveries = append(deliveries, d)
	}
	for index, d := range deliveries {
		action, deployment := "deployment_done", "done"
		if index == 1 {
			action, deployment = "deployment_timeout", "timeout"
		}
		if _, err := s.db.Exec(`INSERT INTO audit_logs(id,event_id,delivery_id,attempt_id,action,before_json,after_json,created_at) VALUES(?,?,?,?,?,'{"transport_status":"enqueued","deployment_status":"running"}',?,2000)`,
			"audit-"+d.target, result.EventID, d.id, d.attempt, action, `{"transport_status":"enqueued","deployment_status":"`+deployment+`"}`); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListNotifications(context.Background(), NotificationQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("items = %#v", page.Items)
	}
	want := []struct{ category, outcome, target string }{
		{"pipeline", "success", ""}, {"deployment", "pending", "api"}, {"deployment", "pending", "worker"},
		{"deployment", "success", "api"}, {"deployment", "timeout", "worker"},
	}
	for index, expected := range want {
		got := page.Items[index]
		if got.Category != expected.category || got.Outcome != expected.outcome || got.TargetID != expected.target || got.EventID != result.EventID || got.Provider != "gitlab" || got.SourceID != "gitlab-a" {
			t.Fatalf("item %d = %#v, want %#v", index, got, expected)
		}
	}
	if page.LatestCursor == "" {
		t.Fatal("latest cursor is empty")
	}
	after, err := s.ListNotifications(context.Background(), NotificationQuery{After: page.Items[2].Cursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 2 || after.Items[0].Outcome != "success" || after.Items[1].Outcome != "timeout" {
		t.Fatalf("after cursor = %#v", after.Items)
	}
}

func TestListNotificationsEmitsIdentifiedPushFactsWithoutRouting(t *testing.T) {
	s := openTestStore(t)
	identified := canonicalCommand("github", "github-a", "app", "identified-push")
	identified.CanonicalEvent.Event = "push"
	identified.RoutingResult = domain.RoutingUnmatched
	if _, err := s.Ingest(context.Background(), identified); err != nil {
		t.Fatal(err)
	}
	emptyRepository := canonicalCommand("github", "github-a", "", "empty-repository-push")
	emptyRepository.CanonicalEvent.Event = "push"
	emptyRepository.RoutingResult = domain.RoutingUnmatched
	if _, err := s.Ingest(context.Background(), emptyRepository); err != nil {
		t.Fatal(err)
	}

	page, err := s.ListNotifications(context.Background(), NotificationQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Category != "push" || page.Items[0].Outcome != "received" {
		t.Fatalf("push facts = %#v", page.Items)
	}
	if page.Items[0].Provider != "github" || page.Items[0].SourceID != "github-a" || page.Items[0].Repository != "app" {
		t.Fatalf("push identity = %#v", page.Items[0])
	}
}

func TestListNotificationsNormalizesPipelineStatusesAndIgnoresUnknown(t *testing.T) {
	s := openTestStore(t)
	statuses := []string{"created", "waiting_for_resource", "in_progress", "success", "failure", "canceled", "new-provider-state"}
	for index, status := range statuses {
		command := canonicalCommand("gitlab", "gitlab-a", "app", fmt.Sprintf("pipeline-%d", index))
		command.CanonicalEvent.Status = status
		command.ReceivedAt = time.UnixMilli(int64(index + 1))
		if _, err := s.Ingest(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListNotifications(context.Background(), NotificationQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []string
	for _, item := range page.Items {
		outcomes = append(outcomes, item.Outcome)
	}
	want := []string{"initializing", "waiting", "running", "success", "failure", "cancelled"}
	if fmt.Sprint(outcomes) != fmt.Sprint(want) {
		t.Fatalf("outcomes = %v, want %v", outcomes, want)
	}
}

func TestListNotificationsEmitsOnlyNormalizedDeploymentTransitions(t *testing.T) {
	s := openTestStore(t)
	result, err := s.Ingest(context.Background(), domain.IngestCommand{
		CanonicalEvent: domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-a", Repository: "app", Event: "pipeline", Status: "success"},
		DeliveryID:     "provider-transition", ReceivedAt: time.UnixMilli(1000), RoutingResult: domain.RoutingDeploy,
		Deliveries: []domain.NewDelivery{{TargetID: "api"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var deliveryID, attemptID string
	if err := s.db.QueryRow(`SELECT id,current_attempt_id FROM deliveries WHERE event_id=?`, result.EventID).Scan(&deliveryID, &attemptID); err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		id, beforeTransport, beforeDeployment, afterTransport, afterDeployment string
	}{
		{"sending", "pending", "not_started", "sending", "not_started"},
		{"enqueued", "sending", "not_started", "enqueued", "locating"},
		{"running", "enqueued", "locating", "enqueued", "running"},
		{"done", "enqueued", "running", "enqueued", "done"},
	}
	for index, transition := range transitions {
		before := fmt.Sprintf(`{"transport_status":%q,"deployment_status":%q}`, transition.beforeTransport, transition.beforeDeployment)
		after := fmt.Sprintf(`{"transport_status":%q,"deployment_status":%q}`, transition.afterTransport, transition.afterDeployment)
		if _, err := s.db.Exec(`INSERT INTO audit_logs(id,event_id,delivery_id,attempt_id,action,before_json,after_json,created_at) VALUES(?,?,?,?,?,?,?,?)`,
			transition.id, result.EventID, deliveryID, attemptID, transition.id, before, after, 2000+index); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListNotifications(context.Background(), NotificationQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []string
	for _, item := range page.Items {
		if item.Category == "deployment" {
			outcomes = append(outcomes, item.Outcome)
		}
	}
	want := []string{"pending", "triggering", "running", "success"}
	if fmt.Sprint(outcomes) != fmt.Sprint(want) {
		t.Fatalf("deployment outcomes = %v, want %v", outcomes, want)
	}
}

func TestListNotificationsRejectsMalformedCursorAndIgnoresNoise(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.ListNotifications(context.Background(), NotificationQuery{After: "broken", Limit: 10}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	command := canonicalCommand("gitlab", "gitlab-a", "", "unmatched")
	command.RoutingResult = domain.RoutingUnmatched
	if _, err := s.Ingest(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListNotifications(context.Background(), NotificationQuery{Limit: 101})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("noise = %#v", page.Items)
	}
}

func TestListNotificationsBaselineCursorSkipsHistoryBeyondLimit(t *testing.T) {
	s := openTestStore(t)
	for index := 0; index < 101; index++ {
		command := canonicalCommand("gitlab", "gitlab-a", "app", fmt.Sprintf("history-%03d", index))
		command.ReceivedAt = time.UnixMilli(int64(index + 1))
		if _, err := s.Ingest(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListNotifications(context.Background(), NotificationQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 100 {
		t.Fatalf("items = %d", len(page.Items))
	}
	after, err := s.ListNotifications(context.Background(), NotificationQuery{After: page.LatestCursor, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 0 {
		t.Fatalf("baseline left historical facts = %#v", after.Items)
	}
}
