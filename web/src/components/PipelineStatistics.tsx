import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { getPipelineStatistics } from "../api";
import { useActivePolling } from "../hooks/useActivePolling";
import { Button } from "./ui/button";
import { SourceStatus } from "./SourceStatus";

export interface PipelineRun {
  provider: string; source: string; repository: string; pipeline_id: string; ref: string; status: string;
  first_received: string; last_received: string; initialization_ms: number; waiting_ms: number; running_ms: number;
  total_ms: number | null; complete: boolean; restarts: number;
}
export interface PipelineStatisticsResponse { server_now: string; retention_days: number; runs: PipelineRun[]; active: boolean }
export function duration(ms: number | null): string {
  if (ms === null) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60000)} m ${Math.round(ms % 60000 / 1000)} s`;
}
export function PipelineStatistics({ source, repository }: { source?: string; repository?: string }) {
  const { t, i18n } = useTranslation();
  const [range, setRange] = useState("7");
  const [from, setFrom] = useState(""); const [to, setTo] = useState("");
  const invalid = range === "custom" && (!from || !to || new Date(from) >= new Date(to) || new Date(to).getTime() - new Date(from).getTime() > 90 * 86400000);
  const load = useCallback(() => {
    if (invalid) return Promise.resolve({ server_now: "", retention_days: 90, runs: [], active: false });
    const end = range === "custom" ? new Date(to) : new Date();
    const start = range === "custom" ? new Date(from) : new Date(end.getTime() - Number(range) * 86400000);
    return getPipelineStatistics(source, repository, start.toISOString(), end.toISOString());
  }, [source, repository, range, from, to, invalid]);
  const { data, error, refresh } = useActivePolling(load);
  const runs = data?.runs ?? [];
  const complete = runs.filter(run => run.complete && run.total_ms !== null);
  const times = complete.map(run => run.total_ms!).sort((a,b) => a-b);
  const terminal = runs.filter(run => ["success","failed","failure","canceled","cancelled","skipped","neutral","timed_out","action_required"].includes(run.status));
  const percentile = (q: number) => times.length ? times[Math.max(0, Math.ceil(times.length*q)-1)] : null;
  const trend = useMemo(() => {
    const buckets = new Map<string, { count: number; total: number; samples: number }>();
    for (const run of runs) {
      const date = range === "1" ? new Date(run.first_received).toLocaleString(i18n.language, { month: "numeric", day: "numeric", hour: "2-digit", hour12: false }) : new Date(run.first_received).toLocaleDateString(i18n.language);
      const bucket = buckets.get(date) ?? { count: 0, total: 0, samples: 0 };
      bucket.count++;
      if (run.total_ms !== null) { bucket.total += run.total_ms; bucket.samples++; }
      buckets.set(date, bucket);
    }
    return [...buckets].reverse();
  }, [runs, i18n.language, range]);
  const maxTime = Math.max(1, ...runs.map(run => run.initialization_ms + run.waiting_ms + run.running_ms));
  const metrics = [
    [t("statistics.count"), String(runs.length)],
    [t("statistics.successRate"), terminal.length ? `${Math.round(runs.filter(run=>run.status==="success").length/terminal.length*100)}%` : "—"],
    [t("statistics.average"), duration(times.length ? times.reduce((a,b)=>a+b,0)/times.length : null)],
    ["P50", duration(percentile(.5))], ["P95", duration(percentile(.95))],
  ];
  return <section className="my-5 space-y-5" aria-label={t("statistics.title")}>
    <div className="flex flex-wrap items-end justify-between gap-3"><h2 className="text-xl font-semibold">{t("statistics.title")}</h2><div className="flex flex-wrap items-center gap-2">
      <label className="text-sm">{t("statistics.range")} <select className="rounded-md border border-input bg-card p-2" value={range} onChange={e=>setRange(e.target.value)}>{["1","7","30","custom"].map(value=><option key={value} value={value}>{t(`statistics.range${value}`)}</option>)}</select></label>
      {range === "custom" && <><input aria-label={t("statistics.from")} type="datetime-local" value={from} onChange={e=>setFrom(e.target.value)} className="rounded border border-input p-2"/><input aria-label={t("statistics.to")} type="datetime-local" value={to} onChange={e=>setTo(e.target.value)} className="rounded border border-input p-2"/></>}
      <Button variant="ghost" onClick={refresh}>{t("common.reload")}</Button>
    </div></div>
    <p className="text-xs text-muted-foreground">{t("statistics.basis", { timezone: Intl.DateTimeFormat().resolvedOptions().timeZone })}</p>
    {invalid && <p role="alert">{t("statistics.invalidRange")}</p>}
    {error && <p role="alert" className="text-destructive">{error.message}</p>}
    <dl className="grid grid-cols-2 gap-4 border-y border-border py-5 sm:grid-cols-5">{metrics.map(([label,value])=><div key={label}><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-2 text-2xl font-semibold tabular-nums">{value}</dd></div>)}</dl>
    <p className="text-xs text-muted-foreground">{t("statistics.coverage", { count: complete.length, total: runs.length })}</p>
    {!data && !error && <p role="status">{t("events.loading")}</p>}
    {data && !runs.length && !invalid && <p>{t("statistics.empty")}</p>}
    {trend.length > 0 && <section className="rounded-xl border border-border bg-card p-4"><h3 className="mb-4 font-semibold">{t("statistics.trend")}</h3><div className="space-y-3">{trend.map(([date,bucket])=><div key={date} className="grid grid-cols-[6rem_1fr_8rem] items-center gap-3 text-xs"><span>{date}</span><div className="h-5 bg-muted"><div className="h-full bg-primary" style={{ width: `${bucket.count/Math.max(...trend.map(([,b])=>b.count))*100}%` }} /></div><span className="text-right tabular-nums">{bucket.count} / {duration(bucket.samples ? bucket.total/bucket.samples : null)}</span></div>)}</div></section>}
    {runs.length > 0 && <section className="overflow-x-auto rounded-xl border border-border bg-card p-4"><h3 className="mb-3 font-semibold">{t("statistics.phases")}</h3><div className="mb-4 flex flex-wrap gap-4 text-xs">{["initialization","waiting","running"].map((phase,index)=><span key={phase} className="flex items-center gap-2"><span className={`size-2 rounded-sm ${["bg-muted-foreground","bg-warning","bg-primary"][index]}`} />{t(`statistics.${phase}`)}</span>)}</div>
      <table className="w-full min-w-[650px] text-left text-xs"><thead><tr className="border-b border-border">{["pipeline","received","phases","total"].map(key=><th key={key} className="pb-3 pr-4">{t(`statistics.${key}`)}</th>)}</tr></thead><tbody>{runs.slice(0,100).map(run=><tr key={`${run.provider}:${run.source}:${run.repository}:${run.pipeline_id}`} className="border-b border-border last:border-0"><td className="py-3 pr-4"><strong>{run.repository} #{run.pipeline_id}</strong><div className="mt-1 text-muted-foreground">{run.ref}</div><SourceStatus status={run.status} animated={false}/></td><td className="pr-4 tabular-nums">{new Date(run.first_received).toLocaleString(i18n.language)}</td><td className="w-2/5 pr-4"><div className="flex h-6 overflow-hidden rounded bg-muted" role="img" aria-label={`${t("statistics.initialization")}: ${duration(run.initialization_ms)}, ${t("statistics.waiting")}: ${duration(run.waiting_ms)}, ${t("statistics.running")}: ${duration(run.running_ms)}`}>{[run.initialization_ms,run.waiting_ms,run.running_ms].map((value,index)=><div key={index} className={["bg-muted-foreground","bg-warning","bg-primary"][index]} style={{width:`${value/maxTime*100}%`}} title={duration(value)}/>)}</div><div className="mt-1 tabular-nums">{duration(run.initialization_ms)} / {duration(run.waiting_ms)} / {duration(run.running_ms)}</div></td><td className="tabular-nums">{duration(run.total_ms)}{!run.complete && <div className="mt-1 text-muted-foreground">{t(run.restarts ? "statistics.restarted" : "statistics.incomplete")}</div>}</td></tr>)}</tbody></table><p className="mt-3 text-xs text-muted-foreground">{t("statistics.limit")}</p>
    </section>}
  </section>;
}
