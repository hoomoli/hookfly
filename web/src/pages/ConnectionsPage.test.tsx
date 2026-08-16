import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { i18n } from "../i18n";
import type { ConnectionResource } from "../types";
import { ConnectionsPage } from "./ConnectionsPage";

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
const originalExecCommand = Object.getOwnPropertyDescriptor(document, "execCommand");

afterEach(() => {
  if (originalClipboard) Object.defineProperty(navigator, "clipboard", originalClipboard);
  else Reflect.deleteProperty(navigator, "clipboard");
  if (originalExecCommand) Object.defineProperty(document, "execCommand", originalExecCommand);
  else Reflect.deleteProperty(document, "execCommand");
});

const connections = [
  { id: "alpha", type: "dokploy", status: "configured", capabilities: ["resource_discovery"] },
  { id: "beta", type: "dokploy", status: "configured", capabilities: ["resource_discovery"] },
  { id: "archive", type: "future", status: "configured", capabilities: [] },
];

const alphaResource = {
  type: "compose",
  project_name: "Alpha project",
  environment_name: "Production",
  name: "API",
  app_name: "api-abc123",
  resource_id: "compose-alpha",
  status: "done",
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function setClipboard(value: Pick<Clipboard, "writeText"> | undefined) {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value });
}

function setExecCommand(implementation: (command: string) => boolean) {
  const execCommand = vi.fn(implementation);
  Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
  return execCommand;
}

function mockResourcePage(resources: ConnectionResource[] = [alphaResource]) {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).endsWith("/connections")
    ? json({ connections: [connections[0]] })
    : json({ resources })));
}

it("loads connection summaries first and discovers resources only after explicit selection", async () => {
  // Break caught: eagerly contacting every upstream when the page or connection list loads.
  let resolveConnections: ((response: Response) => void) | undefined;
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    requests.push(url);
    if (url.endsWith("/connections")) return new Promise<Response>((resolve) => { resolveConnections = resolve; });
    if (url.endsWith("/connections/alpha/resources")) return json({ resources: [alphaResource] });
    throw new Error(`unexpected request ${url}`);
  }));

  render(<ConnectionsPage />);
  expect(screen.getByText("Loading connections…")).toBeInTheDocument();
  await act(async () => { resolveConnections?.(json({ connections })); });

  const alpha = await screen.findByRole("button", { name: /alpha.*dokploy/i });
  expect(alpha.parentElement).toHaveClass("data-surface");
  expect(requests).toEqual(["/api/v1/connections"]);
  expect(alpha).toHaveTextContent("Capabilities: Resource discovery");
  expect(screen.getByText("Select a connection to inspect its discoverable resources.")).toBeInTheDocument();

  fireEvent.click(alpha);
  expect(await screen.findByText("compose-alpha")).toBeInTheDocument();
  expect(screen.getByText("compose-alpha").closest("tbody")).toHaveClass("data-surface");
  expect(screen.getByRole("columnheader", { name: "App Name" })).toBeInTheDocument();
  expect(screen.queryByRole("columnheader", { name: "Status" })).toBeNull();
  expect(requests).toEqual(["/api/v1/connections", "/api/v1/connections/alpha/resources"]);
});

it("hides the App Name column when discovery returns no non-empty app names", async () => {
  // Break caught: rendering an all-placeholder column for Dokploy project.all responses that never include appName.
  mockResourcePage([
    { ...alphaResource, app_name: undefined },
    { ...alphaResource, name: "Worker", resource_id: "compose-worker", app_name: "   " },
  ]);

  render(<ConnectionsPage />);
  fireEvent.click(await screen.findByRole("button", { name: /alpha.*dokploy/i }));
  expect(await screen.findByText("compose-alpha")).toBeInTheDocument();
  expect(screen.queryByRole("columnheader", { name: "App Name" })).toBeNull();
});

it("shows unavailable capability without requesting resources", async () => {
  // Break caught: sending unsupported connection types through the Dokploy discovery path.
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input));
    return json({ connections });
  }));

  render(<ConnectionsPage />);
  fireEvent.click(await screen.findByRole("button", { name: /archive.*future/i }));

  expect(screen.getByText("Resource discovery is unavailable for this connection type.")).toBeInTheDocument();
  expect(requests).toEqual(["/api/v1/connections"]);
});

it("renders empty and retryable connection-list states", async () => {
  // Break caught: leaving operators with a blank page after an empty configuration or transient management API error.
  let calls = 0;
  vi.stubGlobal("fetch", vi.fn(async () => {
    calls += 1;
    if (calls === 1) return json({ error: { code: "unavailable", message: "temporarily unavailable" } }, 503);
    return json({ connections: [] });
  }));

  render(<ConnectionsPage />);
  expect(await screen.findByText("Could not load connections: temporarily unavailable")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("No connections configured")).toBeInTheDocument();
  expect(calls).toBe(2);
});

it("cancels stale discovery when switching connections and supports resource retry", async () => {
  // Break caught: a slow previous connection overwriting the selected connection or a failed resource request becoming terminal.
  let alphaSignal: AbortSignal | undefined;
  let betaCalls = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/connections")) return json({ connections });
    if (url.endsWith("/connections/alpha/resources")) {
      alphaSignal = init?.signal ?? undefined;
      return new Promise<Response>((_resolve, reject) => {
        alphaSignal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
      });
    }
    if (url.endsWith("/connections/beta/resources")) {
      betaCalls += 1;
      if (betaCalls === 1) return json({ error: { code: "upstream_unavailable", message: "resource discovery unavailable" } }, 502);
      return json({ resources: [{ ...alphaResource, name: "Worker", resource_id: "compose-beta" }] });
    }
    throw new Error(`unexpected request ${url}`);
  }));

  render(<ConnectionsPage />);
  fireEvent.click(await screen.findByRole("button", { name: /alpha.*dokploy/i }));
  expect(await screen.findByText("Discovering resources…")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /beta.*dokploy/i }));
  await waitFor(() => expect(alphaSignal?.aborted).toBe(true));

  expect(await screen.findByText("Could not discover resources: resource discovery unavailable")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("compose-beta")).toBeInTheDocument();
  expect(screen.queryByText("compose-alpha")).toBeNull();
  expect(betaCalls).toBe(2);
});

it("copies a Compose ID and localizes the read-only page", async () => {
  // Break caught: copying the display name instead of the deployable resource ID or leaving the new page untranslated.
  const writeText = vi.fn(async () => undefined);
  setClipboard({ writeText });
  const execCommand = setExecCommand(() => true);
  mockResourcePage();

  render(<ConnectionsPage />);
  fireEvent.click(await screen.findByRole("button", { name: /alpha.*dokploy/i }));
  fireEvent.click(await screen.findByRole("button", { name: "Copy Compose ID compose-alpha" }));
  expect(writeText).toHaveBeenCalledWith("compose-alpha");
  expect(execCommand).not.toHaveBeenCalled();
  expect(await screen.findByText("Copied")).toBeInTheDocument();

  await act(async () => { await i18n.changeLanguage("zh-CN"); });
  expect(screen.getByRole("heading", { name: i18n.t("connections.heading") })).toBeInTheDocument();
  expect(screen.getByText(i18n.t("connections.readOnly"))).toBeInTheDocument();
  expect(screen.getByRole("button", { name: i18n.t("connections.copyID", { id: "compose-alpha" }) })).toBeInTheDocument();
});

it("copies through a cleaned-up DOM fallback when the Clipboard API is unavailable", async () => {
  // Break caught: HTTP management pages throwing when navigator.clipboard is undefined or leaving copied data in the DOM.
  setClipboard(undefined);
  const removeAllRanges = vi.spyOn(document.getSelection()!, "removeAllRanges");
  const execCommand = setExecCommand((command) => {
    expect(command).toBe("copy");
    expect(document.querySelector<HTMLTextAreaElement>("textarea[data-hookfly-clipboard]")?.value).toBe("compose-alpha");
    return true;
  });
  mockResourcePage();

  render(<ConnectionsPage />);
  fireEvent.click(await screen.findByRole("button", { name: /alpha.*dokploy/i }));
  fireEvent.click(await screen.findByRole("button", { name: "Copy Compose ID compose-alpha" }));

  expect(await screen.findByText("Copied")).toBeInTheDocument();
  expect(execCommand).toHaveBeenCalledOnce();
  expect(document.querySelector("textarea[data-hookfly-clipboard]")).toBeNull();
  expect(removeAllRanges).toHaveBeenCalled();
});

it.each([
  ["returns false", () => false],
  ["throws", () => { throw new Error("copy denied with sensitive browser details"); }],
])("reports copy failure and cleans up when the DOM fallback %s", async (_name, fallback) => {
  // Break caught: a failed fallback showing a false success, leaking its exception, or retaining a temporary selection.
  setClipboard(undefined);
  const removeAllRanges = vi.spyOn(document.getSelection()!, "removeAllRanges");
  setExecCommand(fallback);
  mockResourcePage();

  render(<ConnectionsPage />);
  fireEvent.click(await screen.findByRole("button", { name: /alpha.*dokploy/i }));
  fireEvent.click(await screen.findByRole("button", { name: "Copy Compose ID compose-alpha" }));

  expect(await screen.findByText("Could not copy Compose ID. Select and copy it manually.")).toBeInTheDocument();
  expect(screen.queryByText("Copied")).toBeNull();
  expect(screen.queryByText(/sensitive browser details/)).toBeNull();
  expect(document.querySelector("textarea[data-hookfly-clipboard]")).toBeNull();
  expect(removeAllRanges).toHaveBeenCalled();
});
