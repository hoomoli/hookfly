import { GitCommitHorizontal, GitPullRequest, Package, Tag, Workflow } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "./ui/badge";

export function EventTypeBadge({ type }: { type: string }) {
  const { t } = useTranslation();
  const Icon = type === "merge_request" ? GitPullRequest : type === "tag_push" ? Tag : type === "push" ? GitCommitHorizontal : type === "artifact_push" ? Package : Workflow;
  const known = ["pipeline", "job", "push", "tag_push", "merge_request", "artifact_push"].includes(type);
  return <Badge tone={known ? "active" : "neutral"} className="gap-1"><Icon className="size-3" aria-hidden="true" />{known ? t(`eventTypes.${type}`) : type}</Badge>;
}
