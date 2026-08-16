import { act, render, screen } from "@testing-library/react";
import { useActivePolling } from "./useActivePolling";

function setVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value,
  });
  document.dispatchEvent(new Event("visibilitychange"));
}

function Probe({ load }: { load: () => Promise<{ active: boolean }> }) {
  const state = useActivePolling(load);
  return <output data-error={state.error?.message ?? ""}>{state.data?.active ? "active" : "terminal"}</output>;
}

describe("useActivePolling", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility("visible");
  });

  afterEach(() => vi.useRealTimers());

  it("fetches immediately and every second until the next terminal response", async () => {
    const load = vi
      .fn<() => Promise<{ active: boolean }>>()
      .mockResolvedValueOnce({ active: true })
      .mockResolvedValueOnce({ active: true })
      .mockResolvedValueOnce({ active: false });

    render(<Probe load={load} />);
    await act(async () => Promise.resolve());
    expect(load).toHaveBeenCalledTimes(1);

    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect(load).toHaveBeenCalledTimes(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect(load).toHaveBeenCalledTimes(3);
    expect(screen.getByText("terminal")).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(load).toHaveBeenCalledTimes(3);
  });

  it("retains active data and retries once per second after a transient rejection", async () => {
    const load = vi
      .fn<() => Promise<{ active: boolean }>>()
      .mockResolvedValueOnce({ active: true })
      .mockRejectedValueOnce(new Error("network interrupted"))
      .mockResolvedValueOnce({ active: false });

    render(<Probe load={load} />);
    await act(async () => Promise.resolve());
    expect(screen.getByText("active")).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(1_000); });
    expect(load).toHaveBeenCalledTimes(2);
    expect(screen.getByText("active").getAttribute("data-error")).toBe("network interrupted");

    await act(async () => { await vi.advanceTimersByTimeAsync(999); });
    expect(load).toHaveBeenCalledTimes(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(load).toHaveBeenCalledTimes(3);
    expect(screen.getByText("terminal")).toBeTruthy();

    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(load).toHaveBeenCalledTimes(3);
  });

  it("stops while hidden and fetches immediately when visible again", async () => {
    const load = vi.fn(async () => ({ active: true }));
    render(<Probe load={load} />);
    await act(async () => Promise.resolve());
    expect(load).toHaveBeenCalledTimes(1);

    act(() => setVisibility("hidden"));
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(load).toHaveBeenCalledTimes(1);

    await act(async () => setVisibility("visible"));
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("waits for visibility before the first fetch when mounted hidden", async () => {
    setVisibility("hidden");
    const load = vi.fn(async () => ({ active: true }));
    render(<Probe load={load} />);
    await act(async () => Promise.resolve());
    expect(load).not.toHaveBeenCalled();

    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); });
    expect(load).not.toHaveBeenCalled();
    await act(async () => setVisibility("visible"));
    expect(load).toHaveBeenCalledTimes(1);
  });
});
