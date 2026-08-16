import { X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ApiError, createAttempt, getEvent, listEvents } from "../api";
import { CommandBar, type SignalTone } from "../components/CommandBar";
import { EmptyState } from "../components/EmptyState";
import { EventDetail } from "../components/EventDetail";
import { EventFilters, type Filters } from "../components/EventFilters";
import { EventTable } from "../components/EventTable";
import { OperationConfirmationDialog } from "../components/OperationConfirmationDialog";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { useActivePolling } from "../hooks/useActivePolling";
import { configReloadLabel, useConfigReload } from "../hooks/useConfigReload";
import { repositoryDisplayName } from "../repositories";
import type { Delivery, EventDeliverySummary, EventDetailResponse, Operation, Repository, RepositorySelection, TargetInventoryItem } from "../types";

function filtersFrom(query: URLSearchParams): Filters {
  const statuses = query.getAll("status");
  return {
    repository: query.get("source") && query.get("repository") ? `${query.get("source")}\u0000${query.get("repository")}` : "",
    target: query.get("target") ?? "",
    transport: statuses.find((item) => item.startsWith("transport:"))?.slice(10) ?? "",
    deployment: statuses.find((item) => item.startsWith("deployment:"))?.slice(11) ?? "",
  };
}

function apiQuery(source: URLSearchParams) {
  const result = new URLSearchParams();
  if (source.get("source") && source.get("repository")) {
    for (const key of ["source", "repository"]) {
      for (const value of source.getAll(key)) result.append(key, value);
    }
  }
  for (const key of ["unmatched", "target", "id", "status", "page", "page_size"]) {
    for (const value of source.getAll(key)) result.append(key, value);
  }
  return result;
}

interface InspectorProps {
  eventID: string;
  repositories: Repository[];
  onClose: () => void;
  refreshList: () => void;
  targets: TargetInventoryItem[];
  targetRefreshedAt?: string | null;
}

function Inspector({ eventID, repositories, onClose, refreshList, targets, targetRefreshedAt }: InspectorProps) {
  const { t } = useTranslation();
  const load = useCallback(() => getEvent(eventID), [eventID]);
  const { data, error, loading, refresh } = useActivePolling(load);

  useEffect(() => {
    if (data) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) onClose();
    };
    document.addEventListener("keydown", closeOnEscape);
    return () => document.removeEventListener("keydown", closeOnEscape);
  }, [data, onClose]);

  const handleAction = async ({ delivery, operation, reason }: { delivery: Delivery; operation: Operation; reason: string }) => {
    try {
      await createAttempt(delivery.id, {
        expected_current_attempt_id: delivery.current_attempt_id,
        operation,
        reason,
      });
    } catch (caught) {
      if (caught instanceof ApiError && (caught.status === 409 || caught.code === "reconciliation_unavailable")) {
        refresh();
        refreshList();
      }
      throw caught;
    }
    refresh();
    refreshList();
  };

  if (!data) return (
    <aside aria-label={t("detail.ariaLabel")} className="relative min-h-72 min-w-0 overflow-hidden rounded-2xl border border-border bg-card shadow-[0_12px_40px_rgb(0_0_0/0.08)] min-[1440px]:sticky min-[1440px]:top-4">
      <header className="border-b border-border px-5 py-5 pr-16"><p className="mb-1 text-[10px] font-medium tracking-[0.12em] text-primary">EVENT INSPECTOR</p><h2 className="text-xl font-semibold tracking-[-0.02em]">{t("detail.title")}</h2><p className="mt-1 font-mono text-[11px] text-muted-foreground">{eventID}</p></header>
      <Button type="button" variant="ghost" size="icon" className="absolute top-4 right-4" aria-label={t("accessibility.closeDetails")} onClick={onClose}><X className="size-4" aria-hidden="true" /></Button>
      {loading && <div aria-live="polite"><EmptyState title={t("detail.loading")} /></div>}
      {error && <EmptyState tone="destructive" title={t("detail.loadFailed", { message: error.message })} action={<div className="flex gap-2"><Button type="button" onClick={refresh}>{t("common.retry")}</Button><Button type="button" onClick={onClose}>{t("common.close")}</Button></div>} />}
    </aside>
  );
  const detail = data as EventDetailResponse;
  return <EventDetail detail={detail} repositoryName={repositoryDisplayName(repositories, detail)} onClose={onClose} onAction={handleAction} staleError={error?.message} onReload={refresh} targets={targets} targetRefreshedAt={targetRefreshedAt} />;
}

interface LedgerPageProps {
  globalActions?: ReactNode;
  search: string;
  repositories: Repository[];
  repositoryError: string | null;
  selection: RepositorySelection;
  replaceQuery: (query: URLSearchParams) => void;
  refreshRepositories: () => void;
  targets?: TargetInventoryItem[];
  targetRefreshedAt?: string | null;
  historyRevision: number;
}

export function LedgerPage({ globalActions, search, repositories, repositoryError, selection, replaceQuery, refreshRepositories, targets = [], targetRefreshedAt, historyRevision }: LedgerPageProps) {
  const { t } = useTranslation();
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [requestedOperation, setRequestedOperation] = useState<{ delivery: EventDeliverySummary; operation: Operation } | null>(null);
  const refreshAfterConfig = useRef<() => void>(() => undefined);
  const configReload = useConfigReload(() => refreshAfterConfig.current());
  const query = useMemo(() => new URLSearchParams(search), [search]);
  const filters = filtersFrom(query);
  const page = Math.max(1, Number(query.get("page")) || 1);
  const selectionIdentity = selection.kind === "repository"
    ? `${selection.kind}:${selection.sourceID}:${selection.repositoryID}`
    : selection.kind;
  const loadPage = useCallback(() => listEvents(apiQuery(new URLSearchParams(search))), [historyRevision, search]);
  const events = useActivePolling(loadPage);

  useEffect(() => setSelectedID(query.get("id")), [query]);

  useEffect(() => setSelectedID(null), [selectionIdentity]);

  refreshAfterConfig.current = () => {
    refreshRepositories();
    events.refresh();
    setSelectedID(null);
  };

  const updateFilter = (key: keyof Filters, value: string) => {
    const next = new URLSearchParams(window.location.search);
    next.delete("page");
    if (key === "repository") {
      next.delete("source");
      next.delete("repository");
      next.delete("unmatched");
      if (value) {
        const [sourceID, repositoryID] = value.split("\u0000", 2);
        next.set("source", sourceID);
        next.set("repository", repositoryID);
      }
    } else if (key === "transport" || key === "deployment") {
      const otherPrefix = key === "transport" ? "deployment:" : "transport:";
      const statuses = next.getAll("status").filter((status) => status.startsWith(otherPrefix));
      next.delete("status");
      for (const status of statuses) next.append("status", status);
      if (value) next.append("status", `${key}:${value}`);
    } else if (value) {
      next.set(key, value);
    } else {
      next.delete(key);
    }
    replaceQuery(next);
  };

  const clearFilters = () => {
    const next = new URLSearchParams(window.location.search);
    for (const key of ["source", "repository", "unmatched", "target", "status", "page"]) next.delete(key);
    replaceQuery(next);
  };

  const changePage = (nextPage: number) => {
    const next = new URLSearchParams(window.location.search);
    if (nextPage <= 1) next.delete("page");
    else next.set("page", String(nextPage));
    replaceQuery(next);
  };

  const data = events.data;
  const signalTone: SignalTone | null = events.error ? "error" : data?.active || events.loading ? "active" : null;
  const signalLabel = events.error
    ? data?.active ? t("command.sync.refreshingFailed") : t("command.sync.failed")
    : data?.active ? t("command.sync.active")
    : events.loading ? t("command.sync.synchronizing") : null;
  const configLabel = configReloadLabel(configReload.status);
  const unconfiguredEmpty = repositories.length === 0
    && selection.kind === "all"
    && !filters.repository && !filters.target && !filters.transport && !filters.deployment
    && data?.total === 0;

  const submitRequestedOperation = async (reason: string) => {
    if (!requestedOperation) return;
    try {
      await createAttempt(requestedOperation.delivery.id, {
        expected_current_attempt_id: requestedOperation.delivery.current_attempt_id,
        operation: requestedOperation.operation,
        reason,
      });
    } catch (caught) {
      if (caught instanceof ApiError && (caught.status === 409 || caught.code === "reconciliation_unavailable")) events.refresh();
      throw caught;
    }
    events.refresh();
  };

  return (
    <>
      <CommandBar
        globalActions={globalActions}
        reloadLabel={configLabel}
        reloadDisabled={configReload.disabled}
        recovery={configReload.status?.recovery ?? false}
        onReload={configReload.submit}
        signalTone={signalTone}
        signalLabel={signalLabel}
      />

      {configReload.error && <Alert tone="destructive" className="mt-4"><AlertDescription>{t("errors.configStatus", { message: configReload.error.message })}</AlertDescription></Alert>}
      {repositoryError && <Alert tone="destructive" className="mt-4"><AlertDescription>{t("errors.repositoryIndex", { message: repositoryError })}</AlertDescription></Alert>}
      <EventFilters filters={filters} repositories={repositories} targets={targets} onChange={updateFilter} onClear={clearFilters} />
      {data && events.error && <Alert tone="destructive" className="mb-4 flex items-center justify-between gap-3">
        <AlertDescription className="mt-0">{t("events.stale", { message: events.error.message, status: data.active ? t("events.retrying") : t("events.refreshStopped") })}</AlertDescription>
        {!data.active && <Button type="button" size="compact" onClick={events.refresh}>{t("common.reload")}</Button>}
      </Alert>}

      <div className={selectedID ? "grid items-start gap-4 min-[1440px]:grid-cols-[minmax(0,1fr)_minmax(360px,420px)]" : ""}>
      <section className="min-w-0 overflow-hidden rounded-2xl border border-border bg-card shadow-[0_1px_2px_rgb(0_0_0/0.04)]" aria-labelledby="ledger-title">
        <header className="flex min-h-16 items-center justify-between gap-4 border-b border-border px-4 py-3"><div><p className="mb-1 text-[10px] font-medium tracking-[0.12em] text-muted-foreground">{t("events.ledger")}</p><h2 className="text-base font-semibold tracking-[-0.01em]" id="ledger-title">{t("events.title")}</h2></div><p className="m-0 text-xs text-muted-foreground"><strong className="text-sm font-semibold text-foreground">{data?.total ?? 0}</strong> {t("events.recordCount", { count: data?.total ?? 0 }).replace(String(data?.total ?? 0), "").trim()}</p></header>
        {!data && events.loading && <div aria-live="polite"><EmptyState title={t("events.loading")} /></div>}
        {!data && events.error && <EmptyState tone="destructive" title={t("events.loadFailed", { message: events.error.message })} action={<Button type="button" onClick={events.refresh}>{t("common.reload")}</Button>} />}
        {data && data.items.length === 0 && <EmptyState title={unconfiguredEmpty ? t("events.unconfigured") : t("events.noMatches")} description={unconfiguredEmpty ? t("events.unconfiguredDescription") : t("events.noMatchesDescription")} />}
        {data && data.items.length > 0 && <EventTable repositories={repositories} events={data.items} selectedID={selectedID} onSelect={setSelectedID} onOperation={(delivery: EventDeliverySummary, operation: Operation) => setRequestedOperation({ delivery, operation })} />}
        <footer className="flex min-h-13 items-center border-t border-border px-3 py-2 text-xs text-muted-foreground"><div className="ml-auto flex items-center gap-1.5"><span>{t("events.page", { page })}</span><span>{data ? t("events.pageSize", { count: data.page_size }) : "—"}</span><Button size="compact" type="button" disabled={!data?.has_previous} onClick={() => changePage(page - 1)}>{t("events.previous")}</Button><Button size="compact" type="button" disabled={data ? !data.has_next : false} onClick={() => changePage(page + 1)}>{t("events.next")}</Button></div></footer>
      </section>
      {selectedID && <Inspector key={selectedID} eventID={selectedID} repositories={repositories} onClose={() => {
        setSelectedID(null);
        const next = new URLSearchParams(window.location.search);
        next.delete("id");
        replaceQuery(next);
      }} refreshList={events.refresh} targets={targets} targetRefreshedAt={targetRefreshedAt} />}
      </div>
      {requestedOperation && <OperationConfirmationDialog currentAttemptID={requestedOperation.delivery.current_attempt_id} operation={requestedOperation.operation} open onOpenChange={(open) => { if (!open) setRequestedOperation(null); }} onConfirm={submitRequestedOperation} />}
    </>
  );
}
