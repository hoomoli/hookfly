import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { listConnectionResources, listConnections } from "../api";
import { EmptyState } from "../components/EmptyState";
import { LanguageMenu } from "../components/LanguageMenu";
import { ThemeMenu } from "../components/ThemeMenu";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { TooltipProvider } from "../components/ui/tooltip";
import type { ConnectionResource, ConnectionSummary } from "../types";

function errorMessage(reason: unknown) {
  return reason instanceof Error ? reason.message : String(reason);
}

function wasAborted(reason: unknown) {
  return reason instanceof Error && reason.name === "AbortError";
}

async function copyToClipboard(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Plain HTTP and browser policy can reject the Clipboard API. Try the DOM fallback below.
    }
  }

  const textarea = document.createElement("textarea");
  textarea.dataset.hookflyClipboard = "";
  textarea.value = text;
  textarea.readOnly = true;
  textarea.tabIndex = -1;
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  textarea.style.opacity = "0";

  try {
    document.body.appendChild(textarea);
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.getSelection()?.removeAllRanges();
    textarea.remove();
  }
}

export function ConnectionsPage({ globalActions }: { globalActions?: ReactNode } = {}) {
  const { t } = useTranslation();
  const [connections, setConnections] = useState<ConnectionSummary[] | null>(null);
  const [connectionsLoading, setConnectionsLoading] = useState(true);
  const [connectionsError, setConnectionsError] = useState<string | null>(null);
  const [connectionsRequest, setConnectionsRequest] = useState(0);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [resources, setResources] = useState<ConnectionResource[] | null>(null);
  const [resourcesLoading, setResourcesLoading] = useState(false);
  const [resourcesError, setResourcesError] = useState<string | null>(null);
  const [resourcesRequest, setResourcesRequest] = useState(0);
  const [copiedID, setCopiedID] = useState<string | null>(null);
  const [copyFailed, setCopyFailed] = useState(false);

  useEffect(() => {
    let active = true;
    setConnectionsLoading(true);
    setConnectionsError(null);
    void listConnections().then(
      (items) => {
        if (!active) return;
        setConnections(items);
        setConnectionsLoading(false);
      },
      (reason: unknown) => {
        if (!active) return;
        setConnections(null);
        setConnectionsError(errorMessage(reason));
        setConnectionsLoading(false);
      },
    );
    return () => { active = false; };
  }, [connectionsRequest]);

  const selected = useMemo(
    () => connections?.find((connection) => connection.id === selectedID) ?? null,
    [connections, selectedID],
  );
  const discoveryAvailable = selected?.capabilities.includes("resource_discovery") ?? false;
  const showAppName = resources?.some((resource) => Boolean(resource.app_name?.trim())) ?? false;

  useEffect(() => {
    setResources(null);
    setResourcesError(null);
    if (!selected || !discoveryAvailable) {
      setResourcesLoading(false);
      return;
    }

    const controller = new AbortController();
    setResourcesLoading(true);
    void listConnectionResources(selected.id, controller.signal).then(
      (items) => {
        setResources(items);
        setResourcesLoading(false);
      },
      (reason: unknown) => {
        if (wasAborted(reason)) return;
        setResourcesError(errorMessage(reason));
        setResourcesLoading(false);
      },
    );
    return () => controller.abort();
  }, [discoveryAvailable, resourcesRequest, selected]);

  const selectConnection = useCallback((connection: ConnectionSummary) => {
    setSelectedID(connection.id);
    setResources(null);
    setResourcesError(null);
    setCopiedID(null);
    setCopyFailed(false);
  }, []);

  const copyResourceID = useCallback(async (resourceID: string) => {
    setCopyFailed(false);
    if (await copyToClipboard(resourceID)) {
      setCopiedID(resourceID);
      return;
    }
    setCopiedID(null);
    setCopyFailed(true);
  }, []);

  return (
    <>
      <header className="flex min-h-20 flex-col justify-center gap-3 border-b border-border py-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <p className="mb-1 text-[11px] font-medium tracking-[0.12em] text-muted-foreground">{t("connections.eyebrow")}</p>
          <h1 className="truncate text-[clamp(1.25rem,2vw,1.75rem)] font-semibold tracking-[-0.025em]">{t("connections.heading")}</h1>
        </div>
        <TooltipProvider delayDuration={300}>
          <div className="flex min-h-11 max-w-full flex-nowrap items-center gap-1.5 overflow-x-auto">
            {globalActions}
            <ThemeMenu />
            <LanguageMenu />
          </div>
        </TooltipProvider>
      </header>

      <section className="py-5" aria-labelledby="connections-summary-title">
        <div className="mb-4">
          <h2 id="connections-summary-title" className="text-sm font-semibold">{t("connections.readOnly")}</h2>
          <p className="mt-1 max-w-3xl text-sm leading-6 text-muted-foreground">{t("connections.description")}</p>
        </div>

        <div className="grid gap-4 xl:grid-cols-[minmax(240px,320px)_minmax(0,1fr)]">
          <section className="overflow-hidden rounded-2xl border border-border bg-card shadow-[0_1px_2px_rgb(0_0_0/0.04)]" aria-label={t("connections.heading")}>
            {connectionsLoading && <div aria-live="polite"><EmptyState title={t("connections.loading")} /></div>}
            {!connectionsLoading && connectionsError && <EmptyState tone="destructive" title={t("connections.loadFailed", { message: connectionsError })} action={<Button type="button" onClick={() => setConnectionsRequest((value) => value + 1)}>{t("common.retry")}</Button>} />}
            {!connectionsLoading && !connectionsError && connections?.length === 0 && <EmptyState title={t("connections.empty")} />}
            {!connectionsLoading && !connectionsError && connections && connections.length > 0 && (
              <div className="data-surface space-y-2 p-3">
                {connections.map((connection) => {
                  const active = connection.id === selectedID;
                  return (
                    <Button
                      key={connection.id}
                      type="button"
                      variant="ghost"
                      size="touch"
                      className={active ? "h-auto min-h-14 w-full justify-between bg-primary/10 px-3 py-2 text-left text-primary hover:bg-primary/15" : "h-auto min-h-14 w-full justify-between px-3 py-2 text-left hover:bg-accent"}
                      aria-pressed={active}
                      onClick={() => selectConnection(connection)}
                    >
                      <span className="min-w-0">
                        <strong className="block truncate">{connection.id}</strong>
                        <small className="block truncate text-muted-foreground">{connection.type}</small>
                        <small className="block truncate text-muted-foreground/80">
                          {t("connections.capabilities")}: {connection.capabilities.length > 0
                            ? connection.capabilities.map((capability) => capability === "resource_discovery" ? t("connections.resourceDiscovery") : capability).join(", ")
                            : t("connections.noCapabilities")}
                        </small>
                      </span>
                      <Badge tone={connection.status === "configured" ? "stable" : "neutral"}>{connection.status}</Badge>
                    </Button>
                  );
                })}
              </div>
            )}
          </section>

          <section className="min-w-0 overflow-hidden rounded-2xl border border-border bg-card shadow-[0_1px_2px_rgb(0_0_0/0.04)]" aria-label={t("connections.resourceDiscovery")}>
            {!selected && <EmptyState title={t("connections.select")} />}
            {selected && !discoveryAvailable && <EmptyState tone="warning" title={t("connections.unavailable")} />}
            {selected && discoveryAvailable && resourcesLoading && <div aria-live="polite"><EmptyState title={t("connections.discovering")} /></div>}
            {selected && discoveryAvailable && !resourcesLoading && resourcesError && <EmptyState tone="destructive" title={t("connections.discoveryFailed", { message: resourcesError })} action={<Button type="button" onClick={() => setResourcesRequest((value) => value + 1)}>{t("common.retry")}</Button>} />}
            {selected && discoveryAvailable && !resourcesLoading && !resourcesError && resources?.length === 0 && <EmptyState title={t("connections.noResources")} />}
            {copyFailed && <p role="alert" className="border-b border-destructive/20 bg-destructive/10 px-4 py-3 text-sm text-destructive">{t("connections.copyFailed")}</p>}
            {selected && discoveryAvailable && !resourcesLoading && !resourcesError && resources && resources.length > 0 && (
              <Table className="min-w-[720px] table-fixed">
                <TableHeader className="bg-muted/35">
                  <TableRow className="hover:bg-transparent">
                    <TableHead>{t("connections.project")}</TableHead>
                    <TableHead>{t("connections.environment")}</TableHead>
                    <TableHead>{t("connections.compose")}</TableHead>
                    {showAppName && <TableHead>{t("connections.appName")}</TableHead>}
                    <TableHead className="w-[230px]">{t("connections.composeID")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody className="data-surface">
                  {resources.map((resource) => (
                    <TableRow key={`${resource.type}:${resource.resource_id}`}>
                      <TableCell>{resource.project_name}</TableCell>
                      <TableCell>{resource.environment_name}</TableCell>
                      <TableCell>{resource.name}</TableCell>
                      {showAppName && <TableCell>{resource.app_name || "—"}</TableCell>}
                      <TableCell>
                        <div className="flex items-center gap-2">
                          <code className="min-w-0 flex-1 truncate">{resource.resource_id}</code>
                          <Button type="button" size="compact" aria-label={t("connections.copyID", { id: resource.resource_id })} onClick={() => void copyResourceID(resource.resource_id)}>
                            {copiedID === resource.resource_id ? t("connections.copied") : t("connections.copy")}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </section>
        </div>
      </section>
    </>
  );
}
