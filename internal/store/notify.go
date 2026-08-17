package store

// SubscribeNotifications registers a listener woken whenever a commit may have
// created notification facts. The channel holds at most one pending signal:
// repeated changes coalesce because a single wake-up already triggers a full
// re-query. Call the returned function to unsubscribe.
func (s *Store) SubscribeNotifications() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.notifyMu.Lock()
	if s.notifySubs == nil {
		s.notifySubs = map[chan struct{}]struct{}{}
	}
	s.notifySubs[ch] = struct{}{}
	s.notifyMu.Unlock()
	return ch, func() {
		s.notifyMu.Lock()
		delete(s.notifySubs, ch)
		s.notifyMu.Unlock()
	}
}

// notifyChanged wakes stream subscribers after a commit that may have created
// notification facts. Sends never block: a full buffer means a wake-up is
// already pending.
func (s *Store) notifyChanged() {
	s.notifyMu.Lock()
	for ch := range s.notifySubs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.notifyMu.Unlock()
}
