import type { NotificationFact, NotificationPreferences, NotificationStatusKey, Repository, StoredNotificationState } from "./types";

export const NOTIFICATION_STORAGE_KEY = "hookfly.notifications.v1";

const defaultPreferences: NotificationPreferences = {
  "push.received": false,
  "pipeline.initializing": false,
  "pipeline.waiting": false,
  "pipeline.running": false,
  "pipeline.success": true,
  "pipeline.failure": true,
  "pipeline.cancelled": false,
  "deployment.pending": false,
  "deployment.triggering": false,
  "deployment.running": false,
  "deployment.success": true,
  "deployment.failure": true,
  "deployment.timeout": true,
  "deployment.cancelled": true,
};

type RepositoryPreferenceIdentity = Pick<Repository, "provider" | "source_id"> & ({ repository: string; id?: string } | { repository?: string; id: string });

export function notificationStatusKey(fact: Pick<NotificationFact, "category" | "outcome">): NotificationStatusKey {
  return `${fact.category}.${fact.outcome}` as NotificationStatusKey;
}

export function defaultNotificationState(): StoredNotificationState {
  return {
    version: 3,
    initialized: false,
    cursor: "",
    items: [],
    unread_ids: [],
    preferences: { ...defaultPreferences },
    repository_preferences: {},
    system_enabled: false,
  };
}

export function parseNotificationState(raw: string | null): StoredNotificationState {
  if (!raw) return defaultNotificationState();
  try {
    const value = JSON.parse(raw) as {
      version?: number;
      initialized?: boolean;
      cursor?: string;
      items?: NotificationFact[];
      unread_ids?: string[];
      preferences?: Record<string, boolean>;
      repository_preferences?: Record<string, Record<string, boolean>>;
      system_enabled?: boolean;
    };
    if (!Array.isArray(value.items) || !Array.isArray(value.unread_ids) || !value.preferences) return defaultNotificationState();
    const preferences = value.version === 1 ? migrateV1Preferences(value.preferences) : sanitizePreferences(value.preferences);
    if (value.version !== 1 && value.version !== 2 && value.version !== 3) return defaultNotificationState();
    const items = value.version === 1 ? value.items.map(migrateV1Fact).slice(-100) : value.items.slice(-100);
    const repository_preferences = value.version === 3 ? sanitizeRepositoryPreferences(value.repository_preferences) : {};
    return { ...defaultNotificationState(), ...value, version: 3, preferences, repository_preferences, items, unread_ids: value.unread_ids.filter((id) => items.some((item) => item.id === id)) };
  } catch {
    return defaultNotificationState();
  }
}

function migrateV1Fact(fact: NotificationFact): NotificationFact {
  const legacy = fact as unknown as Omit<NotificationFact, "category" | "outcome"> & { category: string; outcome: string };
  if (legacy.category === "pipeline") return { ...fact, category: "pipeline", outcome: "initializing" };
  if (legacy.category === "triggered") return { ...fact, category: "deployment", outcome: "triggering" };
  if (legacy.category === "success") return { ...fact, category: "deployment", outcome: "success" };
  if (legacy.category === "failure") {
    const outcome = legacy.outcome === "timeout" || legacy.outcome === "cancelled" ? legacy.outcome : "failure";
    return { ...fact, category: "deployment", outcome };
  }
  return fact;
}

function sanitizePreferences(value: Record<string, boolean>): NotificationPreferences {
  return Object.fromEntries(Object.entries(defaultPreferences).map(([key, fallback]) => [key, typeof value[key] === "boolean" ? value[key] : fallback])) as NotificationPreferences;
}

function sanitizeRepositoryPreferences(value: unknown): Record<string, NotificationPreferences> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  return Object.fromEntries(Object.entries(value).flatMap(([key, preferences]) => {
    if (!preferences || typeof preferences !== "object" || Array.isArray(preferences)) return [];
    return [[key, sanitizePreferences(preferences as Record<string, boolean>)]];
  }));
}

function migrateV1Preferences(value: Record<string, boolean>): NotificationPreferences {
  const pipeline = value.pipeline ?? false;
  const triggered = value.triggered ?? false;
  const success = value.success ?? true;
  const failure = value.failure ?? true;
  return sanitizePreferences({
    "push.received": false,
    "pipeline.initializing": pipeline,
    "pipeline.waiting": pipeline,
    "pipeline.running": pipeline,
    "pipeline.success": success,
    "pipeline.failure": failure,
    "pipeline.cancelled": failure,
    "deployment.pending": triggered,
    "deployment.triggering": triggered,
    "deployment.running": triggered,
    "deployment.success": success,
    "deployment.failure": failure,
    "deployment.timeout": failure,
    "deployment.cancelled": failure,
  });
}

export function appendFacts(state: StoredNotificationState, facts: NotificationFact[]): StoredNotificationState {
  const seen = new Set(state.items.map((item) => item.id));
  const added = facts.filter((item) => {
    if (!notificationFactEnabled(state, item) || seen.has(item.id)) return false;
    seen.add(item.id);
    return true;
  });
  const items = [...state.items, ...added].slice(-100);
  const retained = new Set(items.map((item) => item.id));
  return {
    ...state,
    items,
    unread_ids: [...state.unread_ids.filter((id) => retained.has(id)), ...added.map((item) => item.id)].filter((id, index, all) => all.indexOf(id) === index),
  };
}

export function markRead(state: StoredNotificationState, id: string): StoredNotificationState {
  return { ...state, unread_ids: state.unread_ids.filter((candidate) => candidate !== id) };
}

export function markAllRead(state: StoredNotificationState): StoredNotificationState {
  return { ...state, unread_ids: [] };
}

export function clearNotificationHistory(state: StoredNotificationState): StoredNotificationState {
  return { ...state, cursor: "", items: [], unread_ids: [] };
}

export function repositoryPreferenceKey(identity: RepositoryPreferenceIdentity): string {
  return JSON.stringify([identity.provider, identity.source_id, identity.repository ?? identity.id]);
}

export function effectiveNotificationPreferences(state: StoredNotificationState, identity: RepositoryPreferenceIdentity): NotificationPreferences {
  return sanitizePreferences(state.repository_preferences[repositoryPreferenceKey(identity)] ?? state.preferences);
}

export function setNotificationStatus(state: StoredNotificationState, status: NotificationStatusKey, enabled: boolean, identity?: RepositoryPreferenceIdentity): StoredNotificationState {
  if (!identity) return { ...state, preferences: { ...sanitizePreferences(state.preferences), [status]: enabled } };
  const key = repositoryPreferenceKey(identity);
  return { ...state, repository_preferences: { ...state.repository_preferences, [key]: { ...effectiveNotificationPreferences(state, identity), [status]: enabled } } };
}

export function resetRepositoryPreferences(state: StoredNotificationState, identity: RepositoryPreferenceIdentity): StoredNotificationState {
  const repository_preferences = { ...state.repository_preferences };
  delete repository_preferences[repositoryPreferenceKey(identity)];
  return { ...state, repository_preferences };
}

export function hasRepositoryPreferences(state: StoredNotificationState, identity: RepositoryPreferenceIdentity): boolean {
  return Object.hasOwn(state.repository_preferences, repositoryPreferenceKey(identity));
}

export function notificationFactEnabled(state: StoredNotificationState, fact: NotificationFact): boolean {
  const identity = repositoryIdentityForFact(fact);
  if (!identity) return sanitizePreferences(state.preferences)[notificationStatusKey(fact)];
  return effectiveNotificationPreferences(state, identity)[notificationStatusKey(fact)];
}

function repositoryIdentityForFact(fact: NotificationFact): RepositoryPreferenceIdentity | undefined {
  if ((fact.provider !== "github" && fact.provider !== "gitlab" && fact.provider !== "harbor") || typeof fact.source_id !== "string" || !fact.source_id.trim() || typeof fact.repository !== "string" || !fact.repository.trim()) return undefined;
  return { provider: fact.provider, source_id: fact.source_id, repository: fact.repository };
}
