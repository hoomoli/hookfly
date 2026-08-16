import { Languages } from "lucide-react";
import { useTranslation } from "react-i18next";
import { type Language, SUPPORTED_LANGUAGES } from "../i18n";
import { Button } from "./ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

function isLanguage(value: string): value is Language {
  return SUPPORTED_LANGUAGES.some((language) => language === value);
}

export function LanguageMenu() {
  const { i18n, t } = useTranslation();
  const current = isLanguage(i18n.resolvedLanguage ?? "") ? i18n.resolvedLanguage as Language : "en";

  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button type="button" variant="secondary" size="icon" className="size-11 sm:size-9" aria-label={t("language.choose")}>
              <Languages className="size-4" aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("language.choose")}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end">
        <DropdownMenuRadioGroup value={current} onValueChange={(value) => { if (isLanguage(value)) void i18n.changeLanguage(value); }}>
          <DropdownMenuRadioItem value="en">{t("language.english", { lng: "en" })}</DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="zh-CN">{t("language.simplifiedChinese", { lng: "zh-CN" })}</DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
