import { describe, expect, it } from "vitest";
import { appendFacts, clearNotificationHistory, defaultNotificationState, effectiveNotificationPreferences, hasRepositoryPreferences, markAllRead, markRead, parseNotificationState, repositoryPreferenceKey, resetRepositoryPreferences, setNotificationStatus } from "./notifications";
import type { NotificationFact, NotificationStatusKey } from "./types";

function fact(id: string, category: NotificationFact["category"] = "deployment", outcome: NotificationFact["outcome"] = "success"): NotificationFact {
  return { id, cursor: `cursor-${id}`, category, outcome, event_id: `event-${id}`, delivery_id: `delivery-${id}`, target_id: `target-${id}`, provider: "github", source_id: "github-a", repository: "app", summary: "summary", occurred_at: "2026-08-12T00:00:00Z" };
}

describe("notification state", () => {
  it("clears history without changing notification preferences", () => {
    const item = fact("stored");
    const base = defaultNotificationState();
    const state = {
      ...base,
      initialized: true,
      cursor: "cursor-stored",
      items: [item],
      unread_ids: [item.id],
      preferences: { ...base.preferences, "push.received": true },
      repository_preferences: {
        [repositoryPreferenceKey({ provider: "github", source_id: "github-a", repository: "app" })]: {
          ...base.preferences,
          "deployment.success": false,
        },
      },
      system_enabled: true,
    };

    expect(clearNotificationHistory(state)).toEqual({
      ...state,
      cursor: "",
      items: [],
      unread_ids: [],
    });
  });

  it("uses quiet defaults and survives malformed storage", () => {
    const state = defaultNotificationState();
    expect(state.version).toBe(3);
    expect(state.preferences).toEqual({
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
      "push.received": false,
    });
    expect(state.repository_preferences).toEqual({});
    expect(state.initialized).toBe(false);
    expect(parseNotificationState("broken")).toEqual(state);
  });

  it("deduplicates enabled facts, drops disabled facts, and retains the newest 100", () => {
    const state = defaultNotificationState();
    const facts = [fact("disabled", "pipeline", "running"), ...Array.from({ length: 101 }, (_, index) => fact(String(index))), fact("100")];
    const next = appendFacts(state, facts);
    expect(next.items).toHaveLength(100);
    expect(next.items[0].id).toBe("1");
    expect(next.items.at(-1)?.id).toBe("100");
    expect(next.items.some((item) => item.id === "disabled")).toBe(false);
  });

  it("migrates v1 preferences without deleting retained notification history", () => {
    const { provider: _provider, source_id: _sourceID, ...retained } = { ...fact("retained", "deployment", "success"), category: "failure", outcome: "timeout" };
    const migrated = parseNotificationState(JSON.stringify({
      version: 1,
      initialized: true,
      cursor: "legacy-cursor",
      items: [retained],
      unread_ids: [retained.id],
      preferences: { pipeline: true, triggered: false, success: false, failure: true },
      system_enabled: false,
    }));
    expect(migrated.version).toBe(3);
    expect(migrated.items).toEqual([{ ...retained, category: "deployment", outcome: "timeout" }]);
    expect(migrated.unread_ids).toEqual([retained.id]);
    expect(migrated.preferences["pipeline.initializing"]).toBe(true);
    expect(migrated.preferences["pipeline.success"]).toBe(false);
    expect(migrated.preferences["deployment.pending"]).toBe(false);
    expect(migrated.preferences["deployment.failure"]).toBe(true);
    expect(migrated.preferences["push.received"]).toBe(false);
    expect(migrated.repository_preferences).toEqual({});
  });

  it("migrates v2 state without deleting retained notification history", () => {
    const retained = fact("retained");
    const migrated = parseNotificationState(JSON.stringify({
      version: 2,
      initialized: true,
      cursor: "legacy-cursor",
      items: [retained],
      unread_ids: [retained.id],
      preferences: { "deployment.success": false },
      system_enabled: false,
    }));
    expect(migrated.version).toBe(3);
    expect(migrated.items).toEqual([retained]);
    expect(migrated.unread_ids).toEqual([retained.id]);
    expect(migrated.preferences["deployment.success"]).toBe(false);
    expect(migrated.preferences["push.received"]).toBe(false);
    expect(migrated.repository_preferences).toEqual({});
  });

  it("sanitizes complete v3 repository preference snapshots", () => {
    const retained = fact("retained");
    const key = '["github","github-a","app"]';
    const migrated = parseNotificationState(JSON.stringify({
      version: 3,
      initialized: true,
      cursor: "current-cursor",
      items: [retained],
      unread_ids: [retained.id],
      preferences: { "deployment.success": false },
      repository_preferences: {
        [key]: { "pipeline.running": true, unknown: true },
        broken: true,
      },
      system_enabled: false,
    }));
    expect(migrated.repository_preferences[key]).toEqual({ ...defaultNotificationState().preferences, "pipeline.running": true });
    expect("unknown" in migrated.repository_preferences[key]).toBe(false);
    expect(migrated.repository_preferences.broken).toBeUndefined();
  });

  it("filters each fact by its category and normalized status", () => {
    const state = defaultNotificationState();
    const preferences = { ...state.preferences, "deployment.running": true };
    const next = appendFacts({ ...state, preferences }, [
      fact("pipeline-running", "pipeline", "running"),
      fact("deployment-running", "deployment", "running"),
      fact("deployment-success", "deployment", "success"),
    ]);
    expect(next.items.map((item) => item.id)).toEqual(["deployment-running", "deployment-success"]);
    expect(Object.keys(preferences) as NotificationStatusKey[]).toContain("deployment.running");
  });

  it("keeps a complete repository override unchanged after global preferences change", () => {
    const repository = { provider: "github" as const, source_id: "github-a", repository: "app" };
    const global = setNotificationStatus(defaultNotificationState(), "deployment.running", true);
    const custom = setNotificationStatus(global, "pipeline.running", true, repository);
    const key = repositoryPreferenceKey(repository);
    expect(custom.repository_preferences[key]["pipeline.running"]).toBe(true);
    expect(custom.repository_preferences[key]["push.received"]).toBe(false);
    expect(custom.repository_preferences[key]["deployment.running"]).toBe(true);

    const changedGlobal = setNotificationStatus(custom, "push.received", true);
    expect(changedGlobal.repository_preferences[key]).toEqual(custom.repository_preferences[key]);
    expect(effectiveNotificationPreferences(changedGlobal, repository)["pipeline.running"]).toBe(true);
    expect(effectiveNotificationPreferences(changedGlobal, repository)["push.received"]).toBe(false);
  });

  it("resets a repository override to the latest global preferences", () => {
    const repository = { provider: "github" as const, source_id: "github-a", repository: "app" };
    const custom = setNotificationStatus(defaultNotificationState(), "pipeline.running", true, repository);
    const changedGlobal = setNotificationStatus(custom, "pipeline.running", false);
    const reset = resetRepositoryPreferences(changedGlobal, repository);
    expect(hasRepositoryPreferences(reset, repository)).toBe(false);
    expect(reset.repository_preferences[repositoryPreferenceKey(repository)]).toBeUndefined();
    expect(effectiveNotificationPreferences(reset, repository)).toEqual(changedGlobal.preferences);
  });

  it("filters otherwise equivalent facts by their repository preferences", () => {
    const enabled = { provider: "github" as const, source_id: "github-a", repository: "app" };
    const state = setNotificationStatus(defaultNotificationState(), "pipeline.running", true, enabled);
    const other = { ...fact("other", "pipeline", "running"), source_id: "github-b" };
    const sameSourceDifferentRepository = { ...fact("same-source", "pipeline", "running"), repository: "worker" };
    const next = appendFacts(state, [{ ...fact("enabled", "pipeline", "running") }, other, sameSourceDifferentRepository]);
    expect(next.items.map((item) => item.id)).toEqual(["enabled"]);
  });

  it("applies repository preferences to Harbor artifact notifications", () => {
    const harbor = { provider: "harbor" as const, source_id: "harbor-a", repository: "application-image" };
    const state = setNotificationStatus(defaultNotificationState(), "push.received", true, harbor);
    const artifact = { ...fact("artifact", "push", "received"), ...harbor };

    expect(appendFacts(state, [artifact]).items.map((item) => item.id)).toEqual(["artifact"]);
    expect(repositoryPreferenceKey(harbor)).toBe('["harbor","harbor-a","application-image"]');
  });

  it("uses global preferences for facts with an incomplete repository identity", () => {
    const globallyEnabled = setNotificationStatus(defaultNotificationState(), "pipeline.running", true);
    const malformedOverride = {
      ...globallyEnabled,
      repository_preferences: { '["github","github-a",null]': { ...globallyEnabled.preferences, "pipeline.running": false } },
    };
    const malformedFact = { ...fact("incomplete", "pipeline", "running"), repository: undefined } as unknown as NotificationFact;
    expect(appendFacts(malformedOverride, [malformedFact]).items.map((item) => item.id)).toEqual(["incomplete"]);
  });

  it("marks one or every notification read without changing facts", () => {
    const state = appendFacts(defaultNotificationState(), [fact("a"), fact("b")]);
    expect(state.unread_ids).toEqual(["a", "b"]);
    expect(markRead(state, "a").unread_ids).toEqual(["b"]);
    expect(markAllRead(state).unread_ids).toEqual([]);
  });
});
