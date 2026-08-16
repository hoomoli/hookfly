import { fireEvent, render, screen } from "@testing-library/react";
import { i18n } from "../i18n";
import { zhCN } from "../locales/zh-CN";
import { TargetsPage } from "./TargetsPage";

const inventory = {
  data: {
    targets: [{
      id: "production", connection_id: "primary", resource_type: "compose", resource_id: "compose-1",
      project_name: "Project", environment_name: "Production", name: "API", app_name: "api-abc",
      status: "done", condition: "available" as const, runtime_status: "all_running" as const,
      containers: [
        { name: "api-1", state: "running", health: "healthy", restart_count: 0 },
        { name: "worker-1", state: "exited", restart_count: 1 },
      ],
    }],
    refreshed_at: "2026-08-11T10:00:00Z",
    errors: [],
  },
  loading: false,
  error: null,
};

it("shows the last deployment and a compact running-container count", () => {
  const refresh = vi.fn();
  render(<TargetsPage inventory={{ ...inventory, refresh }} />);

  expect(screen.getByRole("heading", { name: "Targets" })).toBeInTheDocument();
  for (const value of ["production", "primary", "Project", "Production", "API", "api-abc", "compose-1", "Succeeded", "1 / 2", "api-1", "worker-1"]) {
    expect(screen.getByText(value)).toBeInTheDocument();
  }
  expect(screen.getByText(/Health: healthy/)).toBeInTheDocument();
  expect(screen.getByText("Last deployment")).toBeInTheDocument();
  expect(screen.getByText("Container runtime")).toBeInTheDocument();
  expect(screen.queryByText("Inventory status")).toBeNull();
  expect(screen.queryByText("Available")).toBeNull();
  expect(screen.queryByText(/Result of the latest Dokploy/)).toBeNull();
  expect(screen.queryByText(/Live runtime and health summary/)).toBeNull();
  const runtimeRow = screen.getByText("1 / 2").parentElement;
  expect(runtimeRow?.closest("tbody")).toHaveClass("data-surface");
  expect(runtimeRow).toHaveClass("flex", "items-center");
  expect(runtimeRow).toHaveTextContent("1 / 2Container details");
  fireEvent.click(screen.getByRole("button", { name: "Refresh targets" }));
  expect(refresh).toHaveBeenCalledOnce();
});

it("translates the two visible target states in Simplified Chinese", async () => {
  await i18n.changeLanguage("zh-CN");
  render(<TargetsPage inventory={{ ...inventory, refresh: vi.fn() }} />);

  const copy = zhCN.translation.targets;
  expect(screen.getByRole("heading", { name: copy.heading })).toBeInTheDocument();
  for (const value of [copy.composeStatus, copy.runtimeStatus, copy.deploymentStatuses.done, "1 / 2"]) {
    expect(screen.getByText(value)).toBeInTheDocument();
  }
  expect(screen.getAllByRole("columnheader")).toHaveLength(8);
  expect(screen.queryByText(copy.conditions.available)).toBeNull();
  expect(screen.getByText(new RegExp(`${copy.containerStates.running}.*${copy.healthStatuses.healthy}`))).toBeInTheDocument();
  await i18n.changeLanguage("en");
});

it.each([
  { states: [] as string[], count: "0 / 0", tone: "neutral" },
  { states: ["exited", "dead"], count: "0 / 2", tone: "failure" },
  { states: ["running", "exited"], count: "1 / 2", tone: "attention" },
  { states: ["running", "running"], count: "2 / 2", tone: "stable" },
])("colors $count from the live container count", ({ states, count, tone }) => {
  const targets = inventory.data.targets.map((target) => ({
    ...target,
    containers: states.map((state, index) => ({ name: `container-${index}`, state, restart_count: 0 })),
  }));
  render(<TargetsPage inventory={{ ...inventory, data: { ...inventory.data, targets }, refresh: vi.fn() }} />);

  expect(screen.getByText(count)).toHaveAttribute("data-tone", tone);
});
