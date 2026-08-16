import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { DisplaySettingsProvider } from "../display-settings";
import type { NotificationController } from "../hooks/useNotifications";
import { defaultNotificationState, setNotificationStatus } from "../notifications";
import type { Repository } from "../types";
import { SettingsCenter } from "./SettingsCenter";
import { TooltipProvider } from "./ui/tooltip";

const repositories: Repository[] = [
  { provider: "gitlab", source_id: "gitlab-primary", id: "app", name: "Application" },
  { provider: "github", source_id: "github-secondary", id: "app", name: "Application" },
];

function notificationController(overrides: Partial<NotificationController> = {}): NotificationController {
  return {
    state: { ...defaultNotificationState(), initialized: true },
    unreadCount: 0,
    error: null,
    systemState: "default",
    setStatusEnabled: vi.fn(),
    resetRepositoryPreferences: vi.fn(),
    enableSystemNotifications: vi.fn(),
    sendTestSystemNotification: vi.fn(),
    markRead: vi.fn(),
    markAllRead: vi.fn(),
    clearHistory: vi.fn(),
    ...overrides,
  };
}

function renderSettings(controller: NotificationController, configuredRepositories: Repository[] = repositories, onHistoryCleared = vi.fn()) {
  return render(<DisplaySettingsProvider><TooltipProvider><SettingsCenter controller={controller} repositories={configuredRepositories} onHistoryCleared={onHistoryCleared} /></TooltipProvider></DisplaySettingsProvider>);
}

async function openNotificationSettings() {
  await userEvent.click(screen.getByRole("button", { name: "Settings center" }));
  await userEvent.click(screen.getByRole("button", { name: "Notifications" }));
}

async function openHistorySettings() {
  await userEvent.click(screen.getByRole("button", { name: "Settings center" }));
  await userEvent.click(screen.getByRole("button", { name: "History" }));
}

it("deletes event history before clearing client state and invalidating the ledger", async () => {
  let resolveDelete!: (response: Response) => void;
  const requests: Array<{ url: string; method: string | undefined }> = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), method: init?.method });
    return new Promise<Response>((resolve) => { resolveDelete = resolve; });
  }));
  const clearHistory = vi.fn();
  const onHistoryCleared = vi.fn();
  renderSettings(notificationController({ clearHistory }), repositories, onHistoryCleared);
  await openHistorySettings();

  await userEvent.click(screen.getByRole("button", { name: "Clear all history" }));
  const dialog = screen.getByRole("dialog", { name: "Clear all event history?" });
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear all history" }));

  expect(requests).toEqual([{ url: "/api/v1/events", method: "DELETE" }]);
  expect(clearHistory).not.toHaveBeenCalled();
  expect(onHistoryCleared).not.toHaveBeenCalled();

  resolveDelete(new Response(JSON.stringify({ deleted: 3 }), { status: 200 }));
  await waitFor(() => expect(clearHistory).toHaveBeenCalledOnce());
  expect(onHistoryCleared).toHaveBeenCalledOnce();
});

it("keeps active-history errors visible without mutating client state", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "history_active", message: "unstable server text" } }), { status: 409 })));
  const clearHistory = vi.fn();
  const onHistoryCleared = vi.fn();
  renderSettings(notificationController({ clearHistory }), repositories, onHistoryCleared);
  await openHistorySettings();

  await userEvent.click(screen.getByRole("button", { name: "Clear all history" }));
  const dialog = screen.getByRole("dialog", { name: "Clear all event history?" });
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear all history" }));

  expect(await within(dialog).findByRole("alert")).toHaveTextContent("Active deployments or queued events must finish first.");
  expect(clearHistory).not.toHaveBeenCalled();
  expect(onHistoryCleared).not.toHaveBeenCalled();
});

it("shows concrete Push, Pipeline, and Deployment outcomes in non-wrapping rows", async () => {
  renderSettings(notificationController());

  await openNotificationSettings();

  expect(screen.getAllByRole("checkbox").map((checkbox) => checkbox.getAttribute("aria-label"))).toEqual([
    "Push: Received",
    "Pipeline: Initializing",
    "Pipeline: Waiting",
    "Pipeline: Running",
    "Pipeline: Success",
    "Pipeline: Failed",
    "Pipeline: Cancelled",
    "Deployment: Waiting to trigger",
    "Deployment: Triggering",
    "Deployment: Running",
    "Deployment: Success",
    "Deployment: Failed",
    "Deployment: Timed out",
    "Deployment: Cancelled",
  ]);
  for (const groupName of ["Push", "Pipeline", "Deployment"]) {
    const group = screen.getByRole("group", { name: groupName });
    const rowWrapper = group.lastElementChild;
    expect(group).toHaveClass("w-full", "min-w-0");
    expect(group).not.toHaveClass("overflow-x-auto");
    expect(rowWrapper).toHaveClass("max-w-full", "overflow-x-auto");
    expect(rowWrapper?.firstElementChild).toHaveClass("flex", "min-w-max", "flex-nowrap");
  }
  expect(screen.getByRole("group", { name: "Notification preference scope" })).toBeInTheDocument();
});

it("edits an inheriting repository and disambiguates identical repository names", async () => {
  const setStatusEnabled = vi.fn();
  renderSettings(notificationController({ setStatusEnabled }));
  await openNotificationSettings();

  await userEvent.click(screen.getByRole("button", { name: "Repository Overrides" }));
  expect(screen.getByText("Inherits global")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
  expect(screen.getByRole("option", { name: "Application · GitLab · gitlab-primary" })).toBeInTheDocument();
  expect(screen.getByRole("option", { name: "Application · GitHub · github-secondary" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("option", { name: "Application · GitHub · github-secondary" }));

  await userEvent.click(screen.getByRole("checkbox", { name: "Push: Received" }));
  expect(setStatusEnabled).toHaveBeenCalledWith("push.received", true, repositories[1]);
});

it("shows and resets a custom repository override", async () => {
  const resetRepositoryPreferences = vi.fn();
  const state = setNotificationStatus(defaultNotificationState(), "push.received", true, repositories[0]);
  renderSettings(notificationController({ state, resetRepositoryPreferences }));
  await openNotificationSettings();

  await userEvent.click(screen.getByRole("button", { name: "Repository Overrides" }));
  expect(screen.getByText("Custom")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Reset to global" }));

  expect(resetRepositoryPreferences).toHaveBeenCalledWith(repositories[0]);
});

it("handles empty repositories and a disappearing first selection", async () => {
  const controller = notificationController();
  const view = renderSettings(controller);
  await openNotificationSettings();
  await userEvent.click(screen.getByRole("button", { name: "Repository Overrides" }));

  view.rerender(<DisplaySettingsProvider><TooltipProvider><SettingsCenter controller={controller} repositories={[repositories[1]]} onHistoryCleared={vi.fn()} /></TooltipProvider></DisplaySettingsProvider>);
  expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent("Application · GitHub · github-secondary");

  view.rerender(<DisplaySettingsProvider><TooltipProvider><SettingsCenter controller={controller} repositories={[]} onHistoryCleared={vi.fn()} /></TooltipProvider></DisplaySettingsProvider>);
  expect(screen.getByText("No repositories configured.")).toBeInTheDocument();
  expect(screen.queryByRole("checkbox", { name: "Push: Received" })).not.toBeInTheDocument();
});

it("separates display and per-status notification settings", async () => {
  const setStatusEnabled = vi.fn();
  const enableSystemNotifications = vi.fn();
  const controller: NotificationController = {
    state: { ...defaultNotificationState(), initialized: true },
    unreadCount: 0,
    error: null,
    systemState: "default",
    setStatusEnabled,
    resetRepositoryPreferences: vi.fn(),
    enableSystemNotifications,
    sendTestSystemNotification: vi.fn(),
    markRead: vi.fn(),
    markAllRead: vi.fn(),
    clearHistory: vi.fn(),
  };
  renderSettings(controller);

  await userEvent.click(screen.getByRole("button", { name: "Settings center" }));
  expect(screen.getByRole("dialog", { name: "Settings center" })).toHaveTextContent("Data font size");
  await userEvent.click(screen.getByRole("button", { name: "Notifications" }));
  expect(screen.getByRole("checkbox", { name: "Pipeline: Initializing" })).not.toBeChecked();
  expect(screen.getByRole("checkbox", { name: "Pipeline: Success" })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: "Deployment: Timed out" })).toBeChecked();
  expect(screen.getByRole("heading", { name: "System notifications" })).toBeInTheDocument();

  await userEvent.click(screen.getByRole("button", { name: "Enable system notifications" }));
  expect(enableSystemNotifications).toHaveBeenCalledOnce();

  await userEvent.click(screen.getByRole("checkbox", { name: "Pipeline: Initializing" }));
  expect(setStatusEnabled).toHaveBeenCalledWith("pipeline.initializing", true);
});

it.each([
  ["granted", true, "System notifications are enabled."],
  ["denied", false, "Notifications are blocked. Restore permission in your browser or system settings."],
  ["unavailable", false, "System notifications require browser support and a secure connection."],
] as const)("shows the %s system notification state without an enable action", async (systemState, systemEnabled, message) => {
  const controller: NotificationController = {
    state: { ...defaultNotificationState(), initialized: true, system_enabled: systemEnabled },
    unreadCount: 0,
    error: null,
    systemState,
    setStatusEnabled: vi.fn(),
    resetRepositoryPreferences: vi.fn(),
    enableSystemNotifications: vi.fn(),
    sendTestSystemNotification: vi.fn(),
    markRead: vi.fn(),
    markAllRead: vi.fn(),
    clearHistory: vi.fn(),
  };
  renderSettings(controller);

  await userEvent.click(screen.getByRole("button", { name: "Settings center" }));
  await userEvent.click(screen.getByRole("button", { name: "Notifications" }));

  expect(screen.getByText(message)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Enable system notifications" })).not.toBeInTheDocument();
});

it("sends a localized test notification and explains the OS-level check", async () => {
  const sendTestSystemNotification = vi.fn();
  renderSettings(notificationController({
    state: { ...defaultNotificationState(), initialized: true, system_enabled: true },
    systemState: "granted",
    sendTestSystemNotification,
  }));
  await openNotificationSettings();

  expect(screen.getByText("If no notification appears, check your browser and operating system notification settings.")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Send test notification" }));

  expect(sendTestSystemNotification).toHaveBeenCalledWith("Hookfly test notification", "System notifications can reach this device.");
});

it("keeps the enable action when browser permission exists but Hookfly is not enabled", async () => {
  const enableSystemNotifications = vi.fn();
  const controller: NotificationController = {
    state: { ...defaultNotificationState(), initialized: true, system_enabled: false },
    unreadCount: 0,
    error: null,
    systemState: "granted",
    setStatusEnabled: vi.fn(),
    resetRepositoryPreferences: vi.fn(),
    enableSystemNotifications,
    sendTestSystemNotification: vi.fn(),
    markRead: vi.fn(),
    markAllRead: vi.fn(),
    clearHistory: vi.fn(),
  };
  renderSettings(controller);

  await userEvent.click(screen.getByRole("button", { name: "Settings center" }));
  await userEvent.click(screen.getByRole("button", { name: "Notifications" }));
  await userEvent.click(screen.getByRole("button", { name: "Enable system notifications" }));

  expect(enableSystemNotifications).toHaveBeenCalledOnce();
});
