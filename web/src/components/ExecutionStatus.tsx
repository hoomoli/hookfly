import { CircleAlert, CircleCheck, Clock3, LoaderCircle, RotateCcw } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { EventDeliverySummary, Operation } from "../types";
import { Badge, type BadgeProps } from "./ui/badge";
import { Button } from "./ui/button";

interface Presentation { labelKey: string; tone: BadgeProps["tone"]; active: boolean; operation: Operation | null }

export function executionPresentation(transport: string, deployment: string): Presentation {
  if (transport === "pending" || transport === "sending") return { labelKey: "delivering", tone: "active", active: true, operation: null };
  if (transport === "failed") return { labelKey: "deliveryFailed", tone: "failure", active: false, operation: "retry" };
  if (transport === "unknown") return { labelKey: "deliveryUnknown", tone: "failure", active: false, operation: "retry" };
  if (deployment === "not_started") return { labelKey: "waitingDeployment", tone: "neutral", active: false, operation: null };
  if (deployment === "locating" || deployment === "running" || deployment === "unrecognized") return { labelKey: "deploying", tone: "active", active: true, operation: null };
  if (deployment === "done") return { labelKey: "deploymentSucceeded", tone: "stable", active: false, operation: null };
  if (deployment === "error") return { labelKey: "deploymentFailed", tone: "failure", active: false, operation: "redeploy" };
  if (deployment === "cancelled") return { labelKey: "deploymentCancelled", tone: "attention", active: false, operation: "redeploy" };
  if (deployment === "timeout") return { labelKey: "deploymentTimedOut", tone: "failure", active: false, operation: "redeploy" };
  return { labelKey: "deploymentUnknown", tone: "attention", active: false, operation: "redeploy" };
}

export function ExecutionStatus({ delivery, onOperation, showOperation = true }: { delivery: EventDeliverySummary; onOperation: (delivery: EventDeliverySummary, operation: Operation) => void; showOperation?: boolean }) {
  const { t } = useTranslation();
  const presentation = executionPresentation(delivery.transport_status, delivery.deployment_status);
  const Icon = presentation.active ? LoaderCircle : presentation.tone === "stable" ? CircleCheck : presentation.tone === "neutral" ? Clock3 : CircleAlert;
  const operation = showOperation && presentation.operation && delivery.allowed_operations.includes(presentation.operation) ? presentation.operation : null;
  return <div className="flex items-center gap-1.5"><Badge tone={presentation.tone} className="gap-1"><Icon className={presentation.active ? "size-3 animate-spin" : "size-3"} aria-hidden="true" />{t(`execution.${presentation.labelKey}`)}</Badge>{operation && <Button type="button" variant="ghost" size="icon" className="size-7 rounded-full text-destructive hover:bg-destructive/10" aria-label={t(operation === "retry" ? "execution.retryTarget" : "execution.redeployTarget", { target: delivery.target_id })} onClick={(event) => { event.stopPropagation(); onOperation(delivery, operation); }}><RotateCcw className="size-3.5" aria-hidden="true" /></Button>}</div>;
}
