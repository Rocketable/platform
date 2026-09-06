"use client";

import * as Dialog from "@radix-ui/react-dialog";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export const Sheet = Dialog.Root;
export const SheetTrigger = Dialog.Trigger;

export function SheetContent({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <Dialog.Portal>
      <Dialog.Overlay className="fixed inset-0 z-50 bg-black/40" />
      <Dialog.Content
        className={cn(
          "fixed inset-y-0 left-0 z-50 w-[min(100%,20rem)] border-r bg-sidebar p-4 text-sidebar-foreground shadow-lg",
          className,
        )}
      >
        <Dialog.Title className="sr-only">Sessions</Dialog.Title>
        {children}
      </Dialog.Content>
    </Dialog.Portal>
  );
}
