import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Attempt } from "../types";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

async function copyDeploymentRequest(command: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(command);
      return true;
    } catch {
      // Plain HTTP and browser policy can reject the Clipboard API. Try the DOM fallback below.
    }
  }
  const textarea = document.createElement("textarea");
  textarea.dataset.hookflyDeployClipboard = "";
  textarea.value = command;
  textarea.readOnly = true;
  textarea.tabIndex = -1;
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  textarea.style.opacity = "0";
  try {
    document.body.appendChild(textarea);
    textarea.select();
    textarea.setSelectionRange(0, command.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.getSelection()?.removeAllRanges();
    textarea.remove();
  }
}

function DeployRequest({ attempt }: { attempt: Attempt }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const command = attempt.deploy_command;
  if (!command) return null;
  const copy = async () => {
    setCopyFailed(false);
    if (await copyDeploymentRequest(command)) {
      setCopied(true);
      return;
    }
    setCopied(false);
    setCopyFailed(true);
  };
  return (
    <details className="mt-2 rounded-lg border border-border bg-muted/25 px-3 py-2">
      <summary className="cursor-pointer text-[10px] font-medium text-foreground">{t("attempt.deployRequest")}</summary>
      <p className="my-2 text-[10px] text-muted-foreground">{t("attempt.deployCredentialNote")}</p>
      <p className="my-2 text-[10px] text-muted-foreground">{t("attempt.deployDeliveryNote")}</p>
      <pre className="m-0 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-md bg-background p-2 font-mono text-[9px] text-foreground">{command}</pre>
      <Button type="button" size="compact" className="mt-2" aria-label={copied ? t("attempt.deployRequestCopied") : t("attempt.copyDeployRequest")} onClick={() => void copy()}>
        {copied ? t("attempt.deployRequestCopied") : t("attempt.copyDeployRequest")}
      </Button>
      {copyFailed && <p role="alert" className="mb-0 text-[10px] text-destructive">{t("attempt.copyDeployRequestFailed")}</p>}
    </details>
  );
}

export function AttemptTimeline({ attempts }: { attempts: Attempt[] }) {
  const { t } = useTranslation();
  if (attempts.length === 0) return <p className="m-0 px-4 py-3 text-xs text-muted-foreground">{t("attempt.empty")}</p>;
  return (
    <ol className="relative m-0 list-none px-4 pb-4 pl-9 before:absolute before:top-3 before:bottom-7 before:left-[19px] before:w-px before:bg-border">
      {attempts.map((attempt) => (
        <li key={attempt.id} className="relative py-2.5">
          <span className={attempt.current ? "absolute top-4 -left-[22px] size-2 rounded-full bg-primary ring-4 ring-primary/10" : "absolute top-4 -left-[22px] size-2 rounded-full border border-muted-foreground bg-card"} aria-hidden="true" />
          <div className="flex items-center gap-2"><strong className="text-xs font-medium">{attempt.kind}</strong>{attempt.current && <Badge tone="active" className="min-h-4 px-1.5 text-[9px]">{t("attempt.current")}</Badge>}<time className="ml-auto font-mono text-[9px] text-muted-foreground">{attempt.created_at}</time></div>
          <p className="my-1 flex gap-1.5 text-[10px] text-muted-foreground"><span>{attempt.transport_status}</span><span>·</span><span>{attempt.deployment_status}</span></p>
          <small className="block truncate font-mono text-[9px] text-muted-foreground">{attempt.actor ?? "system"} · {attempt.id}</small>
          <DeployRequest attempt={attempt} />
        </li>
      ))}
    </ol>
  );
}
