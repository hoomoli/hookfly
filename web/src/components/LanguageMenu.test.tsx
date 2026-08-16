import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LANGUAGE_STORAGE_KEY } from "../i18n";
import { LanguageMenu } from "./LanguageMenu";
import { TooltipProvider } from "./ui/tooltip";

it("defaults to English and persists an accessible Simplified Chinese choice", async () => {
  const user = userEvent.setup();
  render(<TooltipProvider><LanguageMenu /></TooltipProvider>);

  const trigger = screen.getByRole("button", { name: "Choose language" });
  await user.click(trigger);
  expect(screen.getByRole("menuitemradio", { name: "English" })).toHaveAttribute("data-state", "checked");

  await user.click(screen.getByRole("menuitemradio", { name: "简体中文" }));

  expect(document.documentElement.lang).toBe("zh-CN");
  expect(localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("zh-CN");
});
