import { useTranslation } from "react-i18next";
import { repositoryDisplayName } from "../repositories";
import type { EventDeliverySummary, EventSummary, Operation, Repository } from "../types";
import { EventTypeBadge } from "./EventTypeBadge";
import { ExecutionStatus } from "./ExecutionStatus";
import { ProviderBadge } from "./ProviderBadge";
import { SourceStatus } from "./SourceStatus";
import { Badge, type BadgeProps } from "./ui/badge";
import { Button } from "./ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

interface Props { repositories: Repository[]; events: EventSummary[]; selectedID: string | null; onSelect: (eventID: string) => void; onOperation: (delivery: EventDeliverySummary, operation: Operation) => void }

function displayTime(value: string, language: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString(language === "zh-CN" ? "zh-CN" : "en-US", { hour12: false });
}

function routingTone(result: string): BadgeProps["tone"] {
  if (result === "deploy") return "matched";
  if (result === "unmatched") return "unmatched";
  if (result === "deferred") return "active";
  return "neutral";
}

function shortRevision(event: EventSummary) {
  if (!event.revision) return "—";
  return event.revision.slice(0, event.provider === "harbor" ? 12 : 7);
}

export function EventTable({ repositories, events, selectedID, onSelect, onOperation }: Props) {
  const { i18n, t } = useTranslation();
  return <Table className="min-w-[1360px] table-fixed"><TableHeader className="bg-muted/35"><TableRow className="hover:bg-transparent"><TableHead className="w-[170px]">{t("table.received")}</TableHead><TableHead className="w-[180px]">{t("table.repository")}</TableHead><TableHead className="w-[150px]">{t("table.reference")}</TableHead><TableHead className="w-[100px]">{t("table.revision")}</TableHead><TableHead className="w-[270px]">{t("table.message")}</TableHead><TableHead className="w-[260px]">{t("table.event")}</TableHead><TableHead>{t("table.routeTarget")}</TableHead></TableRow></TableHeader><TableBody className="data-surface [&_td]:align-middle">{events.map((event) => {
    const selected = selectedID === event.id;
    return <TableRow key={event.id} data-state={selected ? "selected" : undefined} className={selected ? "min-h-18 bg-primary/8 hover:bg-primary/12" : "min-h-18"}>
      <TableCell className="py-3"><Button variant="ghost" size="compact" className="h-auto min-h-11 w-full justify-start px-0 text-left font-normal hover:bg-transparent hover:text-primary" onClick={() => onSelect(event.id)} aria-label={t("table.viewEvent", { id: event.id })}><span className="font-medium">{displayTime(event.received_at, i18n.resolvedLanguage ?? i18n.language)}</span></Button></TableCell>
      <TableCell className="py-3"><strong className="block truncate font-semibold">{repositoryDisplayName(repositories, event) || t("table.unmatched")}</strong></TableCell>
      <TableCell className="py-3"><span className="block truncate font-medium">{event.ref ?? "—"}</span></TableCell>
      <TableCell className="py-3"><code className="text-muted-foreground">{shortRevision(event)}</code></TableCell>
      <TableCell className="py-3"><span className="block truncate text-foreground">{event.commit_message || (event.provider === "harbor" ? "—" : t("table.noCommitMessage"))}</span></TableCell>
      <TableCell className="py-3"><div className="flex flex-nowrap gap-1 [&_[data-slot=badge]]:px-1.5"><ProviderBadge provider={event.provider} /><EventTypeBadge type={event.event_type} />{event.status && <SourceStatus status={event.status} />}</div></TableCell>
      <TableCell className="py-3">{event.deliveries.length > 0 ? <div className="space-y-1">{event.deliveries.map((delivery) => <div key={delivery.id} className="flex min-w-0 items-center gap-2 [&_[data-slot=badge]]:px-1.5"><code className="min-w-0 truncate text-muted-foreground">{delivery.target_id}</code><ExecutionStatus delivery={delivery} onOperation={onOperation} /></div>)}</div> : event.status === "waiting_for_pipeline" || event.status === "pipeline_not_received" ? <span className="text-muted-foreground">—</span> : <div className="flex items-center gap-2 [&_[data-slot=badge]]:px-1.5"><Badge tone={routingTone(event.routing_result)}>{t(`routing.${event.routing_result}`, { defaultValue: event.routing_result })}</Badge><span className="text-muted-foreground">{t("table.noDelivery")}</span></div>}</TableCell>
    </TableRow>;
  })}</TableBody></Table>;
}
