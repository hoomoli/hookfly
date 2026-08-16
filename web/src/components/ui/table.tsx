import * as React from "react";
import { cn } from "@/lib/utils";

export const Table = React.forwardRef<HTMLTableElement, React.ComponentProps<"table">>(({ className, ...props }, ref) => (
  <div data-slot="table-container" className="relative w-full overflow-x-auto">
    <table ref={ref} data-slot="table" className={cn("w-full caption-bottom text-sm", className)} {...props} />
  </div>
));
Table.displayName = "Table";

export const TableHeader = React.forwardRef<HTMLTableSectionElement, React.ComponentProps<"thead">>(({ className, ...props }, ref) => (
  <thead ref={ref} data-slot="table-header" className={cn("[&_tr]:border-b", className)} {...props} />
));
TableHeader.displayName = "TableHeader";

export const TableBody = React.forwardRef<HTMLTableSectionElement, React.ComponentProps<"tbody">>(({ className, ...props }, ref) => (
  <tbody ref={ref} data-slot="table-body" className={cn("[&_tr:last-child]:border-0", className)} {...props} />
));
TableBody.displayName = "TableBody";

export const TableRow = React.forwardRef<HTMLTableRowElement, React.ComponentProps<"tr">>(({ className, ...props }, ref) => (
  <tr ref={ref} data-slot="table-row" className={cn("border-b border-border transition-colors hover:bg-muted/45", className)} {...props} />
));
TableRow.displayName = "TableRow";

export const TableHead = React.forwardRef<HTMLTableCellElement, React.ComponentProps<"th">>(({ className, ...props }, ref) => (
  <th ref={ref} data-slot="table-head" className={cn("h-9 px-3 text-left align-middle text-[11px] font-medium text-muted-foreground", className)} {...props} />
));
TableHead.displayName = "TableHead";

export const TableCell = React.forwardRef<HTMLTableCellElement, React.ComponentProps<"td">>(({ className, ...props }, ref) => (
  <td ref={ref} data-slot="table-cell" className={cn("px-3 py-2 align-middle", className)} {...props} />
));
TableCell.displayName = "TableCell";
