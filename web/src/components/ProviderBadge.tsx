import { GitBranch, GitFork } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "./ui/badge";

export function ProviderBadge({ provider }: { provider: string }) {
  const { t } = useTranslation();
  const Icon = provider === "github" ? GitFork : GitBranch;
  return <Badge className="gap-1 border-border bg-card text-foreground"><Icon className="size-3" aria-hidden="true" />{t(provider === "github" ? "providers.github" : "providers.gitlab")}</Badge>;
}
