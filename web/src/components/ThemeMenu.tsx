import { Monitor, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useTranslation } from "react-i18next";
import { Button } from "./ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

const choices = [
  { value: "system", labelKey: "theme.system", Icon: Monitor },
  { value: "light", labelKey: "theme.light", Icon: Sun },
  { value: "dark", labelKey: "theme.dark", Icon: Moon },
] as const;

export function ThemeMenu() {
  const { t } = useTranslation();
  const { theme = "system", setTheme } = useTheme();
  const CurrentIcon = choices.find((choice) => choice.value === theme)?.Icon ?? Monitor;

  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button type="button" variant="secondary" size="icon" className="size-11 sm:size-9" aria-label={t("theme.choose")}>
              <CurrentIcon className="size-4" aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("theme.choose")}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end">
        <DropdownMenuRadioGroup value={theme} onValueChange={setTheme}>
          {choices.map(({ value, labelKey, Icon }) => (
            <DropdownMenuRadioItem key={value} value={value}>
              <Icon className="size-4 text-muted-foreground" aria-hidden="true" />
              {t(labelKey)}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
