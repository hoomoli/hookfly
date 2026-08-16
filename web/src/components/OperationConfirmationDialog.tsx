import type { ReactNode } from "react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Operation } from "../types";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "./ui/dialog";

interface Props {
  currentAttemptID: string;
  operation: Operation;
  onConfirm: (reason: string) => Promise<void> | void;
  onOpenChange?: (open: boolean) => void;
  open?: boolean;
  trigger?: ReactNode;
}

export function OperationConfirmationDialog({ currentAttemptID, operation, onConfirm, onOpenChange, open: controlledOpen, trigger }: Props) {
  const { t } = useTranslation();
  const [internalOpen, setInternalOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const open = controlledOpen ?? internalOpen;

  const reset = () => {
    setReason("");
    setError(null);
  };

  const changeOpen = (nextOpen: boolean) => {
    if (!nextOpen && pending) return;
    if (controlledOpen === undefined) setInternalOpen(nextOpen);
    onOpenChange?.(nextOpen);
    if (!nextOpen) reset();
  };

  const submit = async () => {
    setPending(true);
    setError(null);
    try {
      await onConfirm(reason);
      if (controlledOpen === undefined) setInternalOpen(false);
      onOpenChange?.(false);
      reset();
    } catch (reasonValue) {
      setError(reasonValue instanceof Error ? reasonValue.message : t("operation.failed"));
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      {trigger && <DialogTrigger asChild>{trigger}</DialogTrigger>}
      <DialogContent
        onEscapeKeyDown={(event) => {
          event.preventDefault();
          if (!pending) changeOpen(false);
        }}
        onPointerDownOutside={(event) => {
          event.preventDefault();
          if (!pending) changeOpen(false);
        }}
      >
        <DialogHeader>
          <DialogTitle>{t(operation === "retry" ? "operation.confirmRetry" : "operation.confirmRedeploy")}</DialogTitle>
          <DialogDescription>{t("operation.description", { id: currentAttemptID })}</DialogDescription>
        </DialogHeader>
        <label className="mt-4 grid gap-1.5 text-xs font-medium text-muted-foreground"><span>{t("operation.reason")}</span><textarea className="min-h-20 w-full resize-y rounded-xl border border-input bg-card p-3 text-sm text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/20" value={reason} onChange={(event) => setReason(event.currentTarget.value)} rows={3} /></label>
        {error && <Alert tone="destructive" className="mt-3"><AlertDescription className="mt-0">{error}</AlertDescription></Alert>}
        <DialogFooter><DialogClose asChild><Button type="button" disabled={pending}>{t("common.cancel")}</Button></DialogClose><Button variant="primary" type="button" disabled={pending} onClick={() => void submit()}>{pending ? t("operation.submitting") : t("operation.confirm")}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
