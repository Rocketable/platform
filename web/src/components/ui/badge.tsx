import { cn } from "@/lib/utils";
import type { HTMLAttributes } from "react";

export function Badge({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-medium text-muted-foreground", className)}
      {...props}
    />
  );
}
