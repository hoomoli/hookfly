import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defaultNotificationState, NOTIFICATION_STORAGE_KEY } from "../notifications";
import { i18n } from "../i18n";
import type { NotificationFact, NotificationFeed, Repository } from "../types";
import { useNotifications } from "./useNotifications";

const feeds: NotificationFeed[] = [];
const deferredFeeds: Array<Promise<NotificationFeed>> = [];
const feedErrors: Error[] = [];
const requestedCursors: Array<string | undefined> = [];

class FakeStream {
  static instances: FakeStream[] = [];
  readonly after: string | undefined;
  closed = false;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private readonly factListeners = new Set<(event: MessageEvent<string>) => void>();

  constructor(after?: string) {
    this.after = after;
    FakeStream.instances.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void) {
    if (type === "fact") this.factListeners.add(listener);
  }

  removeEventListener(type: string, listener: (event: MessageEvent<string>) => void) {
    if (type === "fact") this.factListeners.delete(listener);
  }

  close() {
    this.closed = true;
  }

  open() {
    this.onopen?.();
  }

  fail() {
    this.onerror?.();
  }

  emit(fact: NotificationFact) {
    const event = { data: JSON.stringify(fact) } as MessageEvent<string>;
    for (const listener of this.factListeners) listener(event);
  }
}

vi.mock("../api", () => ({
  listNotifications: vi.fn(async (after?: string) => {
    requestedCursors.push(after);
    const error = feedErrors.shift();
    if (error) throw error;
    return deferredFeeds.shift() ?? feeds.shift() ?? { items: [], latest_cursor: "" };
  }),
  openNotificationStream: vi.fn((after?: string) => new FakeStream(after) as unknown as EventSource),
}));

function fact(id: string, cursor = id): NotificationFact {
  return { id, cursor, category: "deployment", outcome: "success", event_id: `event-${id}`, delivery_id: `delivery-${id}`, target_id: `target-${id}`, repository: "app", summary: "Deployment succeeded", occurred_at: "2026-08-12T00:00:00Z" };
}

function feed(id: string, cursor = id): NotificationFeed {
  return { latest_cursor: cursor, items: id ? [fact(id, cursor)] : [] };
}

function pushFact(id: string, repository: string): NotificationFact {
  return { id, cursor: id, category: "push", outcome: "received", event_id: `event-${id}`, delivery_id: `delivery-${id}`, target_id: `target-${id}`, provider: "github", source_id: "github-a", repository, summary: `${repository} received a push`, occurred_at: "2026-08-12T00:00:00Z" };
}

const appRepository: Repository = { provider: "github", source_id: "github-a", id: "app", name: "app" };

function latestStream(): FakeStream {
  const stream = FakeStream.instances[FakeStream.instances.length - 1];
  expect(stream).toBeDefined();
  return stream;
}

describe("useNotifications", () => {
  beforeEach(() => {
    localStorage.clear();
    feeds.length = 0;
    deferredFeeds.length = 0;
    feedErrors.length = 0;
    requestedCursors.length = 0;
    FakeStream.instances.length = 0;
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("establishes a baseline without replay and appends pushed facts once", async () => {
    feeds.push(feed("old", "old"));
    const { result } = renderHook(() => useNotifications(() => undefined));
    await waitFor(() => expect(result.current.state.cursor).toBe("old"));
    expect(result.current.state.items).toEqual([]);
    expect(FakeStream.instances).toHaveLength(1);
    expect(latestStream().after).toBe("old");

    act(() => { latestStream().emit(fact("new", "new")); });
    act(() => { latestStream().emit(fact("new", "new")); });

    expect(result.current.state.items.map((item) => item.id)).toEqual(["new"]);
    expect(result.current.state.cursor).toBe("new");
  });

  it("does not skip the first fact after an empty baseline", async () => {
    feeds.push(feed("", ""));
    const { result } = renderHook(() => useNotifications(() => undefined));
    await waitFor(() => expect(result.current.state.initialized).toBe(true));
    expect(FakeStream.instances).toHaveLength(1);
    expect(latestStream().after).toBeUndefined();

    act(() => { latestStream().emit(fact("first", "first")); });

    expect(result.current.state.items.map((item) => item.id)).toEqual(["first"]);
  });

  it("requests system permission only from enable and notifies about pushed facts", async () => {
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
    feeds.push(feed("", "base"));
    const open = vi.fn();
    const { result } = renderHook(() => useNotifications(open));
    await waitFor(() => expect(result.current.state.cursor).toBe("base"));
    expect(requestPermission).not.toHaveBeenCalled();
    await act(async () => { await result.current.enableSystemNotifications(); });
    FakeNotification.permission = "granted";
    expect(requestPermission).toHaveBeenCalledTimes(1);

    act(() => { latestStream().emit(fact("system", "system")); });

    expect(created).toEqual([{ title: "#app · Deployment · Success", options: { body: "Deployment succeeded", tag: "system" } }]);
  });

  it("uses the configured repository name, current language, status, and commit message", async () => {
	await i18n.changeLanguage("en");
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
    }));
    const facts: NotificationFact[] = [
      { id: "push", cursor: "push", category: "push", outcome: "received", event_id: "event-push", provider: "gitlab", source_id: "gitlab-a", repository: "client-dist", summary: "feat: publish assets", occurred_at: "2026-08-12T00:00:00Z" },
      { id: "success", cursor: "success", category: "pipeline", outcome: "success", event_id: "event-success", provider: "gitlab", source_id: "gitlab-a", repository: "client-dist", summary: "fix: verify release", occurred_at: "2026-08-12T00:01:00Z" },
      { id: "failure", cursor: "failure", category: "pipeline", outcome: "failure", event_id: "event-failure", provider: "gitlab", source_id: "gitlab-a", repository: "client-dist", summary: "deploy(web-mobile): sync build to 1135", occurred_at: "2026-08-12T00:02:00Z" },
    ];
    feeds.push({ latest_cursor: "failure", items: facts });
    const repositories: Repository[] = [{ provider: "gitlab", source_id: "gitlab-a", id: "client-dist", name: "gop-client-dist" }];

    renderHook(() => useNotifications(() => undefined, repositories));

    await waitFor(() => expect(created).toEqual([
      { title: "#gop-client-dist · Push", options: { body: "feat: publish assets", tag: "push" } },
      { title: "#gop-client-dist · Pipeline · Success", options: { body: "fix: verify release", tag: "success" } },
      { title: "#gop-client-dist · Pipeline · Failed", options: { body: "deploy(web-mobile): sync build to 1135", tag: "failure" } },
    ]));
  });

  it("refreshes browser notification permission when the page becomes visible", async () => {
    class FakeNotification {
      static permission: NotificationPermission = "default";
      static requestPermission = vi.fn();
    }
    vi.stubGlobal("Notification", FakeNotification);
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    deferredFeeds.push(new Promise(() => undefined));
    const { result } = renderHook(() => useNotifications(() => undefined));

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
    const { result } = renderHook(() => useNotifications(() => undefined));

    expect(result.current.systemState).toBe("denied");
    FakeNotification.permission = "granted";
    act(() => { window.dispatchEvent(new Event("focus")); });

    expect(result.current.systemState).toBe("granted");
  });

  it("creates independent notifications for repeated tests", () => {
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
    const { result } = renderHook(() => useNotifications(() => undefined));

    act(() => {
      result.current.sendTestSystemNotification("Hookfly test", "Notifications can reach this device.");
      result.current.sendTestSystemNotification("Hookfly test", "Notifications can reach this device.");
    });

    expect(created).toEqual([
      { title: "Hookfly test", options: { body: "Notifications can reach this device." } },
      { title: "Hookfly test", options: { body: "Notifications can reach this device." } },
    ]);
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

    const { result } = renderHook(() => useNotifications(() => undefined));

    await waitFor(() => expect(result.current.state.items.map((item) => item.id)).toEqual(["app-push"]));
    expect(result.current.unreadCount).toBe(1);
    await waitFor(() => expect(created).toEqual([{ title: "#app · Push", options: { body: "app received a push", tag: "app-push" } }]));
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
    const { result } = renderHook(() => useNotifications(() => undefined));

    act(() => { result.current.setStatusEnabled("push.received", false, appRepository); });
    await act(async () => { resolveFeed({ latest_cursor: "next", items: [pushFact("app-push", "app")] }); });

    await waitFor(() => expect(result.current.state.cursor).toBe("next"));
    expect(result.current.state.items).toEqual([]);
    expect(result.current.unreadCount).toBe(0);
    expect(created).toEqual([]);
  });

  it("ignores a pre-clear feed and restarts the stream from the empty cursor", async () => {
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
    const { result } = renderHook(() => useNotifications(() => undefined));

    act(() => { result.current.clearHistory(); });
    await act(async () => { resolveOld(feed("stale", "stale-cursor")); });

    expect(result.current.state.cursor).toBe("");
    expect(result.current.state.items).toEqual([]);
    expect(result.current.unreadCount).toBe(0);
    expect(created).toEqual([]);
    expect(JSON.parse(localStorage.getItem(NOTIFICATION_STORAGE_KEY) ?? "{}")).toMatchObject({ cursor: "", items: [], unread_ids: [] });
    await waitFor(() => expect(requestedCursors).toEqual(["old-cursor", undefined]));
    expect(FakeStream.instances).toHaveLength(0);

    await act(async () => { resolveFresh(feed("fresh", "fresh-cursor")); });
    await waitFor(() => expect(result.current.state.items.map((item) => item.id)).toEqual(["fresh"]));
    expect(result.current.state.cursor).toBe("fresh-cursor");
    expect(result.current.unreadCount).toBe(1);
    expect(created).toEqual([{ title: "#app · Deployment · Success", options: { body: "Deployment succeeded", tag: "fresh" } }]);
    expect(FakeStream.instances).toHaveLength(1);
    expect(latestStream().after).toBe("fresh-cursor");
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

    const { result } = renderHook(() => useNotifications(() => undefined));

    await waitFor(() => expect(result.current.state.cursor).toBe("next"));
    expect(result.current.state.items.map((item) => item.id)).toEqual(["retained-push"]);
    expect(created).toEqual([]);
  });

  it("retries the baseline with backoff after a failure and then opens the stream", async () => {
    vi.useFakeTimers();
    try {
      feedErrors.push(new Error("boom"));
      feeds.push(feed("ok", "ok"));
      const { result } = renderHook(() => useNotifications(() => undefined));

      await act(async () => { await vi.advanceTimersByTimeAsync(0); });
      expect(result.current.error?.message).toBe("boom");
      expect(FakeStream.instances).toHaveLength(0);

      await act(async () => { await vi.advanceTimersByTimeAsync(4_999); });
      expect(FakeStream.instances).toHaveLength(0);

      await act(async () => { await vi.advanceTimersByTimeAsync(1); });
      expect(result.current.error).toBeNull();
      expect(result.current.state.cursor).toBe("ok");
      expect(FakeStream.instances).toHaveLength(1);
      expect(latestStream().after).toBe("ok");
    } finally {
      vi.useRealTimers();
    }
  });

  it("reports stream errors and clears them when the stream reopens", async () => {
    feeds.push(feed("base", "base"));
    const { result } = renderHook(() => useNotifications(() => undefined));
    await waitFor(() => expect(FakeStream.instances).toHaveLength(1));

    act(() => { latestStream().fail(); });
    expect(result.current.error?.message).toBe("Notification feed failed");

    act(() => { latestStream().open(); });
    expect(result.current.error).toBeNull();
  });

  it("closes the stream when unmounted", async () => {
    feeds.push(feed("base", "base"));
    const { unmount } = renderHook(() => useNotifications(() => undefined));
    await waitFor(() => expect(FakeStream.instances).toHaveLength(1));

    unmount();

    expect(latestStream().closed).toBe(true);
  });

  it("persists and resets repository-specific notification settings", async () => {
    const { result } = renderHook(() => useNotifications(() => undefined));
    await waitFor(() => expect(result.current.state.initialized).toBe(true));

    act(() => { result.current.setStatusEnabled("push.received", true, appRepository); });
    const withOverride = JSON.parse(localStorage.getItem(NOTIFICATION_STORAGE_KEY) ?? "{}") as ReturnType<typeof defaultNotificationState>;
    expect(withOverride.repository_preferences['["github","github-a","app"]']["push.received"]).toBe(true);

    act(() => { result.current.resetRepositoryPreferences(appRepository); });
    const reset = JSON.parse(localStorage.getItem(NOTIFICATION_STORAGE_KEY) ?? "{}") as ReturnType<typeof defaultNotificationState>;
    expect(reset.repository_preferences['["github","github-a","app"]']).toBeUndefined();
  });
});
