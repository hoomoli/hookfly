import { fireEvent, render, screen } from "@testing-library/react";
import type { EventSummary, Repository } from "../types";
import { EventTable } from "./EventTable";

const repositories: Repository[] = [
  { provider: "gitlab", source_id: "gitlab-a", id: "app", name: "Application" },
  { provider: "harbor", source_id: "harbor-a", id: "application-image", name: "Application Image" },
];

function event(overrides: Partial<EventSummary> = {}): EventSummary {
  return {
    id: "event-1",
    received_at: "2026-08-05T01:02:03Z",
    event_type: "pipeline",
    provider: "gitlab",
    source_id: "gitlab-a",
    repository: "app",
    ref: "main",
    status: "success",
    external_id: "42",
    trigger: "push",
    revision: "0123456789abcdef",
    commit_message: "fix checkout timeout",
    routing_result: "deploy",
    rule_id: "deploy-main",
    delivery_summary: { kind: "uniform", count: 1, transport_status: "enqueued", deployment_status: "done" },
    deliveries: [{ id: "delivery-1", target_id: "production", current_attempt_id: "attempt-1", transport_status: "enqueued", deployment_status: "done", active: false, attention: false, allowed_operations: [] }],
    active: false,
    attention: false,
    ...overrides,
  };
}

it("keeps each target beside its execution status without a separate status column or route rule", () => {
  render(<EventTable repositories={repositories} events={[event()]} selectedID={null} onSelect={vi.fn()} onOperation={vi.fn()} />);

  expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
    "Received",
    "Repository",
    "Reference",
    "Revision",
    "Message",
    "Event",
    "Route target",
  ]);
  expect(screen.getByRole("columnheader", { name: "Route target" })).toBeInTheDocument();
  expect(screen.queryByRole("columnheader", { name: "Execution status" })).toBeNull();
  expect(screen.queryByRole("columnheader", { name: "Signal" })).toBeNull();
  expect(screen.queryByText("event-1")).toBeNull();
  expect(screen.getByText("Application")).toBeInTheDocument();
  expect(screen.getByText("GitLab")).toBeInTheDocument();
  expect(screen.getByText("Pipeline")).toBeInTheDocument();
  expect(screen.getByText("Success")).toBeInTheDocument();
  expect(screen.queryByText("gitlab-a")).toBeNull();
  expect(screen.getByText("production")).toBeInTheDocument();
  expect(screen.queryByText("deploy-main")).toBeNull();
  expect(screen.getByText("main")).toBeInTheDocument();
  expect(screen.getByText("fix checkout timeout")).toBeInTheDocument();
  expect(screen.getByText("0123456")).toBeInTheDocument();
  expect(screen.getByText("Deployment succeeded")).toBeInTheDocument();
  expect(screen.getByText("fix checkout timeout").closest("tbody")).toHaveClass("data-surface");
  expect(screen.getByRole("columnheader", { name: "Event" })).toHaveClass("w-[260px]");
  expect(screen.getByText("Pipeline").parentElement).toHaveClass("flex", "[&_[data-slot=badge]]:px-1.5");
  expect(screen.getByText("Pipeline").parentElement).not.toHaveClass("flex-wrap");
  const deliveryLine = screen.getByText("production").parentElement;
  expect(deliveryLine).toHaveTextContent("productionDeployment succeeded");
  expect(deliveryLine).toHaveClass("flex", "items-center", "gap-2");
  expect(deliveryLine).not.toHaveClass("rounded-lg", "border", "bg-muted/20", "px-2.5", "py-2");
  expect(deliveryLine).toHaveClass("[&_[data-slot=badge]]:px-1.5");
  expect(screen.getByText("production")).toHaveClass("text-muted-foreground");
});

it("shows Harbor artifact identity without Git commit terminology", () => {
  render(<EventTable repositories={repositories} events={[event({
    provider: "harbor",
    source_id: "harbor-a",
    repository: "application-image",
    event_type: "artifact_push",
    ref: "latest",
    revision: "sha256:0123456789abcdef",
    commit_message: null,
    status: null,
  })]} selectedID={null} onSelect={vi.fn()} onOperation={vi.fn()} />);

  expect(screen.getByText("Application Image")).toBeInTheDocument();
  expect(screen.getByText("Harbor")).toBeInTheDocument();
  expect(screen.getByText("Artifact push")).toBeInTheDocument();
  expect(screen.getByText("latest")).toBeInTheDocument();
  expect(screen.getByText("sha256:01234")).toBeInTheDocument();
  expect(screen.queryByText("No commit message")).toBeNull();
});

it("keeps row selection and exposes a retry icon only for an eligible failure", () => {
  const onSelect = vi.fn();
  const onOperation = vi.fn();
  const failed = event({ id: "event-2", deliveries: [{ id: "delivery-2", target_id: "staging", current_attempt_id: "attempt-2", transport_status: "failed", deployment_status: "not_started", active: false, attention: true, allowed_operations: ["retry"] }] });
  render(<EventTable repositories={repositories} events={[event(), failed]} selectedID="event-2" onSelect={onSelect} onOperation={onOperation} />);

  expect(screen.getByRole("row", { name: /staging/ })).toHaveAttribute("data-state", "selected");
  expect(screen.queryByRole("button", { name: "Retry production" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry staging" }));
  expect(onOperation).toHaveBeenCalledWith(failed.deliveries[0], "retry");
  fireEvent.click(screen.getByRole("button", { name: "View event event-1" }));
  expect(onSelect).toHaveBeenCalledWith("event-1");
});

it("shows a routing result when an event has no target delivery", () => {
  render(<EventTable repositories={repositories} events={[event({ routing_result: "unmatched", rule_id: null, delivery_summary: { kind: "none", count: 0, transport_status: null, deployment_status: null }, deliveries: [] })]} selectedID={null} onSelect={vi.fn()} onOperation={vi.fn()} />);
  expect(screen.getByText("Unmatched")).toHaveAttribute("data-tone", "unmatched");
  expect(screen.getByText("No delivery")).toBeInTheDocument();
});

it("shows a push lifecycle waiting for its pipeline without an unmatched route warning", () => {
  render(<EventTable repositories={repositories} events={[event({ event_type: "push", status: "waiting_for_pipeline", routing_result: "unmatched", delivery_summary: { kind: "none", count: 0, transport_status: null, deployment_status: null }, deliveries: [], active: true })]} selectedID={null} onSelect={vi.fn()} onOperation={vi.fn()} />);

  expect(screen.getByText("Push")).toBeInTheDocument();
  expect(screen.getByText("Waiting for pipeline")).toHaveAttribute("data-tone", "active");
  expect(screen.queryByText("Unmatched")).toBeNull();
  expect(screen.queryByText("No delivery")).toBeNull();
});

it("shows a push lifecycle timeout without an unmatched route warning", () => {
  render(<EventTable repositories={repositories} events={[event({ event_type: "push", status: "pipeline_not_received", routing_result: "unmatched", delivery_summary: { kind: "none", count: 0, transport_status: null, deployment_status: null }, deliveries: [], active: false })]} selectedID={null} onSelect={vi.fn()} onOperation={vi.fn()} />);

  expect(screen.getByText("Pipeline not received")).toHaveAttribute("data-tone", "failure");
  expect(screen.queryByText("Unmatched")).toBeNull();
  expect(screen.queryByText("No delivery")).toBeNull();
});

it("falls back to a stored repository ID that is no longer configured", () => {
  render(<EventTable repositories={repositories} events={[event({ repository: "removed" })]} selectedID={null} onSelect={vi.fn()} onOperation={vi.fn()} />);

  expect(screen.getByText("removed")).toBeInTheDocument();
});
