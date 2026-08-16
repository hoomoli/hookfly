import { render, screen } from "@testing-library/react";
import { SourceStatus } from "./SourceStatus";

it.each(["waiting_for_pipeline", "created", "preparing", "scheduled", "pending", "queued", "waiting_for_resource", "requested", "running", "in_progress"])("animates active source status %s", (status) => {
  render(<SourceStatus status={status} />);
  expect(screen.getByTestId("source-status-icon")).toHaveClass("animate-spin");
});

it("keeps a successful source status static", () => {
  render(<SourceStatus status="success" />);
  expect(screen.getByTestId("source-status-icon")).not.toHaveClass("animate-spin");
});

it("can render an active historical status without animation", () => {
  render(<SourceStatus status="running" animated={false} />);
  expect(screen.getByTestId("source-status-icon")).not.toHaveClass("animate-spin");
});

it("can render a superseded active status as completed", () => {
  render(<SourceStatus status="pending" completed />);
  expect(screen.getByTestId("source-status-icon")).toHaveClass("lucide-circle-check");
  expect(screen.getByTestId("source-status-icon").closest("[data-slot='badge']")).toHaveAttribute("data-tone", "stable");
});

it("does not override a terminal failure as completed", () => {
  render(<SourceStatus status="failed" completed />);
  expect(screen.getByTestId("source-status-icon")).toHaveClass("lucide-circle-alert");
  expect(screen.getByTestId("source-status-icon").closest("[data-slot='badge']")).toHaveAttribute("data-tone", "failure");
});

it("renders provider canceled as a terminal failure", () => {
  render(<SourceStatus status="canceled" />);
  expect(screen.getByTestId("source-status-icon")).toHaveClass("lucide-circle-alert");
  expect(screen.getByTestId("source-status-icon").closest("[data-slot='badge']")).toHaveAttribute("data-tone", "failure");
  expect(screen.getByText("Canceled")).toBeInTheDocument();
});
