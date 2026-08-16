import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Alert, AlertDescription } from "./ui/alert";
import { Button } from "./ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "./ui/dialog";

interface Props {
  onConfirm: () => Promise<void>;
}

export function HistoryClearDialog({ onConfirm }: Props) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const changeOpen = (nextOpen: boolean) => {
    if (!nextOpen && pending) return;
    setOpen(nextOpen);
    if (!nextOpen) setError(null);
  };

  const submit = async () => {
    setPending(true);
    setError(null);
    try {
      await onConfirm();
      setOpen(false);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("errors.requestFailed"));
    } finally {
      setPending(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogTrigger asChild><Button type="button" variant="destructive" className="mt-5">{t("settingsCenter.history.clear")}</Button></DialogTrigger>
      <DialogContent
        onEscapeKeyDown={(event) => { if (pending) event.preventDefault(); }}
        onPointerDownOutside={(event) => { if (pending) event.preventDefault(); }}
      >
        <DialogHeader>
          <DialogTitle>{t("settingsCenter.history.confirmTitle")}</DialogTitle>
          <DialogDescription>{t("settingsCenter.history.confirmDescription")}</DialogDescription>
        </DialogHeader>
        {error && <Alert tone="destructive" className="mt-3"><AlertDescription className="mt-0">{error}</AlertDescription></Alert>}
        <DialogFooter>
          <DialogClose asChild><Button type="button" disabled={pending}>{t("common.cancel")}</Button></DialogClose>
          <Button type="button" variant="destructive" disabled={pending} onClick={() => void submit()}>{pending ? t("settingsCenter.history.pending") : t("settingsCenter.history.clear")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
