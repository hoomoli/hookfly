import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeMenu } from "./ThemeMenu";
import { AppThemeProvider, THEME_STORAGE_KEY } from "../theme";
import { TooltipProvider } from "./ui/tooltip";

function renderThemeMenu() {
  return render(<AppThemeProvider><TooltipProvider><ThemeMenu /></TooltipProvider></AppThemeProvider>);
}

it("persists a manual theme without coupling appearance to the API", async () => {
  const user = userEvent.setup();
  const fetchSpy = vi.fn();
  vi.stubGlobal("fetch", fetchSpy);
  renderThemeMenu();

  const trigger = screen.getByRole("button", { name: "Choose theme" });
  await user.click(trigger);
  await user.click(screen.getByRole("menuitemradio", { name: "Light" }));

  expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
  await waitFor(() => expect(document.documentElement).toHaveClass("light"));
  expect(fetchSpy).not.toHaveBeenCalled();
});

it("exposes all three choices and returns focus when dismissed with Escape", async () => {
  const user = userEvent.setup();
  renderThemeMenu();

  const trigger = screen.getByRole("button", { name: "Choose theme" });
  await user.click(trigger);
  expect(screen.getByRole("menuitemradio", { name: "System" })).toHaveAttribute("data-state", "checked");
  expect(screen.getByRole("menuitemradio", { name: "Light" })).toBeInTheDocument();
  expect(screen.getByRole("menuitemradio", { name: "Dark" })).toBeInTheDocument();

  await user.keyboard("{Escape}");
  expect(trigger).toHaveFocus();
});
