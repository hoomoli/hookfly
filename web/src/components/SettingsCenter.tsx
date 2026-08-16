import { BellRing, Settings2 } from "lucide-react";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { DATA_FONT_SIZES, useDisplaySettings } from "../display-settings";
import { ApiError, clearHistory } from "../api";
import type { NotificationController } from "../hooks/useNotifications";
import { effectiveNotificationPreferences, hasRepositoryPreferences, repositoryPreferenceKey } from "../notifications";
import type { NotificationCategory, NotificationOutcome, NotificationStatusKey, Repository } from "../types";
import { Badge, type BadgeProps } from "./ui/badge";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { HistoryClearDialog } from "./HistoryClearDialog";
import { Dialog, DialogContent, DialogDescription, DialogTitle, DialogTrigger } from "./ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

const pixels = { compact: 11, standard: 12, comfortable: 14 } as const;
const statusGroups: Array<{ category: NotificationCategory; outcomes: NotificationOutcome[] }> = [
  { category: "push", outcomes: ["received"] },
  { category: "pipeline", outcomes: ["initializing", "waiting", "running", "success", "failure", "cancelled"] },
  { category: "deployment", outcomes: ["pending", "triggering", "running", "success", "failure", "timeout", "cancelled"] },
];

function statusTone(outcome: NotificationOutcome): BadgeProps["tone"] {
  if (outcome === "success") return "stable";
  if (outcome === "failure") return "failure";
  if (outcome === "timeout" || outcome === "cancelled") return "attention";
  return "neutral";
}

export function SettingsCenter({ controller, repositories, onHistoryCleared }: { controller: NotificationController; repositories: Repository[]; onHistoryCleared: () => void }) {
  const { t } = useTranslation();
  const { dataFontSize, setDataFontSize } = useDisplaySettings();
  const [section, setSection] = useState<"display" | "notifications" | "history">("display");
  const [notificationScope, setNotificationScope] = useState<"global" | "repository">("global");
  const [selectedRepositoryKey, setSelectedRepositoryKey] = useState(() => repositories[0] ? repositoryPreferenceKey(repositories[0]) : "");
  const optionRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const systemDisplayState = controller.systemState === "granted" && !controller.state.system_enabled ? "default" : controller.systemState;
  const selectedRepository = repositories.find((repository) => repositoryPreferenceKey(repository) === selectedRepositoryKey) ?? repositories[0];
  const selectedKey = selectedRepository ? repositoryPreferenceKey(selectedRepository) : "";
  const preferences = selectedRepository && notificationScope === "repository"
    ? effectiveNotificationPreferences(controller.state, selectedRepository)
    : controller.state.preferences;
  const repositoryIsCustom = selectedRepository ? hasRepositoryPreferences(controller.state, selectedRepository) : false;

  useEffect(() => {
    if (selectedRepositoryKey !== selectedKey) setSelectedRepositoryKey(selectedKey);
  }, [selectedKey, selectedRepositoryKey]);

  const selectFromKeyboard = (event: KeyboardEvent<HTMLButtonElement>, currentIndex: number) => {
    let nextIndex: number | null = null;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") nextIndex = (currentIndex + 1) % DATA_FONT_SIZES.length;
    if (event.key === "ArrowLeft" || event.key === "ArrowUp") nextIndex = (currentIndex - 1 + DATA_FONT_SIZES.length) % DATA_FONT_SIZES.length;
    if (event.key === "Home") nextIndex = 0;
    if (event.key === "End") nextIndex = DATA_FONT_SIZES.length - 1;
    if (nextIndex === null) return;
    event.preventDefault();
    setDataFontSize(DATA_FONT_SIZES[nextIndex]);
    optionRefs.current[nextIndex]?.focus();
  };

  const confirmHistoryClear = async () => {
    try {
      await clearHistory();
    } catch (reason) {
      if (reason instanceof ApiError && reason.code === "history_active") throw new Error(t("settingsCenter.history.blocked"));
      throw reason;
    }
    controller.clearHistory();
    onHistoryCleared();
  };

  return (
    <Dialog onOpenChange={(open) => { if (!open) setSection("display"); }}>
      <Tooltip>
        <TooltipTrigger asChild>
          <DialogTrigger asChild>
            <Button type="button" variant="secondary" size="icon" className="size-11 sm:size-9" aria-label={t("settingsCenter.choose")}>
              <Settings2 className="size-4" aria-hidden="true" />
            </Button>
          </DialogTrigger>
        </TooltipTrigger>
        <TooltipContent>{t("settingsCenter.choose")}</TooltipContent>
      </Tooltip>
      <DialogContent className="flex max-h-[min(760px,calc(100vh-32px))] w-[min(1040px,calc(100vw-32px))] flex-col overflow-hidden p-0">
        <div className="shrink-0 border-b border-border px-5 py-5 sm:px-6">
          <DialogTitle>{t("settingsCenter.title")}</DialogTitle>
          <DialogDescription className="mt-1">{t("settingsCenter.description")}</DialogDescription>
        </div>
        <div className="flex min-h-0 flex-1 flex-col sm:grid sm:grid-cols-[180px_minmax(0,1fr)]">
          <nav aria-label={t("settingsCenter.title")} className="flex shrink-0 gap-2 border-b border-border bg-muted/25 p-3 sm:block sm:border-r sm:border-b-0">
            {(["display", "notifications", "history"] as const).map((value) => (
              <Button key={value} type="button" variant={section === value ? "primary" : "ghost"} className="flex-1 justify-start sm:mb-1 sm:w-full" aria-pressed={section === value} onClick={() => setSection(value)}>
                {t(`settingsCenter.sections.${value}`)}
              </Button>
            ))}
          </nav>
          <div className="min-h-0 flex-1 overflow-y-auto p-5 sm:p-6">
            {section === "display" ? (
              <section aria-labelledby="settings-display-title">
                <h2 id="settings-display-title" className="text-base font-semibold">{t("settingsCenter.display.title")}</h2>
                <p className="mt-1 text-sm text-muted-foreground">{t("settingsCenter.display.description")}</p>
                <h3 className="mt-5 text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">{t("settingsCenter.display.dataFontSize")}</h3>
                <div role="radiogroup" aria-label={t("settingsCenter.display.dataFontSize")} className="mt-2 grid gap-2">
                  {DATA_FONT_SIZES.map((value, index) => (
                    <button key={value} type="button" role="radio" aria-checked={dataFontSize === value} tabIndex={dataFontSize === value ? 0 : -1} aria-label={`${t(`settingsCenter.display.sizes.${value}`)} ${pixels[value]} px`} className={dataFontSize === value ? "flex min-h-14 items-center justify-between rounded-xl border border-primary bg-primary/10 px-4 text-left text-primary" : "flex min-h-14 items-center justify-between rounded-xl border border-border px-4 text-left hover:bg-accent"} onClick={() => setDataFontSize(value)} onKeyDown={(event) => selectFromKeyboard(event, index)} ref={(element) => { optionRefs.current[index] = element; }}>
                      <span><strong className="block text-sm font-medium">{t(`settingsCenter.display.sizes.${value}`)}</strong><small className="mt-0.5 block text-xs text-muted-foreground">{pixels[value]} px</small></span>
                      <code style={{ fontSize: `${pixels[value]}px` }}>Aa 0123456</code>
                    </button>
                  ))}
                </div>
              </section>
            ) : section === "notifications" ? (
              <section aria-labelledby="settings-notifications-title">
                <h2 id="settings-notifications-title" className="text-base font-semibold">{t("settingsCenter.notifications.title")}</h2>
                <p className="mt-1 text-sm text-muted-foreground">{t("settingsCenter.notifications.description")}</p>
                <div className="mt-5 rounded-xl border border-border bg-card p-4">
                  <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
                    <div className="flex min-w-0 gap-3">
                      <span className="grid size-9 shrink-0 place-items-center rounded-[10px] bg-primary/10 text-primary">
                        <BellRing className="size-4" aria-hidden="true" />
                      </span>
                      <div>
                        <h3 className="text-sm font-semibold">{t("notifications.systemTitle")}</h3>
                        <p className={systemDisplayState === "granted" ? "mt-1 text-sm text-success" : systemDisplayState === "denied" ? "mt-1 text-sm text-destructive" : "mt-1 text-sm text-muted-foreground"} aria-live="polite">
                          {t(`notifications.system.${systemDisplayState}`)}
                        </p>
                        {systemDisplayState === "granted" && <p className="mt-1 text-xs text-muted-foreground">{t("notifications.testHelp")}</p>}
                      </div>
                    </div>
                    {systemDisplayState === "default" && <Button type="button" variant="primary" className="self-start sm:self-center" onClick={() => void controller.enableSystemNotifications()}>{t("notifications.enableSystem")}</Button>}
                    {systemDisplayState === "granted" && <Button type="button" variant="secondary" className="self-start shrink-0 sm:self-center" onClick={() => controller.sendTestSystemNotification(t("notifications.testTitle"), t("notifications.testBody"))}>{t("notifications.testAction")}</Button>}
                  </div>
                </div>
                <div role="group" aria-label={t("settingsCenter.notifications.scopeLabel")} className="mt-5 flex flex-wrap items-center gap-2 border-b border-border pb-4">
                  <Button type="button" size="compact" variant={notificationScope === "global" ? "primary" : "ghost"} aria-pressed={notificationScope === "global"} onClick={() => setNotificationScope("global")}>
                    {t("settingsCenter.notifications.scopes.global")}
                  </Button>
                  <Button type="button" size="compact" variant={notificationScope === "repository" ? "primary" : "ghost"} aria-pressed={notificationScope === "repository"} onClick={() => setNotificationScope("repository")}>
                    {t("settingsCenter.notifications.scopes.repository")}
                  </Button>
                </div>
                {notificationScope === "repository" && (
                  repositories.length === 0 ? (
                    <p className="mt-5 rounded-xl border border-dashed border-border bg-muted/20 px-4 py-5 text-sm text-muted-foreground">{t("settingsCenter.notifications.repositoryEmpty")}</p>
                  ) : selectedRepository ? (
                    <div className="mt-5 flex flex-col gap-3 rounded-xl border border-border bg-card p-4 sm:flex-row sm:items-end">
                      <div className="min-w-0 flex-1">
                        <label htmlFor="settings-repository" className="mb-2 block text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">{t("settingsCenter.notifications.repository")}</label>
                        <Select value={selectedKey} onValueChange={setSelectedRepositoryKey}>
                          <SelectTrigger id="settings-repository" aria-label={t("settingsCenter.notifications.repository")}>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {repositories.map((repository) => {
                              const key = repositoryPreferenceKey(repository);
                              return <SelectItem key={key} value={key}>{repository.name} · {t(`providers.${repository.provider}`)} · {repository.source_id}</SelectItem>;
                            })}
                          </SelectContent>
                        </Select>
                      </div>
                      <div className="flex min-h-9 shrink-0 items-center gap-2">
                        <Badge tone={repositoryIsCustom ? "attention" : "neutral"}>{t(repositoryIsCustom ? "settingsCenter.notifications.custom" : "settingsCenter.notifications.inheritsGlobal")}</Badge>
                        {repositoryIsCustom && <Button type="button" size="compact" variant="ghost" onClick={() => controller.resetRepositoryPreferences(selectedRepository)}>{t("settingsCenter.notifications.reset")}</Button>}
                      </div>
                    </div>
                  ) : null
                )}
                {(notificationScope === "global" || selectedRepository) && <div className="mt-5 space-y-6">
                  {statusGroups.map(({ category, outcomes }) => {
                    const groupLabel = t(`settingsCenter.notifications.groups.${category}`);
                    return (
                    <fieldset key={category} className="w-full min-w-0">
                      <legend className="mb-2 text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">{groupLabel}</legend>
                      <div className="max-w-full overflow-x-auto">
                        <div className="flex min-w-max flex-nowrap gap-2">
                          {outcomes.map((outcome) => {
                            const key = `${category}.${outcome}` as NotificationStatusKey;
                            const label = `${groupLabel}: ${t(`notifications.outcomes.${outcome}`)}`;
                            return (
                              <label key={key} className="flex min-h-11 shrink-0 items-center gap-3 rounded-xl border border-border px-3 py-2 hover:bg-accent/40">
                                <input type="checkbox" className="size-4 shrink-0 accent-primary" aria-label={label} checked={preferences[key]} onChange={(event) => notificationScope === "repository" && selectedRepository
                                  ? controller.setStatusEnabled(key, event.currentTarget.checked, selectedRepository)
                                  : controller.setStatusEnabled(key, event.currentTarget.checked)} />
                                <Badge tone={statusTone(outcome)}>{t(`notifications.outcomes.${outcome}`)}</Badge>
                              </label>
                            );
                          })}
                        </div>
                      </div>
                    </fieldset>
                  );})}
                </div>}
              </section>
            ) : (
              <section aria-labelledby="settings-history-title">
                <h2 id="settings-history-title" className="text-base font-semibold">{t("settingsCenter.history.title")}</h2>
                <p className="mt-1 text-sm text-muted-foreground">{t("settingsCenter.history.description")}</p>
                <Alert tone="destructive" className="mt-5"><AlertDescription className="mt-0">{t("settingsCenter.history.warning")}</AlertDescription></Alert>
                <HistoryClearDialog onConfirm={confirmHistoryClear} />
              </section>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
