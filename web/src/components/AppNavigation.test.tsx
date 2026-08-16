import { fireEvent, render, screen, within } from "@testing-library/react";
import type { AuthSession, Repository } from "../types";
import { AppNavigation } from "./AppNavigation";

const repositories: Repository[] = [
  { provider: "gitlab", source_id: "gitlab-a", id: "app", name: "Application" },
  { provider: "github", source_id: "github-a", id: "app", name: "Application" },
];
const session: AuthSession = { user: { display_name: "De Jong", username: "dj", provider: "authentik" }, expires_at: "2026-08-12T16:00:00Z" };

it("keeps repository identity in the Events section and exposes a separate Configuration section", () => {
  // Break caught: mixing connections into repository identity or removing same-ID source context.
  const onSelectRepository = vi.fn();
  const onSelectPage = vi.fn();
  render(
    <AppNavigation
      repositories={repositories}
      selected={{ kind: "all" }}
      page="ledger"
      onSelectRepository={onSelectRepository}
      onSelectPage={onSelectPage}
      session={session}
      onSignOut={vi.fn()}
    />,
  );

  expect(screen.getByText("Repositories")).toBeInTheDocument();
  expect(screen.getByText("Configuration")).toBeInTheDocument();
  const gitLab = screen.getByRole("button", { name: "app · gitlab · gitlab-a" });
  expect(gitLab).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "app · github · github-a" })).toBeInTheDocument();
  expect(within(gitLab).getByText("app", { selector: "span" })).toHaveClass("text-xs");

  fireEvent.click(gitLab);
  expect(onSelectRepository).toHaveBeenCalledWith({ kind: "repository", sourceID: "gitlab-a", repositoryID: "app" });
  expect(onSelectPage).not.toHaveBeenCalled();

  fireEvent.click(screen.getByRole("button", { name: "Connections" }));
  expect(onSelectPage).toHaveBeenCalledWith("connections");
});

it("exposes Targets as a separate configuration page", () => {
  const onSelectPage = vi.fn();
  render(<AppNavigation repositories={[]} selected={{ kind: "all" }} page="targets" onSelectRepository={vi.fn()} onSelectPage={onSelectPage} session={session} onSignOut={vi.fn()} />);
  const targets = screen.getByRole("button", { name: "Targets" });
  expect(targets).toHaveAttribute("aria-pressed", "true");
  fireEvent.click(targets);
  expect(onSelectPage).toHaveBeenCalledWith("targets");
});

it("marks Connections independently from the event ledger selection", () => {
  // Break caught: making the connection page appear as a repository selection.
  render(
    <AppNavigation
      repositories={repositories}
      selected={{ kind: "repository", sourceID: "gitlab-a", repositoryID: "app" }}
      page="connections"
      onSelectRepository={() => undefined}
      onSelectPage={() => undefined}
      session={session}
      onSignOut={vi.fn()}
    />,
  );

  expect(screen.getByRole("button", { name: "Connections" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "app · gitlab · gitlab-a" })).toHaveAttribute("aria-pressed", "false");
});

it("keeps the current user reachable in desktop and compact navigation", () => {
  // Break caught: authentication succeeds but the navigation exposes no current-user control.
  render(<AppNavigation repositories={[]} selected={{ kind: "all" }} page="ledger" onSelectRepository={vi.fn()} onSelectPage={vi.fn()} session={session} onSignOut={vi.fn()} />);
  const account = screen.getByRole("button", { name: "Account menu for De Jong" });
  expect(account).toHaveTextContent("DJ");
  expect(account.parentElement).toHaveClass("lg:mt-auto");
});
