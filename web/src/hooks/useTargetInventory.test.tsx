import { act, renderHook, waitFor } from "@testing-library/react";
import { useTargetInventory } from "./useTargetInventory";

const inventory = {
  targets: [{ id: "production", connection_id: "primary", resource_type: "compose", resource_id: "compose-1", project_name: "Project", environment_name: "Production", name: "API", app_name: "api-abc", status: "done", condition: "available" as const, containers: [] }],
  refreshed_at: "2026-08-11T10:00:00Z",
  errors: [],
};

it("loads once when enabled and retains the last inventory when refresh fails", async () => {
  let calls = 0;
  vi.stubGlobal("fetch", vi.fn(async () => {
    calls += 1;
    if (calls === 1) return new Response(JSON.stringify(inventory), { status: 200 });
    return new Response(JSON.stringify({ error: { code: "unavailable", message: "refresh failed" } }), { status: 503 });
  }));
  const { result, rerender } = renderHook(({ enabled }) => useTargetInventory(enabled), { initialProps: { enabled: false } });
  expect(calls).toBe(0);

  rerender({ enabled: true });
  await waitFor(() => expect(result.current.data?.targets[0].id).toBe("production"));
  expect(calls).toBe(1);

  rerender({ enabled: false });
  rerender({ enabled: true });
  await act(async () => Promise.resolve());
  expect(calls).toBe(1);

  act(() => result.current.refresh());
  await waitFor(() => expect(result.current.error).toBe("refresh failed"));
  expect(result.current.data?.targets[0].status).toBe("done");
  expect(calls).toBe(2);
});
