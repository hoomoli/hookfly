import { act, fireEvent, render, screen } from "@testing-library/react";
import { configReloadLabel, useConfigReload } from "./useConfigReload";
import type { ConfigStatus } from "../types";

const idleStatus: ConfigStatus = {
  state: "idle",
  current_digest: "digest-a",
  loaded_at: "2026-08-06T01:02:03Z",
  active_deliveries: 0,
  queued_events: 0,
};

function status(state: ConfigStatus["state"], activeDeliveries = 0, queuedEvents = 0): ConfigStatus {
  return { ...idleStatus, state, active_deliveries: activeDeliveries, queued_events: queuedEvents };
}

function json(body: unknown, responseStatus = 200) {
  return new Response(JSON.stringify(body), {
    status: responseStatus,
    headers: { "Content-Type": "application/json" },
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => { resolve = next; });
  return { promise, resolve };
}

function Probe({ onIdle = () => undefined }: { onIdle?: () => void }) {
  const reload = useConfigReload(onIdle);
  return (
    <div>
      <button type="button" disabled={reload.disabled} onClick={reload.submit}>
        {configReloadLabel(reload.status)}
      </button>
      <output aria-live="polite">{reload.status?.recovery ? "Startup recovery" : configReloadLabel(reload.status)}</output>
      {reload.error && <p role="alert">{reload.error.message}</p>}
    </div>
  );
}

describe("useConfigReload", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("locks synchronously before the reload response", async () => {
    const reloadResponse = deferred<Response>();
    const mockFetch = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValueOnce(json(idleStatus))
      .mockImplementationOnce(() => reloadResponse.promise);
    vi.stubGlobal("fetch", mockFetch);

    render(<Probe />);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    const button = screen.getByRole("button", { name: "Reload configuration" });
    fireEvent.click(button);
    fireEvent.click(button);

    expect(mockFetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });

  it("polls every second and exposes each lifecycle count", async () => {
    const onIdle = vi.fn();
    const statuses = [
      { ...status("validating"), recovery: true },
      status("waiting", 2),
      status("applying"),
      status("draining", 0, 3),
      idleStatus,
    ];
    vi.stubGlobal("fetch", vi.fn(async () => json(statuses.shift())));

    render(<Probe onIdle={onIdle} />);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect((screen.getByRole("button", { name: "Validating" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("status").textContent).toBe("Startup recovery");

    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect((screen.getByRole("button", { name: "Waiting for 2 active deployments" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect((screen.getByRole("button", { name: "Applying" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect((screen.getByRole("button", { name: "Routing 3 queued events" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect((screen.getByRole("button", { name: "Reload configuration" }) as HTMLButtonElement).disabled).toBe(false);
    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  it("keeps the local lock after an uncertain POST until status confirms idle", async () => {
    const onIdle = vi.fn();
    const mockFetch = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValueOnce(json(idleStatus))
      .mockRejectedValueOnce(new TypeError("network interrupted"))
      .mockResolvedValueOnce(json(status("waiting", 1)))
      .mockResolvedValueOnce(json(idleStatus));
    vi.stubGlobal("fetch", mockFetch);

    render(<Probe onIdle={onIdle} />);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    fireEvent.click(screen.getByRole("button", { name: "Reload configuration" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });

    expect((screen.getByRole("button", { name: "Waiting for 1 active deployment" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(999); });
    expect((screen.getByRole("button", { name: "Waiting for 1 active deployment" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect((screen.getByRole("button", { name: "Reload configuration" }) as HTMLButtonElement).disabled).toBe(false);
    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  it("unlocks after a definitive invalid-config response", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(json(idleStatus))
      .mockResolvedValueOnce(json({ error: { code: "invalid_config", message: "configuration is invalid" } }, 422)));

    render(<Probe />);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    fireEvent.click(screen.getByRole("button", { name: "Reload configuration" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });

    expect((screen.getByRole("button", { name: "Reload configuration" }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.getByRole("alert").textContent).toContain("configuration is invalid");
  });
});
