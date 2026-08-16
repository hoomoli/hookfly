import { BellRing } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { NotificationController } from "../hooks/useNotifications";
import { Alert, AlertDescription, AlertTitle } from "./ui/alert";
import { Button } from "./ui/button";

export function SystemNotificationPrompt({ controller }: { controller: NotificationController }) {
  const { t } = useTranslation();
  if (controller.systemState === "granted" && controller.state.system_enabled) return null;

  const displayState = controller.systemState === "granted" ? "default" : controller.systemState;
  const actionable = displayState === "default";

  return (
    <Alert tone={displayState === "denied" ? "destructive" : "warning"} className="mt-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex min-w-0 items-start gap-3">
        <BellRing className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
        <div>
          <AlertTitle>{t(`notifications.prompt.${displayState}.title`)}</AlertTitle>
          <AlertDescription>{t(`notifications.system.${displayState}`)}</AlertDescription>
        </div>
      </div>
      {actionable && <Button type="button" size="compact" variant="secondary" className="self-start shrink-0 sm:self-center" onClick={() => void controller.enableSystemNotifications()}>{t("notifications.enableSystem")}</Button>}
    </Alert>
  );
}
