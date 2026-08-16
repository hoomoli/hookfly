import { Bell } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { NotificationController } from "../hooks/useNotifications";
import type { NotificationOutcome } from "../types";
import { Badge, type BadgeProps } from "./ui/badge";
import { Button } from "./ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "./ui/sheet";

type Filter = "all" | "push" | "pipeline" | "deployment" | "success" | "failure";

function outcomeTone(outcome: NotificationOutcome): BadgeProps["tone"] {
  if (outcome === "success") return "stable";
  if (outcome === "timeout" || outcome === "cancelled") return "attention";
  if (outcome === "failure") return "failure";
  return "neutral";
}

export function NotificationCenter({ controller, onOpenEvent }: { controller: NotificationController; onOpenEvent: (eventID: string) => void }) {
  const { i18n, t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const items = [...controller.state.items].reverse().filter((item) => filter === "all" || item.category === filter || item.outcome === filter);
  return (
    <>
      <Button type="button" variant="ghost" size="icon" className="relative order-last" aria-label={t("notifications.open", { count: controller.unreadCount })} onClick={() => setOpen(true)}>
        <Bell className="size-4" aria-hidden="true" />
        {controller.unreadCount > 0 && <span className="absolute top-0 right-0 min-w-4 rounded-full bg-destructive px-1 text-[10px] leading-4 text-destructive-foreground">{controller.unreadCount}</span>}
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent>
          <SheetHeader><SheetTitle>{t("notifications.title")}</SheetTitle><SheetDescription>{t("notifications.unread", { count: controller.unreadCount })}</SheetDescription></SheetHeader>
          <div className="border-b border-border px-5 py-3">
            <div className="flex gap-1 overflow-x-auto">
              {(["all", "failure", "success", "push", "pipeline", "deployment"] as const).map((value) => <Button key={value} type="button" size="compact" variant={filter === value ? "primary" : "ghost"} aria-pressed={filter === value} onClick={() => setFilter(value)}>{t(`notifications.filters.${value}`)}</Button>)}
            </div>
          </div>
          {controller.unreadCount > 0 && <div className="px-5 pt-3 text-right"><Button type="button" variant="ghost" size="compact" onClick={controller.markAllRead}>{t("notifications.markAllRead")}</Button></div>}
          <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-5">
            {items.length === 0 && <p className="py-16 text-center text-sm text-muted-foreground">{t("notifications.empty")}</p>}
            {items.map((item) => <button key={item.id} type="button" className="grid min-h-20 w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-4 rounded-xl border border-border bg-card p-4 text-left hover:bg-accent/50" onClick={() => { controller.markRead(item.id); setOpen(false); onOpenEvent(item.event_id); }}>
              <span className="min-w-0"><span className="mb-1.5 flex items-center gap-2"><strong className="truncate text-sm font-medium">{item.target_id || item.repository}</strong><Badge tone={outcomeTone(item.outcome)}>{t(`notifications.outcomes.${item.outcome}`)}</Badge></span><small className="block text-xs text-muted-foreground">{item.summary}</small></span>
              <time className="text-[10px] text-muted-foreground">{new Date(item.occurred_at).toLocaleString(i18n.resolvedLanguage ?? i18n.language, { hour12: false })}</time>
            </button>)}
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}
