import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { i18n } from "../i18n";
import { EventDetail } from "./EventDetail";
import type { EventDetailResponse, Operation, TargetInventoryItem } from "../types";

const currentTarget: TargetInventoryItem = {
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
};

function detail(
  allowedOperations: Operation[],
  bindingStatus: "compatible" | "target_changed" | "target_unavailable" = "compatible",
): EventDetailResponse {
  return {
    id: "event-1",
    received_at: "2026-08-05T01:02:03Z",
    event_type: "Pipeline Hook",
    provider: "github",
    source_id: "github-a",
    repository: "app",
    ref: "refs/heads/main",
    status: "success",
    external_id: "run-42",
    trigger: "push",
    revision: "0123456789abcdef",
    commit_message: "fix checkout timeout",
    routing_result: "matched",
    rule_id: null,
    delivery_summary: { kind: "uniform", count: 1, transport_status: "failed", deployment_status: "error" },
    active: false,
    attention: true,
    headers: {},
    payload: { unsafe: '<script data-test="attack">alert(1)</script>' },
    rule_snapshot: null,
    source_states: [
      { event_id: "event-0", status: "pending", received_at: "2026-08-05T01:01:03Z" },
      { event_id: "event-1", status: "success", received_at: "2026-08-05T01:02:03Z" },
    ],
    activities: [],
    deliveries: [{
      id: "delivery-1",
      target_id: "target-1",
      current_attempt_id: "attempt-1",
      transport_status: "failed",
      deployment_status: "error",
      active: false,
      attention: true,
      target_binding_status: bindingStatus,
      allowed_operations: allowedOperations,
      created_at: "2026-08-05T01:02:03Z",
      updated_at: "2026-08-05T01:03:03Z",
      attempts: [],
    }],
  };
}

it("renders raw payload as inert text", () => {
  render(<EventDetail detail={detail([])} onAction={vi.fn()} onClose={vi.fn()} />);
  const rawText = [...document.querySelectorAll("pre")].map((node) => node.textContent).join("\n");
  expect(rawText).toContain(JSON.stringify(detail([]).payload, null, 2));
  expect(document.querySelector("script[data-test='attack']")).toBeNull();
});

it("renders canonical repository evidence labels and values", () => {
  render(<EventDetail detail={detail([])} repositoryName="Application" onAction={vi.fn()} onClose={vi.fn()} />);

  expect(screen.getByRole("heading", { name: "Application" })).toBeInTheDocument();
  expect(screen.getByLabelText("Event evidence")).toHaveTextContent("Ref");
  expect(screen.getByLabelText("Event evidence")).toHaveTextContent("Revision");
  expect(screen.getByLabelText("Event evidence")).toHaveTextContent("Status");
  expect(screen.getByText("github-a · app · event-1")).toBeInTheDocument();
  expect(screen.getByLabelText("Event evidence")).toHaveTextContent("0123456789abcdef");
});

it("renders the correlated pipeline source state timeline", () => {
  render(<EventDetail detail={detail([])} onAction={vi.fn()} onClose={vi.fn()} />);

  const timeline = screen.getByRole("region", { name: "Pipeline status history" });
  expect(timeline).toHaveTextContent("Pending");
  expect(timeline).toHaveTextContent("Success");
  expect(timeline.querySelectorAll("li")).toHaveLength(2);
});

it("marks superseded pipeline stages complete while keeping the latest active stage current", async () => {
  await i18n.changeLanguage("zh-CN");
  const value = detail([]);
  value.source_states = [
    { event_id: "event-0", status: "waiting_for_resource", received_at: "2026-08-05T01:00:03Z" },
    { event_id: "event-1", status: "pending", received_at: "2026-08-05T01:01:03Z" },
    { event_id: "event-2", status: "running", received_at: "2026-08-05T01:02:03Z" },
  ];

  render(<EventDetail detail={value} onAction={vi.fn()} onClose={vi.fn()} />);

  const timeline = screen.getByRole("region", { name: "\u6d41\u6c34\u7ebf\u72b6\u6001\u5386\u53f2" });
  const badges = timeline.querySelectorAll("[data-slot='badge']");
  expect([...badges].map((badge) => badge.getAttribute("data-tone"))).toEqual(["stable", "stable", "active"]);
  expect(badges[0].querySelector("svg")).toHaveClass("lucide-circle-check");
  expect(badges[1].querySelector("svg")).toHaveClass("lucide-circle-check");
  expect(badges[2].querySelector("svg")).toHaveClass("lucide-loader-circle");
  for (const icon of timeline.querySelectorAll("[data-testid='source-status-icon']")) {
    expect(icon).not.toHaveClass("animate-spin");
  }
});

it.each([
  [["retry"] as Operation[], true, false],
  [["redeploy"] as Operation[], false, true],
  [[] as Operation[], false, false],
])("shows only backend-allowed operations", (operations, retry, redeploy) => {
  render(<EventDetail detail={detail(operations)} onAction={vi.fn()} onClose={vi.fn()} />);
  expect(screen.queryByRole("button", { name: "Retry" }) !== null).toBe(retry);
  expect(screen.queryByRole("button", { name: "Redeploy" }) !== null).toBe(redeploy);
});

it.each([
  ["target_changed" as const, "The configured target no longer points to this historical resource"],
  ["target_unavailable" as const, "This historical target cannot be resolved"],
])("blocks operations when the historical binding is %s", (bindingStatus, message) => {
  render(<EventDetail detail={detail(["retry", "redeploy"], bindingStatus)} onAction={vi.fn()} onClose={vi.fn()} />);

  expect(screen.getByRole("alert").textContent).toContain(message);
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Redeploy" })).toBeNull();
});

it("confirms an allowed operation with an optional reason", async () => {
  const onAction = vi.fn(async () => undefined);
  render(<EventDetail detail={detail(["retry"])} onAction={onAction} onClose={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  fireEvent.input(screen.getByLabelText("Reason (optional)"), { target: { value: "operator review" } });
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));

  await waitFor(() => expect(onAction).toHaveBeenCalledWith(expect.objectContaining({
    operation: "retry",
    reason: "operator review",
  })));
});

it("renders as a sibling panel and closes from its explicit control", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(<EventDetail detail={detail([])} onAction={vi.fn()} onClose={onClose} />);

  expect(screen.getByRole("complementary", { name: "Event details" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Close details" }));

  expect(onClose).toHaveBeenCalledTimes(1);
});

it("closes the sibling panel on Escape", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(<EventDetail detail={detail([])} onAction={vi.fn()} onClose={onClose} />);

  await user.keyboard("{Escape}");

  expect(onClose).toHaveBeenCalledTimes(1);
});

it("separates current target inventory from historical deployment state", () => {
  render(<EventDetail detail={detail([])} targets={[currentTarget]} onAction={vi.fn()} onClose={vi.fn()} />);

  const current = screen.getByRole("region", { name: "Current target" });
  expect(current).toHaveTextContent("target-1");
  expect(current).toHaveTextContent("Checkout");
  expect(current).toHaveTextContent("Production");
  expect(current).toHaveTextContent("Checkout API");
  expect(current).toHaveTextContent("checkout-api-abc123");
  expect(current).toHaveTextContent("compose-1");
  expect(current).toHaveTextContent("done");

  const history = screen.getByRole("region", { name: "Deployment history" });
  expect(history).toHaveTextContent("Delivery failed");
  expect(history).not.toHaveTextContent("Checkout API");
});

it("warns when the current target inventory is unavailable", () => {
  render(<EventDetail detail={detail([])} targets={[{ ...currentTarget, condition: "unavailable" }]} onAction={vi.fn()} onClose={vi.fn()} />);

  expect(screen.getByRole("region", { name: "Current target" })).toHaveTextContent("Current Compose inventory is unavailable");
});

it("returns focus to the operation trigger after cancelling", async () => {
  const user = userEvent.setup();
  render(<EventDetail detail={detail(["retry"])} onAction={vi.fn()} onClose={vi.fn()} />);
  const trigger = screen.getByRole("button", { name: "Retry" });

  await user.click(trigger);
  await user.click(screen.getByRole("button", { name: "Cancel" }));

  await waitFor(() => expect(trigger).toHaveFocus());
});

it("closes only the operation dialog on the first Escape", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(<EventDetail detail={detail(["retry"])} onAction={vi.fn()} onClose={onClose} />);
  const trigger = screen.getByRole("button", { name: "Retry" });

  await user.click(trigger);
  await user.keyboard("{Escape}");

  await waitFor(() => expect(trigger).toHaveFocus());
  expect(onClose).not.toHaveBeenCalled();
});
