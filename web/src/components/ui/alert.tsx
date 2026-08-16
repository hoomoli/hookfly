import type * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const alertVariants = cva("relative w-full rounded-xl border px-4 py-3 text-sm", {
  variants: {
    tone: {
      neutral: "border-border bg-card text-card-foreground",
      warning: "border-warning/25 bg-warning/10 text-warning",
      destructive: "border-destructive/25 bg-destructive/10 text-destructive",
    },
  },
  defaultVariants: { tone: "neutral" },
});

export interface AlertProps extends React.ComponentProps<"div">, VariantProps<typeof alertVariants> {}

export function Alert({ className, tone, ...props }: AlertProps) {
  return <div data-slot="alert" role={tone === "destructive" ? "alert" : undefined} className={cn(alertVariants({ tone }), className)} {...props} />;
}

export function AlertTitle({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="alert-title" className={cn("font-medium", className)} {...props} />;
}

export function AlertDescription({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="alert-description" className={cn("mt-1 text-sm opacity-85", className)} {...props} />;
}
