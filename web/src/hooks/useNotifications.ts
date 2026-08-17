import { useCallback, useEffect, useRef, useState } from "react";
import { listNotifications, openNotificationStream } from "../api";
import { i18n } from "../i18n";
import { appendFacts, clearNotificationHistory, defaultNotificationState, markAllRead, markRead, NOTIFICATION_STORAGE_KEY, notificationFactEnabled, parseNotificationState, resetRepositoryPreferences, setNotificationStatus } from "../notifications";
import type { NotificationFact, NotificationStatusKey, Repository, StoredNotificationState } from "../types";

export type SystemNotificationState = "unavailable" | NotificationPermission;

export interface NotificationController {
  state: StoredNotificationState;
  unreadCount: number;
  error: Error | null;
  systemState: SystemNotificationState;
  setStatusEnabled: (status: NotificationStatusKey, enabled: boolean, repository?: Repository) => void;
  resetRepositoryPreferences: (repository: Repository) => void;
  enableSystemNotifications: () => Promise<void>;
  sendTestSystemNotification: (title: string, body: string) => void;
  markRead: (id: string) => void;
  markAllRead: () => void;
  clearHistory: () => void;
}

function readInitialState(): StoredNotificationState {
  try { return parseNotificationState(localStorage.getItem(NOTIFICATION_STORAGE_KEY)); } catch { return defaultNotificationState(); }
}

function permissionState(): SystemNotificationState {
  return typeof Notification === "undefined" || !window.isSecureContext ? "unavailable" : Notification.permission;
}

function systemNotificationTitle(fact: NotificationFact, repositories: readonly Repository[]): string {
  const configured = repositories.find((repository) => repository.provider === fact.provider && repository.source_id === fact.source_id && repository.id === fact.repository);
  const repositoryName = (configured?.name || fact.repository).replace(/^#+/, "");
  const category = i18n.t(`notifications.categories.${fact.category}`);
  if (fact.category === "push") return `#${repositoryName} · ${category}`;
  return `#${repositoryName} · ${category} · ${i18n.t(`notifications.outcomes.${fact.outcome}`)}`;
}

export function useNotifications(onOpenEvent: (eventID: string) => void, repositories: readonly Repository[] = []): NotificationController {
  const [state, setState] = useState(readInitialState);
  const [error, setError] = useState<Error | null>(null);
  const [systemState, setSystemState] = useState<SystemNotificationState>(permissionState);
  const [streamRevision, setStreamRevision] = useState(0);
  const stateRef = useRef(state);
  const streamRevisionRef = useRef(0);
  const openRef = useRef(onOpenEvent);
  const repositoriesRef = useRef(repositories);
  stateRef.current = state;
  openRef.current = onOpenEvent;
  repositoriesRef.current = repositories;

  const update = useCallback((change: (current: StoredNotificationState) => StoredNotificationState) => {
    const next = change(stateRef.current);
    stateRef.current = next;
    try { localStorage.setItem(NOTIFICATION_STORAGE_KEY, JSON.stringify(next)); } catch { /* browser storage is best effort */ }
    setState(next);
    return next;
  }, []);

  useEffect(() => {
    const syncPermission = () => setSystemState(permissionState());
    const onVisibility = () => { if (document.visibilityState === "visible") syncPermission(); };
    window.addEventListener("focus", syncPermission);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.removeEventListener("focus", syncPermission);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, []);

  useEffect(() => {
    const revision = streamRevision;
    let disposed = false;
    let source: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;
    const live = () => !disposed && revision === streamRevisionRef.current;

    const acceptFacts = (facts: NotificationFact[], latestCursor?: string) => {
      if (facts.length === 0) return;
      const current = stateRef.current;
      const seen = new Set(current.items.map((item) => item.id));
      const accepted = facts.filter((fact) => {
        if (seen.has(fact.id) || !notificationFactEnabled(current, fact)) return false;
        seen.add(fact.id);
        return true;
      });
      const cursor = latestCursor || facts[facts.length - 1].cursor;
      update(() => ({ ...appendFacts(current, accepted), cursor: cursor || current.cursor }));
      if (current.system_enabled && permissionState() === "granted") {
        for (const fact of accepted) {
          try {
            const notification = new Notification(systemNotificationTitle(fact, repositoriesRef.current), { body: fact.summary, tag: fact.id });
            notification.onclick = () => { window.focus(); openRef.current(fact.event_id); notification.close?.(); };
          } catch { /* in-app notifications remain available */ }
        }
      }
    };

    const openStream = () => {
      source = openNotificationStream(stateRef.current.cursor || undefined);
      source.addEventListener("fact", (event) => {
        if (!live()) return;
        try {
          acceptFacts([JSON.parse((event as MessageEvent<string>).data) as NotificationFact]);
        } catch { /* malformed payloads are skipped */ }
      });
      source.onopen = () => { if (live()) setError(null); };
      source.onerror = () => { if (live()) setError(new Error("Notification feed failed")); };
    };

    // The REST feed establishes the baseline, then the stream pushes new facts.
    const baseline = () => {
      if (!live()) return;
      const current = stateRef.current;
      void listNotifications(current.cursor || undefined).then((feed) => {
        if (!live()) return;
        failures = 0;
        setError(null);
        if (!current.initialized) {
          update((value) => ({ ...value, initialized: true, cursor: feed.latest_cursor }));
        } else {
          acceptFacts(feed.items, feed.latest_cursor);
        }
        openStream();
      }, (reason: unknown) => {
        if (!live()) return;
        failures += 1;
        setError(reason instanceof Error ? reason : new Error("Notification feed failed"));
        retryTimer = setTimeout(baseline, failures === 1 ? 5_000 : failures === 2 ? 10_000 : 30_000);
      });
    };

    baseline();
    return () => {
      disposed = true;
      if (retryTimer) clearTimeout(retryTimer);
      source?.close();
    };
  }, [streamRevision, update]);

  const enableSystemNotifications = useCallback(async () => {
    if (permissionState() === "unavailable") { setSystemState("unavailable"); return; }
    const permission = await Notification.requestPermission();
    setSystemState(permission);
    update((current) => ({ ...current, system_enabled: permission === "granted" }));
  }, [update]);

  const sendTestSystemNotification = useCallback((title: string, body: string) => {
    const permission = permissionState();
    setSystemState(permission);
    if (permission !== "granted" || !stateRef.current.system_enabled) return;
    try { new Notification(title, { body }); } catch { /* the visible guidance remains available */ }
  }, []);

  return {
    state,
    unreadCount: state.unread_ids.length,
    error,
    systemState,
    setStatusEnabled: (status, enabled, repository) => update((current) => setNotificationStatus(current, status, enabled, repository)),
    resetRepositoryPreferences: (repository) => update((current) => resetRepositoryPreferences(current, repository)),
    enableSystemNotifications,
    sendTestSystemNotification,
    markRead: (id) => update((current) => markRead(current, id)),
    markAllRead: () => update(markAllRead),
    clearHistory: () => {
      streamRevisionRef.current += 1;
      update(clearNotificationHistory);
      setStreamRevision(streamRevisionRef.current);
    },
  };
}
