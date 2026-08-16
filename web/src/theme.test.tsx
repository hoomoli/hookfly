import { act, render, screen } from "@testing-library/react";
import { useTheme } from "next-themes";
import { setSystemDark } from "./test-setup";
import { AppThemeProvider } from "./theme";

function ThemeProbe() {
  const { theme, resolvedTheme } = useTheme();
  return <output>{theme} / {resolvedTheme}</output>;
}

it("defaults to system and follows system appearance changes", async () => {
  setSystemDark(true);
  render(<AppThemeProvider><ThemeProbe /></AppThemeProvider>);
  expect(await screen.findByText("system / dark")).toBeInTheDocument();

  act(() => setSystemDark(false));
  expect(await screen.findByText("system / light")).toBeInTheDocument();
});

it("falls back to system when theme storage cannot be read", async () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("storage denied"); });
  setSystemDark(true);

  render(<AppThemeProvider><ThemeProbe /></AppThemeProvider>);

  expect(await screen.findByText("system / dark")).toBeInTheDocument();
});
