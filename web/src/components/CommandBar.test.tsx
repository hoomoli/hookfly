import { render, screen } from "@testing-library/react";
import { AppThemeProvider } from "../theme";
import { CommandBar } from "./CommandBar";
import { TooltipProvider } from "./ui/tooltip";

it("hides the synchronized state while keeping reload status accessible", () => {
  render(
    <AppThemeProvider>
      <TooltipProvider>
        <CommandBar
          reloadLabel="Reload configuration"
          reloadDisabled={false}
          recovery={false}
          onReload={vi.fn()}
          signalTone={null}
          signalLabel={null}
        />
      </TooltipProvider>
    </AppThemeProvider>,
  );

  expect(screen.getByRole("heading", { name: "Deployment activity ledger" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Reload configuration" })).toBeInTheDocument();
  expect(screen.getAllByText("Reload configuration")).toHaveLength(2);
  expect(screen.getByRole("status")).toHaveClass("sr-only");
  expect(screen.queryByText("Synchronized")).not.toBeInTheDocument();
  expect(document.querySelector('[data-slot="sync-status"]')).toBeNull();
});

it("keeps non-idle synchronization feedback visible", () => {
  render(
    <AppThemeProvider>
      <TooltipProvider>
        <CommandBar
          reloadLabel="Reload configuration"
          reloadDisabled={false}
          recovery={false}
          onReload={vi.fn()}
          signalTone="error"
          signalLabel="Synchronization failed"
        />
      </TooltipProvider>
    </AppThemeProvider>,
  );

  expect(screen.getByText("Synchronization failed")).toHaveAttribute("data-slot", "sync-status");
});
