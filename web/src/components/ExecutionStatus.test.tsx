import { render, screen } from "@testing-library/react";
import type { EventDeliverySummary } from "../types";
import { ExecutionStatus } from "./ExecutionStatus";
import { executionPresentation } from "./ExecutionStatus";

it.each([
  ["pending", "not_started", "delivering", null],
  ["sending", "not_started", "delivering", null],
  ["failed", "not_started", "deliveryFailed", "retry"],
  ["unknown", "unknown", "deliveryUnknown", "retry"],
  ["enqueued", "not_started", "waitingDeployment", null],
  ["enqueued", "locating", "deploying", null],
  ["enqueued", "running", "deploying", null],
  ["enqueued", "unrecognized", "deploying", null],
  ["enqueued", "done", "deploymentSucceeded", null],
  ["enqueued", "error", "deploymentFailed", "redeploy"],
  ["enqueued", "cancelled", "deploymentCancelled", "redeploy"],
  ["enqueued", "timeout", "deploymentTimedOut", "redeploy"],
  ["enqueued", "unknown", "deploymentUnknown", "redeploy"],
])("maps %s/%s to one execution state", (transport, deployment, labelKey, operation) => {
  expect(executionPresentation(transport, deployment)).toMatchObject({ labelKey, operation });
});

it("can omit the direct action when a confirmation flow owns the operation", () => {
  const delivery: EventDeliverySummary = {
    id: "delivery-1",
    target_id: "production",
    current_attempt_id: "attempt-1",
    transport_status: "failed",
    deployment_status: "error",
    active: false,
    attention: true,
    allowed_operations: ["retry"],
  };

  render(<ExecutionStatus delivery={delivery} onOperation={vi.fn()} showOperation={false} />);

  expect(screen.getByText("Delivery failed")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Retry production" })).toBeNull();
});
