import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AuthSession } from "../types";
import { UserMenu } from "./UserMenu";

const oidcSession: AuthSession = {
  user: { display_name: "dj", username: "dj", provider: "authentik" },
  expires_at: "2026-08-12T16:00:00Z",
};

it("shows the minimal current Authentik user and signs out from the account menu", async () => {
  // Break caught: adding login without a visible identity/logout control, or leaking claims into the UI.
  const onSignOut = vi.fn(async () => undefined);
  render(<UserMenu session={oidcSession} onSignOut={onSignOut} />);

  const trigger = screen.getByRole("button", { name: "Account menu for dj" });
  expect(trigger).toHaveTextContent("DJ");
  expect(trigger).toHaveTextContent("dj");
  expect(trigger).toHaveTextContent("Hookfly user");

  await userEvent.click(trigger);
  expect(screen.getByText("Authentik")).toBeInTheDocument();
  expect(screen.queryByText(/email|groups|subject|token/i)).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("menuitem", { name: "Sign out" }));
  expect(onSignOut).toHaveBeenCalledTimes(1);
});

it("shows a distinct username once inside the account menu", async () => {
  render(<UserMenu session={{ ...oidcSession, user: { ...oidcSession.user, display_name: "De Jong", username: "dj" } }} onSignOut={vi.fn()} />);

  await userEvent.click(screen.getByRole("button", { name: "Account menu for De Jong" }));
  expect(screen.getAllByText("dj")).toHaveLength(1);
});

it("shows local development without a misleading logout action", async () => {
  const local: AuthSession = { user: { display_name: "Local development", provider: "local" } };
  render(<UserMenu session={local} onSignOut={vi.fn()} />);

  const trigger = screen.getByRole("button", { name: "Account menu for Local development" });
  expect(trigger).toHaveTextContent("Local development");
  await userEvent.click(trigger);
  expect(screen.getAllByText("Local authentication disabled")).toHaveLength(2);
  expect(screen.queryByRole("menuitem", { name: "Sign out" })).not.toBeInTheDocument();
});

it("opens from the keyboard and exposes local initials instead of a remote image", async () => {
  render(<UserMenu session={{ ...oidcSession, user: { ...oidcSession.user, display_name: "De Jong" } }} onSignOut={vi.fn()} />);
  const trigger = screen.getByRole("button", { name: "Account menu for De Jong" });
  trigger.focus();
  await userEvent.keyboard("{Enter}");

  expect(screen.getByRole("menu")).toBeInTheDocument();
  expect(screen.getAllByText("DJ").length).toBeGreaterThan(0);
  expect(document.querySelector("img")).toBeNull();
});
