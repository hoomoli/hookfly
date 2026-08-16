import type * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex min-h-5 items-center rounded-full border px-2 py-0.5 text-[11px] font-medium leading-none",
  {
    variants: {
      tone: {
        neutral: "border-border bg-muted text-muted-foreground",
        matched: "border-success/20 bg-success/10 text-success",
        unmatched: "border-warning/20 bg-warning/10 text-warning",
        active: "border-primary/20 bg-primary/10 text-primary",
        attention: "border-warning/20 bg-warning/10 text-warning",
        stable: "border-success/20 bg-success/10 text-success",
        failure: "border-destructive/20 bg-destructive/10 text-destructive",
      },
    },
    defaultVariants: { tone: "neutral" },
  },
);

export interface BadgeProps extends React.ComponentProps<"span">, VariantProps<typeof badgeVariants> {}

export function Badge({ className, tone, ...props }: BadgeProps) {
  return <span data-slot="badge" data-tone={tone ?? "neutral"} className={cn(badgeVariants({ tone }), className)} {...props} />;
}
