import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { NotificationController } from "../hooks/useNotifications";
import { defaultNotificationState } from "../notifications";
import { SystemNotificationPrompt } from "./SystemNotificationPrompt";

function controller(overrides: Partial<NotificationController> = {}): NotificationController {
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

it("prompts the user to enable system notifications from the page", async () => {
  const enableSystemNotifications = vi.fn();
  render(<SystemNotificationPrompt controller={controller({ enableSystemNotifications })} />);

  expect(screen.getByText("System notifications are off")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Enable system notifications" }));

  expect(enableSystemNotifications).toHaveBeenCalledOnce();
});

it.each([
  ["denied", "System notifications are blocked", "Notifications are blocked. Restore permission in your browser or system settings."],
  ["unavailable", "System notifications are unavailable", "System notifications require browser support and a secure connection."],
] as const)("explains the %s state without offering a browser permission request", (systemState, title, description) => {
  render(<SystemNotificationPrompt controller={controller({ systemState })} />);

  expect(screen.getByText(title)).toBeInTheDocument();
  expect(screen.getByText(description)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Enable system notifications" })).not.toBeInTheDocument();
});

it("stays hidden when browser permission and Hookfly notifications are enabled", () => {
  const value = controller({
    systemState: "granted",
    state: { ...defaultNotificationState(), initialized: true, system_enabled: true },
  });

  const { container } = render(<SystemNotificationPrompt controller={value} />);

  expect(container).toBeEmptyDOMElement();
});
