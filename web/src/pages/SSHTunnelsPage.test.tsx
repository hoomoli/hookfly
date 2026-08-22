import { act, fireEvent, render, screen } from "@testing-library/react";
import { SSHTunnelsPage } from "./SSHTunnelsPage";

it("shows an empty SSH configuration", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ tunnels: [] }), { status: 200 })));
  render(<SSHTunnelsPage />);
  expect(await screen.findByText("No SSH tunnels configured")).toBeInTheDocument();
});

it("shows latency and restores the check button after five seconds", async () => {
  const item = { id: "jump", host: "jump.example.invalid", port: 22, status: "not_checked", destination_count: 1 };
  vi.stubGlobal("fetch", vi.fn(async (_url, init) => new Response(JSON.stringify(init?.method === "POST" ? { ...item, status: "connected", latency_ms: 128, checked_at: "2026-09-30T00:00:00Z" } : { tunnels: [item] }), { status: 200 })));
  render(<SSHTunnelsPage />);
  const button = await screen.findByRole("button", { name: "Check" });
  vi.useFakeTimers();
  try {
    await act(async () => { fireEvent.click(button); });
    expect(screen.getByRole("button", { name: "128.0 ms" })).toBeDisabled();
    await act(async () => { vi.advanceTimersByTime(4999); });
    expect(screen.queryByRole("button", { name: "Check" })).toBeNull();
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(screen.getByRole("button", { name: "Check" })).toBeEnabled();
    expect(screen.getByText("Forwarding available")).toBeInTheDocument();
  } finally { vi.useRealTimers(); }
});

it("shows configuration errors and permits checking without crashing", async () => {
  const item = { id: "jump", status: "configuration_error", error: "key_or_trust_error", destination_count: 1 };
  vi.stubGlobal("fetch", vi.fn(async (_url, init) => new Response(JSON.stringify(init?.method === "POST" ? item : { tunnels: [item] }), { status: 200 })));
  render(<SSHTunnelsPage />);
  fireEvent.click(await screen.findByRole("button", { name: "Check" }));
  expect(await screen.findByRole("button", { name: "Failed" })).toBeDisabled();
  expect(screen.getByText("Private key or trust file unavailable")).toBeInTheDocument();
});
