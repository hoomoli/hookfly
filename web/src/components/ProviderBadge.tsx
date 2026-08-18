import { GitBranch, GitFork, Package, Webhook } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "./ui/badge";

export function ProviderBadge({ provider }: { provider: string }) {
  const { t } = useTranslation();
  const known = provider === "github" || provider === "gitlab" || provider === "harbor";
  const Icon = provider === "github" ? GitFork : provider === "harbor" ? Package : known ? GitBranch : Webhook;
  const label = known ? t(`providers.${provider}`) : provider;
  return <Badge className="gap-1 border-border bg-card text-foreground"><Icon className="size-3" aria-hidden="true" />{label}</Badge>;
}
