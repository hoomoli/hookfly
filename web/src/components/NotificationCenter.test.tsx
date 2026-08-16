import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { NotificationCenter } from "./NotificationCenter";
import type { NotificationController } from "../hooks/useNotifications";
import { defaultNotificationState } from "../notifications";

function controller(): NotificationController {
  return {
    state: { ...defaultNotificationState(), initialized: true, cursor: "c", unread_ids: ["failure"], items: [
      { id: "failure", cursor: "a", category: "deployment", outcome: "timeout", event_id: "event-f", target_id: "payments-api", repository: "app", summary: "Exceeded monitoring deadline", occurred_at: "2026-08-12T00:00:00Z" },
      { id: "success", cursor: "b", category: "deployment", outcome: "success", event_id: "event-s", target_id: "console-web", repository: "app", summary: "Deployment succeeded", occurred_at: "2026-08-12T00:01:00Z" },
    ] },
    unreadCount: 1, error: null, systemState: "default", setStatusEnabled: vi.fn(), resetRepositoryPreferences: vi.fn(), enableSystemNotifications: vi.fn(), sendTestSystemNotification: vi.fn(), markRead: vi.fn(), markAllRead: vi.fn(), clearHistory: vi.fn(),
  };
}

describe("NotificationCenter", () => {
  it("filters target-first tagged notifications and opens their event", async () => {
    const value = controller(); const open = vi.fn();
    render(<NotificationCenter controller={value} onOpenEvent={open} />);
    const trigger = screen.getByRole("button", { name: /notifications.*1/i });
    expect(trigger).toHaveClass("order-last");
    await userEvent.click(trigger);
    expect(screen.queryByRole("button", { name: "Notification settings" })).not.toBeInTheDocument();
    expect(screen.getByText("payments-api")).toBeInTheDocument();
    expect(screen.getByText("Timed out")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Success" }));
    expect(screen.queryByText("payments-api")).not.toBeInTheDocument();
    await userEvent.click(screen.getByText("console-web"));
    expect(value.markRead).toHaveBeenCalledWith("success");
    expect(open).toHaveBeenCalledWith("event-s");
  });

  it("filters push notifications and opens the matching event", async () => {
    const value = controller(); const open = vi.fn();
    value.state.items = [
      { id: "pipeline-101", cursor: "a", category: "pipeline", outcome: "running", event_id: "event-pipeline-101", repository: "engineering/api", summary: "Pipeline is running", occurred_at: "2026-08-12T00:00:00Z" },
      { id: "deployment-102", cursor: "b", category: "deployment", outcome: "success", event_id: "event-deployment-102", target_id: "api-production", repository: "engineering/api", summary: "Deployment succeeded", occurred_at: "2026-08-12T00:01:00Z" },
      { id: "push-103", cursor: "c", category: "push", outcome: "received", event_id: "event-push-103", provider: "github", source_id: "github-a", repository: "engineering/docs", summary: "Pushed 4f8c2a1 to main", occurred_at: "2026-08-12T00:02:00Z" },
    ];
    render(<NotificationCenter controller={value} onOpenEvent={open} />);

    await userEvent.click(screen.getByRole("button", { name: /notifications.*1/i }));
    await userEvent.click(screen.getByRole("button", { name: "Push" }));

    expect(screen.getByText("engineering/docs")).toBeInTheDocument();
    expect(screen.getByText("Received")).toBeInTheDocument();
    expect(screen.getByText("Pushed 4f8c2a1 to main")).toBeInTheDocument();
    expect(screen.queryByText("engineering/api")).not.toBeInTheDocument();
    expect(screen.queryByText("api-production")).not.toBeInTheDocument();
    await userEvent.click(screen.getByText("engineering/docs"));
    expect(value.markRead).toHaveBeenCalledWith("push-103");
    expect(open).toHaveBeenCalledWith("event-push-103");
  });
});
