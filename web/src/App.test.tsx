import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";
import { TooltipProvider } from "./components/ui/tooltip";
import { AppThemeProvider } from "./theme";

const authSession = {
  user: { display_name: "De Jong", username: "dj", provider: "authentik" as const },
  expires_at: "2026-08-12T16:00:00Z",
};

function renderApp() {
  const businessFetch = globalThis.fetch;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/api/v1/auth/session") {
      return Promise.resolve(new Response(JSON.stringify(authSession), { status: 200, headers: { "Content-Type": "application/json" } }));
    }
    return businessFetch(input, init);
  }));
  return render(<AppThemeProvider><TooltipProvider><App /></TooltipProvider></AppThemeProvider>);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((promiseResolve) => { resolve = promiseResolve; });
  return { promise, resolve };
}

const emptyEvents = {
  items: [],
  page: 1,
  page_size: 50,
  total: 0,
  has_previous: false,
  has_next: true,
  active: false,
};

const idleConfigStatus = {
  state: "idle",
  current_digest: "digest-a",
  loaded_at: "2026-08-06T01:02:03Z",
  active_deliveries: 0,
  queued_events: 0,
};

const emptyNotifications = { items: [], latest_cursor: "baseline" };

const connectionsFixture = {
  id: "primary",
  type: "dokploy",
  status: "configured",
  capabilities: ["resource_discovery"],
};

const targetInventoryFixture = {
  targets: [{
    id: "target-1",
    connection_id: "primary",
    resource_type: "compose",
    resource_id: "compose-1",
    project_name: "Checkout",
    environment_name: "Production",
    name: "Checkout API",
    app_name: "checkout-api-abc123",
    status: "done",
    condition: "available",
    containers: [],
  }],
  refreshed_at: "2026-08-11T10:00:00Z",
  errors: [],
};

const activeEvents = {
  ...emptyEvents,
  total: 1,
  active: true,
  items: [{
    id: "event-1",
    received_at: "2026-08-05T01:02:03Z",
    event_type: "Pipeline Hook",
    provider: "gitlab",
    source_id: "gitlab-a",
    repository: "app",
    ref: "main",
    status: "running",
    external_id: "42",
    trigger: "push",
    revision: "0123456789abcdef",
    commit_message: "fix checkout timeout",
    routing_result: "matched",
    rule_id: "deploy-main",
    delivery_summary: { kind: "uniform", count: 1, transport_status: "enqueued", deployment_status: "running" },
    deliveries: [{ id: "delivery-1", target_id: "target-1", current_attempt_id: "attempt-1", transport_status: "enqueued", deployment_status: "running", active: true, attention: false, allowed_operations: [] }],
    active: true,
    attention: false,
  }],
};

const attentionEvents = {
  ...activeEvents,
  active: false,
  items: [{
    ...activeEvents.items[0],
    active: false,
    attention: true,
    delivery_summary: { kind: "uniform", count: 1, transport_status: "failed", deployment_status: "error" },
    deliveries: [{ id: "delivery-1", target_id: "target-1", current_attempt_id: "attempt-1", transport_status: "failed", deployment_status: "error", active: false, attention: true, allowed_operations: ["retry"] }],
  }],
};

const activeDetail = {
  ...activeEvents.items[0],
  headers: {},
  payload: {},
  rule_snapshot: null,
  deliveries: [],
  activities: [],
};

const terminalDetail = {
  ...activeDetail,
  active: false,
  attention: true,
  deliveries: [{
    id: "delivery-1",
    target_id: "target-1",
    current_attempt_id: "attempt-1",
    transport_status: "failed",
    deployment_status: "error",
    active: false,
    attention: true,
    target_binding_status: "compatible",
    allowed_operations: ["retry"],
    created_at: "2026-08-05T01:02:03Z",
    updated_at: "2026-08-05T01:03:03Z",
    attempts: [],
  }],
};

function mockApi() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      return new Response(
        JSON.stringify(
          url.endsWith("/config/status")
            ? idleConfigStatus
            : url.endsWith("/repositories")
            ? { repositories: [
              { provider: "gitlab", source_id: "gitlab-a", id: "app", name: "Application" },
              { provider: "github", source_id: "github-a", id: "app", name: "Application" },
            ] }
            : url.endsWith("/targets")
            ? targetInventoryFixture
            : emptyEvents,
        ),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }),
  );
}

describe("App", () => {
  beforeEach(mockApi);
  afterEach(() => vi.useRealTimers());

  it("starts no business requests before the real app resolves authentication", async () => {
    const requests: string[] = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      requests.push(String(input));
      return new Promise<Response>(() => undefined);
    }));

    render(<AppThemeProvider><TooltipProvider><App /></TooltipProvider></AppThemeProvider>);

    await waitFor(() => expect(requests).toEqual(["/api/v1/auth/session"]));
    expect(screen.queryByRole("heading", { name: "Deployment activity ledger" })).not.toBeInTheDocument();
  });

  it("renders the management shell in English by default", async () => {
    renderApp();

    expect(await screen.findByRole("heading", { name: "Deployment activity ledger" })).toBeInTheDocument();
    expect(screen.queryByText("System notifications are unavailable")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Account menu for De Jong" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Reload configuration" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Choose language" })).toBeInTheDocument();
    const settings = screen.getByRole("button", { name: "Settings center" });
    const notifications = screen.getByRole("button", { name: /Notifications, 0 unread/i });
    expect(settings.parentElement).toBe(notifications.parentElement);
  });

  it("clears history, closes the inspector, removes paging state, and reloads the mounted ledger", async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, "", "/?page=2&id=event-1");
    let deleted = false;
    const initialEventsRequested = deferred<void>();
    const initialEventsResponse = deferred<Response>();
    const eventRequests: Array<{ url: string; method: string | undefined }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (url.endsWith("/targets")) return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      if (url.endsWith("/events/event-1")) return new Response(JSON.stringify(terminalDetail), { status: 200 });
      if (url.startsWith("/api/v1/events")) {
        eventRequests.push({ url, method: init?.method });
        if (init?.method === "DELETE") {
          deleted = true;
          return new Response(JSON.stringify({ deleted: 1 }), { status: 200 });
        }
        if (!deleted) {
          initialEventsRequested.resolve();
          return initialEventsResponse.promise;
        }
        return new Response(JSON.stringify(emptyEvents), { status: 200 });
      }
      throw new Error(`Unexpected request: ${url}`);
    }));

    renderApp();
    await initialEventsRequested.promise;
    await act(async () => {
      initialEventsResponse.resolve(new Response(JSON.stringify({ ...attentionEvents, page: 2, has_previous: true }), { status: 200 }));
    });
    await user.click(screen.getByRole("button", { name: "View event event-1" }));
    expect(await screen.findByRole("complementary", { name: "Event details" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Settings center" }));
    await user.click(screen.getByRole("button", { name: "History" }));
    await user.click(screen.getByRole("button", { name: "Clear all history" }));
    const dialog = screen.getByRole("dialog", { name: "Clear all event history?" });
    await user.click(within(dialog).getByRole("button", { name: "Clear all history" }));

    await waitFor(() => expect(window.location.search).toBe(""));
    await waitFor(() => expect(screen.queryByRole("complementary", { name: "Event details" })).not.toBeInTheDocument());
    await waitFor(() => expect(eventRequests).toContainEqual({ url: "/api/v1/events", method: undefined }));
    expect(eventRequests).toContainEqual({ url: "/api/v1/events", method: "DELETE" });
  });

  it("reloads the mounted ledger when clearing history does not change the URL", async () => {
    const user = userEvent.setup();
    let deleted = false;
    let postDeleteReads = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (url.endsWith("/targets")) return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      if (url === "/api/v1/events" && init?.method === "DELETE") {
        deleted = true;
        return new Response(JSON.stringify({ deleted: 1 }), { status: 200 });
      }
      if (url === "/api/v1/events") {
        if (deleted) postDeleteReads += 1;
        return new Response(JSON.stringify(deleted ? emptyEvents : attentionEvents), { status: 200 });
      }
      throw new Error(`Unexpected request: ${url}`);
    }));

    renderApp();
    await screen.findByRole("button", { name: "View event event-1" });
    await user.click(screen.getByRole("button", { name: "Settings center" }));
    await user.click(screen.getByRole("button", { name: "History" }));
    await user.click(screen.getByRole("button", { name: "Clear all history" }));
    const dialog = screen.getByRole("dialog", { name: "Clear all event history?" });
    await user.click(within(dialog).getByRole("button", { name: "Clear all history" }));

    await waitFor(() => expect(postDeleteReads).toBe(1));
  });

  it("groups pagination details and controls at the bottom right", async () => {
    renderApp();

    const pageSize = await screen.findByText("50 / page");
    const page = screen.getByText("Page 1");
    const group = page.parentElement;
    expect(group).not.toBeNull();
    expect(group).toHaveClass("ml-auto");
    expect(group).toContainElement(pageSize);
    expect(group).toContainElement(screen.getByRole("button", { name: "Previous" }));
    expect(group).toContainElement(screen.getByRole("button", { name: "Next" }));
  });

  it("opens the settings center and persists the selected data font size", async () => {
    const user = userEvent.setup();
    renderApp();
    await screen.findByRole("heading", { name: "Deployment activity ledger" });

    await user.click(screen.getByRole("button", { name: "Settings center" }));
    const dialog = screen.getByRole("dialog", { name: "Settings center" });
    expect(dialog).toHaveTextContent("Compact11 px");
    expect(dialog).toHaveTextContent("Standard12 px");
    expect(dialog).toHaveTextContent("Comfortable14 px");
    expect(screen.getByRole("radio", { name: "Compact 11 px" })).toHaveAttribute("aria-checked", "true");

    await user.click(screen.getByRole("radio", { name: "Comfortable 14 px" }));
    expect(document.documentElement).toHaveAttribute("data-data-font-size", "comfortable");
    expect(localStorage.getItem("hookfly-data-font-size")).toBe("comfortable");
  });

  it("supports the radio-group keyboard model in display settings", async () => {
    const user = userEvent.setup();
    renderApp();
    await screen.findByRole("heading", { name: "Deployment activity ledger" });

    await user.click(screen.getByRole("button", { name: "Settings center" }));
    const compact = screen.getByRole("radio", { name: "Compact 11 px" });
    const standard = screen.getByRole("radio", { name: "Standard 12 px" });
    const comfortable = screen.getByRole("radio", { name: "Comfortable 14 px" });
    expect(compact).toHaveAttribute("tabindex", "0");
    expect(standard).toHaveAttribute("tabindex", "-1");
    expect(comfortable).toHaveAttribute("tabindex", "-1");

    compact.focus();
    await user.keyboard("{ArrowRight}");
    expect(standard).toHaveFocus();
    expect(standard).toHaveAttribute("aria-checked", "true");
    await user.keyboard("{End}");
    expect(comfortable).toHaveFocus();
    expect(comfortable).toHaveAttribute("aria-checked", "true");
  });

  it("restores a persisted data font size when the app starts", async () => {
    localStorage.setItem("hookfly-data-font-size", "standard");
    renderApp();

    await screen.findByRole("heading", { name: "Deployment activity ledger" });
    expect(document.documentElement).toHaveAttribute("data-data-font-size", "standard");
  });

  it("keeps Connections lazy on the default ledger page", async () => {
    // Break caught: adding Connections causes ledger startup to fetch configuration inventory or Dokploy resources.
    const requests: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requests.push(url);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      return new Response(JSON.stringify(emptyEvents), { status: 200 });
    }));

    renderApp();
    await screen.findByText("No repositories configured");
    expect(requests.some((url) => url.includes("/connections"))).toBe(false);
  });

  it("mounts Connections without starting ledger or upstream discovery requests", async () => {
    // Break caught: keeping ledger polling mounted behind the Connections page or eagerly discovering the first connection.
    window.history.replaceState(null, "", "/?view=connections");
    const requests: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requests.push(url);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (url.endsWith("/connections")) return new Response(JSON.stringify({ connections: [connectionsFixture] }), { status: 200 });
      throw new Error(`unexpected request ${url}`);
    }));

    renderApp();
    expect(await screen.findByRole("heading", { name: "Connections" })).toBeInTheDocument();
    expect(requests).toContain("/api/v1/connections");
    expect(requests.some((url) => url.includes("/events") || url.includes("/config/status") || url.includes("/resources"))).toBe(false);
  });

  it("switches the complete management shell to Simplified Chinese", async () => {
    const user = userEvent.setup();
    renderApp();
    await screen.findByRole("heading", { name: "Deployment activity ledger" });

    await user.click(screen.getByRole("button", { name: "Choose language" }));
    await user.click(screen.getByRole("menuitemradio", { name: "简体中文" }));

    expect(screen.getByRole("heading", { name: "部署活动账本" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新加载配置" })).toBeInTheDocument();
    expect(screen.getByLabelText("仓库")).toBeInTheDocument();
    expect(screen.getByLabelText("Target")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "下一页" })).toBeInTheDocument();
  });

  it("renders global, unmatched, then same-ID repositories with provider and source context", async () => {
    renderApp();
    expect(await screen.findByRole("button", { name: "app · gitlab · gitlab-a" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "app · github · github-a" })).toBeInTheDocument();
  });

  it("reads config status first and shows the unconfigured empty state", async () => {
    const requests: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      requests.push(url);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      return new Response(JSON.stringify(emptyEvents), { status: 200 });
    }));

    renderApp();

    expect(await screen.findByText("No repositories configured")).toBeTruthy();
    expect(requests[0]).toMatch(/\/config\/status$/);
  });

  it("removes only stale repository identity parameters after repositories refresh", async () => {
    window.history.replaceState(null, "", "/?source=removed&repository=app&target=production&status=transport%3Afailed");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [{ provider: "gitlab", source_id: "kept", id: "app", name: "Kept" }] }), { status: 200 });
      return new Response(JSON.stringify(emptyEvents), { status: 200 });
    }));

    renderApp();
    await screen.findByRole("button", { name: "app · gitlab · kept" });

    await waitFor(() => expect(new URLSearchParams(window.location.search).get("source")).toBeNull());
    expect(new URLSearchParams(window.location.search).get("repository")).toBeNull();
    expect(new URLSearchParams(window.location.search).get("target")).toBe("production");
    expect(new URLSearchParams(window.location.search).getAll("status")).toEqual(["transport:failed"]);
  });

  it("writes both repository identity parameters when selecting one source", async () => {
    renderApp();
    fireEvent.click(await screen.findByRole("button", { name: "app · gitlab · gitlab-a" }));
    await waitFor(() => {
      const query = new URLSearchParams(window.location.search);
      expect(query.get("source")).toBe("gitlab-a");
      expect(query.get("repository")).toBe("app");
    });
  });

  it.each([
    ["source only", "/?source=gitlab-a&target=target-1&status=transport%3Afailed"],
    ["repository only", "/?repository=app&target=target-1&status=transport%3Afailed"],
  ])("normalizes a %s URL before the first event request", async (_name, initialURL) => {
    window.history.replaceState(null, "", initialURL);
    const eventRequests: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) throw new Error("repository index unavailable");
      if (url.includes("/events")) eventRequests.push(url);
      return new Response(JSON.stringify(emptyEvents), { status: 200 });
    }));

    renderApp();
    await waitFor(() => expect(eventRequests.length).toBeGreaterThan(0));

    const firstEventQuery = new URL(eventRequests[0], "https://hookfly.example.invalid").searchParams;
    expect(firstEventQuery.get("source")).toBeNull();
    expect(firstEventQuery.get("repository")).toBeNull();
    expect(firstEventQuery.get("target")).toBe("target-1");
    expect(firstEventQuery.getAll("status")).toEqual(["transport:failed"]);
    const browserQuery = new URLSearchParams(window.location.search);
    expect(browserQuery.get("source")).toBeNull();
    expect(browserQuery.get("repository")).toBeNull();
    expect(browserQuery.get("target")).toBe("target-1");
    expect(browserQuery.getAll("status")).toEqual(["transport:failed"]);
  });

  it("stores repository, target, statuses, and page in the URL and restores them", async () => {
    // Break caught: treating the existing repository control as proof that its asynchronous options have loaded.
    const businessFetch = globalThis.fetch;
    let repositoryRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/repositories") && ++repositoryRequests === 2) {
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      return businessFetch(input, init);
    }));
    const user = userEvent.setup();
    const first = renderApp();
    await screen.findByRole("button", { name: "app · gitlab · gitlab-a" });

    await user.click(screen.getByLabelText("Repository"));
    await user.click(screen.getByRole("option", { name: /Application.*GitLab/i }));
    await user.click(screen.getByLabelText("Target"));
    await user.click(screen.getByRole("option", { name: "target-1" }));
    await user.click(screen.getByLabelText("Delivery status"));
    await user.click(screen.getByRole("option", { name: "failed" }));
    await user.click(screen.getByLabelText("Deployment status"));
    await user.click(screen.getByRole("option", { name: "unrecognized" }));
    fireEvent.click(screen.getByRole("button", { name: "Next" }));

    await waitFor(() => {
      const query = new URLSearchParams(window.location.search);
      expect(query.get("source")).toBe("gitlab-a");
      expect(query.get("repository")).toBe("app");
      expect(query.get("target")).toBe("target-1");
      expect(query.getAll("status")).toEqual(["transport:failed", "deployment:unrecognized"]);
      expect(query.get("page")).toBe("2");
    });

    first.unmount();
    renderApp();
    await waitFor(() => expect(screen.getByLabelText("Repository")).toHaveTextContent("Application · GitLab"));
    expect(screen.getByLabelText("Target")).toHaveTextContent("target-1");
    expect(screen.getByLabelText("Delivery status")).toHaveTextContent("failed");
    expect(screen.getByLabelText("Deployment status")).toHaveTextContent("unrecognized");
    expect(screen.getByText("Page 2")).toBeTruthy();
  });

  it("restores URL-backed filters when browser history changes", async () => {
    renderApp();
    await screen.findByRole("button", { name: "app · github · github-a" });
    act(() => {
      window.history.pushState(null, "", "/?target=target-1&page=3");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() => expect(screen.getByLabelText("Target")).toHaveTextContent("target-1"));
    expect(screen.getByText("Page 3")).toBeTruthy();
  });

  it("keeps active rows visible and surfaces a stale-data error after a transient refresh failure", async () => {
    vi.useFakeTimers();
    let eventRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (String(input).endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (String(input).endsWith("/repositories")) {
        return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      }
      if (String(input).endsWith("/targets")) return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      eventRequests += 1;
      if (eventRequests === 1) return new Response(JSON.stringify(activeEvents), { status: 200 });
      throw new Error("network interrupted");
    }));

    renderApp();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByRole("button", { name: "View event event-1" })).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect(screen.getByRole("button", { name: "View event event-1" })).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("network interrupted");
    expect(screen.getByRole("alert").textContent).toContain("Retrying");
  });

  it("offers manual reload without claiming retry when retained list data is terminal", async () => {
    const user = userEvent.setup();
    let eventRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (String(input).endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (String(input).endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (String(input).endsWith("/targets")) return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      eventRequests += 1;
      if (eventRequests === 2) throw new Error("terminal list interrupted");
      return new Response(JSON.stringify(attentionEvents), { status: 200 });
    }));

    renderApp();
    await screen.findByRole("button", { name: "View event event-1" });
    await user.click(screen.getByLabelText("Deployment status"));
    await user.click(screen.getByRole("option", { name: "error" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("terminal list interrupted");
    expect(alert.textContent).not.toContain("Retrying");
    const reload = screen.getByRole("button", { name: "Reload" });
    fireEvent.click(reload);
    await waitFor(() => expect(eventRequests).toBe(3));
  });

  it("keeps active detail visible and surfaces its transient refresh failure", async () => {
    vi.useFakeTimers();
    let detailRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (url.endsWith("/targets")) return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      if (url.endsWith("/events/event-1")) {
        detailRequests += 1;
        if (detailRequests === 1) return new Response(JSON.stringify(activeDetail), { status: 200 });
        throw new Error("detail interrupted");
      }
      return new Response(JSON.stringify(activeEvents), { status: 200 });
    }));

    renderApp();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    fireEvent.click(screen.getByRole("button", { name: "View event event-1" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByLabelText("Event details")).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect(screen.getByLabelText("Event details")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("detail interrupted");
    expect(screen.getByRole("alert").textContent).toContain("Retrying");
  });

  it("offers manual reload without claiming retry when retained detail data is terminal", async () => {
    let detailRequests = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (url.endsWith("/targets")) return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      if (init?.method === "POST") return new Response(JSON.stringify({ delivery_id: "delivery-1", attempt_id: "attempt-2" }), { status: 201 });
      if (url.endsWith("/events/event-1")) {
        detailRequests += 1;
        if (detailRequests === 2) throw new Error("terminal detail interrupted");
        return new Response(JSON.stringify(terminalDetail), { status: 200 });
      }
      return new Response(JSON.stringify(attentionEvents), { status: 200 });
    }));

    renderApp();
    fireEvent.click(await screen.findByRole("button", { name: "View event event-1" }));
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("terminal detail interrupted");
    expect(alert.textContent).not.toContain("Retrying");
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(detailRequests).toBe(3));
  });

  it("keeps terminal attention in the event row without showing an idle sync indicator", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      return new Response(JSON.stringify(url.includes("/notifications") ? emptyNotifications : url.endsWith("/config/status") ? idleConfigStatus : url.endsWith("/repositories") ? { repositories: [] } : url.endsWith("/targets") ? targetInventoryFixture : attentionEvents), { status: 200 });
    }));

    renderApp();
    await screen.findByRole("button", { name: "View event event-1" });
    expect(screen.queryByText("Synchronized")).toBeNull();
    expect(screen.queryByText("Backend reports attention required")).toBeNull();
    expect(screen.getByRole("button", { name: "Retry target-1" })).toBeTruthy();
  });

  it("opens the confirmation flow directly from a failed row action", async () => {
    const user = userEvent.setup();
    let detailRequests = 0;
    let attemptBody: unknown;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (url.endsWith("/targets")) return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      if (url.endsWith("/events/event-1")) {
        detailRequests += 1;
        return new Response(JSON.stringify(terminalDetail), { status: 200 });
      }
      if (url.endsWith("/deliveries/delivery-1/attempts") && init?.method === "POST") {
        attemptBody = JSON.parse(String(init.body));
        return new Response(JSON.stringify({ delivery_id: "delivery-1", attempt_id: "attempt-2" }), { status: 201 });
      }
      return new Response(JSON.stringify(attentionEvents), { status: 200 });
    }));

    renderApp();
    await user.click(await screen.findByRole("button", { name: "Retry target-1" }));
    expect(await screen.findByRole("heading", { name: "Confirm retry" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("Reason (optional)"), "operator review");
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(attemptBody).toEqual({
      expected_current_attempt_id: "attempt-1",
      operation: "retry",
      reason: "operator review",
    }));
    expect(detailRequests).toBe(0);
  });

  it("switches one sibling inspector between rows without refreshing targets", async () => {
    const user = userEvent.setup();
    let resolveSecond: ((response: Response) => void) | undefined;
    let targetRequests = 0;
    const twoEvents = {
      ...attentionEvents,
      total: 2,
      items: [
        { ...attentionEvents.items[0], id: "event-a" },
        { ...attentionEvents.items[0], id: "event-b" },
      ],
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/notifications")) return new Response(JSON.stringify(emptyNotifications), { status: 200 });
      if (url.endsWith("/config/status")) return new Response(JSON.stringify(idleConfigStatus), { status: 200 });
      if (url.endsWith("/repositories")) return new Response(JSON.stringify({ repositories: [] }), { status: 200 });
      if (url.endsWith("/targets")) {
        targetRequests += 1;
        return new Response(JSON.stringify(targetInventoryFixture), { status: 200 });
      }
      if (url.endsWith("/events/event-a")) return new Response(JSON.stringify({ ...terminalDetail, id: "event-a", repository: "app-a" }), { status: 200 });
      if (url.endsWith("/events/event-b")) return new Promise<Response>((resolve) => { resolveSecond = resolve; });
      return new Response(JSON.stringify(twoEvents), { status: 200 });
    }));

    renderApp();
    fireEvent.click(await screen.findByRole("button", { name: "View event event-a" }));
    const firstInspector = await screen.findByRole("complementary", { name: "Event details" });
    expect(firstInspector.textContent).toContain("event-a");
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "View event event-b" }));
    const loadingInspector = await screen.findByRole("complementary", { name: "Event details" });
    expect(loadingInspector.textContent).toContain("Loading event details…");
    expect(loadingInspector.textContent).not.toContain("event-a");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();

    resolveSecond?.(new Response(JSON.stringify({ ...terminalDetail, id: "event-b", repository: "app-b" }), { status: 200 }));
    await waitFor(() => expect(screen.getByRole("complementary", { name: "Event details" })).toHaveTextContent("event-b"));
    expect(screen.getAllByRole("complementary", { name: "Event details" })).toHaveLength(1);
    expect(targetRequests).toBe(1);

    await user.click(screen.getByRole("button", { name: "Close details" }));
    await waitFor(() => expect(screen.queryByRole("complementary", { name: "Event details" })).toBeNull());
    expect(screen.getByRole("button", { name: "View event event-a" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View event event-b" })).toBeInTheDocument();
  });
});
