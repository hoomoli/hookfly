import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defaultNotificationState, NOTIFICATION_STORAGE_KEY } from "../notifications";
import type { NotificationFact, Repository } from "../types";
import { useNotifications } from "./useNotifications";
import type { NotificationFeed } from "../types";

const feeds: NotificationFeed[] = [];
const deferredFeeds: Array<Promise<NotificationFeed>> = [];
const requestedCursors: Array<string | undefined> = [];

vi.mock("../api", () => ({
  listNotifications: vi.fn(async (after?: string) => {
    requestedCursors.push(after);
    return deferredFeeds.shift() ?? feeds.shift() ?? { items: [], latest_cursor: "" };
  }),
}));

function feed(id: string, cursor = id): NotificationFeed {
  return { latest_cursor: cursor, items: id ? [{ id, cursor, category: "deployment", outcome: "success", event_id: `event-${id}`, delivery_id: `delivery-${id}`, target_id: `target-${id}`, repository: "app", summary: "Deployment succeeded", occurred_at: "2026-08-12T00:00:00Z" }] : [] };
}

function pushFact(id: string, repository: string): NotificationFact {
  return { id, cursor: id, category: "push", outcome: "received", event_id: `event-${id}`, delivery_id: `delivery-${id}`, target_id: `target-${id}`, provider: "github", source_id: "github-a", repository, summary: `${repository} received a push`, occurred_at: "2026-08-12T00:00:00Z" };
}

const appRepository: Repository = { provider: "github", source_id: "github-a", id: "app", name: "app" };

describe("useNotifications", () => {
  beforeEach(() => { localStorage.clear(); feeds.length = 0; deferredFeeds.length = 0; requestedCursors.length = 0; });
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

  it("establishes a baseline without replay and appends later facts once", async () => {
    feeds.push(feed("old", "old"), feed("new", "new"), feed("new", "new"));
    const { result } = renderHook(() => useNotifications(() => undefined, 100));
    await waitFor(() => expect(result.current.state.cursor).toBe("old"));
    expect(result.current.state.items).toEqual([]);
    await waitFor(() => expect(result.current.state.items.map((item) => item.id)).toEqual(["new"]));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(result.current.state.items.map((item) => item.id)).toEqual(["new"]);
  });

  it("does not skip the first fact after an empty baseline", async () => {
    feeds.push(feed("", ""), feed("first", "first"));
    const { result } = renderHook(() => useNotifications(() => undefined, 100));
    await waitFor(() => expect(result.current.state.initialized).toBe(true));
    await waitFor(() => expect(result.current.state.items.map((item) => item.id)).toEqual(["first"]));
  });

  it("requests system permission only from enable and emits future notifications", async () => {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    const requestPermission = vi.fn(async () => "granted" as NotificationPermission);
    class FakeNotification {
      static permission: NotificationPermission = "default";
      static requestPermission = requestPermission;
      onclick: (() => void) | null = null;
      constructor(title: string, options?: NotificationOptions) { created.push({ title, options }); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    feeds.push(feed("", "base"), feed("system", "system"));
    const open = vi.fn();
    const { result } = renderHook(() => useNotifications(open, 100));
    await waitFor(() => expect(result.current.state.cursor).toBe("base"));
    expect(requestPermission).not.toHaveBeenCalled();
    await act(async () => { await result.current.enableSystemNotifications(); });
    FakeNotification.permission = "granted";
    expect(requestPermission).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]).toEqual({ title: "target-system · success", options: { body: "Deployment succeeded", tag: "system" } });
  });

  it("refreshes browser notification permission when the page becomes visible", async () => {
    class FakeNotification {
      static permission: NotificationPermission = "default";
      static requestPermission = vi.fn();
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    deferredFeeds.push(new Promise(() => undefined), new Promise(() => undefined));
    const { result } = renderHook(() => useNotifications(() => undefined, 60_000));

    expect(result.current.systemState).toBe("default");
    FakeNotification.permission = "denied";
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    act(() => { document.dispatchEvent(new Event("visibilitychange")); });

    expect(result.current.systemState).toBe("denied");
  });

  it("refreshes browser notification permission when the window regains focus", () => {
    class FakeNotification {
      static permission: NotificationPermission = "denied";
      static requestPermission = vi.fn();
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    deferredFeeds.push(new Promise(() => undefined));
    const { result } = renderHook(() => useNotifications(() => undefined, 60_000));

    expect(result.current.systemState).toBe("denied");
    FakeNotification.permission = "granted";
    act(() => { window.dispatchEvent(new Event("focus")); });

    expect(result.current.systemState).toBe("granted");
  });

  it("sends a test notification only when browser permission and Hookfly are enabled", () => {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    class FakeNotification {
      static permission: NotificationPermission = "granted";
      static requestPermission = vi.fn();
      constructor(title: string, options?: NotificationOptions) { created.push({ title, options }); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    const stored = defaultNotificationState();
    localStorage.setItem(NOTIFICATION_STORAGE_KEY, JSON.stringify({ ...stored, initialized: true, system_enabled: true }));
    deferredFeeds.push(new Promise(() => undefined));
    const { result } = renderHook(() => useNotifications(() => undefined, 60_000));

    act(() => { result.current.sendTestSystemNotification("Hookfly test", "Notifications can reach this device."); });

    expect(created).toEqual([{ title: "Hookfly test", options: { body: "Notifications can reach this device.", tag: "hookfly-notification-test" } }]);
  });

  it("uses a repository override for both unread facts and system notifications", async () => {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    class FakeNotification {
      static permission: NotificationPermission = "granted";
      static requestPermission = vi.fn();
      onclick: (() => void) | null = null;
      constructor(title: string, options?: NotificationOptions) { created.push({ title, options }); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    const stored = defaultNotificationState();
    localStorage.setItem(NOTIFICATION_STORAGE_KEY, JSON.stringify({
      ...stored,
      initialized: true,
      system_enabled: true,
      repository_preferences: { '["github","github-a","app"]': { ...stored.preferences, "push.received": true } },
    }));
    feeds.push({ latest_cursor: "next", items: [pushFact("app-push", "app"), pushFact("other-push", "other")] });

    const { result } = renderHook(() => useNotifications(() => undefined, 100));

    await waitFor(() => expect(result.current.state.items.map((item) => item.id)).toEqual(["app-push"]));
    expect(result.current.unreadCount).toBe(1);
    await waitFor(() => expect(created).toEqual([{ title: "target-app-push · received", options: { body: "app received a push", tag: "app-push" } }]));
  });

  it("uses response-time repository preferences for both in-app and system notifications", async () => {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    class FakeNotification {
      static permission: NotificationPermission = "granted";
      static requestPermission = vi.fn();
      onclick: (() => void) | null = null;
      constructor(title: string, options?: NotificationOptions) { created.push({ title, options }); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    const stored = defaultNotificationState();
    localStorage.setItem(NOTIFICATION_STORAGE_KEY, JSON.stringify({
      ...stored,
      initialized: true,
      system_enabled: true,
      preferences: { ...stored.preferences, "push.received": true },
      repository_preferences: { '["github","github-a","app"]': { ...stored.preferences, "push.received": true } },
    }));
    let resolveFeed!: (feed: NotificationFeed) => void;
    deferredFeeds.push(new Promise((resolve) => { resolveFeed = resolve; }));
    const { result } = renderHook(() => useNotifications(() => undefined, 60_000));

    act(() => { result.current.setStatusEnabled("push.received", false, appRepository); });
    await act(async () => { resolveFeed({ latest_cursor: "next", items: [pushFact("app-push", "app")] }); });

    await waitFor(() => expect(result.current.state.cursor).toBe("next"));
    expect(result.current.state.items).toEqual([]);
    expect(result.current.unreadCount).toBe(0);
    expect(created).toEqual([]);
  });

  it("ignores a pre-clear feed and restarts polling from the empty cursor", async () => {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    class FakeNotification {
      static permission: NotificationPermission = "granted";
      static requestPermission = vi.fn();
      onclick: (() => void) | null = null;
      constructor(title: string, options?: NotificationOptions) { created.push({ title, options }); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    const stored = defaultNotificationState();
    localStorage.setItem(NOTIFICATION_STORAGE_KEY, JSON.stringify({
      ...stored,
      initialized: true,
      cursor: "old-cursor",
      system_enabled: true,
    }));
    let resolveOld!: (feed: NotificationFeed) => void;
    let resolveFresh!: (feed: NotificationFeed) => void;
    deferredFeeds.push(
      new Promise((resolve) => { resolveOld = resolve; }),
      new Promise((resolve) => { resolveFresh = resolve; }),
    );
    const { result } = renderHook(() => useNotifications(() => undefined, 60_000));

    act(() => { result.current.clearHistory(); });
    await act(async () => { resolveOld(feed("stale", "stale-cursor")); });

    expect(result.current.state.cursor).toBe("");
    expect(result.current.state.items).toEqual([]);
    expect(result.current.unreadCount).toBe(0);
    expect(created).toEqual([]);
    expect(JSON.parse(localStorage.getItem(NOTIFICATION_STORAGE_KEY) ?? "{}")).toMatchObject({ cursor: "", items: [], unread_ids: [] });
    await waitFor(() => expect(requestedCursors).toEqual(["old-cursor", undefined]));

    await act(async () => { resolveFresh(feed("fresh", "fresh-cursor")); });
    await waitFor(() => expect(result.current.state.items.map((item) => item.id)).toEqual(["fresh"]));
    expect(result.current.state.cursor).toBe("fresh-cursor");
    expect(result.current.unreadCount).toBe(1);
    expect(created).toEqual([{ title: "target-fresh · success", options: { body: "Deployment succeeded", tag: "fresh" } }]);
  });

  it("does not repeat a system notification for a fact already retained", async () => {
    const created: Array<{ title: string; options?: NotificationOptions }> = [];
    class FakeNotification {
      static permission: NotificationPermission = "granted";
      static requestPermission = vi.fn();
      onclick: (() => void) | null = null;
      constructor(title: string, options?: NotificationOptions) { created.push({ title, options }); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    const stored = defaultNotificationState();
    localStorage.setItem(NOTIFICATION_STORAGE_KEY, JSON.stringify({
      ...stored,
      initialized: true,
      system_enabled: true,
      preferences: { ...stored.preferences, "push.received": true },
      items: [pushFact("retained-push", "app")],
      unread_ids: ["retained-push"],
    }));
    feeds.push({ latest_cursor: "next", items: [pushFact("retained-push", "app")] });

    const { result } = renderHook(() => useNotifications(() => undefined, 60_000));

    await waitFor(() => expect(result.current.state.cursor).toBe("next"));
    expect(result.current.state.items.map((item) => item.id)).toEqual(["retained-push"]);
    expect(created).toEqual([]);
  });

  it("persists and resets repository-specific notification settings", async () => {
    const { result } = renderHook(() => useNotifications(() => undefined, 60_000));
    await waitFor(() => expect(result.current.state.initialized).toBe(true));

    act(() => { result.current.setStatusEnabled("push.received", true, appRepository); });
    const withOverride = JSON.parse(localStorage.getItem(NOTIFICATION_STORAGE_KEY) ?? "{}") as ReturnType<typeof defaultNotificationState>;
    expect(withOverride.repository_preferences['["github","github-a","app"]']["push.received"]).toBe(true);

    act(() => { result.current.resetRepositoryPreferences(appRepository); });
    const reset = JSON.parse(localStorage.getItem(NOTIFICATION_STORAGE_KEY) ?? "{}") as ReturnType<typeof defaultNotificationState>;
    expect(reset.repository_preferences['["github","github-a","app"]']).toBeUndefined();
  });
});
