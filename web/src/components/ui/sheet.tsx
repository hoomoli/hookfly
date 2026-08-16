import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

export const Sheet = DialogPrimitive.Root;

export function SheetContent({ className, children, ...props }: React.ComponentProps<typeof DialogPrimitive.Content>) {
  const { t } = useTranslation();
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/20 backdrop-blur-[1px] data-[state=open]:animate-[fade-in_160ms_ease-out]" />
      <DialogPrimitive.Content
        data-slot="sheet-content"
        className={cn("fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-border bg-background text-foreground shadow-[-24px_0_80px_rgb(0_0_0/0.18)] outline-none data-[state=open]:animate-[sheet-in_190ms_ease-out] sm:max-w-[560px]", className)}
        {...props}
      >
        {children}
        <DialogPrimitive.Close className="absolute top-4 right-4 grid size-11 place-items-center rounded-[11px] text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring sm:size-9" aria-label={t("accessibility.closeDetails")}>
          <X className="size-4" aria-hidden="true" />
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

export function SheetHeader({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="sheet-header" className={cn("border-b border-border px-5 py-5 pr-16", className)} {...props} />;
}

export function SheetTitle({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Title>) {
  return <DialogPrimitive.Title data-slot="sheet-title" className={cn("text-xl font-semibold tracking-[-0.02em]", className)} {...props} />;
}

export function SheetDescription({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Description>) {
  return <DialogPrimitive.Description data-slot="sheet-description" className={cn("mt-1 font-mono text-[11px] text-muted-foreground", className)} {...props} />;
}
