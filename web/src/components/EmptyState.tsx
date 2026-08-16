import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  title: string;
  description?: string;
  tone?: "neutral" | "warning" | "destructive";
  action?: ReactNode;
}

export function EmptyState({ title, description, tone = "neutral", action }: EmptyStateProps) {
  return (
    <div
      data-slot="empty-state"
      role={tone === "destructive" ? "alert" : undefined}
      className={cn(
        "grid min-h-56 place-content-center justify-items-center px-6 py-10 text-center",
        tone === "destructive" ? "text-destructive" : tone === "warning" ? "text-warning" : "text-muted-foreground",
      )}
    >
      <strong className="text-sm font-medium text-foreground">{title}</strong>
      {description && <p className="mt-1.5 max-w-md text-sm leading-6">{description}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
