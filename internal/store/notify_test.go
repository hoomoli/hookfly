package store

import (
	"context"
	"testing"
)

func TestSubscribeNotificationsCoalescesSignalsAndStopsAfterUnsubscribe(t *testing.T) {
	s := openTestStore(t)
	ch, unsubscribe := s.SubscribeNotifications()

	s.notifyChanged()
	s.notifyChanged()
	select {
	case <-ch:
	default:
		t.Fatal("expected a pending notification signal")
	}
	select {
	case <-ch:
		t.Fatal("expected repeated signals to coalesce")
	default:
	}

	unsubscribe()
	s.notifyChanged()
	select {
	case <-ch:
		t.Fatal("unexpected signal after unsubscribe")
	default:
	}
}

func TestIngestSignalsNotificationSubscribers(t *testing.T) {
	s := openTestStore(t)
	ch, unsubscribe := s.SubscribeNotifications()
	defer unsubscribe()

	if _, err := s.Ingest(context.Background(), canonicalCommand("github", "production", "owner/repository", "delivery-notify")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("expected ingest to signal notification subscribers")
	}
}
