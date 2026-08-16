import { CircleAlert, CircleCheck, Clock3, LoaderCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge, type BadgeProps } from "./ui/badge";

interface Presentation { tone: BadgeProps["tone"]; active: boolean }

export function sourceStatusPresentation(status: string): Presentation {
  if (["waiting_for_pipeline", "created", "preparing", "scheduled", "pending", "queued", "waiting_for_resource", "requested", "running", "in_progress"].includes(status)) return { tone: "active", active: true };
  if (["success", "done", "completed"].includes(status)) return { tone: "stable", active: false };
  if (["failed", "failure", "error", "cancelled", "canceled", "pipeline_not_received"].includes(status)) return { tone: "failure", active: false };
  return { tone: "neutral", active: false };
}

export function SourceStatus({ status, animated = true, completed = false }: { status: string; animated?: boolean; completed?: boolean }) {
  const { t } = useTranslation();
  const current = sourceStatusPresentation(status);
  const presentation = completed && current.active ? { tone: "stable" as const, active: false } : current;
  const Icon = presentation.active ? LoaderCircle : presentation.tone === "stable" ? CircleCheck : presentation.tone === "failure" ? CircleAlert : Clock3;
  return <Badge tone={presentation.tone} className="gap-1"><Icon data-testid="source-status-icon" className={presentation.active && animated ? "size-3 animate-spin" : "size-3"} aria-hidden="true" />{t(`sourceStatuses.${status}`, { defaultValue: status })}</Badge>;
}
