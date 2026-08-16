import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { Button } from "./ui/button";
import { LanguageMenu } from "./LanguageMenu";
import { ThemeMenu } from "./ThemeMenu";

export type SignalTone = "active" | "attention" | "error";

interface CommandBarProps {
  globalActions?: ReactNode;
  reloadLabel: string;
  reloadDisabled: boolean;
  recovery: boolean;
  onReload: () => void;
  signalTone: SignalTone | null;
  signalLabel: string | null;
}

const signalClasses: Record<SignalTone, string> = {
  active: "bg-primary",
  attention: "bg-warning",
  error: "bg-destructive",
};

export function CommandBar({ globalActions, reloadLabel, reloadDisabled, recovery, onReload, signalTone, signalLabel }: CommandBarProps) {
  const { t } = useTranslation();
  return (
    <header className="flex min-h-20 flex-col justify-center gap-3 border-b border-border py-4 sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0">
        <p className="mb-1 text-[11px] font-medium tracking-[0.12em] text-muted-foreground">{t("command.eyebrow")}</p>
        <h1 className="truncate text-[clamp(1.25rem,2vw,1.75rem)] font-semibold tracking-[-0.025em]">{t("command.heading")}</h1>
      </div>
      <div className="flex min-h-11 max-w-full flex-nowrap items-center gap-1.5 overflow-x-auto" data-slot="command-actions">
        {globalActions}
        <ThemeMenu />
        <LanguageMenu />
        <Button
          type="button"
          variant="primary"
          className="h-11 min-w-32 px-4 sm:h-9"
          disabled={reloadDisabled}
          onClick={onReload}
        >
          {reloadLabel}
        </Button>
        <span className="sr-only" role="status" aria-live="polite">
          {recovery ? t("command.recovery", { label: reloadLabel }) : reloadLabel}
        </span>
        {signalTone && signalLabel && <span
          className="flex h-11 items-center gap-2 whitespace-nowrap px-1 text-xs text-muted-foreground sm:h-9"
          data-slot="sync-status"
          data-tone={signalTone}
          aria-live="polite"
        >
          <span className={`size-1.5 rounded-full ${signalClasses[signalTone]}`} aria-hidden="true" />
          {signalLabel}
        </span>}
      </div>
    </header>
  );
}
