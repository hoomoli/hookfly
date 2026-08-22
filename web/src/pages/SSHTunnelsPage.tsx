import { useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { checkSSHTunnel, listSSHTunnels } from "../api";
import type { SSHTunnelSummary } from "../types";
import { Button } from "../components/ui/button";
import { Badge } from "../components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";

export function SSHTunnelsPage({ globalActions }: { globalActions?: ReactNode }) {
  const { t } = useTranslation();
  const [items, setItems] = useState<SSHTunnelSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [revision, setRevision] = useState(0);
  const [buttons, setButtons] = useState<Record<string, "checking" | "result">>({});
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const requests = useRef(new Map<string, AbortController>());
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; timers.current.forEach(clearTimeout); requests.current.forEach((controller) => controller.abort()); };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setError(null);
    listSSHTunnels(controller.signal).then(setItems).catch((reason: unknown) => {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : t("ssh.loadFailed"));
    });
    return () => controller.abort();
  }, [revision, t]);
  const check = async (id: string) => {
    if (requests.current.has(id)) return;
    const controller = new AbortController(); requests.current.set(id, controller);
    setButtons((current) => ({ ...current, [id]: "checking" }));
    try {
      const result = await checkSSHTunnel(id, controller.signal);
      if (mounted.current) setItems((current) => current?.map((item) => item.id === id ? result : item) ?? null);
    } catch (reason) {
      if (mounted.current && !controller.signal.aborted) setItems((current) => current?.map((item) => item.id === id ? { ...item, status: "connection_error", error: "check_failed" } : item) ?? null);
    } finally {
      requests.current.delete(id);
      if (mounted.current) {
        setButtons((current) => ({ ...current, [id]: "result" }));
        timers.current.set(id, setTimeout(() => {
          setButtons((current) => { const next = { ...current }; delete next[id]; return next; });
          timers.current.delete(id);
        }, 5000));
      }
    }
  };
  return <div className="space-y-6 p-4 lg:p-6">
    <header className="flex flex-wrap items-center justify-between gap-3">
      <div><h1 className="text-2xl font-semibold">{t("ssh.heading")}</h1><p className="text-sm text-muted-foreground">{t("ssh.description")}</p></div>
      <div className="flex items-center gap-2">{globalActions}<Button variant="secondary" onClick={() => setRevision((value) => value + 1)}>{t("ssh.refresh")}</Button></div>
    </header>
    {error && <p role="alert">{error}</p>}
    {items === null && !error && <p>{t("ssh.loading")}</p>}
    {items?.length === 0 && <p>{t("ssh.empty")}</p>}
    {!!items?.length && <div className="overflow-x-auto rounded-xl border bg-card"><Table>
      <TableHeader><TableRow>{["name", "host", "status", "latency", "action"].map((label) => <TableHead key={label}>{t(`ssh.${label}`)}</TableHead>)}</TableRow></TableHeader>
      <TableBody>{items.map((item) => <TableRow key={item.id}>
        <TableCell className="font-medium">{item.id}</TableCell>
        <TableCell>{item.host ? `${item.host}:${item.port ?? 22}` : "—"}</TableCell>
        <TableCell><Badge>{t(`ssh.states.${item.status}`, { defaultValue: item.status })}</Badge>{item.error && <p className="mt-1 text-sm text-muted-foreground">{t(`ssh.errors.${item.error}`, { defaultValue: t("ssh.errors.connection_failed") })}</p>}</TableCell>
        <TableCell>{item.checked_at && item.latency_ms !== undefined ? `${item.latency_ms.toFixed(1)} ms` : "—"}</TableCell>
        <TableCell><Button disabled={!!buttons[item.id]} onClick={() => void check(item.id)}>{buttons[item.id] === "checking" ? t("ssh.checking") : buttons[item.id] === "result" ? (item.status === "connected" || item.status === "ssh_only" ? `${(item.latency_ms ?? 0).toFixed(1)} ms` : t("ssh.failed")) : t("ssh.check")}</Button></TableCell>
      </TableRow>)}</TableBody>
    </Table></div>}
  </div>;
}
