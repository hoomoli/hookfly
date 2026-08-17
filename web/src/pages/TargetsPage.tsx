import { ChevronRight, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { ReactNode } from "react";
import { EmptyState } from "../components/EmptyState";
import { LanguageMenu } from "../components/LanguageMenu";
import { ThemeMenu } from "../components/ThemeMenu";
import { Badge, type BadgeProps } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { TooltipProvider } from "../components/ui/tooltip";
import type { TargetInventoryState } from "../hooks/useTargetInventory";

interface Props { inventory: TargetInventoryState; globalActions?: ReactNode }

export function TargetsPage({ inventory, globalActions }: Props) {
  const { i18n, t } = useTranslation();
  const targets = inventory.data?.targets ?? [];
  const refreshedAt = inventory.data?.refreshed_at
    ? new Date(inventory.data.refreshed_at).toLocaleString(i18n.resolvedLanguage === "zh-CN" ? "zh-CN" : "en-US", { hour12: false })
    : "—";
  const deploymentKey = (status?: string) => status === "done" ? "done" : status === "running" ? "running" : status === "error" ? "error" : status === "cancelled" ? "cancelled" : "unknown";
  const deploymentTone = (status?: string): BadgeProps["tone"] => status === "done" ? "stable" : status === "running" ? "active" : status === "error" ? "failure" : "neutral";
  const runtimeTone = (running: number, total: number): BadgeProps["tone"] => total === 0 ? "neutral" : running === total ? "stable" : running === 0 ? "failure" : "attention";

  return (
    <>
      <header className="flex min-h-20 flex-col justify-center gap-3 border-b border-border py-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0"><p className="mb-1 text-[11px] font-medium tracking-[0.12em] text-muted-foreground">{t("targets.eyebrow")}</p><h1 className="truncate text-[clamp(1.25rem,2vw,1.75rem)] font-semibold tracking-[-0.025em]">{t("targets.heading")}</h1></div>
        <TooltipProvider delayDuration={300}><div className="flex min-h-11 max-w-full flex-nowrap items-center gap-1.5 overflow-x-auto">{globalActions}<Button type="button" onClick={inventory.refresh} disabled={inventory.loading} aria-label={t("targets.refresh")}><RefreshCw className={inventory.loading ? "size-4 animate-spin" : "size-4"} />{t("targets.refreshLabel")}</Button><ThemeMenu /><LanguageMenu /></div></TooltipProvider>
      </header>
      <section className="py-5" aria-labelledby="targets-summary-title">
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3"><div><h2 id="targets-summary-title" className="text-sm font-semibold">{t("targets.configured")}</h2><p className="mt-1 max-w-3xl text-sm leading-6 text-muted-foreground">{t("targets.description")}</p></div><p className="text-xs text-muted-foreground">{t("targets.refreshedAt", { time: refreshedAt })}</p></div>
        {inventory.error && <p role="alert" className="mb-3 rounded-xl border border-destructive/20 bg-destructive/10 px-4 py-3 text-sm text-destructive">{t("targets.refreshFailed", { message: inventory.error })}</p>}
        {!inventory.data && inventory.loading && <div aria-live="polite"><EmptyState title={t("targets.loading")} /></div>}
        {!inventory.data && !inventory.loading && inventory.error && <EmptyState tone="destructive" title={t("targets.loadFailed", { message: inventory.error })} action={<Button type="button" onClick={inventory.refresh}>{t("common.retry")}</Button>} />}
        {inventory.data && targets.length === 0 && <EmptyState title={t("targets.empty")} />}
        {targets.length > 0 && <section className="overflow-hidden rounded-2xl border border-border bg-card shadow-[0_1px_2px_rgb(0_0_0/0.04)]"><Table className="min-w-[1120px] table-fixed"><colgroup><col className="w-[110px]" /><col className="w-[80px]" /><col className="w-[90px]" /><col className="w-[75px]" /><col className="w-[170px]" /><col className="w-[140px]" /><col className="w-[85px]" /><col className="w-[370px]" /></colgroup><TableHeader className="bg-muted/35"><TableRow className="hover:bg-transparent"><TableHead>{t("targets.targetID")}</TableHead><TableHead>{t("targets.connection")}</TableHead><TableHead>{t("targets.projectEnvironment")}</TableHead><TableHead>{t("targets.compose")}</TableHead><TableHead>{t("targets.appName")}</TableHead><TableHead>{t("targets.composeID")}</TableHead><TableHead>{t("targets.composeStatus")}</TableHead><TableHead>{t("targets.runtimeStatus")}</TableHead></TableRow></TableHeader><TableBody className="data-surface">{targets.map((target) => {
          const totalContainers = target.containers.length;
          const runningContainers = target.containers.filter((container) => container.state === "running").length;
          return <TableRow key={target.id}><TableCell><strong className="font-semibold text-primary">{target.id}</strong></TableCell><TableCell><span className="block">{target.connection_id}</span><small className="text-muted-foreground">{target.resource_type}</small></TableCell><TableCell><span className="block">{target.project_name || "—"}</span><small className="text-muted-foreground">{target.environment_name || "—"}</small></TableCell><TableCell>{target.name || "—"}</TableCell><TableCell><span className="block truncate" title={target.app_name}>{target.app_name || "—"}</span></TableCell><TableCell><code className="block truncate" title={target.resource_id}>{target.resource_id}</code></TableCell><TableCell><Badge tone={deploymentTone(target.status)}>{t(`targets.deploymentStatuses.${deploymentKey(target.status)}`)}</Badge></TableCell><TableCell><div className="flex min-w-0 items-center gap-2"><Badge className="shrink-0" tone={runtimeTone(runningContainers, totalContainers)}>{runningContainers} / {totalContainers}</Badge>{target.containers.length > 0 && <details className="group min-w-0 flex-1"><summary className="flex cursor-pointer list-none items-center gap-0.5 font-medium text-primary [&::-webkit-details-marker]:hidden"><ChevronRight aria-hidden="true" className="size-3.5 shrink-0 transition-transform group-open:rotate-90" />{t("targets.containerDetails")}</summary><ul aria-label={t("targets.containerDetails")} className="mt-2 overflow-hidden rounded-lg border border-border/70 bg-muted/20">{target.containers.map((container) => <li key={container.name} className="flex items-center justify-between gap-3 border-b border-border/70 px-2 py-1.5 last:border-b-0"><strong className="whitespace-nowrap font-semibold">{container.name}</strong><span className={container.state === "running" ? "shrink-0 whitespace-nowrap text-success" : "shrink-0 whitespace-nowrap text-muted-foreground"}>{t(`targets.containerStates.${container.state}`, { defaultValue: container.state })}{container.health ? ` · ${t("targets.health", { health: t(`targets.healthStatuses.${container.health}`, { defaultValue: container.health }) })}` : ""} · {t("targets.restarts", { count: container.restart_count })}</span></li>)}</ul></details>}</div></TableCell></TableRow>;
        })}</TableBody></Table></section>}
      </section>
    </>
  );
}
