import { useCallback, useEffect, useRef, useState } from "react";
import { listNotifications } from "../api";
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

export function useNotifications(onOpenEvent: (eventID: string) => void, pollInterval = 5_000, repositories: readonly Repository[] = []): NotificationController {
  const [state, setState] = useState(readInitialState);
  const [error, setError] = useState<Error | null>(null);
  const [systemState, setSystemState] = useState<SystemNotificationState>(permissionState);
  const [pollRevision, setPollRevision] = useState(0);
  const stateRef = useRef(state);
  const pollRevisionRef = useRef(0);
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
    const revision = pollRevision;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;
    const schedule = (delay: number) => { if (!disposed && revision === pollRevisionRef.current) timer = setTimeout(run, delay); };
    const run = () => {
      if (revision !== pollRevisionRef.current) return;
      if (timer) clearTimeout(timer);
      const current = stateRef.current;
      void listNotifications(current.cursor || undefined).then((feed) => {
        if (disposed || revision !== pollRevisionRef.current) return;
        failures = 0;
        setError(null);
        if (!current.initialized) {
          update((value) => ({ ...value, initialized: true, cursor: feed.latest_cursor }));
        } else {
          const responseState = stateRef.current;
          const seen = new Set(responseState.items.map((item) => item.id));
          const accepted = feed.items.filter((fact) => {
            if (seen.has(fact.id) || !notificationFactEnabled(responseState, fact)) return false;
            seen.add(fact.id);
            return true;
          });
          update(() => ({ ...appendFacts(responseState, accepted), cursor: feed.latest_cursor || responseState.cursor }));
          if (responseState.system_enabled && permissionState() === "granted") {
            for (const fact of accepted) {
              try {
                const notification = new Notification(systemNotificationTitle(fact, repositoriesRef.current), { body: fact.summary, tag: fact.id });
                notification.onclick = () => { window.focus(); openRef.current(fact.event_id); notification.close?.(); };
              } catch { /* in-app notifications remain available */ }
            }
          }
        }
        schedule(pollInterval);
      }, (reason: unknown) => {
        if (disposed || revision !== pollRevisionRef.current) return;
        failures += 1;
        setError(reason instanceof Error ? reason : new Error("Notification feed failed"));
        schedule(failures === 1 ? 5_000 : failures === 2 ? 10_000 : 30_000);
      });
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        if (timer) clearTimeout(timer);
        run();
      }
    };
    document.addEventListener("visibilitychange", onVisibility);
    run();
    return () => { disposed = true; if (timer) clearTimeout(timer); document.removeEventListener("visibilitychange", onVisibility); };
  }, [pollInterval, pollRevision, update]);

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
      pollRevisionRef.current += 1;
      update(clearNotificationHistory);
      setPollRevision(pollRevisionRef.current);
    },
  };
}
