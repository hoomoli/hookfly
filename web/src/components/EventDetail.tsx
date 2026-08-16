import type { TFunction } from "i18next";
import { X } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Delivery, EventDetailResponse, Operation, TargetInventoryItem } from "../types";
import { AttemptTimeline } from "./AttemptTimeline";
import { EventTypeBadge } from "./EventTypeBadge";
import { ExecutionStatus } from "./ExecutionStatus";
import { ProviderBadge } from "./ProviderBadge";
import { SourceStatus } from "./SourceStatus";
import { Alert, AlertDescription } from "./ui/alert";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { OperationConfirmationDialog } from "./OperationConfirmationDialog";

interface ActionInput { delivery: Delivery; operation: Operation; reason: string }
interface Props {
  detail: EventDetailResponse;
  repositoryName?: string;
  onClose: () => void;
  onAction: (input: ActionInput) => Promise<void> | void;
  staleError?: string;
  onReload?: () => void;
  targets?: TargetInventoryItem[];
  targetRefreshedAt?: string | null;
}

function JsonEvidence({ title, value }: { title: string; value: unknown }) {
  return (
    <details className="border-b border-border last:border-b-0">
      <summary className="cursor-pointer py-2.5 text-xs text-muted-foreground outline-none focus-visible:text-primary">{title}</summary>
      <pre className="mb-3 max-h-80 overflow-auto rounded-xl border border-border bg-muted/45 p-3 font-mono text-[10px] leading-5 whitespace-pre-wrap text-foreground break-words">{JSON.stringify(value, null, 2)}</pre>
    </details>
  );
}

function bindingBlocker(delivery: Delivery, t: TFunction): string | null {
  if (delivery.target_binding_status === "target_changed") {
    return t("detail.targetChanged");
  }
  if (delivery.target_binding_status === "target_unavailable") {
    return t("detail.targetUnavailable");
  }
  return null;
}

function OperationDialog({ delivery, operation, onAction, onOpenChange }: { delivery: Delivery; operation: Operation; onAction: Props["onAction"]; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation();
  const label = operation === "retry" ? t("operation.retry") : t("operation.redeploy");
  return (
    <OperationConfirmationDialog
      currentAttemptID={delivery.current_attempt_id}
      operation={operation}
      onConfirm={(reason) => onAction({ delivery, operation, reason })}
      onOpenChange={onOpenChange}
      trigger={<Button type="button" size="compact">{label}</Button>}
    />
  );
}

function inventoryWarning(target: TargetInventoryItem, t: TFunction): string | null {
  if (target.condition === "unavailable") return t("detail.currentTargetUnavailable");
  if (target.condition === "refresh_failed") return t("detail.currentTargetRefreshFailed");
  return null;
}

function statusTone(status: string | undefined) {
  if (status === "done") return "stable" as const;
  if (status === "running" || status === "pending") return "active" as const;
  if (status === "error" || status === "failed") return "failure" as const;
  return "neutral" as const;
}

export function EventDetail({ detail, repositoryName, onClose, onAction, staleError, onReload, targets = [], targetRefreshedAt }: Props) {
  const { i18n, t } = useTranslation();
  const [operationOpen, setOperationOpen] = useState(false);
  const sourceStates = detail.source_states ?? [];
  const refreshedAt = targetRefreshedAt
    ? new Date(targetRefreshedAt).toLocaleString(i18n.resolvedLanguage === "zh-CN" ? "zh-CN" : "en-US", { hour12: false })
    : "—";
  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented && !operationOpen) onClose();
    };
    document.addEventListener("keydown", closeOnEscape);
    return () => document.removeEventListener("keydown", closeOnEscape);
  }, [onClose, operationOpen]);
  return (
    <aside aria-label={t("detail.ariaLabel")} className="flex max-h-[calc(100vh-2rem)] min-h-[36rem] min-w-0 flex-col overflow-hidden rounded-2xl border border-border bg-card shadow-[0_12px_40px_rgb(0_0_0/0.08)] min-[1440px]:sticky min-[1440px]:top-4">
      <header className="relative border-b border-border px-5 py-5 pr-16">
        <p className="mb-1 text-[10px] font-medium tracking-[0.12em] text-primary">EVENT INSPECTOR</p>
        <h2 className="truncate text-xl font-semibold tracking-[-0.02em]">{repositoryName || detail.repository || t("detail.unmatched")}</h2>
        <div className="mt-2 flex flex-wrap gap-1.5"><ProviderBadge provider={detail.provider} /><EventTypeBadge type={detail.event_type} /></div>
        <p className="mt-2 truncate font-mono text-[10px] text-muted-foreground">{detail.source_id} · {detail.repository} · {detail.id}</p>
        <Button type="button" variant="ghost" size="icon" className="absolute top-4 right-4" aria-label={t("accessibility.closeDetails")} onClick={() => { if (!operationOpen) onClose(); }}><X className="size-4" aria-hidden="true" /></Button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
            {staleError && <Alert tone="destructive" className="mb-4 flex items-center justify-between gap-3">
              <AlertDescription className="mt-0">{t("detail.stale", { message: staleError, status: detail.active ? t("events.retrying") : t("events.refreshStopped") })}</AlertDescription>
              {!detail.active && <Button type="button" size="compact" onClick={onReload}>{t("common.reload")}</Button>}
            </Alert>}

            <section className="mb-6 grid grid-cols-2 overflow-hidden rounded-xl border border-border" aria-label={t("detail.evidence")}>
              {[
                [t("detail.received"), detail.received_at], [t("detail.type"), detail.event_type],
                [t("detail.provider"), detail.provider], [t("detail.source"), detail.source_id],
                [t("detail.ref"), detail.ref ?? "—"], [t("detail.revision"), detail.revision ?? "—"],
                [t("detail.status"), detail.status ?? "—"], [t("detail.externalID"), detail.external_id ?? "—"],
                [t("detail.trigger"), detail.trigger ?? "—"], [t("detail.routing"), detail.routing_result],
                [t("detail.rule"), detail.rule_id ?? "—"],
              ].map(([label, value]) => <div key={label} className="min-w-0 border-r border-b border-border p-3 even:border-r-0 nth-[n+5]:border-b-0"><span className="block text-[10px] text-muted-foreground">{label}</span><strong className="mt-1 block [overflow-wrap:anywhere] font-mono text-[11px] font-medium">{value}</strong></div>)}
            </section>

            {sourceStates.length > 1 && <section className="mb-6" aria-label={t("detail.pipelineStatusHistory")}>
              <h3 className="mb-2 border-b border-border pb-2 text-[11px] font-medium tracking-[0.1em] text-muted-foreground">{t("detail.pipelineStatusHistory")}</h3>
              <ol className="m-0 list-none space-y-2 p-0 data-surface">{sourceStates.map((state, index) => <li key={state.event_id} className="flex items-center gap-2"><time className="text-muted-foreground">{new Date(state.received_at).toLocaleString(i18n.resolvedLanguage === "zh-CN" ? "zh-CN" : "en-US", { hour12: false })}</time><SourceStatus status={state.status} animated={false} completed={index < sourceStates.length - 1} /></li>)}</ol>
            </section>}

            <section className="mb-6" aria-label={t("detail.currentTarget")}>
              <div className="mb-2 flex items-center justify-between border-b border-border pb-2"><h3 className="m-0 text-[11px] font-medium tracking-[0.1em] text-muted-foreground">{t("detail.currentTarget")}</h3><span className="text-[10px] text-muted-foreground">{t("detail.targetRefreshedAt", { time: refreshedAt })}</span></div>
              {detail.deliveries.length === 0 && <p className="m-0 rounded-xl border border-border p-4 text-xs text-muted-foreground">{t("detail.noCurrentTargets")}</p>}
              <div className="space-y-3">
                {detail.deliveries.map((delivery) => {
                  const target = targets.find((candidate) => candidate.id === delivery.target_id);
                  if (!target) return <article key={delivery.id} className="rounded-xl border border-border bg-muted/25 p-4"><strong className="text-sm text-primary">{delivery.target_id}</strong><Alert tone="warning" className="mt-3"><AlertDescription className="mt-0">{t("detail.currentTargetMissing")}</AlertDescription></Alert></article>;
                  const warning = inventoryWarning(target, t);
                  return <article key={delivery.id} className="overflow-hidden rounded-xl border border-border bg-muted/20">
                    <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3"><strong className="text-sm font-semibold text-primary">{target.id}</strong><Badge tone={target.condition === "available" ? "stable" : target.condition === "refresh_failed" ? "failure" : "attention"}>{t(`targets.conditions.${target.condition}`)}</Badge></header>
                    {warning && <Alert tone="warning" className="mx-4 mt-3 w-auto"><AlertDescription className="mt-0">{warning}</AlertDescription></Alert>}
                    <dl className="grid grid-cols-2 gap-px bg-border text-xs">
                      {[
                        [t("targets.projectEnvironment"), `${target.project_name || "—"} / ${target.environment_name || "—"}`],
                        [t("targets.compose"), target.name || "—"],
                        [t("targets.appName"), target.app_name || "—"],
                        [t("targets.composeID"), target.resource_id],
                        [t("targets.connection"), target.connection_id],
                      ].map(([label, value]) => <div key={label} className="min-w-0 bg-card px-3 py-2.5"><dt className="text-[10px] text-muted-foreground">{label}</dt><dd className="mt-1 [overflow-wrap:anywhere] font-medium">{value}</dd></div>)}
                      <div className="min-w-0 bg-card px-3 py-2.5"><dt className="text-[10px] text-muted-foreground">{t("targets.composeStatus")}</dt><dd className="mt-1"><Badge tone={statusTone(target.status)}>{target.status || "—"}</Badge></dd></div>
                    </dl>
                  </article>;
                })}
              </div>
            </section>

            <section className="mb-6" aria-label={t("detail.deploymentHistory")}>
              <div className="mb-2 flex items-center justify-between border-b border-border pb-2"><h3 className="m-0 text-[11px] font-medium tracking-[0.1em] text-muted-foreground">{t("detail.deploymentHistory")}</h3><Badge>{detail.deliveries.length}</Badge></div>
              {detail.deliveries.length === 0 && <p className="m-0 rounded-xl border border-border p-4 text-xs text-muted-foreground">{t("detail.noDeliveries")}</p>}
              <div className="space-y-3">
                {detail.deliveries.map((delivery) => {
                  const blocker = bindingBlocker(delivery, t);
                  return (
                    <article key={delivery.id} className="overflow-hidden rounded-xl border border-border bg-card">
                      <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3"><div className="min-w-0"><strong className="block truncate text-sm font-medium">{delivery.target_id}</strong><code className="mt-1 block truncate text-[10px] text-muted-foreground">{delivery.id}</code></div><ExecutionStatus delivery={delivery} onOperation={() => undefined} showOperation={false} /></header>
                      {(delivery.active || delivery.attention) && <p className={delivery.active ? "mx-4 mt-3 mb-0 text-xs text-primary" : "mx-4 mt-3 mb-0 text-xs text-warning"}>{delivery.active ? t("detail.backendActive") : t("detail.backendAttention")}</p>}
                      {blocker && <Alert role="alert" tone="warning" className="mx-4 mt-3 w-auto"><AlertDescription className="mt-0">{blocker}</AlertDescription></Alert>}
                      <div className="flex gap-2 px-4 pt-3 empty:hidden">
                        {delivery.target_binding_status === "compatible" && delivery.allowed_operations.includes("retry") && <OperationDialog delivery={delivery} operation="retry" onAction={onAction} onOpenChange={setOperationOpen} />}
                        {delivery.target_binding_status === "compatible" && delivery.allowed_operations.includes("redeploy") && <OperationDialog delivery={delivery} operation="redeploy" onAction={onAction} onOpenChange={setOperationOpen} />}
                      </div>
                      <AttemptTimeline attempts={delivery.attempts} />
                    </article>
                  );
                })}
              </div>
            </section>

            <section className="mb-6">
              <div className="mb-2 flex items-center justify-between border-b border-border pb-2"><h3 className="m-0 text-[11px] font-medium tracking-[0.1em] text-muted-foreground">{t("detail.auditActivity")}</h3><Badge>{detail.activities.length}</Badge></div>
              {detail.activities.length === 0 ? <p className="m-0 py-2 text-xs text-muted-foreground">{t("detail.noAuditActivity")}</p> : <ol className="m-0 list-none p-0">{detail.activities.map((activity) => <li key={activity.id} className="grid grid-cols-[1fr_auto] gap-1 border-b border-border py-3 text-xs"><time className="col-span-2 font-mono text-[9px] text-muted-foreground">{activity.created_at}</time><strong className="font-medium">{activity.action}</strong><span className="text-muted-foreground">{activity.actor ?? t("common.system")}</span><div className="col-span-2"><JsonEvidence title={t("detail.changeDetails")} value={{ before: activity.before, after: activity.after }} /></div></li>)}</ol>}
            </section>

            <section><h3 className="mb-1 border-b border-border pb-2 text-[11px] font-medium tracking-[0.1em] text-muted-foreground">{t("detail.rawJson")}</h3><JsonEvidence title={t("detail.headers")} value={detail.headers} /><JsonEvidence title={t("detail.payload")} value={detail.payload} /><JsonEvidence title={t("detail.ruleSnapshot")} value={detail.rule_snapshot} /></section>
      </div>
    </aside>
  );
}
