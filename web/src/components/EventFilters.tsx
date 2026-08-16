import { useTranslation } from "react-i18next";
import type { Repository, TargetInventoryItem } from "../types";
import { Button } from "./ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

export interface Filters { repository: string; target: string; transport: string; deployment: string }

interface Props {
  filters: Filters;
  repositories: Repository[];
  targets: TargetInventoryItem[];
  onChange: (key: keyof Filters, value: string) => void;
  onClear: () => void;
}

const transportStatuses = ["pending", "sending", "enqueued", "failed", "unknown"];
const deploymentStatuses = ["not_started", "locating", "running", "unrecognized", "done", "error", "cancelled", "timeout", "unknown"];

export function EventFilters({ filters, repositories, targets, onChange, onClear }: Props) {
  const { t } = useTranslation();
  return <form className="grid grid-cols-1 items-end gap-3 py-5 sm:grid-cols-2 xl:grid-cols-[minmax(190px,1.25fr)_minmax(150px,1fr)_170px_180px_auto]" onSubmit={(event) => event.preventDefault()}>
    <label className="grid gap-1.5 text-xs font-medium text-muted-foreground"><span>{t("filters.repository")}</span><Select value={filters.repository || "__all"} onValueChange={(value) => onChange("repository", value === "__all" ? "" : value)}><SelectTrigger aria-label={t("filters.repository")}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__all">{t("common.all")}</SelectItem>{repositories.map((repository) => <SelectItem key={`${repository.source_id}:${repository.id}`} value={`${repository.source_id}\u0000${repository.id}`}>{repository.name} · {t(repository.provider === "gitlab" ? "providers.gitlab" : "providers.github")}</SelectItem>)}</SelectContent></Select></label>
    <label className="grid gap-1.5 text-xs font-medium text-muted-foreground"><span>{t("filters.target")}</span><Select value={filters.target || "__all"} onValueChange={(value) => onChange("target", value === "__all" ? "" : value)}><SelectTrigger aria-label={t("filters.target")}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__all">{t("common.all")}</SelectItem>{targets.map((target) => <SelectItem key={target.id} value={target.id}>{target.id}</SelectItem>)}</SelectContent></Select></label>
    <label className="grid gap-1.5 text-xs font-medium text-muted-foreground"><span>{t("filters.deliveryStatus")}</span><Select value={filters.transport || "__all"} onValueChange={(value) => onChange("transport", value === "__all" ? "" : value)}><SelectTrigger aria-label={t("filters.deliveryStatus")}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__all">{t("common.all")}</SelectItem>{transportStatuses.map((status) => <SelectItem key={status} value={status}>{status}</SelectItem>)}</SelectContent></Select></label>
    <label className="grid gap-1.5 text-xs font-medium text-muted-foreground"><span>{t("filters.deploymentStatus")}</span><Select value={filters.deployment || "__all"} onValueChange={(value) => onChange("deployment", value === "__all" ? "" : value)}><SelectTrigger aria-label={t("filters.deploymentStatus")}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__all">{t("common.all")}</SelectItem>{deploymentStatuses.map((status) => <SelectItem key={status} value={status}>{status}</SelectItem>)}</SelectContent></Select></label>
    <Button className="h-11 sm:h-9" type="button" onClick={onClear}>{t("filters.clear")}</Button>
  </form>;
}
