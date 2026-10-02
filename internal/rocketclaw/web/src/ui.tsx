"use client";

import { Effect, Fiber, Option, Queue, Schedule, Schema, Stream } from "effect";
import { AsyncResult, Atom, AtomRegistry } from "effect/reactivity";
import { RegistryContext, useAtomValue } from "@effect/atom-react";
import { histories, invalidate, queries, registry, useAction, useRemote } from "./state";
import { Menu } from "@base-ui/react/menu";
import { Dialog, DialogContent, DialogTitle, DialogDescription, DialogClose, DialogHeader, DialogFooter, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Field, FieldGroup, FieldLabel, FieldError } from "@/components/ui/field";
import { queries as requests, mutations, listSessions, uploadAttachment, downloadAttachment } from "./api";
import type { ChatOrigin, HistoryView, MessageMatch, PromptDelivery } from "./types";
import { Bot, Check, CircleAlert, Clock, Command, Copy, CornerUpLeft, Download, Ellipsis, FileIcon, GitFork, GripVertical, Info, LoaderCircle, PanelLeftClose, PanelLeftOpen, Pin, Play, Plus, Search, Send, Square, SquarePen, TextCursorInput, Undo2, X } from "lucide-react";
import Link, { usePathname, useSearch, navigate } from "./navigation";
import { createContext, memo, use, useCallback, useContext, useEffect, useId, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type Dispatch, type SetStateAction, type ReactNode, type SyntheticEvent, type RefObject, type ComponentProps } from "react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { flushSync } from "react-dom";
import { PaletteChooser, ThemeToggle } from "@/components/theme";
import { CodeBlock, TranscriptText, copyText } from "./transcript-text";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Attachment, AttachmentGroup, AttachmentMedia, AttachmentContent, AttachmentTitle, AttachmentDescription, AttachmentActions, AttachmentAction } from "@/components/ui/attachment";
import { Message, MessageContent } from "@/components/ui/message";
import { MessageScrollerProvider, MessageScroller, MessageScrollerViewport, MessageScrollerContent, MessageScrollerItem, MessageScrollerButton, useMessageScroller } from "@/components/ui/message-scroller";
import { Sheet, SheetContent, SheetTrigger, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { Attachment as AttachmentMeta, ConfigView, CronJob, QueueItem, Session, TranscriptEvent } from "@/types";
import { runPreload } from "@/preload";
import { decodeSessionId, encodeSessionId } from "@/session-id";
import {
  invalidatePendingSaves,
  loadSavedSessions,
  loadSnapshotGeneration,
  mergeSessionRows,
  readSessionEnumeration,
  rowPreview,
  saveCompleteSessions,
  SESSION_HISTORY_CHANNEL,
  searchIsAuthoritative,
  shouldCommitSnapshot,
  stripSessionHistory,
} from "@/session-list";

const tabReturnTo = { current: "/" };
type SessionCommand = { mode: "fork" | "handoff" | "name" | "snooze" | "queue"; source: string; target?: MessageMatch };
const SessionCommands = createContext<{ command?: SessionCommand; setCommand: Dispatch<SetStateAction<SessionCommand | undefined>>; composer: RefObject<((command: string) => void) | null> }>(null!);

function sessionPath(id: string) {
  return `/s/${encodeSessionId(id)}`;
}

const Delegations = createContext<string[] | undefined>(undefined);

function delegationHref(id?: string) {
  const params = new URLSearchParams(location.search);
  if (id) params.set("delegation", id);
  else params.delete("delegation");
  return `${location.pathname}${params.size ? `?${params}` : ""}`;
}

function subscribeWide(listener: () => void) {
  const media = matchMedia("(min-width: 64rem)");
  media.addEventListener("change", listener);
  return () => media.removeEventListener("change", listener);
}

function slackSession(id: string) {
  return id.startsWith("slack-thread:");
}

function typedPrefix(query: string, prefix: string) {
  if (!query.toLowerCase().startsWith(prefix)) {
    return null;
  }
  return query.slice(prefix.length);
}

function slackRooms(sessions: { id: string; title?: string }[]) {
  return [...new Set(sessions.flatMap((session) =>
    slackSession(session.id) && session.title ? [session.title] : []
  ))];
}

function overlayChoices(
  agentPrefix: string | null,
  roomPrefix: string | null,
  catalog: { name: string; model?: string; reasoning?: string }[],
  rooms: string[],
) {
  const items: { key: string; label: string; detail?: string }[] = [];
  if (agentPrefix !== null) {
    const needle = agentPrefix.trim().toLowerCase();
    for (const item of catalog) {
      if (item.name.toLowerCase().includes(needle)) {
        items.push({ key: item.name, label: item.name, detail: [item.model, item.reasoning].filter(Boolean).join(" · ") });
      }
    }
    return items;
  }
  if (roomPrefix !== null) {
    const needle = roomPrefix.trim().toLowerCase();
    for (const name of rooms) {
      if (name.toLowerCase().includes(needle)) {
        items.push({ key: name, label: name });
      }
    }
  }
  return items;
}

function FilterPill({ label, onClear }: { label: string; onClear: () => void }) {
  return (
    <button type="button" className="flex h-5 max-w-[8rem] shrink-0 items-center rounded-full bg-sidebar-row-active px-2 text-xs" onMouseDown={(event) => event.preventDefault()} onClick={onClear}>
      <span className="truncate">{label}</span>
    </button>
  );
}

const dollarCommands = [
  { name: "fork", label: "Fork session", hint: "", desc: "Fork from a chosen message" },
  { name: "handoff", label: "Handoff session", hint: "", desc: "Copy a handoff or send it to another session" },
  { name: "goal", label: "Start goal", hint: "<objective>", desc: "Start a goal loop" },
  { name: "stop", label: "Stop turn", hint: "", desc: "End the active turn" },
  { name: "cron", label: "Run cron", hint: "[job]", desc: "List or run a cron job" },
  { name: "workflow", label: "Run workflow", hint: "<name> [args]", desc: "Run a saved workflow" },
  { name: "agent", label: "Choose agent", hint: "[name]", desc: "List or switch agent" },
  { name: "enqueue", label: "Stash work", hint: "<text>", desc: "Stash later work" },
  { name: "queue", label: "Show queue", hint: "", desc: "List pending steers and later work" },
  { name: "skill", label: "Invoke skill", hint: "<name> [args]", desc: "Invoke a skill by name" },
];

function dollarMatches(text: string, skills: { name: string; description?: string }[]) {
  const skillQuery = /^\$skill(?:[\t ]+([^\s]*))?$/i.exec(text);
  if (skillQuery) {
    return skills.flatMap((skill) => skill.name.toLowerCase().startsWith((skillQuery[1] ?? "").toLowerCase()) ? [{
      name: skill.name,
      hint: "[args]",
      desc: skill.description ?? "",
      invocation: `$skill ${skill.name} `,
    }] : []);
  }
  if (!text.startsWith("$") || text.includes("\n") || text.includes(" ")) {
    return [];
  }
  const query = text.slice(1).toLowerCase();
  if (dollarCommands.some((cmd) => cmd.name === query && cmd.hint === "")) {
    return [];
  }
  return [
    ...dollarCommands.flatMap((cmd) => cmd.name.startsWith(query) ? [{ ...cmd, invocation: `$${cmd.name} ` }] : []),
    ...skills.flatMap((skill) => skill.name.toLowerCase().startsWith(query) ? [{
      name: skill.name,
      hint: "[args]",
      desc: skill.description ?? "",
      invocation: dollarCommands.some((cmd) => cmd.name === skill.name.toLowerCase()) ? `$skill ${skill.name} ` : `$${skill.name} `,
    }] : []),
  ];
}

function isStopCommand(text: string) {
  const trimmed = text.trim().toLowerCase();
  return trimmed === "$stop" || trimmed.startsWith("$stop ");
}

function moveQueueId(ids: string[], from: string, to: string) {
  const next = ids.slice();
  const i = next.indexOf(from);
  const j = next.indexOf(to);
  if (i < 0 || j < 0 || i === j) {
    return ids;
  }
  next.splice(i, 1);
  next.splice(j, 0, from);
  return next;
}

function QueuePanel({
  conversationId,
  items,
  busy,
  onSteer,
  onPop,
  onRemove,
  onReorder,
}: {
  conversationId: string;
  items: QueueItem[];
  busy: boolean;
  onSteer: (id: string, text: string) => void;
  onPop: (id: string) => Promise<unknown>;
  onRemove: (id: string) => void;
  onReorder: (itemIds: string[]) => void;
}) {
  const dragId = useRef<string | null>(null);
  const [order, setOrder] = useState<string[] | null>(null);
  const [poppingId, setPoppingId] = useState("");
  const queueActionLabel = busy ? "Steer" : "Send";
  if (items.length === 0) {
    return null;
  }
  const ids = order ?? items.map((item) => item.id);
  const rows = ids.flatMap((id) => items.filter((item) => item.id === id));
  const finish = () => {
    const next = order;
    dragId.current = null;
    setOrder(null);
    if (!next || next.length !== items.length || next.every((id, index) => id === items[index]?.id)) {
      return;
    }
    onReorder(next);
  };
  return (
    <div className="relative z-0 -mb-3 rounded-[22px] bg-muted px-2 pt-2 pb-5 shadow-[inset_0_0_0_1px_var(--border)]">
      <ul className={cn("flex flex-col gap-0.5", items.length > 3 && "max-h-32 overflow-y-auto")}>
        {rows.map((item) => (
          <li key={item.id} data-queue-id={item.id} className="flex items-center gap-2 rounded-md px-2 py-1">
            <button
              type="button"
              className="cursor-grab touch-none p-1 text-muted-foreground"
              aria-label="Reorder"
              onPointerDown={(event) => {
                if (event.button !== 0) {
                  return;
                }
                event.preventDefault();
                event.currentTarget.setPointerCapture(event.pointerId);
                dragId.current = item.id;
                setOrder(ids);
              }}
              onPointerMove={(event) => {
                if (!dragId.current) {
                  return;
                }
                const to = document.elementFromPoint(event.clientX, event.clientY)?.closest("[data-queue-id]")?.getAttribute("data-queue-id");
                if (!to) {
                  return;
                }
                setOrder((current) => moveQueueId(current ?? ids, dragId.current ?? "", to));
              }}
              onPointerUp={finish}
              onPointerCancel={() => {
                dragId.current = null;
                setOrder(null);
              }}
            >
              <GripVertical className="h-3.5 w-3.5" />
            </button>
            <span className="min-w-0 flex-1 truncate text-sm">{item.delivery === "STASH" ? "Stashed · " : "Queued · "}{item.text}</span>
            <MessageAttachments attachments={item.attachments} conversationId={conversationId} />
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={item.delivery === "STASH" && poppingId !== ""}
              onClick={async () => {
                if (item.delivery !== "STASH") {
                  onSteer(item.id, item.text);
                  return;
                }
                setPoppingId(item.id);
                try { await onPop(item.id); } finally { setPoppingId(""); }
              }}
            >
              {item.id === poppingId ? "Popping…" : item.delivery === "STASH" ? "Pop" : queueActionLabel}
            </Button>
            <button type="button" className="shrink-0 rounded-md p-1 text-muted-foreground hover:bg-accent" aria-label="Remove" onClick={() => onRemove(item.id)}>
              <X className="h-3.5 w-3.5" />
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function sessionLabel(id: string) {
  if (id.startsWith("web-session:")) {
    return id.slice("web-session:".length);
  }
  if (slackSession(id)) {
    const parts = id.split(":");
    return parts[1] ? `slack ${parts[1]}` : id;
  }
  return id;
}

function useRoute() {
  const pathname = usePathname() || "/";
  const cron = pathname === "/cron";
  const agents = pathname === "/agents";
  const skills = pathname === "/skills";
  const config = pathname === "/config";
  const settled = pathname === "/settled";
  const search = pathname === "/search";
  const id = pathname.startsWith("/s/") ? decodeSessionId(pathname.slice(3)) : "";
  return {
    cron,
    agents,
    skills,
    config,
    settled,
    search,
    id,
    goHome: () => navigate("/"),
    goCron: () => navigate("/cron"),
    goSession: (sessionId: string) => navigate(sessionPath(sessionId)),
  };
}

function TabPane({ show, children }: { show: boolean; children: ReactNode }) {
  return <div className={cn("min-h-0 flex-1 flex-col", show ? "flex" : "hidden")}>{children}</div>;
}

function WarmTabs({ cron, agents, skills, config }: { cron: boolean; agents: boolean; skills: boolean; config: boolean }) {
  const [warm, setWarm] = useState({ cron, agents, skills, config });
  useEffect(() => {
    setWarm((current) => ({
      cron: current.cron || cron,
      agents: current.agents || agents,
      skills: current.skills || skills,
      config: current.config || config,
    }));
  }, [cron, agents, skills, config]);
  useEffect(() => {
    return runPreload(
      AtomRegistry.getResult(registry, queries.agents()).pipe(Effect.ignore),
      AtomRegistry.getResult(registry, queries.skills()).pipe(Effect.ignore),
      AtomRegistry.getResult(registry, queries.cronJobs()).pipe(Effect.ignore),
      AtomRegistry.getResult(registry, queries.config()).pipe(Effect.ignore),
      () => setWarm({ cron: true, agents: true, skills: true, config: true }),
    );
  }, []);
  return (
    <>
      {warm.cron ? (
        <TabPane show={cron}>
          <CronPage />
        </TabPane>
      ) : null}
      {warm.agents ? (
        <TabPane show={agents}>
          <AgentsPage />
        </TabPane>
      ) : null}
      {warm.skills ? (
        <TabPane show={skills}>
          <SkillsPage />
        </TabPane>
      ) : null}
      {warm.config ? (
        <TabPane show={config}>
          <ConfigPage />
        </TabPane>
      ) : null}
    </>
  );
}

function ProtocolGuard() {
  const proto = useRemote(queries.protocol());
  const seen = useRef("");
  useEffect(() => {
    const hash = proto.data ?? "";
    if (hash === "") {
      return;
    }
    if (seen.current === "") {
      seen.current = hash;
      return;
    }
    if (hash !== seen.current) {
      window.location.reload();
    }
  }, [proto.data]);
  return null;
}

type SidebarView = {
  rows: Session[];
  refreshing: boolean;
  enumerationComplete: boolean;
  summariesComplete: boolean;
  loadingIds: ReadonlySet<string>;
};

type SidebarState = SidebarView & {
  invalidateQueries: () => void;
};

const Sidebar = createContext<SidebarState>({
  rows: [],
  refreshing: false,
  enumerationComplete: false,
  summariesComplete: false,
  loadingIds: new Set(),
  invalidateQueries: () => {},
});
const SidebarInvalidation = createContext<() => void>(() => {});

function SidebarOwner({ children }: { children: ReactNode }) {
  const identity = useRemote(queries.identity());
  const protocol = useRemote(queries.protocol());
  const owner = identity.isSuccess ? identity.data : undefined;
  const identityRejected = identity.isError;
  const [identityGeneration, setIdentityGeneration] = useState(0);
  const [generation, setGeneration] = useState(0);
  const [view, setView] = useState<SidebarView>({ rows: [], refreshing: false, enumerationComplete: false, summariesComplete: false, loadingIds: new Set() });
  const gen = useRef(0);
  const rowsRef = useRef<Session[]>([]);
  const committed = useRef(false);
  const ownerRef = useRef(owner);
  const protocolRef = useRef(protocol.data);
  const bump = useCallback(() => {
    gen.current += 1;
    setGeneration((value) => value + 1);
  }, []);
  const reset = useCallback(() => {
    invalidatePendingSaves();
    gen.current += 1;
    rowsRef.current = [];
    committed.current = false;
    setView({ rows: [], refreshing: false, enumerationComplete: false, summariesComplete: false, loadingIds: new Set() });
  }, []);
  useEffect(() => {
    const prev = ownerRef.current;
    const prevProtocol = protocolRef.current;
    ownerRef.current = owner;
    protocolRef.current = protocol.data;
    if (!identityRejected && prev === owner && prevProtocol === protocol.data) {
      return;
    }
    reset();
  }, [owner, protocol.data, identityRejected, identityGeneration, reset]);
  useEffect(() => {
    if (owner === undefined || protocol.data === undefined || identityRejected) {
      return;
    }
    const captured = gen.current;
    const currentOwner = owner;
    const currentProtocol = protocol.data;
    const hydration = Effect.runFork(loadSavedSessions(currentOwner, currentProtocol).pipe(Effect.tap((saved) => Effect.sync(() => {
      if (captured !== gen.current || ownerRef.current !== currentOwner || committed.current || saved === undefined) {
        return;
      }
      const merged = mergeSessionRows(saved, rowsRef.current);
      rowsRef.current = merged;
      setView((current) => ({ ...current, rows: merged }));
    })), Effect.ignore));
    return () => { Effect.runFork(Fiber.interrupt(hydration)); };
  }, [owner, protocol.data, identityRejected, identityGeneration]);
  useEffect(() => {
    if (owner === undefined || protocol.data === undefined || identityRejected) {
      return;
    }
    const captured = gen.current;
    const currentOwner = owner;
    const currentProtocol = protocol.data;
    const tick = Effect.gen(function* () {
      if (captured !== gen.current) return;
      setView((current) => ({ ...current, refreshing: true, enumerationComplete: false, summariesComplete: true }));
      const snapshotGeneration = yield* loadSnapshotGeneration(currentOwner, currentProtocol).pipe(Effect.catch(() => Effect.succeed(undefined)));
      const result = yield* readSessionEnumeration(currentOwner, listSessions(), () => rowsRef.current, (merged, summaries, complete) => {
        if (captured !== gen.current) {
          return;
        }
        rowsRef.current = merged;
        setView((current) => {
          const next = new Set(current.loadingIds);
          for (const [id, ready] of summaries) {
            if (ready) next.delete(id);
            else next.add(id);
          }
          return { ...current, rows: merged, summariesComplete: complete, loadingIds: next };
        });
      });
      if (captured !== gen.current) return;
      if (result.mismatch) {
        reset();
        ownerRef.current = undefined;
        // A fast same-owner refetch can hide the intermediate pending state.
        registry.refresh(queries.identity());
        yield* AtomRegistry.getResult(registry, queries.identity(), { suspendOnWaiting: true }).pipe(Effect.ignore);
        setIdentityGeneration((value) => value + 1);
      } else if (shouldCommitSnapshot(result)) {
        committed.current = true;
        rowsRef.current = result.received;
        setView({ rows: result.received, enumerationComplete: true, summariesComplete: true, refreshing: false, loadingIds: new Set() });
        yield* saveCompleteSessions(currentOwner, currentProtocol, result.received, snapshotGeneration).pipe(Effect.ignore);
      } else {
        setView((current) => ({ ...current, enumerationComplete: result.exhausted && result.upstreamSuccess && !result.mismatch, refreshing: false }));
      }
    });
    const refresh = Effect.runFork(tick.pipe(Effect.repeat(Schedule.spaced(2000))));
    return () => {
      Effect.runFork(Fiber.interrupt(refresh));
      invalidatePendingSaves(currentOwner, currentProtocol);
    };
  }, [owner, protocol.data, identityRejected, identityGeneration, generation, reset]);
  const forgetHistory = useCallback((id: string) => {
    if (owner === undefined || protocol.data === undefined) return;
    if (ownerRef.current !== owner || protocolRef.current !== protocol.data) return;
    invalidatePendingSaves(owner, protocol.data);
    bump();
    committed.current = false;
    rowsRef.current = stripSessionHistory(rowsRef.current, id);
    setView((current) => {
      const next = new Set(current.loadingIds);
      next.delete(id);
      return { ...current, rows: rowsRef.current, loadingIds: next };
    });
  }, [owner, protocol.data, bump]);
  useEffect(() => {
    const channel = new BroadcastChannel(SESSION_HISTORY_CHANNEL);
    channel.onmessage = ({ data }: MessageEvent<{ owner: string; protocol: string; id: string }>) => {
      if (data.owner === owner && data.protocol === protocol.data) forgetHistory(data.id);
    };
    return () => channel.close();
  }, [owner, protocol.data, forgetHistory]);
  const visible = owner !== undefined && ownerRef.current === owner && protocolRef.current === protocol.data;
  const value = useMemo(() => ({ ...view, rows: visible ? view.rows : [], invalidateQueries: bump }), [view, visible, bump]);
  return (
    <SidebarInvalidation.Provider value={bump}>
      <Sidebar.Provider value={value}>{children}</Sidebar.Provider>
    </SidebarInvalidation.Provider>
  );
}

type PendingFile = { id: string; file: File };
type ComposerDraft = { text: string; files: PendingFile[]; agent: string; sessionId: string; sending: boolean; busy: boolean; lines: Line[]; parked?: Line[]; consumed?: Set<string>; revision?: string; origin?: ChatOrigin; terminal?: string; historyError?: string; historyRead?: Fiber.Fiber<void>; historyAgain?: boolean; error: string; edit: number; submission: number };

function BottomNavigation({ children }: { children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(false);
  const swipe = useRef({ pointerId: -1, y: 0, completed: false });
  return (
    <footer className="order-last flex shrink-0 flex-col md:border-t px-2 pb-[env(safe-area-inset-bottom)] [--navigation-button:32px] [--navigation-icon:24px] [--navigation-gap:4px] [--navigation-handle:24px] max-lg:[--navigation-button:48px] max-lg:[--navigation-gap:8px] [@media(any-pointer:coarse)]:[--navigation-button:48px] [@media(any-pointer:coarse)]:[--navigation-gap:8px]">
      <div className="relative h-6 shrink-0 max-md:h-4">
      <button type="button" className="flex h-[var(--navigation-handle)] w-full touch-none items-center justify-center rounded-sm focus-visible:outline-2 focus-visible:outline-ring max-md:absolute max-md:left-1/2 max-md:top-1/2 max-md:z-40 max-md:h-11 max-md:w-20 max-md:-translate-x-1/2 max-md:-translate-y-1/2" aria-label={collapsed ? "Show bottom navigation" : "Hide bottom navigation"} title={collapsed ? "Show bottom navigation" : "Hide bottom navigation"} aria-expanded={!collapsed} aria-controls="bottom-navigation"
        onPointerDown={(event) => {
          if (event.pointerType !== "touch" || !event.isPrimary) return;
          swipe.current = { pointerId: event.pointerId, y: event.clientY, completed: false };
          event.currentTarget.setPointerCapture(event.pointerId);
        }}
        onPointerUp={(event) => {
          if (swipe.current.pointerId !== event.pointerId) return;
          swipe.current.pointerId = -1;
          const distance = event.clientY - swipe.current.y;
          swipe.current.completed = Math.abs(distance) >= 32;
          if (swipe.current.completed) setCollapsed(distance > 0);
          event.currentTarget.releasePointerCapture(event.pointerId);
        }}
        onPointerCancel={(event) => { if (swipe.current.pointerId === event.pointerId) { swipe.current.pointerId = -1; swipe.current.completed = false; } }}
        onClick={(event) => {
          if (event.detail > 0 && swipe.current.completed) { swipe.current.completed = false; return; }
          setCollapsed(!collapsed);
        }}>
        <span aria-hidden="true" className="h-1 w-10 rounded-full bg-muted-foreground" />
      </button>
      </div>
      <div id="bottom-navigation" className={cn("min-h-0 grid-cols-[1fr_auto_1fr] items-center gap-[var(--navigation-gap)] py-1 max-md:grid-cols-1", collapsed ? "hidden" : "grid")}>{children}</div>
    </footer>
  );
}

function MobileSidebar({ children, chat }: { children: ReactNode; chat: boolean }) {
  const [open, setOpen] = useState(false);
  const panel = useRef<HTMLDivElement>(null);
  const swipe = useRef<{ x: number; y: number; open: boolean } | null>(null);
  return <div className="flex h-dvh min-h-0 flex-col overflow-hidden overscroll-y-none bg-background"
    onTouchStart={(event) => {
      swipe.current = null;
      const target = event.target as HTMLElement;
      if (window.matchMedia("(min-width: 48rem)").matches || event.touches.length !== 1 || target.closest('input, textarea, select, [contenteditable="true"]')) return;
      const surface = target.closest("[data-sidebar-swipe]");
      if (!surface) return;
      swipe.current = { x: event.touches[0].clientX, y: event.touches[0].clientY, open: surface.getAttribute("data-sidebar-swipe") === "open" };
    }}
    onTouchCancel={() => { swipe.current = null; }}
    onTouchEnd={(event) => {
      const start = swipe.current;
      swipe.current = null;
      if (!start || window.getSelection()?.toString()) return;
      const touch = event.changedTouches.item(0)!;
      const x = touch.clientX - start.x;
      const y = touch.clientY - start.y;
      if (Math.abs(x) < 60 || Math.abs(x) < Math.abs(y) * 1.5 || (x > 0) !== start.open) return;
      event.preventDefault();
      setOpen(start.open);
    }}>
    <Sheet open={open} onOpenChange={setOpen}>
      {children}
      {chat ? <div className="fixed top-2 left-2 z-40 rounded-md bg-background shadow-sm md:hidden">
        <SheetTrigger render={<Button variant="ghost" size="icon-sm" />} aria-label="Sessions"><PanelLeftOpen /></SheetTrigger>
      </div> : null}
      <SheetContent ref={panel} initialFocus={panel} side="left" className="w-72" showCloseButton={false} data-sidebar-swipe="close" onClick={(event) => { if ((event.target as Element).closest('a[href^="/s/"]')) setOpen(false); }}>
        <SheetTitle className="sr-only">Sessions</SheetTitle>
        <SheetDescription className="sr-only">Find and open a conversation.</SheetDescription>
        <SessionList />
      </SheetContent>
    </Sheet>
  </div>;
}

function ResizableAside({ side, className, children, ...props }: ComponentProps<"aside"> & { side: "sidebar" | "delegation" }) {
  const wide = useSyncExternalStore(subscribeWide, () => matchMedia("(min-width: 64rem)").matches);
  const [edge, label, fallback, min, max] = side === "sidebar" ? [1, "Resize sidebar", wide ? 288 : 256, 192, 512] : [-1, "Resize delegation panel", 384, 288, Math.round(innerWidth * 0.6)];
  const clamp = (value: number) => Math.round(Math.min(Math.max(value, min), max));
  const [stored, setStored] = useState(() => Number(localStorage.getItem(`${side}-width`)));
  const width = clamp(stored || fallback);
  const resize = (value: number) => {
    const room = document.querySelector("main")!.getBoundingClientRect().width - 26 * parseFloat(getComputedStyle(document.documentElement).fontSize);
    const next = clamp(Math.min(value, width + room));
    setStored(next);
    localStorage.setItem(`${side}-width`, String(next));
  };
  return <aside {...props} className={cn("relative", className)} style={{ width, minWidth: min }}>
    {children}
    <div role="separator" aria-orientation="vertical" aria-label={label} aria-valuenow={width} aria-valuemin={min} aria-valuemax={max} tabIndex={0}
      className={cn("absolute inset-y-0 z-10 w-1.5 cursor-col-resize touch-none outline-none hover:bg-border focus-visible:bg-ring", edge > 0 ? "-right-0.75" : "-left-0.75")}
      onPointerDown={(event) => event.currentTarget.setPointerCapture(event.pointerId)}
      onPointerMove={(event) => {
        if (!event.currentTarget.hasPointerCapture(event.pointerId)) return;
        const rect = event.currentTarget.parentElement!.getBoundingClientRect();
        resize(edge > 0 ? event.clientX - rect.left : rect.right - event.clientX);
      }}
      onKeyDown={(event) => {
        if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
        event.preventDefault();
        resize(width + (event.key === "ArrowRight" ? 16 : -16) * edge);
      }} />
  </aside>;
}

export function App() {
  const [command, setCommand] = useState<SessionCommand>();
  const composer = useRef<((command: string) => void) | null>(null);
  const commands = useMemo(() => ({ command, setCommand, composer }), [command]);
  const route = useRoute();
  const showChat = ![route.cron, route.agents, route.skills, route.config, route.settled, route.search].some(Boolean);
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [palette, setPalette] = useState<{ mode: "sessions" | "commands" | "cron" | undefined; key: number }>({ mode: undefined, key: 0 });
  const openPalette = useCallback((mode: "sessions" | "commands") => setPalette((current) => ({ mode, key: current.key + 1 })), []);
  const drafts = useRef(new Map<string, ComposerDraft>());
  const [, setDraftVersion] = useState(0);
  const onDraftChange = useCallback(() => setDraftVersion((version) => version + 1), []);
  const [conversation, setConversation] = useState({ id: route.id, created: "", key: 0 });
  const returnTo = conversation.id === "" ? "/" : sessionPath(conversation.id);
  tabReturnTo.current = returnTo;
  const newChat = useCallback(() => {
    drafts.current.delete("");
    setConversation((current) => ({ ...current, created: "", key: current.key + 1 }));
    navigate("/");
  }, []);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat) return;
      if ((event.metaKey || event.ctrlKey) && !event.altKey && event.code === "KeyP") {
        event.preventDefault();
        openPalette(event.shiftKey ? "commands" : "sessions");
        return;
      }
      if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === "b" && !event.shiftKey) {
        event.preventDefault();
        setSidebarOpen((open) => !open);
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.altKey && !event.shiftKey && event.code === "KeyN") {
        event.preventDefault();
        newChat();
        return;
      }
    };
    const onEscape = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || showChat || event.key !== "Escape") return;
      if (document.querySelector('[role="dialog"], [role="listbox"], [role="menu"], [role="tooltip"]')) return;
      event.preventDefault();
      navigate(tabReturnTo.current);
    };
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("keydown", onEscape);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("keydown", onEscape);
    };
  }, [newChat, showChat, openPalette]);
  if (showChat && conversation.id !== route.id) {
    // Creation assigns this conversation its ID; other navigation starts a fresh subtree.
    const created = conversation.id === "" && conversation.created === route.id;
    setConversation({ id: route.id, created: "", key: created ? conversation.key : conversation.key + 1 });
  }
  return (
      <RegistryContext.Provider value={registry}><TooltipProvider>
        <ProtocolGuard />
        <SidebarOwner>
          <SessionCommands value={commands}>
           {command ? <SessionCommandDialog key={`${command.mode}:${command.source}`} command={command} drafts={drafts.current} onDraftChange={onDraftChange} /> : null}
            <CommandPalette key={palette.key} drafts={drafts.current} mode={palette.mode} setMode={(mode) => setPalette((current) => ({ ...current, mode }))} newChat={newChat} sidebarOpen={sidebarOpen} onToggleSidebar={() => setSidebarOpen((open) => !open)} />
         <MobileSidebar chat={showChat}>
            <BottomNavigation>
              <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="hidden size-[var(--navigation-button)] md:inline-flex" />} aria-label={sidebarOpen ? "Hide sidebar" : "Show sidebar"} aria-expanded={sidebarOpen} aria-controls="session-sidebar" onClick={() => setSidebarOpen((open) => !open)}>
                {sidebarOpen ? <PanelLeftClose className="size-[var(--navigation-icon)]" /> : <PanelLeftOpen className="size-[var(--navigation-icon)]" />}
              </TooltipTrigger><TooltipContent side="top">{sidebarOpen ? "Hide sidebar" : "Show sidebar"}</TooltipContent></Tooltip>
              <div className="min-w-0 max-w-full justify-self-center overflow-x-auto overflow-y-hidden scrollbar-none">
                <div className="flex w-max items-center gap-[var(--navigation-gap)]">
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="New session" onClick={newChat}>
                    <SquarePen className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">New session</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="Search sessions" onClick={() => navigate("/search")}>
                    <Search className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">Search sessions</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="Open command palette" onClick={() => openPalette("commands")}>
                    <Command className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">Open command palette</TooltipContent></Tooltip>
                </div>
              </div>
            </BottomNavigation>
            <div className="fixed top-2 right-2 z-40 rounded-md bg-background shadow-sm"><ThemeToggle /></div>
          <div className="flex min-h-0 min-w-0 flex-1">
            <ResizableAside side="sidebar" id="session-sidebar" className={cn("hidden flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground", sidebarOpen && "md:flex")}>
              <SessionList />
            </ResizableAside>
            <main className={cn("flex min-h-0 min-w-0 flex-1 flex-col md:min-w-[26rem]", command?.target && "pt-[min(75dvh,30rem)]")} data-sidebar-swipe="open">
              <WarmTabs cron={route.cron} agents={route.agents} skills={route.skills} config={route.config} />
              {route.settled ? <SessionList settledOnly /> : null}
              {route.search ? <SearchPage /> : null}
              <TabPane show={showChat}>
                <MessageScrollerProvider key={conversation.key} autoScroll scrollEdgeThreshold={48}>
                  <Transcript id={conversation.id} drafts={drafts.current} onDraftChange={onDraftChange} onCreated={(id) => setConversation((current) => ({ ...current, created: id }))} />
                </MessageScrollerProvider>
              </TabPane>
             </main>
             {showChat ? <DelegationPanel id={route.id} /> : null}
           </div>
          </MobileSidebar>
         </SessionCommands>
        </SidebarOwner>
       </TooltipProvider></RegistryContext.Provider>
  );
}

function SessionCommandDialog({ command, drafts, onDraftChange }: { command: SessionCommand; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  if (command.mode === "name" || command.mode === "snooze") return <NameSessionDialog id={command.source} snooze={command.mode === "snooze"} />;
  if (command.mode === "queue") return <SessionQueueDialog id={command.source} />;
  return command.mode === "fork" ? <ForkDialog source={command.source} drafts={drafts} onDraftChange={onDraftChange} /> : <HandoffDialog command={command} drafts={drafts} onDraftChange={onDraftChange} />;
}

function ForkDialog({ source, drafts, onDraftChange }: { source: string; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  const { setCommand } = useContext(SessionCommands);
  const sidebar = useContext(Sidebar);
  const [query, setQuery] = useState("");
  const history = useRemote(queries.history({ id: source }));
  const fork = useAction({ action: (message?: TranscriptEvent) => Effect.gen(function* () {
    const draft = yield* forkDraft(source, message);
    drafts.set(draft.sessionId, draft);
    onDraftChange();
    sidebar.invalidateQueries();
    navigate(sessionPath(draft.sessionId));
    setCommand(undefined);
  }) });
  // OpenCode V2: packages/app/src/session/commands/fork-dialog.tsx lists user
  // messages newest first, forks BEFORE selection, and restores it for editing.
  const items = [{ key: "full", label: "Full session", detail: "Copy all recorded history", choose: () => fork.fire(undefined) }, ...(history.data?.messages ?? []).filter((message) => message.role === "user" && message.messageId && message.text.toLowerCase().includes(query.toLowerCase())).toReversed().map((message) => ({ key: message.messageId!, label: message.text, detail: "Continue before this message", choose: () => fork.fire(message) }))];
  const error = fork.error ?? history.error;
  return <Dialog open onOpenChange={(open) => { if (!open && !fork.isPending) setCommand(undefined); }}>
    <DialogContent className="top-4 translate-y-0 sm:max-w-lg" showCloseButton={!fork.isPending}>
      <DialogTitle>Fork session</DialogTitle>
      <DialogDescription>Choose where to fork. The selected message will be ready to edit in the new session.</DialogDescription>
      {error ? <p role="alert" className="text-destructive">{error.message}</p> : null}
      <SessionCommandPicker items={items} query={query} setQuery={setQuery} forking disabled={fork.isPending || !history.data} />
      {fork.isPending ? <p role="status">Forking session…</p> : null}
    </DialogContent>
  </Dialog>;
}

function HandoffDialog({ command, drafts, onDraftChange }: { command: SessionCommand; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  const { setCommand } = useContext(SessionCommands);
  const popup = useRef<HTMLDivElement>(null);
  const route = useRoute();
  const sidebar = useContext(Sidebar);
  const [query, setQuery] = useState("");
  const search = useRemote(queries.searchMessages(query.trim()), query.trim() !== "");
  const handoffRequest = queries.handoff(command.source);
  const handoff = useRemote(handoffRequest);
  const action = useAction({ action: (kind: "copy" | "new" | "stash") => Effect.gen(function* () {
    if (AsyncResult.isFailure(registry.get(handoffRequest))) registry.refresh(handoffRequest);
    const { document } = yield* AtomRegistry.getResult(registry, handoffRequest, { suspendOnWaiting: true });
    if (kind === "copy") yield* copyText(document, popup.current!);
    if (kind === "new") {
      const id = yield* mutations.createSession({ agent: "main" });
      const optimistic: Line = { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), role: "user", text: document };
      const draft: ComposerDraft = { text: "", files: [], agent: "", sessionId: id, sending: false, busy: true, lines: [optimistic], error: "", edit: 0, submission: 0 };
      drafts.set(id, draft);
      onDraftChange();
      sidebar.invalidateQueries();
      navigate(sessionPath(id));
      Effect.runFork(mutations.prompt({ id, text: document, messageId: optimistic.id }).pipe(Effect.catch((err) => Effect.sync(() => {
        draft.text = [document, draft.text].filter(Boolean).join("\n\n");
        draft.lines = draft.lines.filter((line) => line.id !== optimistic.id);
        draft.error = err.message;
        if (draft.submission === 0) draft.busy = false;
        onDraftChange();
      }))));
    }
    if (kind === "stash") {
      yield* mutations.prompt({ id: command.target!.conversationId, text: document, delivery: "STASH" });
      invalidate("queue");
    }
    setCommand(undefined);
  }) });
  const target = command.target;
  const preview = useRemote(queries.history({ id: target?.conversationId ?? "" }), !!target);
  const items = (query.trim() ? search.data ?? [] : []).map((match) => ({ key: `${match.conversationId}:${match.message.messageId}`, label: match.message.text, session: sidebar.rows.find((row) => row.id === match.conversationId) ?? { id: match.conversationId }, choose: () => {
      setQuery("");
      setCommand({ ...command, target: match });
      navigate(sessionPath(match.conversationId));
    } }));
  const pending = action.isPending;
  const choices = ([{ key: "copy", label: "Copy handoff", progress: "Copying handoff…" }, { key: "new", label: "Start new session", progress: "Starting session…" }] as const).map(({ key, label, progress }) => ({
    key, label: pending && action.variables === key ? progress : label, choose: () => action.fire(key),
  }));
  const error = [action.error, search.error, handoff.error, preview.error].find(Boolean);
  return <Dialog open modal={!target} onOpenChange={(open, details) => { if (!open && !pending && details.reason !== "outside-press") setCommand(undefined); }}>
    <DialogContent ref={popup} initialFocus={popup} className={cn("top-2 flex max-h-[calc(100dvh-1rem)] max-w-[calc(100%-1rem)] translate-y-0 flex-col overflow-hidden sm:max-w-lg", target && "max-h-[calc(min(75dvh,30rem)-1rem)]")} preview={!!target} showCloseButton={!pending}>
      <DialogHeader className="mr-8 shrink-0">
        <DialogTitle>Session handoff</DialogTitle>
        <DialogDescription>{target ? "Review below, then stash. No turn starts until you pop it." : "Copy, start a new session, or search for a session to stash in."}</DialogDescription>
      </DialogHeader>
      <div className="flex min-h-0 flex-col gap-3 overflow-y-auto overscroll-contain">
        <SessionCommandPicker items={[...choices, ...items]} query={query} setQuery={setQuery} forking={false} disabled={pending} />
        {target ? <div className="flex min-w-0 items-center gap-3 rounded-lg border p-2">
          <div className="min-w-0 flex-1"><SessionRowContent session={sidebar.rows.find((row) => row.id === target.conversationId) ?? { id: target.conversationId }} /><p className="truncate text-xs text-muted-foreground">{target.message.text}</p></div>
          <Button variant="ghost" className="min-h-11 shrink-0" disabled={pending} onClick={() => setCommand({ ...command, target: undefined })}>Change</Button>
        </div> : null}
        {error ? <p role="alert" className="break-words text-destructive">{error.message}</p> : null}
        {handoff.isError ? <Button variant="outline" className="min-h-11" onClick={() => void handoff.refetch()}>Retry handoff</Button> : null}
      </div>
      {target && <DialogFooter className="shrink-0 flex-row flex-wrap items-center justify-between sm:justify-between">
        {handoff.data ? <CodeBlock text={handoff.data.document} label="Handoff" compact /> : <p hidden={pending} role="status" className="flex items-center gap-2 text-xs text-muted-foreground">{handoff.isPending ? <><LoaderCircle className="size-4 animate-spin" />Preparing handoff…</> : "Handoff not ready"}</p>}
        <Button className="ml-auto min-h-11" aria-label="Stash handoff here" disabled={[pending, !preview.data, preview.isError, route.id !== target.conversationId].some(Boolean)} onClick={() => action.fire("stash")}>{pending && action.variables === "stash" ? "Stashing…" : "Stash"}</Button>
      </DialogFooter>}
    </DialogContent>
  </Dialog>;
}

const forkDraft = Effect.fnUntraced(function* (source: string, message?: TranscriptEvent): Effect.fn.Return<ComposerDraft, Error> {
  // Restore files before creating a session; a failed download must not leave a fork.
  const files = yield* Effect.forEach(message?.attachments ?? [], (attachment) => downloadAttachment(attachment).pipe(Effect.map((file) => ({ id: crypto.getRandomValues(new Uint32Array(4)).join("-"), file }))), { concurrency: "unbounded" });
  const result = yield* mutations.forkSession({ id: source, before: message?.messageId });
  return { text: result.prompt.text, files, agent: "", sessionId: result.id, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0 };
});

function SessionCommandPicker({ items, query, setQuery, forking, disabled }: { items: { key: string; label: string; detail?: string; session?: Session; choose: () => void }[]; query: string; setQuery: (query: string) => void; forking: boolean; disabled: boolean }) {
  const [pick, setPick] = useState(0);
  const active = useRef<HTMLButtonElement>(null);
  const selected = items.length ? pick % items.length : 0;
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [selected]);
  return <>
    <Input aria-label={forking ? "Search fork messages" : "Search messages"} placeholder="Search messages" value={query} disabled={disabled} onChange={(event) => { setPick(0); setQuery(event.target.value); }} onKeyDown={(event) => setPick(paletteMove(event, selected, items.length, () => items[selected]?.choose()))} />
    <ul className="max-h-[40vh] overflow-y-auto">
      {items.map((item, index) => <li key={item.key}><Button ref={index === selected ? active : null} variant={index === selected ? "secondary" : "ghost"} size="lg" className="min-h-11 h-auto w-full flex-col items-start" disabled={disabled} onClick={item.choose}>{item.session ? <SessionRowContent session={item.session} /> : null}<span aria-live="polite" className="line-clamp-2 text-left whitespace-normal break-words">{item.label}</span>{item.detail ? <span className="max-w-full truncate text-xs">{item.detail}</span> : null}</Button></li>)}
    </ul>
  </>;
}

function paletteRows(
  mode: "sessions" | "commands" | "cron",
  needle: string,
  sidebar: { rows: Session[]; loadingIds: ReadonlySet<string> },
  jobs: { stem: string; status: string; schedule?: string; agent?: string; channel?: string }[] | undefined,
  newChat: () => void,
  sidebarOpen: boolean,
  onToggleSidebar: () => void,
  openCron: () => void,
  runStem: (stem: string) => void,
  origins: ReadonlyMap<string, string>,
  actions: { key: string; label: string; detail?: string; keep?: boolean; disabled?: boolean; run: () => void }[],
  filters: ReturnType<typeof sessionSearchTerms>,
  agentFilter: string,
  roomFilter: string,
): { key: string; label?: string; detail?: string; session?: Session; loading?: boolean; keep?: boolean; disabled?: boolean; run: () => void }[] {
  if (mode === "sessions") {
    return sidebar.rows.filter((session) => sessionMatchesSearch(session, filters, agentFilter, roomFilter, origins.get(session.id) ?? "")).sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned)).map((session) => ({
      key: session.id,
      session, loading: sidebar.loadingIds.has(session.id),
      run: () => navigate(sessionPath(session.id)),
    }));
  }
  if (mode === "cron") {
    return (jobs ?? []).filter((job) => job.status !== "ran" && (needle === "" || `${job.stem} ${job.schedule ?? ""} ${job.agent ?? ""} ${job.channel ?? ""}`.toLowerCase().includes(needle))).map((job) => ({
      key: job.stem, label: job.stem, detail: [job.schedule, job.agent, job.channel].filter(Boolean).join(" · "), keep: true, run: () => runStem(job.stem),
    }));
  }
  return [
    ...actions,
    { key: "new", label: "Session: New session", detail: "", run: newChat },
    { key: "search", label: "Session: Search", detail: "", run: () => navigate("/search") },
    { key: "run-cron", label: "Cron: Run cron", detail: "", keep: true, run: openCron },
    ...(["settled", "cron", "agents", "skills", "config"] as const).map((key) => ({ key, label: `Page: ${key[0].toUpperCase() + key.slice(1)}`, run: () => navigate(`/${key}`) })),
    { key: "sidebar", label: `Sidebar: ${sidebarOpen ? "Hide sidebar" : "Show sidebar"}`, detail: "", run: onToggleSidebar },
  ].filter((item) => needle === "" || item.label.toLowerCase().includes(needle));
}

const pendingCronRuns = new Map<string, string>();
const pendingCronListeners = new Set<() => void>();

function notePendingCron(id: string, stem: string) {
  if (stem === "") pendingCronRuns.delete(id);
  else pendingCronRuns.set(id, stem);
  for (const listener of pendingCronListeners) listener();
}

function usePendingCron(id: string) {
  return useSyncExternalStore(
    (listener) => {
      pendingCronListeners.add(listener);
      return () => pendingCronListeners.delete(listener);
    },
    () => pendingCronRuns.get(id) ?? "",
  );
}

function originSearchText({ origin }: { origin?: ChatOrigin }) {
  if (!origin) return "";
  return (origin.kind === "cron"
    ? `Cron Source: ${origin.sourcePath} Stem: ${origin.stem} Run kind: ${origin.runKind} Run ID: ${origin.runId} Agent: ${origin.agent} Ran at: ${origin.ranAt}`
    : `External MCP External conversation: ${origin.externalConversationId} Agent: ${origin.agent} ${origin.pairs?.map(({ key, value }) => `${key}=${value}`).join(" ") ?? ""}`).toLowerCase();
}

const sessionOrigins = Atom.family((key: string) => {
  const [owner, protocol, ids] = Schema.decodeUnknownSync(Schema.fromJsonString(Schema.Tuple([Schema.NullOr(Schema.String), Schema.NullOr(Schema.String), Schema.Array(Schema.String)])))(key);
  return Atom.make(Stream.suspend(() => {
    const values = new Map<string, string>();
    let failed = false;
    return Stream.fromIterable(Array.from({ length: Math.max(1, Math.ceil(ids.length / 24)) }, (_, index) => index * 24)).pipe(Stream.mapEffect((start) => Effect.gen(function* () {
      yield* Effect.forEach(ids.slice(start, start + 24), (id) => Effect.gen(function* () {
        const atom = queries.origin(owner, protocol, id);
        const result = registry.get(atom);
        if (!result.waiting && (AsyncResult.isFailure(result) || AsyncResult.isSuccess(result) && Date.now() - result.timestamp >= 10_000)) registry.refresh(atom);
        return yield* AtomRegistry.getResult(registry, atom, { suspendOnWaiting: true });
      }).pipe(
        Effect.match({ onSuccess: (view) => { values.set(id, originSearchText(view)); }, onFailure: () => { failed = true; } }),
      ), { concurrency: "unbounded" });
      return { values: new Map(values), failed, complete: start + 24 >= ids.length };
    })));
  })).pipe(Atom.swr({ staleTime: 10_000 }));
});

function useSessionOrigins(rows: Session[], enabled: boolean) {
  const identity = useRemote(queries.identity());
  const protocol = useRemote(queries.protocol());
  const ids = useMemo(() => rows.map(({ id }) => id).toSorted(), [rows]);
  const query = useRemote(sessionOrigins(JSON.stringify([identity.data, protocol.data, ids])), enabled);
  return { values: query.data?.values ?? new Map<string, string>(), pending: enabled && (query.isPending || query.isFetching || !query.data?.complete), failed: !!query.data?.failed || query.isError };
}

const paletteCopy = { sessions: { title: "Go to session", desc: "Search and open a session.", placeholder: "Search sessions" }, commands: { title: "Run command", desc: "Search and run a command.", placeholder: "Type a command" },
  cron: { title: "Run cron", desc: "Search and run a cron job.", placeholder: "Search cron jobs" } };

function CommandPalette({ drafts, mode, setMode, newChat, sidebarOpen, onToggleSidebar }: { drafts: Map<string, ComposerDraft>; mode: "sessions" | "commands" | "cron" | undefined; setMode: (mode: "sessions" | "commands" | "cron" | undefined) => void; newChat: () => void; sidebarOpen: boolean; onToggleSidebar: () => void }) {
  const sidebar = useContext(Sidebar);
  const { setCommand, composer } = useContext(SessionCommands);
  const { id } = useRoute();
  const actions = useSessionActions(sidebar.rows.find((row) => row.id === id), () => setMode(undefined));
  const choices = useRemote(queries.agents({ conversationId: id }), id !== "");
  const draft = drafts.get(id);
  const commands = id ? dollarCommands.filter(({ name }) => name !== "cron" && (name !== "stop" || (draft?.busy ?? sidebar.rows.find((row) => row.id === id)?.running)) && (name !== "agent" || !!choices.data?.agents.length)).map(({ name, label }) => ({ key: name, label: `${["fork", "handoff", "queue", "stop", "agent"].includes(name) ? "Session" : "Command"}: ${label}`, disabled: !draft || draft.sending, run: () => {
    if (name === "fork" || name === "handoff" || name === "queue") setCommand({ mode: name, source: id });
    else composer.current!(name);
  } })) : [];
  const [query, setQuery] = useState("");
  const [pick, setPick] = useState(0);
  const [agentFilter, setAgentFilter] = useState("");
  const [roomFilter, setRoomFilter] = useState("");
  const input = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => {
    const viewport = window.visualViewport;
    if (!viewport || mode === undefined || !window.matchMedia("(pointer: coarse)").matches) return;
    const style = document.documentElement.style;
    const resize = () => {
      style.setProperty("--search-top", `${viewport.offsetTop + 16}px`);
      style.setProperty("--search-height", `${viewport.height - 32}px`);
    };
    resize();
    viewport.addEventListener("resize", resize);
    viewport.addEventListener("scroll", resize);
    return () => {
      viewport.removeEventListener("resize", resize);
      viewport.removeEventListener("scroll", resize);
      style.removeProperty("--search-top");
      style.removeProperty("--search-height");
    };
  }, [mode]);
  useLayoutEffect(() => { input.current?.focus(); }, []);
  const agents = useRemote(queries.agents(), mode === "sessions");
  const filters = sessionSearchTerms(query);
  const active = useRef<HTMLButtonElement>(null);
  const origins = useSessionOrigins(sidebar.rows, mode === "sessions" && filters.needle !== "");
  const jobs = useRemote(queries.cronJobs(), mode === "commands" || mode === "cron");
  const runCron = useAction({
    action: mutations.runCron,
    onSuccess: (id, input) => {
      invalidate("cronJobs");
      sidebar.invalidateQueries();
      if (id === "") return;
      notePendingCron(id, input.stem);
      navigate(sessionPath(id));
      setMode(undefined);
    },
  });
  const items = mode === undefined ? [] : paletteRows(mode, query.trim().toLowerCase(), sidebar, jobs.data, newChat, sidebarOpen, onToggleSidebar, () => { setQuery(""); setPick(0); setMode("cron"); }, (stem) => runCron.fire({ stem }), origins.values, [...actions.items.map((item) => ({ ...item, label: `Session: ${item.label}` })), ...commands], filters, agentFilter, roomFilter);
  const selected = items.length === 0 ? 0 : pick % items.length;
  const choose = (item: (typeof items)[number]) => {
    if (item.disabled) return;
    if (!item.keep) setMode(undefined);
    item.run();
  };
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [selected, mode]);
  const copy = paletteCopy[mode ?? "sessions"];
  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => setPick(paletteMove(event, selected, items.length, () => { if (items[selected]) choose(items[selected]); }));
  return (
    <Dialog open={mode !== undefined} onOpenChange={(open) => { if (!open) setMode(undefined); }}>
      <DialogContent initialFocus={input} showCloseButton={false} className="top-[20%] flex max-h-[75vh] translate-y-0 flex-col overflow-hidden sm:max-w-lg [@media(pointer:coarse)]:top-[var(--search-top,1rem)] [@media(pointer:coarse)]:max-h-[var(--search-height,75vh)]">
        <DialogTitle className="sr-only">{copy.title}</DialogTitle>
        <DialogDescription className="sr-only">{copy.desc}</DialogDescription>
        {mode === "sessions" ? <SessionSearch rows={sidebar.rows} catalog={agents.data?.agents ?? []} query={query} setQuery={(value) => { setPick(0); setQuery(value); }} agentFilter={agentFilter} setAgentFilter={(value) => { setPick(0); setAgentFilter(value); }} roomFilter={roomFilter} setRoomFilter={(value) => { setPick(0); setRoomFilter(value); }} inputRef={input} placeholder={copy.placeholder} onKeyDown={onKeyDown} /> : <Input
          ref={input}
          value={query}
          onChange={(event) => { setPick(0); setQuery(event.target.value); }}
          placeholder={copy.placeholder}
          variant="embedded"
          onKeyDown={onKeyDown}
        />}
        {runCron.error || actions.error ? <p role="alert" className="px-3 text-sm text-destructive">{(runCron.error ?? actions.error)?.message}</p> : null}
        {origins.failed ? <p role="alert" className="px-3 text-sm text-destructive">Could not search all chat origins.</p> : null}
        <ul className="max-h-[min(24rem,50vh)] overflow-y-auto p-1 [scrollbar-width:thin] [scrollbar-color:var(--muted-foreground)_transparent]">
          {items.length === 0 ? <li className="px-3 py-2 text-sm text-muted-foreground">{paletteEmpty(mode, sidebar, origins.pending, origins.failed, jobs.isLoading)}</li> : items.map((item, index) => (
            <li key={item.key}>
              <button disabled={item.disabled} ref={index === selected ? active : null} type="button" className={cn("flex w-full flex-col items-start justify-center rounded-md px-3 text-left text-sm disabled:opacity-50", mode === "commands" ? "min-h-9 py-1.5 [@media(pointer:coarse)]:min-h-11" : "py-2", index === selected && "bg-accent")} onMouseDown={(event) => event.preventDefault()} onClick={() => choose(item)}>
                {item.session ? <SessionRowContent session={item.session} loading={item.loading} /> : <><span className="font-medium">{item.label}</span>{item.detail ? <span className="text-xs text-muted-foreground">{item.detail}</span> : null}</>}
              </button>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  );
}

function paletteEmpty(mode: "sessions" | "commands" | "cron" | undefined, sidebar: SidebarView, originsPending: boolean, originsFailed: boolean, loadingJobs: boolean) {
  if (mode === "sessions" && originsFailed) return "Search incomplete";
  if (mode === "sessions" && (!searchIsAuthoritative(sidebar) || originsPending)) return "loading...";
  return mode === "cron" && loadingJobs ? "Loading…" : "No matches";
}

function paletteMove(event: { key: string; preventDefault: () => void }, selected: number, count: number, enter: () => void) {
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    return selected + (event.key === "ArrowDown" ? 1 : Math.max(count, 1) - 1);
  }
  if (event.key === "Enter") {
    event.preventDefault();
    enter();
  }
  return selected;
}

function relativeTime(iso: string) {
  if (iso === "") return "";
  const ms = Date.now() - Date.parse(iso);
  if (!Number.isFinite(ms) || ms < 60_000) return "now";
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`;
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h`;
  return `${Math.floor(ms / 86_400_000)}d`;
}

function SessionRowContent({ session, loading = false, age = relativeTime(session.updatedAt ?? ""), channelOnly = false }: { session: Session; loading?: boolean; age?: string; channelOnly?: boolean }) {
  const title = session.name || rowPreview(session, loading).split("\n", 1)[0] || sessionLabel(session.id);
  const channel = slackSession(session.id) ? (session.title ?? "") : "";
  const meta = channelOnly ? channel : [session.snoozedUntil ? `Snoozed until ${new Date(session.snoozedUntil).toLocaleString()}` : session.settled ? "Settled" : "", channel, session.agent].filter(Boolean).join(" · ");
  const updated = session.updatedAt ? `Updated ${new Date(session.updatedAt).toLocaleString(undefined, { timeZoneName: "short" })}` : "";
  return <span className="flex min-w-0 w-full flex-1 flex-col gap-0.5">
    <span className="flex items-center gap-1.5 text-sm font-medium">{session.forkedFrom ? <GitFork role="img" aria-label="Forked session" className="size-3.5 shrink-0" /> : null}<span data-slot="session-title" className="truncate">{title}</span></span>
    <span className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"><span className="min-w-0 flex-1 truncate" title={meta}>{meta}</span>{session.running ? <LoaderCircle role="img" aria-label="Turn running" className="size-3 shrink-0 animate-spin motion-reduce:animate-none" /> : null}{age ? <Tooltip><TooltipTrigger render={<time dateTime={session.updatedAt} />} aria-label={updated} className="shrink-0 tabular-nums">{age}</TooltipTrigger><TooltipContent>{updated}</TooltipContent></Tooltip> : null}</span>
  </span>;
}

function useSessionActions(session: Session | undefined, onSuccess?: () => void) {
  const invalidate = useContext(SidebarInvalidation);
  const { setCommand } = useContext(SessionCommands);
  const saved = () => { invalidate(); onSuccess?.(); };
  const update = useAction({ action: mutations.updateSession, onSuccess: saved });
  const settle = useAction({ action: mutations.settleSession, onSuccess: saved });
  const id = session?.id ?? "";
  const items = session ? [
    ...(session.forkedFrom ? [{ key: "origin", label: "Open original conversation", icon: CornerUpLeft, run: () => navigate(sessionPath(session.forkedFrom!)) }] : []),
    { key: "name", label: "Name session", icon: TextCursorInput, run: () => setCommand({ mode: "name", source: id }) },
    { key: "pin", label: session.pinned ? "Unpin session" : "Pin session", icon: Pin, pressed: !!session.pinned, keep: true, run: () => update.fire({ id, pinned: !session.pinned }) },
    { key: "snooze", label: "Snooze session", icon: Clock, run: () => setCommand({ mode: "snooze", source: id }) },
    { key: "settle", label: session.settled ? "Unsettle" : "Settle", icon: session.settled ? Undo2 : Check, keep: true, run: () => settle.fire({ id, settled: !session.settled }) },
  ].map((item) => ({ ...item, disabled: update.isPending || settle.isPending })) : [];
  return { items, error: update.error ?? settle.error };
}

function SessionActions({ session }: { session?: Session }) {
  const { items, error } = useSessionActions(session);
  return <>{items.filter((item) => ["name", "pin", "snooze"].includes(item.key)).map(({ key, label, icon: Icon, pressed, disabled, run }) => <Tooltip key={key}>
    <TooltipTrigger render={<Button variant="ghost" size="icon" className="size-11 sm:size-8" disabled={disabled} />} aria-label={label} aria-pressed={pressed} onClick={run}><Icon className={cn(pressed && "fill-current")} /></TooltipTrigger><TooltipContent>{label}</TooltipContent>
  </Tooltip>)}{error ? <span role="alert" className="text-xs text-destructive">{error.message}</span> : null}</>;
}

function SessionHeaderActions({ id }: { id: string }) {
  const sidebar = useContext(Sidebar);
  return <SessionActions session={sidebar.rows.find((row) => row.id === id)} />;
}

function SessionRowActions({ session }: { session: Session }) {
  const { items, error } = useSessionActions(session);
  return <><ButtonGroup aria-label="Session controls">
    {items.filter((item) => item.key === "settle" && !session.settled).map(({ key, label, icon: Icon, disabled, run }) => <Tooltip key={key}>
      <TooltipTrigger render={<Button variant="ghost" size="icon-sm" disabled={disabled} />} aria-label={label} onClick={run}><Icon /></TooltipTrigger><TooltipContent>{label}</TooltipContent>
    </Tooltip>)}
    <Menu.Root>
      <Menu.Trigger render={<Button variant="ghost" size="icon-sm" />} aria-label="Session actions"><Ellipsis /></Menu.Trigger>
      <Menu.Portal><Menu.Positioner sideOffset={4} align="end" className="z-50 outline-none"><Menu.Popup onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className="min-w-40 rounded-md border bg-popover p-1 text-popover-foreground shadow-md outline-none">
        {items.filter((item) => item.key !== "settle" || session.settled).map(({ key, label, icon: Icon, pressed, disabled, run }) => <Menu.Item key={key} disabled={disabled} onClick={run} className="flex cursor-default items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none data-highlighted:bg-accent data-disabled:opacity-50">
          <Icon className={cn("size-4", pressed && "fill-current")} />{label}
        </Menu.Item>)}
      </Menu.Popup></Menu.Positioner></Menu.Portal>
    </Menu.Root>
  </ButtonGroup>{error ? <span role="alert" className="px-2 text-xs text-destructive">{error.message}</span> : null}</>;
}

function SessionQueueDialog({ id }: { id: string }) {
  const { setCommand } = useContext(SessionCommands);
  const queue = useRemote(queries.queue({ id }));
  return <Dialog open onOpenChange={(open) => { if (!open) setCommand(undefined); }}><DialogContent>
    <DialogTitle>Session queue</DialogTitle><DialogDescription>Pending steers and later work. Viewing this list starts no turn.</DialogDescription>
    {queue.error ? <p role="alert">{queue.error.message}</p> : queue.isPending ? <p role="status">Loading…</p> : <ul className="max-h-[50dvh] overflow-y-auto">{!queue.data!.length ? <li>No pending work</li> : null}{queue.data!.map((item) => <li key={item.id} className="border-b py-2"><p className="text-xs text-muted-foreground">{item.delivery === "STASH" ? "Stashed" : item.delivery === "STEER" ? "Pending steer" : "Queued"}</p><p className="whitespace-pre-wrap break-words">{item.text}</p><MessageAttachments attachments={item.attachments} conversationId={id} /></li>)}</ul>}
  </DialogContent></Dialog>;
}

function NameSessionDialog({ id, snooze }: { id: string; snooze: boolean }) {
  const sidebar = useContext(Sidebar);
  const session = sidebar.rows.find((row) => row.id === id);
  const { setCommand } = useContext(SessionCommands);
  const [value, setValue] = useState(snooze ? "" : session?.name ?? "");
  const nameId = useId();
  const update = useAction({ action: mutations.updateSession, onSuccess: () => { sidebar.invalidateQueries(); setCommand(undefined); } });
  return <Dialog open onOpenChange={(open) => { if (!open) setCommand(undefined); }}>
        <DialogContent>
          <DialogTitle>{snooze ? "Snooze session" : "Name session"}</DialogTitle>
          <DialogDescription>{snooze ? "Hide until this local time. New messages bring the chat back early. Find it under Settled to Unsettle sooner." : "Shared with everyone who can see this session. Leave blank to show the last message."}</DialogDescription>
          <form className="mt-4 flex flex-col gap-3" action={() => update.fire({ id, ...(snooze ? { snoozedUntil: new Date(value).toISOString() } : { name: value }) })}>
            <FieldGroup>
              <Field data-invalid={!!update.error}>
                <FieldLabel htmlFor={nameId}>{snooze ? "Return at (local time)" : "Session name"}</FieldLabel>
                <Input id={nameId} name={snooze ? "snoozedUntil" : "name"} type={snooze ? "datetime-local" : "text"} required={snooze} value={value} aria-invalid={!!update.error} onChange={(event) => setValue(event.target.value)} />
                {update.error ? <FieldError>{update.error.message}</FieldError> : null}
              </Field>
            </FieldGroup>
            <div className="flex justify-end gap-2">
              <DialogClose render={<Button variant="ghost" />}>Cancel</DialogClose>
              <Button type="submit" disabled={update.isPending}>{update.isPending ? "Saving…" : snooze ? "Snooze" : "Save"}</Button>
            </div>
          </form>
        </DialogContent>
    </Dialog>;
}

function matchesSession(session: Session, needle: string, agentFilter: string, roomFilter: string) {
  if (agentFilter !== "" && (session.agent ?? "") !== agentFilter) return false;
  if (roomFilter !== "" && (slackSession(session.id) ? (session.title ?? "") : "") !== roomFilter) return false;
  return needle === "" || `${session.name ?? ""} ${session.title ?? ""} ${session.preview ?? ""} ${session.agent ?? ""} ${sessionLabel(session.id)}`.toLowerCase().includes(needle);
}

function sessionSearchTerms(query: string) {
  const pinnedOnly = /(?:^|\s)is:pinned(?=\s|$)/i.test(query);
  const forkedOnly = /(?:^|\s)is:forked(?=\s|$)/i.test(query);
  const text = query.replace(/(?:^|\s)is:(?:settled|pinned|forked)(?=\s|$)/gi, " ").trim();
  const needle = typedPrefix(text, "agent:") === null && typedPrefix(text, "room:") === null ? text.toLowerCase() : "";
  return { pinnedOnly, forkedOnly, text, needle };
}

function sessionMatchesSearch(session: Session, filters: ReturnType<typeof sessionSearchTerms>, agentFilter: string, roomFilter: string, origin: string) {
  return (!filters.pinnedOnly || session.pinned) && (!filters.forkedOnly || session.forkedFrom) && matchesSession(session, "", agentFilter, roomFilter) && (matchesSession(session, filters.needle, "", "") || origin.includes(filters.needle));
}

type SavedSearch = { id: string; name?: string; query: string; agentFilter: string; roomFilter: string };
type SavedSearches = { tabs: SavedSearch[]; active: string };

function SearchPage() {
  const identity = useRemote(queries.identity());
  if (!identity.isSuccess) return null;
  return <SearchTabs key={identity.data} owner={identity.data!} />;
}

function SearchTabs({ owner }: { owner: string }) {
  const storageKey = `search-tabs:${owner}`;
  const [rename, setRename] = useState<string | null>(null);
  const [saved, setSaved] = useState<SavedSearches>(() => {
    const stored = localStorage.getItem(storageKey);
    if (stored) return JSON.parse(stored) as SavedSearches;
    const id = crypto.getRandomValues(new Uint32Array(4)).join("-");
    return { tabs: [{ id, query: "", agentFilter: "", roomFilter: "" }], active: id };
  });
  const draft = useRef(localStorage.getItem(storageKey) === null);
  const savedRef = useRef(saved);
  const focusAfterClose = useRef(false);
  const activeTab = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => { input.current?.focus(); }, []);
  const sidebar = useContext(Sidebar);
  const agents = useRemote(queries.agents());
  const selected = saved.tabs.findIndex((tab) => tab.id === saved.active);
  const tab = saved.tabs[selected];
  useLayoutEffect(() => {
    if (rename !== null) { activeTab.current!.focus(); window.getSelection()?.selectAllChildren(activeTab.current!); }
    if (focusAfterClose.current) {
      activeTab.current?.focus();
      focusAfterClose.current = false;
    }
  }, [saved, rename]);
  const update = (next: SavedSearches) => {
    savedRef.current = next;
    setSaved(next);
    draft.current = false;
    localStorage.setItem(storageKey, JSON.stringify(next));
  };
  const edit = (change: Partial<SavedSearch>) => update({ ...savedRef.current, tabs: savedRef.current.tabs.map((item) => item.id === tab.id ? { ...item, ...change } : item) });
  const select = (active: string) => {
    const next = { ...savedRef.current, active };
    savedRef.current = next;
    setSaved(next);
    if (!draft.current) localStorage.setItem(storageKey, JSON.stringify(next));
  };
  const close = (index: number) => {
    const tabs = saved.tabs.filter((_, position) => position !== index);
    if (!tabs.length) {
      localStorage.removeItem(storageKey);
      const last = localStorage.getItem(`last-seen:${owner}`);
      navigate(last ?? "/");
      return;
    }
    focusAfterClose.current = true;
    update({ tabs, active: saved.active === saved.tabs[index].id ? tabs[Math.min(index, tabs.length - 1)].id : saved.active });
  };
  return <section aria-label="Saved searches" className="flex min-h-0 min-w-0 flex-1 flex-col gap-4 p-4 sm:p-6">
    <div className="flex min-w-0 items-center gap-2">
      <div role="tablist" aria-label="Searches" className="flex min-w-0 gap-1 overflow-x-auto">
        {saved.tabs.map((item, index) => <div key={item.id} className="flex shrink-0 items-center rounded-md border border-sidebar-border bg-background">
          <div role={item.id === saved.active && rename !== null ? "textbox" : "tab"} aria-label={item.id === saved.active && rename !== null ? "Search name" : undefined} contentEditable={item.id === saved.active && rename !== null ? "plaintext-only" : false} suppressContentEditableWarning ref={item.id === saved.active ? activeTab : null} tabIndex={item.id === saved.active ? 0 : -1} aria-selected={rename === null || item.id !== saved.active ? item.id === saved.active : undefined} aria-controls="search-tab-panel" className={cn("max-w-44 truncate rounded-l-md px-3 py-2 text-sm", item.id === saved.active && "bg-sidebar-row-active", item.id === saved.active && rename !== null ? "cursor-text" : "cursor-pointer")} onClick={() => { if (item.id !== saved.active) select(item.id); else if (rename === null) setRename(item.name || item.query || `Search ${index + 1}`); }} onBlur={(event) => {
            if (item.id !== saved.active || rename === null) return;
            const name = event.currentTarget.textContent!.trim();
            event.currentTarget.textContent = name || item.query || `Search ${index + 1}`;
            edit({ name }); setRename(null);
          }} onKeyDown={(event) => {
            if (item.id === saved.active && rename !== null) {
              if (event.nativeEvent.isComposing) return;
              if (event.key === "Enter" || event.key === "Escape") {
                event.preventDefault(); event.stopPropagation();
                if (event.key === "Enter") { focusAfterClose.current = true; event.currentTarget.blur(); }
                else { event.currentTarget.textContent = rename; setRename(null); }
              }
              return;
            }
            if (event.key === "Enter" || event.key === " ") {
              event.preventDefault();
              if (item.id === saved.active) setRename(item.name || item.query || `Search ${index + 1}`); else select(item.id);
              return;
            }
            const position = event.key === "ArrowRight" ? (index + 1) % saved.tabs.length : event.key === "ArrowLeft" ? (index + saved.tabs.length - 1) % saved.tabs.length : event.key === "Home" ? 0 : event.key === "End" ? saved.tabs.length - 1 : -1;
            if (position < 0) return;
            event.preventDefault();
            select(saved.tabs[position].id);
            event.currentTarget.parentElement?.parentElement?.querySelectorAll<HTMLElement>('[role="tab"]')[position]?.focus();
          }}>{item.name || item.query || `Search ${index + 1}`}</div>
          <button type="button" aria-label={`Close search ${index + 1}`} className="rounded-r-md px-2 py-2 text-muted-foreground hover:text-foreground" onClick={() => close(index)}><X className="size-4" /></button>
        </div>)}
      </div>
      <Button variant="outline" size="icon" aria-label="New search" onClick={() => {
        const id = crypto.getRandomValues(new Uint32Array(4)).join("-");
        update({ tabs: [...saved.tabs, { id, query: "", agentFilter: "", roomFilter: "" }], active: id });
        requestAnimationFrame(() => input.current?.focus());
      }}><Plus className="size-4" /></Button>
    </div>
    <SearchResults key={tab.id} tab={tab} rows={sidebar.rows} catalog={agents.data?.agents ?? []} edit={edit} input={input} onFirstSubmit={() => { if (draft.current) update(savedRef.current); }} />
  </section>;
}

function MatchedExcerpt({ text, needle }: { text: string; needle: string }) {
  const lower = text.toLowerCase(), index = needle ? lower.indexOf(needle) : -1;
  const start = index < 0 ? 0 : Math.max(0, index - 48);
  const end = index < 0 ? text.length : Math.min(text.length, index + needle.length + 48);
  const parts: ReactNode[] = [];
  for (let position = start; position < end;) {
    const match = needle ? lower.indexOf(needle, position) : -1;
    const next = match < 0 || match >= end ? end : match;
    parts.push(text.slice(position, next));
    if (next === end) break;
    parts.push(<mark key={match} className="rounded-sm bg-primary/20 text-foreground ring-1 ring-primary/30">{text.slice(match, match + needle.length)}</mark>);
    position = match + needle.length;
  }
  return <span className="min-w-0 whitespace-pre-wrap break-words">{start ? "…" : ""}{parts}{end < text.length ? "…" : ""}</span>;
}

function SearchMatches({ matching, messages, rows, origins, needle }: { matching: Session[]; messages: MessageMatch[]; rows: Session[]; origins: string[]; needle: string }) {
  const bySession = Map.groupBy(messages, (match) => match.conversationId);
  const matched = new Set(matching);
  const sessions = rows.filter((session) => bySession.has(session.id) || matched.has(session)).sort((a, b) => Number(bySession.has(b.id)) - Number(bySession.has(a.id)) || Number(!!b.pinned) - Number(!!a.pinned));
  return <ul className="flex min-w-0 flex-col gap-4">
    {sessions.map((session) => {
      const origin = origins[rows.findIndex((row) => row.id === session.id)] ?? "";
      const hits = bySession.get(session.id) ?? [];
      const label = session.name || rowPreview(session, false).split("\n", 1)[0] || sessionLabel(session.id);
      const field = needle ? [["Name", session.name], ["Room", session.title], ["Agent", session.agent], ["Session", sessionLabel(session.id)], ["Origin", origin]].find(([, text]) => text?.toLowerCase().includes(needle)) : undefined;
      const text = field?.[1] || (!hits.length && matched.has(session) ? session.preview || sessionLabel(session.id) : "");
      const count = hits.length + Number(!!text);
      return <li key={session.id} role="group" aria-label={label} className="min-w-0">
        <h2 className="flex min-w-0 items-center gap-1 text-sm font-medium"><Link href={sessionPath(session.id)} title={label} className="min-w-0 truncate py-1 hover:underline focus-visible:outline-2 focus-visible:outline-ring">{label}</Link><span className="shrink-0 text-muted-foreground">({count})</span>{session.pinned ? <Pin aria-label="Pinned" className="size-3 shrink-0" /> : null}</h2>
        <ul className="mt-1 border-l pl-2 font-mono text-sm leading-5">
          {text ? <li><Link href={sessionPath(session.id)} className="flex min-h-11 min-w-0 items-start gap-3 px-2 py-1 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring sm:min-h-7"><span className="w-16 shrink-0 text-xs text-muted-foreground">{field?.[0] || "Preview"}</span><MatchedExcerpt text={text} needle={needle} /></Link></li> : null}
          {hits.map((match) => <li key={match.message.messageId}><Link href={`${sessionPath(session.id)}?message=${encodeURIComponent(match.message.messageId!)}`} className="flex min-h-11 min-w-0 items-start gap-3 px-2 py-1 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring sm:min-h-7"><span className="w-16 shrink-0 text-xs text-muted-foreground">{match.message.role === "user" ? "You" : "Assistant"}</span><MatchedExcerpt text={match.message.text} needle={needle} /></Link></li>)}
        </ul>
      </li>;
    })}
  </ul>;
}

function SearchStatus({ pending, error, originError, empty, incomplete, retry }: { pending: boolean; error?: string; originError: boolean; empty: boolean; incomplete: boolean; retry: () => void }) {
  return <>
    {pending ? <p role="status" className="text-muted-foreground">Searching…</p> : null}
    {error ? <p role="alert" className="text-destructive">Search failed: {error} <button type="button" className="underline" onClick={retry}>Retry</button></p> : null}
    {originError ? <p role="alert" className="text-destructive">Some chat origins could not be searched.</p> : null}
    {empty ? <p role="status" className="text-muted-foreground">{incomplete ? "Session search is still loading." : "No matches"}</p> : null}
  </>;
}

function SearchResults({ tab, rows, catalog, edit, input, onFirstSubmit }: { tab: SavedSearch; rows: Session[]; catalog: { name: string }[]; edit: (change: Partial<SavedSearch>) => void; input: React.Ref<HTMLInputElement>; onFirstSubmit: () => void }) {
  const sidebar = useContext(Sidebar);
  const filters = sessionSearchTerms(tab.query);
  const searchKey = filters.needle;
  const origins = useSessionOrigins(rows, !!filters.needle);
  const [result, setResult] = useState<{ query: string; matches: MessageMatch[]; error?: string; pending: boolean }>({ query: "", matches: [], pending: false });
  const request = useRef<Fiber.Fiber<void>>(null);
  const pause = useRef<Fiber.Fiber<void>>(null);
  const firstEdit = useRef(0);
  const submit = useCallback(() => {
    if (pause.current) Effect.runFork(Fiber.interrupt(pause.current));
    firstEdit.current = 0;
    if (request.current) Effect.runFork(Fiber.interrupt(request.current));
    setResult({ query: searchKey, matches: [], pending: !!searchKey });
    if (!searchKey) return;
    request.current = Effect.runFork(requests.searchMessages(searchKey).pipe(Effect.match({
      onSuccess: (matches) => setResult({ query: searchKey, matches, pending: false }),
      onFailure: (error) => setResult({ query: searchKey, matches: [], error: error.message, pending: false }),
    })));
  }, [searchKey]);
  useEffect(() => {
    if (!firstEdit.current) firstEdit.current = Date.now();
    const timer = Effect.runFork(Effect.sleep(Math.min(250, Math.max(0, 1000 - (Date.now() - firstEdit.current)))).pipe(Effect.map(() => { pause.current = null; submit(); })));
    pause.current = timer;
    return () => { Effect.runFork(Fiber.interrupt(timer)); };
  }, [submit]);
  useEffect(() => () => { if (request.current) Effect.runFork(Fiber.interrupt(request.current)); }, []);
  const pending = result.pending || result.query !== searchKey || origins.pending;
  const originError = origins.failed;
  const current = !pending && !result.error;
  const matching = rows.filter((row) => sessionMatchesSearch(row, filters, tab.agentFilter, tab.roomFilter, origins.values.get(row.id) ?? ""));
  const visible = new Set(rows.filter((row) => sessionMatchesSearch(row, { ...filters, needle: "" }, tab.agentFilter, tab.roomFilter, "")).map((row) => row.id));
  const messages = result.matches.filter((match) => visible.has(match.conversationId));
  const searching = !!tab.query.trim() || !!tab.agentFilter || !!tab.roomFilter;
  return <>
    <div id="search-tab-panel" role="tabpanel" aria-label="Search" className="min-w-0">
      <SessionSearch rows={rows} catalog={catalog} query={tab.query} setQuery={(query) => edit({ query })} agentFilter={tab.agentFilter} setAgentFilter={(agentFilter) => edit({ agentFilter })} roomFilter={tab.roomFilter} setRoomFilter={(roomFilter) => edit({ roomFilter })} inputRef={input} onKeyDown={(event) => { if (event.key === "Enter") { onFirstSubmit(); submit(); } }} />
    </div>
    <div className="min-h-0 flex-1 overflow-y-auto text-sm" aria-label="Search results">
      <SearchStatus pending={pending} error={result.query === searchKey ? result.error : undefined} originError={originError} empty={searching && current && matching.length + messages.length === 0} incomplete={!searchIsAuthoritative(sidebar) || originError} retry={submit} />
      {current && searching ? <SearchMatches matching={matching} messages={messages} rows={rows} origins={rows.map((row) => origins.values.get(row.id) ?? "")} needle={filters.needle} /> : null}
      {!searching ? <p className="text-muted-foreground">Type to search messages and conversations.</p> : null}
    </div>
  </>;
}

function SessionSearch({ rows, catalog, query, setQuery, agentFilter, setAgentFilter, roomFilter, setRoomFilter, inputRef, placeholder, onKeyDown }: {
  rows: Session[];
  catalog: { name: string }[];
  query: string;
  setQuery: (value: string) => void;
  agentFilter: string;
  setAgentFilter: (value: string) => void;
  roomFilter: string;
  setRoomFilter: (value: string) => void;
  inputRef?: React.Ref<HTMLInputElement>;
  placeholder?: string;
  onKeyDown?: React.KeyboardEventHandler<HTMLInputElement>;
}) {
  const sidebar = useContext(Sidebar);
  const stale = !sidebar.refreshing && rows.length > 0 && !searchIsAuthoritative(sidebar);
  const [overlayPick, setOverlayPick] = useState(0);
  const { text } = sessionSearchTerms(query);
  const agentPrefix = typedPrefix(text, "agent:");
  const roomPrefix = typedPrefix(text, "room:");
  const choices = overlayChoices(agentPrefix, roomPrefix, catalog, roomPrefix === null ? [] : slackRooms(rows));
  const pick = choices.length === 0 ? 0 : overlayPick % choices.length;
  const active = useRef<HTMLButtonElement>(null);
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [pick, text]);
  const applyOverlay = (name: string) => {
    if (agentPrefix !== null) {
      setAgentFilter(name);
    } else {
      setRoomFilter(name);
    }
    setQuery(query.split(/\s+/).filter((term) => /^is:(settled|pinned|forked)$/i.test(term)).join(" "));
    setOverlayPick(0);
  };
  return (
        <div className="min-w-0 flex-1 shrink-0">
          <div className="flex min-h-8 min-w-0 flex-wrap items-center gap-1 rounded-md border border-sidebar-border bg-background pr-2 pl-2">
            <span className="flex size-3.5 shrink-0 items-center text-muted-foreground">
              {stale ? <Tooltip>
                <TooltipTrigger render={<span />} tabIndex={0} role="img" aria-label="Conversation list may be out of date">
                  <CircleAlert className="size-3.5" />
                </TooltipTrigger>
                <TooltipContent side="bottom">Conversation list may be out of date</TooltipContent>
              </Tooltip> : <Search className="size-3.5" />}
            </span>
            {agentFilter ? <FilterPill label={`agent:${agentFilter}`} onClear={() => setAgentFilter("")} /> : null}
            {roomFilter ? <FilterPill label={`room:${roomFilter}`} onClear={() => setRoomFilter("")} /> : null}
            <Input
              ref={inputRef}
              value={query}
              onChange={(event) => {
                setOverlayPick(0);
                setQuery(event.target.value);
              }}
              aria-label={placeholder ?? "Search sessions"}
              placeholder={placeholder ?? (agentFilter || roomFilter ? "Search" : "Search or agent: or room:")}
              variant="embedded"
              className="h-8 min-w-24 flex-1"
              onKeyDown={(event) => {
                if (choices.length > 0) {
                  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                    event.preventDefault();
                    setOverlayPick(pick + (event.key === "ArrowDown" ? 1 : choices.length - 1));
                    return;
                  }
                  if (event.key === "Tab" || event.key === "Enter") {
                    event.preventDefault();
                    applyOverlay(choices[pick].key);
                    return;
                  }
                  if (event.key === "Escape") {
                    event.preventDefault();
                    event.stopPropagation();
                    setQuery("");
                    return;
                  }
                }
                if (event.key === "Enter" && (agentPrefix !== null || roomPrefix !== null)) {
                  event.preventDefault();
                  return;
                }
                onKeyDown?.(event);
              }}
            />
          </div>
          {choices.length > 0 ? <ul className="mt-1 max-h-[min(12rem,25vh)] overflow-y-auto rounded-lg border bg-popover text-popover-foreground shadow-md">
            {choices.map((item, index) => (
              <li key={item.key}>
                <button ref={index === pick ? active : null} type="button" className={cn("flex w-full flex-col items-start px-3 py-2 text-left text-sm", index === pick && "bg-accent")} onMouseDown={(event) => event.preventDefault()} onClick={() => applyOverlay(item.key)}>
                  <span className="font-medium">{item.label}</span>
                  {item.detail ? <span className="text-xs text-muted-foreground">{item.detail}</span> : null}
                </button>
              </li>
            ))}
          </ul> : null}
        </div>
  );
}

function PageTitle({ children }: { children: ReactNode }) {
  return <header className="flex w-full shrink-0 items-center gap-2">
    <SheetTrigger render={<Button variant="ghost" size="icon-sm" className="md:hidden" />} aria-label="Sessions"><PanelLeftOpen /></SheetTrigger>
    <h1 className="text-lg font-semibold">{children}</h1>
    <Button type="button" variant="ghost" size="icon-sm" className="ml-auto hidden md:inline-flex" aria-label="Close" onClick={() => navigate(tabReturnTo.current)}><X /></Button>
  </header>;
}

const SessionRow = memo(function SessionRow({ session, active, loading, age }: { session: Session; active: boolean; loading: boolean; age: string }) {
  return <li className="group relative flex list-none items-stretch py-0.5">
    <Link href={sessionPath(session.id)} className={cn("relative flex min-w-0 flex-1 cursor-pointer overflow-hidden rounded-md px-2.5 py-2 text-left outline-none select-none", active ? "bg-sidebar-row-active text-sidebar-foreground" : "text-sidebar-foreground hover:bg-sidebar-row-hover")}>
      <SessionRowContent session={session} loading={loading} age={age} channelOnly={!session.settled} />
    </Link>
    <div className="pointer-events-none absolute top-1 right-1 rounded-md bg-sidebar shadow-sm opacity-0 group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100 [@media(hover:none)]:pointer-events-auto [@media(hover:none)]:opacity-100">
      <SessionRowActions session={session} />
    </div>
  </li>;
}, (a, b) => a.active === b.active && a.loading === b.loading && a.age === b.age &&
  a.session.id === b.session.id && a.session.title === b.session.title && a.session.preview === b.session.preview && a.session.updatedAt === b.session.updatedAt &&
  a.session.agent === b.session.agent && a.session.settled === b.session.settled && a.session.running === b.session.running && a.session.pinned === b.session.pinned &&
  a.session.name === b.session.name && a.session.snoozedUntil === b.session.snoozedUntil && a.session.forkedFrom === b.session.forkedFrom);

const SessionList = memo(function SessionList({ settledOnly = false }: { settledOnly?: boolean }) {
  const sidebar = useContext(Sidebar);
  const agents = useRemote(queries.agents(), settledOnly);
  const route = useRoute();
  const [query, setQuery] = useState("");
  const [agentFilter, setAgentFilter] = useState("");
  const [roomFilter, setRoomFilter] = useState("");
  const catalog = agents.data?.agents ?? [];
  const rows = sidebar.rows;
  const { pinnedOnly, forkedOnly, needle } = sessionSearchTerms(query);
  const filtered = rows.filter((session) => (settledOnly ? session.settled : !session.settled) && (!pinnedOnly || session.pinned) && (!forkedOnly || session.forkedFrom) && matchesSession(session, needle, agentFilter, roomFilter)).sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned));
  const searching = [needle, agentFilter, roomFilter, pinnedOnly, forkedOnly].some(Boolean);
  return (
    <div className={cn("flex h-full min-h-0 flex-col", settledOnly && "mx-auto w-full max-w-3xl gap-6 p-4")}>
      {settledOnly ? <PageTitle>Settled</PageTitle> : null}
      {settledOnly ? <div className="flex shrink-0 items-center gap-1">
        <SessionSearch rows={rows} catalog={catalog} query={query} setQuery={setQuery} agentFilter={agentFilter} setAgentFilter={setAgentFilter} roomFilter={roomFilter} setRoomFilter={setRoomFilter} />
      </div> : null}
      {filtered.length === 0 && (settledOnly || searching) ? <p role="status" className="px-3 pb-1 text-xs text-muted-foreground">{searchIsAuthoritative(sidebar) ? searching ? "No matches" : "No settled chats" : "loading..."}</p> : null}
      <ScrollArea className="min-h-0 flex-1"><ul className="flex flex-col px-2 pb-2">
        {filtered.map((session) => <SessionRow key={session.id} session={session} active={route.id === session.id} loading={sidebar.loadingIds.has(session.id)} age={relativeTime(session.updatedAt ?? "")} />)}
      </ul></ScrollArea>
    </div>
  );
});

type Line = { id: string; text: string; role: "user" | "assistant" | "thinking" | "tool" | "developer"; complete?: boolean; entryKey?: string; inputId?: string; messageId?: string; turnId?: string; toolCallId?: string; toolName?: string; toolParts?: Line[]; attachments?: (AttachmentMeta & { file?: File })[] } & Pick<TranscriptEvent, "agent" | "model" | "reasoningEffort" | "origin" | "header" | "state" | "parentId">;
type OriginFilter = { sandboxed: boolean; canonical: boolean };

function lineId(role: Line["role"], text: string, seen: Map<string, number>) {
  const base = `${role}:${text}`;
  const n = (seen.get(base) ?? 0) + 1;
  seen.set(base, n);
  return `${base}:${n}`;
}

function transcriptTurns(lines: Line[], filter: OriginFilter = { sandboxed: true, canonical: true }) {
  const turns: { user: Line[]; traces: Line[]; replies: Line[] }[] = [];
  let current = { user: [] as Line[], traces: [] as Line[], replies: [] as Line[] };
  const calls = new Map<string, Line & { toolParts: Line[] }>();
  const skills = new Map<string, Line & { toolParts: Line[] }>();
  const flush = () => {
    if (current.user.length > 0 || current.traces.length > 0 || current.replies.length > 0) {
      turns.push(current);
      current = { user: [], traces: [], replies: [] };
      calls.clear();
      skills.clear();
    }
  };
  for (const line of lines) {
    if (line.role === "user") flush();
    const visible = (filter.sandboxed && filter.canonical)
      || (line.origin === "sandboxed" && filter.sandboxed)
      || (line.origin === "canonical" && filter.canonical);
    if (!visible) continue;
    const callKey = `${line.parentId ?? line.entryKey ?? ""}/${line.toolCallId ?? ""}`;
    const resultCall = line.role === "tool" ? calls.get(callKey) : undefined;
    const skillHeader = line.text.split("\n", 1)[0];
    const skillCall = line.role === "developer" ? skills.get(skillHeader) : undefined;
    if (line.role === "user") {
      current.user.push(line);
    } else if (line.role === "assistant") {
      current.replies.push(line);
    } else if (line.role === "tool" && line.toolName) {
      const call = { ...line, toolParts: [] as Line[] };
      current.traces.push(call);
      if (line.toolCallId) calls.set(callKey, call);
    } else if (resultCall) {
      resultCall.toolParts.push(line);
      if (line.state) {
        resultCall.state = line.state;
        resultCall.complete = line.complete;
      }
      if (resultCall.toolName === "skill" && line.text.startsWith("skill ") && line.text.endsWith(" loaded")) {
        skills.set(`<skill_content name=${JSON.stringify(line.text.slice(6, -7))}>`, resultCall);
      }
    } else if (skillCall) {
      skillCall.toolParts.push(line);
      skills.delete(skillHeader);
    } else {
      current.traces.push(line);
    }
  }
  flush();
  return turns;
}

function toolTitle(line: Line) {
  const name = line.toolName;
  if (!name) return "Tool result";
  let input: { name?: string; code?: string; command?: string; path?: string };
  try {
    input = JSON.parse(line.text.slice(name.length + 1));
  } catch {
    return name;
  }
  if (!input || typeof input !== "object") return name;
  if (name === "skill" && typeof input.name === "string") return `Skill · ${input.name}`;
  if (name === "rocketclaw_i_want_human_partner_to_see_this") return "Send report";
  if (name === "execute" && typeof input.code === "string") {
    const operation = input.code.match(/\b(bash|read|glob|grep)\(\s*(?:command|filePath|pattern)\s*=\s*r?("""|'''|"|')([\s\S]*?)\2/);
    if (operation) {
      const detail = operation[3].split("\n").map((line) => line.trim()).find((line) => line !== "" && !line.startsWith("set ")) ?? operation[3];
      return `${operation[1] === "bash" ? "Run" : operation[1] === "read" ? "Read" : "Search"} · ${detail}`;
    }
  }
  const detail = [input.command, input.code, input.path].find((value) => typeof value === "string" && value.trim() !== "");
  return detail ? `${name === "execute" ? "Run" : name} · ${detail.replace(/\s+/g, " ").slice(0, 120)}` : name;
}

function MessageAttachments({ attachments, conversationId }: { attachments?: Line["attachments"]; conversationId: string }) {
  if (!attachments?.length) return null;
  return <AttachmentGroup>{attachments.map((file) => <MessageAttachment key={file.id} file={file} conversationId={conversationId} />)}</AttachmentGroup>;
}

function MessageAttachment({ file, conversationId }: { file: NonNullable<Line["attachments"]>[number]; conversationId: string }) {
  const [preview, setPreview] = useState<string>();
  useEffect(() => {
    if (!file.file) return;
    const url = URL.createObjectURL(file.file);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [file.file]);
  const url = file.file ? preview : `/api/DownloadAttachment?${new URLSearchParams({ conversationId, id: file.id })}`;
  const image = /^image\/(png|jpeg|gif|webp|avif)$/.test(file.mimeType);
  return <Attachment orientation={image ? "vertical" : "horizontal"}>
    <AttachmentMedia variant={image ? "image" : "icon"}>{image ? <img src={url} alt={file.name} loading="lazy" /> : <FileIcon />}</AttachmentMedia>
    <AttachmentContent><AttachmentTitle>{file.name}</AttachmentTitle><AttachmentDescription>{file.size ?? "0"} bytes{file.originalUnverified ? " · Original unverified" : ""}</AttachmentDescription></AttachmentContent>
    <AttachmentActions><AttachmentAction role="link" nativeButton={false} render={<a href={file.file ? url : `${url}&download=1`} download={file.name} aria-label={`Download ${file.name}`} />} aria-label={`Download ${file.name}`}><Download /></AttachmentAction></AttachmentActions>
  </Attachment>;
}

// OpenCode 048a47e89e859f9928f5f04a56eebf013063152a: packages/session-ui/src/message/message-content.tsx, CurrentUserMessageDisplay and AssistantTextContent.
function MessageFooter({ line, hasSandboxed }: { line: Line; hasSandboxed: boolean }) {
  const model = `${line.model ?? ""}${line.reasoningEffort ? `#${line.reasoningEffort}` : ""}`;
  const attribution = [line.agent, model && (line.agent ? `(${model})` : model)].filter(Boolean).join(" ");
  const origin = hasSandboxed && ["sandboxed", "canonical"].includes(line.origin ?? "") ? line.origin : "";
  const text = line.role === "assistant" || line.state ? [attribution, origin].filter(Boolean).join(" - ") : "";
  return text || line.header ? <div data-slot="message-footer" className="flex max-w-full items-center gap-1 break-words px-3 text-[11px] text-muted-foreground/85 group-has-data-[variant=ghost]/message:px-0">
    {text ? <span className="min-w-0">{text}</span> : null}
    {line.header ? <Dialog>
      <Tooltip>
        <TooltipTrigger render={<DialogTrigger render={<Button type="button" variant="ghost" size="icon-xs" className="size-11 sm:size-6" />} />} aria-label="Show message header"><Info aria-hidden="true" data-icon="inline-start" /></TooltipTrigger>
        <TooltipContent className="max-w-[min(24rem,calc(100vw-2rem))] whitespace-pre-wrap [overflow-wrap:anywhere]">{line.header}</TooltipContent>
      </Tooltip>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
        <DialogTitle className="mr-8">Message header</DialogTitle>
        <DialogDescription className="min-w-0 whitespace-pre-wrap [overflow-wrap:anywhere]">{line.header}</DialogDescription>
      </DialogContent>
    </Dialog> : null}
  </div> : null;
}

function MessageActions({ line, hasSandboxed }: { line: Line; hasSandboxed: boolean }) {
  const [copied, setCopied] = useState<string>();
  const [error, setError] = useState(false);
  return <div data-slot="message-actions" className={cn("invisible flex max-w-full items-center gap-1 group-focus-visible/message:visible [@media(hover:hover)]:group-hover/message:visible [@media(hover:hover)]:group-has-[:focus-visible]/message:visible [@media(hover:none)]:group-focus-within/message:visible", line.role === "user" && "self-end")}>
    {line.role === "assistant" || line.header ? <MessageFooter line={line} hasSandboxed={hasSandboxed} /> : null}
    <Button type="button" size="icon-xs" variant="ghost" aria-label="Copy message" title="Copy message" onClick={async () => {
      setError(false);
      try {
        await Effect.runPromise(copyText(line.text, document.body));
        setCopied(line.text);
      } catch {
        setError(true);
      }
    }}>{copied === line.text && !error ? <Check /> : <Copy />}</Button>
    <span role="status" className={error ? "text-xs text-destructive" : "sr-only"}>{error ? "Could not copy. Select and copy the text." : copied === line.text ? "Copied" : ""}</span>
  </div>;
}

function TranscriptLine({ line, conversationId, hasSandboxed }: { line: Line; conversationId: string; hasSandboxed: boolean }) {
  if (line.role === "thinking") {
    return (
      <div className="flex items-start gap-1.5 px-1 py-0.5 text-[12px] leading-5 text-muted-foreground">
        <Bot className="size-3.5 shrink-0 opacity-80" />
        <span className="min-w-0 whitespace-pre-wrap break-words">{line.text}</span>
      </div>
    );
  }
  const footer = (line.role === "tool" ? line.state : line.header) ? <MessageFooter line={line} hasSandboxed={hasSandboxed} /> : null;
  if (line.role === "tool") {
    const title = toolTitle(line);
    const delegation = use(Delegations)?.find((child) => child.slice(child.lastIndexOf("/") + 1) === line.toolCallId);
    const parts = [line, ...(line.toolParts ?? [])];
    const text = [
      line.toolName ? `Arguments\n${line.text.slice(line.toolName.length + 1)}` : `Result\n${line.text}`,
      ...parts.slice(1).map((part) => `${part.role === "developer" ? "Skill instructions" : "Result"}\n${part.text}`),
    ].join("\n\n");
    return (
      <details open className="mb-3 min-w-0">
        <summary className="cursor-pointer px-3 py-2 text-xs font-medium" title={title}>
          <span className="ml-1 inline-block max-w-[calc(100%-1.5rem)] truncate align-middle font-mono">{title}</span>
          {line.state ? <span aria-live="polite" className="ml-2 text-muted-foreground">{line.state}</span> : null}
        </summary>
        {delegation ? <Link href={delegationHref(delegation)} aria-label={`Open delegation: ${title}`} className="block w-fit px-3 pb-2 text-xs text-muted-foreground underline hover:text-foreground">Open delegation</Link> : null}
        {footer}
        <CodeBlock label={title} text={text} />
        {parts.map((part) => (
          <div key={part.id}>
            <MessageAttachments attachments={part.attachments} conversationId={conversationId} />
          </div>
        ))}
        <button type="button" className="px-3 py-2 text-xs text-muted-foreground hover:text-foreground" onClick={(event) => {
          const details = event.currentTarget.closest("details")!;
          details.open = false;
          const summary = details.querySelector("summary")!;
          summary.scrollIntoView({ block: "nearest" });
          summary.focus({ preventScroll: true });
        }}>Collapse tool ↑</button>
      </details>
    );
  }
  if (line.role === "developer") {
    return (
      <div className="min-w-0 px-1 pb-3">
        <CodeBlock label="Instructions" text={line.text} />
        {footer}
      </div>
    );
  }
  const align = line.role === "user" ? "end" : undefined;
  return (
    <Message data-message-id={line.messageId || undefined} align={align} className="mb-4" tabIndex={0} onPointerDown={(event) => {
      if (event.pointerType === "touch" && !(event.target as Element).closest("button, a, input, textarea, summary")) event.currentTarget.focus({ preventScroll: true });
    }}>
      <MessageContent>
        <Bubble variant={line.role === "user" ? "secondary" : "ghost"} align={align}>
          <BubbleContent><TranscriptText text={line.text} /></BubbleContent>
          <MessageAttachments attachments={line.attachments} conversationId={conversationId} />
        </Bubble>
        <MessageActions line={line} hasSandboxed={hasSandboxed} />
      </MessageContent>
    </Message>
  );
}

function useTranscriptPosition(conversationId: string, lines: Line[], turns: ReturnType<typeof transcriptTurns>) {
  const viewport = useRef<HTMLDivElement>(null);
  const identity = useAtomValue(queries.identity(), (result) => AsyncResult.isSuccess(result) ? result.value : undefined);
  const firstOwner = useRef(identity);
  const { scrollToMessage } = useMessageScroller();
  const target = useContext(SessionCommands).command?.target;
  const search = useSearch();
  const messageId = conversationId && location.pathname === sessionPath(conversationId) ? new URLSearchParams(search).get("message") : null;
  const targetId = messageId ?? (target?.conversationId === conversationId ? target.message.messageId : null);
  const targetTurn = targetId ? turns.findIndex((turn) => turn.user.some((line) => line.messageId === targetId) || turn.replies.some((line) => line.messageId === targetId)) : -1;
  const seen = useCallback(() => {
    const element = viewport.current;
    if (!element || identity === undefined || !element.getClientRects().length) return;
    if (firstOwner.current === undefined) firstOwner.current = identity;
    if (firstOwner.current !== identity) return;
    const bounds = element.getBoundingClientRect();
    const visible = [...element.querySelectorAll<HTMLElement>('[data-slot="message"][data-message-id]')].filter((node) => {
      const rect = node.getBoundingClientRect();
      return rect.bottom > bounds.top && rect.top < bounds.bottom;
    });
    const last = visible.at(-1)?.dataset.messageId;
    if (last) localStorage.setItem(`last-seen:${identity}`, `${sessionPath(conversationId)}?message=${encodeURIComponent(last)}`);
  }, [conversationId, identity]);
  useEffect(() => {
    const frame = requestAnimationFrame(() => requestAnimationFrame(seen));
    return () => cancelAnimationFrame(frame);
  }, [lines, seen]);
  useEffect(() => {
    if (targetTurn < 0) return;
    scrollToMessage(`turn-${targetTurn}`, { align: "center", behavior: "instant" });
    const frame = requestAnimationFrame(() => {
      const line = [...(viewport.current?.querySelectorAll<HTMLElement>("[data-message-id]") ?? [])].find((node) => node.dataset.messageId === targetId);
      line?.scrollIntoView({ block: "center", behavior: "instant" });
      seen();
    });
    return () => cancelAnimationFrame(frame);
  }, [targetTurn, targetId, scrollToMessage, seen, viewport]);
  return { viewport, seen, scrollToMessage };
}

function TranscriptLog({
  conversationId,
  lines,
  thinking,
  terminal,
  origin,
  filter,
  hasSandboxed,
}: {
  lines: Line[];
  conversationId: string;
  thinking: boolean;
  terminal?: string;
  origin?: ChatOrigin;
  filter: OriginFilter;
  hasSandboxed: boolean;
}) {
  const turns = transcriptTurns(lines, filter);
  const { viewport, seen, scrollToMessage } = useTranscriptPosition(conversationId, lines, turns);
  const turnNodes = useRef<(HTMLElement | null)[]>([]);
  const running = usePendingCron(conversationId);
  const emptyMessage = lines.length === 0
    ? (running ? `${running} is running` : "Send a message to start the conversation.")
    : "No messages for selected origins.";
  useEffect(() => { if (running && lines.length > 0) notePendingCron(conversationId, ""); }, [running, lines.length, conversationId]);
  const jumpToTurn = (index: number) => {
    scrollToMessage(`turn-${index}`, { align: "start", behavior: "instant" });
    turnNodes.current[index]?.focus({ preventScroll: true });
  };
  return (
    <MessageScroller className="flex-1">
    <MessageScrollerViewport ref={viewport} id="transcript-scroll" onScroll={seen} className="overflow-x-hidden [overflow-anchor:none]">
      <div className="min-h-full pl-3 pr-8 pt-3 pb-4 sm:pl-5 sm:pr-10 sm:pt-4">
      <MessageScrollerContent className="mx-auto w-full min-w-0 max-w-3xl">
      {origin && (origin.kind === "cron" || origin.kind === "external_mcp") ? <MessageScrollerItem messageId="origin"><OriginCard origin={origin} /></MessageScrollerItem> : null}
      {turns.length === 0 && !thinking ? (
        <MessageScrollerItem className="flex flex-1 items-center justify-center">
          <p role="status" className="text-sm text-muted-foreground">{emptyMessage}</p>
        </MessageScrollerItem>
      ) : (
        <>
          {turns.map((turn, index) => (
              <MessageScrollerItem key={turn.user[0]?.id ?? turn.traces[0]?.id ?? turn.replies[0]?.id} messageId={`turn-${index}`} ref={(node) => { turnNodes.current[index] = node; }} role="region" aria-label={`Turn ${index + 1}`} tabIndex={-1}>
                {turn.user.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
                {turn.traces.length > 0 ? (
                  <details open className="group pb-3">
                    <summary className="flex w-fit cursor-pointer list-none items-center gap-1 px-1 py-2 text-xs text-muted-foreground hover:text-foreground [&::-webkit-details-marker]:hidden">
                      Thinking <span aria-hidden="true" className="transition-transform group-open:rotate-90">▸</span>
                    </summary>
                    <div className="ml-1 border-l pl-3">
                      {turn.traces.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
                    </div>
                  </details>
                ) : null}
                {turn.replies.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
              </MessageScrollerItem>
          ))}
          {thinking ? <MessageScrollerItem><p className="px-1 pb-4 text-sm text-muted-foreground">Thinking…</p></MessageScrollerItem> : null}
          {terminal ? <MessageScrollerItem><p role="status" className="px-1 pb-4 text-sm text-muted-foreground">Turn {terminal}.</p></MessageScrollerItem> : null}
        </>
      )}
      </MessageScrollerContent>
      </div>
    </MessageScrollerViewport>
    {turns.length > 0 ? (
      <nav aria-label="Conversation turns" className="group/rail absolute top-12 bottom-0 right-[6px] flex w-8 items-center justify-end py-3">
        <div role="group" aria-label="Message previews" className="absolute right-full top-1/2 hidden max-h-[calc(100%-1.5rem)] w-[min(20rem,calc(100vw-4rem))] -translate-y-1/2 overflow-y-auto overscroll-contain rounded-md border bg-popover p-1 text-popover-foreground shadow-lg group-hover/rail:block group-focus-within/rail:block">
          {turns.map((turn, index) => {
            const preview = (turn.user[0]?.text ?? turn.replies[0]?.text ?? "Thinking").replace(/\s+/g, " ").slice(0, 120);
            return <button key={turn.user[0]?.id ?? turn.traces[0]?.id ?? turn.replies[0]?.id} type="button" aria-label={`Jump to turn ${index + 1}: ${preview}`} className="block w-full rounded-sm px-3 py-2 text-left text-xs hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring" onClick={() => jumpToTurn(index)}><span className="line-clamp-2 break-words">{index + 1}. {preview}</span></button>;
          })}
        </div>
        <div className="max-h-full w-8 overflow-y-auto">
          {turns.map((turn, index) => {
            const preview = (turn.user[0]?.text ?? turn.replies[0]?.text ?? "Thinking").replace(/\s+/g, " ").slice(0, 120);
            const label = `Turn ${index + 1}: ${preview}`;
            return (
              <button key={turn.user[0]?.id ?? turn.traces[0]?.id ?? turn.replies[0]?.id} type="button" aria-label={label} className="group flex min-h-6 w-full items-center justify-end rounded-sm text-left text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring" onClick={() => jumpToTurn(index)}>
                <span aria-hidden="true" className="flex w-6 shrink-0 items-center justify-center"><span className="h-0.5 w-2 rounded-full bg-current transition-[width] group-hover:w-4 group-focus-visible:w-4" /></span>
              </button>
            );
          })}
        </div>
      </nav>
    ) : null}
    <MessageScrollerButton aria-label="Scroll to latest" title="Scroll to latest" behavior="instant" />
    </MessageScroller>
  );
}

function applyHistoryDelta(draft: ComposerDraft, view: HistoryView) {
  const consumed = draft.consumed ??= new Set<string>();
  let newlyConsumed = false;
  for (const message of view.messages) {
    if (message.inputId && !consumed.has(message.inputId)) {
      consumed.add(message.inputId);
      newlyConsumed = true;
    }
  }
  draft.parked = draft.parked?.filter((line) => !consumed.has(line.inputId || line.id));
  if (view.reset || newlyConsumed || view.replacedKeys.length || view.removedKeys.length) {
    const reset = view.reset && !draft.revision;
    const changed = new Set([...(view.reset ? view.entryKeys : view.replacedKeys), ...view.removedKeys]);
    const groups = Map.groupBy([...draft.lines.filter((line) => !reset && line.entryKey && !changed.has(line.entryKey)), ...historyLines(view.messages)], (line) => line.entryKey!);
    const retained = new Set(view.entryKeys);
    let anchor = "";
    for (const line of draft.lines) {
      if (line.entryKey && retained.has(line.entryKey)) anchor = line.entryKey;
      if (!reset && !line.entryKey && line.complete && !consumed.has(line.inputId || line.id)) {
        if (!groups.has(anchor)) groups.set(anchor, []);
        groups.get(anchor)!.push(line);
      }
    }
    const pending = draft.lines.filter((line) => !line.entryKey && line.role === "user" && !line.complete && !consumed.has(line.inputId || line.id));
    draft.lines = [...(groups.get("") ?? []), ...view.entryKeys.flatMap((key) => groups.get(key) ?? []), ...pending];
  }
  draft.busy = view.running || draft.lines.some((line) => !line.entryKey && line.role === "user" && !line.complete);
  draft.terminal = view.terminal;
  draft.origin = view.origin;
  draft.revision = view.revision;
  draft.historyError = "";
  return newlyConsumed;
}

const readHistoryDelta = Effect.fnUntraced(function* (id: string, draft: ComposerDraft, onDraftChange: () => void) {
  draft.historyAgain = true;
  if (draft.historyRead) return yield* Fiber.join(draft.historyRead);
  draft.historyRead = Effect.runFork(Effect.gen(function* () {
    do {
      draft.historyAgain = false;
      const cached = registry.get(histories(id));
      yield* requests.history({ id, revision: cached ? draft.revision : undefined }).pipe(Effect.match({ onSuccess: (view) => {
        if (!view.reset && view.revision === draft.revision
          && draft.busy === (view.running || draft.lines.some((line) => !line.entryKey && line.role === "user" && !line.complete))
          && draft.terminal === view.terminal && JSON.stringify(draft.origin) === JSON.stringify(view.origin)
          && JSON.stringify(cached?.delegations) === JSON.stringify(view.delegations)
          && !draft.historyError) return;
        if (applyHistoryDelta(draft, view)) invalidate("queue");
        // Main's delegation panel reads a full durable parent view from this cache.
        const changed = new Set([...view.replacedKeys, ...view.removedKeys]);
        const groups = Map.groupBy([...(view.reset ? [] : cached?.messages ?? []).filter((message) => !changed.has(message.entryKey)), ...view.messages], (message) => message.entryKey);
        registry.set(histories(id), { ...view, reset: true, replacedKeys: [], removedKeys: [], messages: view.entryKeys.flatMap((entry) => groups.get(entry) ?? []) });
        onDraftChange();
      }, onFailure: (err) => {
        // A history failure must not turn an accepted Prompt into a failed send.
        draft.historyError = err.message;
        onDraftChange();
      } }));
    } while (draft.historyAgain);
  }).pipe(Effect.ensuring(Effect.sync(() => { draft.historyRead = undefined; }))));
  yield* Fiber.join(draft.historyRead);
});

function historyLines(messages: TranscriptEvent[]): Line[] {
  const seen = new Map<string, number>();
  return messages.map((message) => {
    const role = message.role === "thinking" || message.role === "user" || message.role === "tool" || message.role === "developer" ? message.role : "assistant";
    return { ...message, id: message.inputId || message.itemId || message.messageId || lineId(role, message.text, seen), role };
  });
}

function useSessionStream(id: string, draft: ComposerDraft, onDraftChange: () => void) {
  const history = useAtomValue(histories(id));
  const refreshHistory = useCallback(() => readHistoryDelta(draft.sessionId, draft, onDraftChange), [draft, onDraftChange]);
  const setBusy = useCallback((value: boolean) => { draft.busy = value; onDraftChange(); }, [draft, onDraftChange]);
  const setLines = useCallback((update: (current: Line[]) => Line[]) => { draft.lines = update(draft.lines); onDraftChange(); }, [draft, onDraftChange]);
  useEffect(() => {
    if (!id) return;
    const changes = Stream.callback<void>((queue) => Effect.acquireRelease(Effect.sync(() => {
      const stream = new EventSource(`/stream?${new URLSearchParams({ id })}`);
      const notify = () => { Queue.offerUnsafe(queue, undefined); };
      notify();
      stream.onopen = notify;
      const decode = Schema.decodeUnknownOption(Schema.fromJsonString(Schema.Struct({ conversationId: Schema.String, revision: Schema.String })));
      stream.onmessage = (event) => {
        const change = decode(String(event.data));
        if (Option.isSome(change) && change.value.conversationId === id) notify();
      };
      return stream;
    }), (stream) => Effect.sync(() => stream.close())), { bufferSize: 1, strategy: "sliding" });
    const listener = Effect.runFork(changes.pipe(Stream.runForEach(refreshHistory)));
    return () => { Effect.runFork(Fiber.interrupt(listener)); };
  }, [id, refreshHistory]);
  return { busy: draft.busy, setBusy, lines: draft.lines, setLines, refreshHistory, opening: id !== "" && !draft.revision, historyError: draft.historyError, origin: draft.origin, terminal: draft.terminal, delegations: history?.delegations, hasSandboxed: draft.lines.some((line) => line.origin === "sandboxed") };
}

export function OriginCard({ origin }: { origin?: ChatOrigin }) {
  if (!origin || (origin.kind !== "cron" && origin.kind !== "external_mcp")) return null;
  return (
    <details aria-label="Chat origin" className="group/origin mb-3 min-w-0 rounded-lg border">
      <summary className="cursor-pointer px-3 py-2 text-xs font-medium break-words">
        {origin.kind === "cron" ? `Cron · ${origin.stem}` : `External MCP · ${origin.externalConversationId}`} · {origin.agent}
        <span className="ml-2 text-muted-foreground group-open/origin:hidden">show more</span>
        <span className="ml-2 hidden text-muted-foreground group-open/origin:inline">show less</span>
      </summary>
      <div className="border-t px-3 py-2 text-sm whitespace-pre-wrap break-words">
        {origin.kind === "cron" ? <>
          <p>Source: {origin.sourcePath}</p>
          <p>Stem: {origin.stem}</p>
          <p>Run kind: {origin.runKind}</p>
          <p>Run ID: {origin.runId}</p>
          <p>Agent: {origin.agent}</p>
          <p>Ran at: {origin.ranAt}</p>
        </> : <>
          <p>External conversation: {origin.externalConversationId}</p>
          <p>Agent: {origin.agent}</p>
          {origin.pairs?.map((pair) => <p key={pair.key}>{pair.key}={pair.value}</p>)}
        </>}
      </div>
    </details>
  );
}

function Transcript({ id, drafts, onDraftChange, onCreated }: { id: string; drafts: Map<string, ComposerDraft>; onDraftChange: () => void; onCreated: (id: string) => void }) {
  const [filter, setFilter] = useState<OriginFilter>({ sandboxed: true, canonical: true });
  const search = useSearch();
  const target = useContext(SessionCommands).command?.target;
  const previewing = target?.conversationId === id;
  const preview = useRemote(queries.history({ id }), previewing);
  const previewLines = useMemo(() => previewing && preview.data ? historyLines(preview.data.messages) : undefined, [previewing, preview.data]);
  const [draft] = useState(() => {
    const value = drafts.get(id) ?? { text: "", files: [], agent: "", sessionId: id, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0 };
    drafts.set(id, value);
    return value;
  });
  const route = useRoute();
  useLayoutEffect(() => {
    if (id === "" && draft.sessionId !== "" && drafts.get("") === draft) {
      drafts.delete("");
      onCreated(draft.sessionId);
      route.goSession(draft.sessionId);
    }
  });
  const { busy, setBusy, lines, setLines, refreshHistory, opening, historyError, origin, terminal, delegations, hasSandboxed } = useSessionStream(id, draft, onDraftChange);
  const messageId = location.pathname === sessionPath(id) ? new URLSearchParams(search).get("message") : null;
  const matchedOrigin = (previewLines ?? lines).find((line) => line.messageId === messageId)?.origin;
  const visibleFilter = { sandboxed: filter.sandboxed || matchedOrigin === "sandboxed", canonical: filter.canonical || matchedOrigin === "canonical" };
  return (
    <>
      <Delegations value={previewing ? preview.data?.delegations : delegations}><TranscriptLog conversationId={id} lines={previewLines ?? lines} thinking={!previewing && busy && lines.at(-1)?.role !== "thinking"} terminal={previewing ? undefined : terminal} origin={origin} filter={visibleFilter} hasSandboxed={hasSandboxed} /></Delegations>
      {historyError ? <p role="alert" className="px-3 text-sm text-destructive">{historyError}</p> : null}
      {hasSandboxed ? <ButtonGroup aria-label="Show messages from" className="mx-auto my-2.5">
        {(["sandboxed", "canonical"] as const).map((choice) => (
          <Button key={choice} type="button" size="xs" className="relative before:absolute before:-inset-y-2.5 before:inset-x-0" variant={visibleFilter[choice] ? "default" : "outline"} aria-pressed={visibleFilter[choice]} onClick={() => {
            if (matchedOrigin === choice) navigate(sessionPath(id));
            setFilter((current) => ({ ...current, [choice]: !visibleFilter[choice] }));
          }}>
            {choice}
          </Button>
        ))}
      </ButtonGroup> : null}
      <fieldset disabled={opening || previewing} className={previewing ? "hidden" : "contents"}>
        <SessionComposer id={id} draft={draft} drafts={drafts} onDraftChange={onDraftChange} busy={busy} setBusy={setBusy} setLines={setLines} refreshHistory={refreshHistory} />
      </fieldset>
    </>
  );
}

function DelegationPanel({ id }: { id: string }) {
  const child = new URLSearchParams(useSearch()).get("delegation") ?? "";
  const wide = useSyncExternalStore(subscribeWide, () => matchMedia("(min-width: 64rem)").matches);
  const main = useAtomValue(histories(id));
  const history = useRemote(queries.history({ id: child }), child !== "");
  if (!child) return null;
  const first = main?.delegations.find((level) => child === level || child.startsWith(`${level}/`)) ?? child;
  const levels = [...child.matchAll(/\/|$/g)].map((match) => child.slice(0, match.index)).filter((level) => level.length >= first.length).map((level, index, all) => {
    const call = level.slice(level.lastIndexOf("/") + 1);
    const parentView = index ? Option.getOrUndefined(AsyncResult.value(registry.get(queries.history({ id: all[index - 1] })))) : main;
    const row = historyLines(parentView?.messages ?? []).find((line) => line.toolName && line.toolCallId === call);
    return { level, label: row ? toolTitle(row) : call };
  });
  const parent = levels.at(-2);
  const lines = historyLines(history.data?.messages ?? []);
  const sandboxed = lines.some((line) => line.origin === "sandboxed");
  const close = () => navigate(delegationHref());
  const body = <>
    <header className="flex shrink-0 items-center gap-1 border-b p-2 text-xs lg:pr-12">
      <nav aria-label="Delegation breadcrumbs" className="flex min-w-0 flex-1 items-center gap-1 whitespace-nowrap">
        <Link href={delegationHref()} className="shrink-0 hover:underline">Conversation</Link>
        {levels.map(({ level, label }, index) => <span key={level} className="flex min-w-0 items-center gap-1"><span aria-hidden="true">/</span>{index === levels.length - 1 ? <span aria-current="page" title={label} className="min-w-0 truncate font-medium">{label}</span> : <Link href={delegationHref(level)} title={label} className="min-w-0 truncate hover:underline">{label}</Link>}</span>)}
      </nav>
      <Button variant="ghost" size="icon-sm" aria-label="Close delegation" onClick={close}><X /></Button>
    </header>
    <ScrollArea className="min-h-0 flex-1"><div className="p-3">
      {history.isPending ? <p role="status" className="text-sm text-muted-foreground">Loading delegation…</p> : null}
      {history.error ? <p role="alert" className="text-sm text-destructive">{history.error.message}</p> : null}
      {history.data?.messages.length === 0 ? <p role="status" className="text-sm text-muted-foreground">No saved transcript</p> : null}
      <Delegations value={history.data?.delegations}>
        {transcriptTurns(lines).flatMap((turn) => [...turn.user, ...turn.traces, ...turn.replies]).map((line) => <TranscriptLine key={line.id} line={line} conversationId={child} hasSandboxed={sandboxed} />)}
      </Delegations>
      {history.isSuccess ? <Link href={delegationHref(parent?.level)} className="block w-fit text-xs text-muted-foreground underline hover:text-foreground">{parent ? `Back to ${parent.label}` : "Back to conversation"}</Link> : null}
    </div></ScrollArea>
  </>;
  return wide ? <ResizableAside side="delegation" aria-label="Delegation" className="flex flex-col border-l bg-background">{body}</ResizableAside> : <Sheet open onOpenChange={(open) => { if (!open) close(); }}>
    <SheetContent side="right" showCloseButton={false} className="data-[side=right]:w-full data-[side=right]:sm:max-w-none"><SheetTitle className="sr-only">Delegation</SheetTitle>{body}</SheetContent>
  </Sheet>;
}

const sendComposer = Effect.fnUntraced(function* (input: {
  draft: ComposerDraft;
  onDraftChange: () => void;
  text: string;
  files: PendingFile[];
  delivery?: PromptDelivery;
  busy: boolean;
  sessionId: string;
  selected: string;
  currentAgent: string;
  goSession: (id: string) => void;
  prompt: typeof mutations.prompt;
  create: typeof mutations.createSession;
  scrollToEnd: () => boolean;
  setBusy: (value: boolean) => void;
  setAgentOpen: (value: boolean) => void;
  setSendError: (value: string) => void;
  setLines: (update: (current: Line[]) => Line[]) => void;
  refreshHistory: () => Effect.Effect<unknown>;
}) {
  const { draft } = input;
  const stashing = input.delivery === "STASH";
  const stopping = !stashing && isStopCommand(input.text);
  if (draft.sending || (input.text.trim() === "" && input.files.length === 0)) {
    return;
  }
  draft.sending = true;
  const submission = ++draft.submission;
  input.onDraftChange();
  const agent = draft.agent;
  let dispatchedEdit: number | undefined;
  const followUp = input.delivery ?? (input.busy ? "QUEUE" : "STEER");
  const enqueue = stashing || (followUp === "QUEUE" || /^\s*\$enqueue(?:\s|$)/.test(input.text)) && !stopping;
  if (!input.busy && !enqueue) {
    input.setBusy(true);
  }
  input.scrollToEnd();
  input.setSendError("");
  const optimistic: Line = { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), role: "user", text: input.text };
  yield* Effect.gen(function* () {
    let sessionId = input.sessionId;
    if (sessionId === "") {
      sessionId = yield* input.create({ agent: input.selected });
      input.goSession(sessionId);
    }
    optimistic.attachments = input.files.map(({ id, file }) => ({ id, name: file.name, mimeType: file.type, size: String(file.size), conversationId: sessionId, file }));
    if (!enqueue && input.busy && !stopping) {
      draft.parked = [...(draft.parked ?? []), optimistic];
      input.onDraftChange();
    } else if (!enqueue) input.setLines((current) => [...current, optimistic]);
    const attachments = yield* Effect.forEach(input.files, ({ file }) => uploadAttachment(sessionId, file), { concurrency: "unbounded" });
    if (!stashing && input.sessionId !== "" && input.selected !== "" && input.selected !== input.currentAgent) {
      yield* input.prompt({ id: sessionId, text: `$agent ${input.selected}` });
      invalidate("agents");
    }
    if (attachments.length) {
      optimistic.attachments = attachments.map((file, index) => ({ ...file, file: input.files[index].file }));
      input.setLines((current) => current.map((line) => line.id === optimistic.id ? optimistic : line));
    }
    // Prompt waits for the whole turn. Consume only this submission before waiting.
    draft.text = "";
    draft.files = [];
    draft.agent = "";
    dispatchedEdit = ++draft.edit;
    const response = yield* input.prompt({ id: sessionId, text: input.text, delivery: followUp, messageId: optimistic.id, ...(attachments.length ? { attachmentIds: attachments.map((file) => file.id) } : {}) }).pipe(Effect.forkChild({ startImmediately: true }));
    draft.sending = false;
    input.onDraftChange();
    input.setAgentOpen(false);
    const privateText = yield* Fiber.join(response);
    optimistic.complete = true;
    invalidate("agents");
    if (enqueue) {
      invalidate("queue");
      return;
    }
    yield* input.refreshHistory();
    if (privateText) {
      const parked = draft.parked?.some((line) => line.id === optimistic.id);
      draft.parked = draft.parked?.filter((line) => line.id !== optimistic.id);
      input.setLines((current) => [...current, ...(parked ? [optimistic] : []), { id: `${optimistic.id}:reply`, role: "assistant", text: privateText, complete: true }]);
    }
  }).pipe(Effect.catch((err) => Effect.sync(() => {
    draft.parked = draft.parked?.filter((line) => line.id !== optimistic.id);
    if (dispatchedEdit === undefined) draft.sending = false;
    else if (draft.edit === dispatchedEdit && draft.submission === submission) {
      draft.text = input.text;
      draft.files = input.files;
      draft.agent = agent;
    }
    input.setLines((current) => current.filter((line) => line.id !== optimistic.id));
    if (draft.submission === submission) {
      input.setSendError(err instanceof Error ? err.message : "send failed");
      if (!stashing) input.setBusy(input.busy);
    }
  })));
});

const promoteComposer = Effect.fnUntraced(function* (input: {
  draft: ComposerDraft;
  id: string;
  itemId: string;
  busy: boolean;
  steerQueueItem: typeof mutations.steerQueueItem;
  setBusy: (value: boolean) => void;
  setSendError: (value: string) => void;
}) {
  if (input.id === "") {
    return;
  }
  const submission = ++input.draft.submission;
  if (!input.busy) {
    input.setBusy(true);
  }
  input.setSendError("");
  yield* input.steerQueueItem({ id: input.id, itemId: input.itemId }).pipe(Effect.catch((err) => Effect.sync(() => {
    if (input.draft.submission === submission) {
      input.setSendError(err instanceof Error ? err.message : "steer failed");
      input.setBusy(input.busy);
    }
  })));
});

const stopComposer = Effect.fnUntraced(function* (input: {
  draft: ComposerDraft;
  id: string;
  busy: boolean;
  prompt: typeof mutations.prompt;
  refreshHistory: () => Effect.Effect<unknown>;
  setSendError: (value: string) => void;
}) {
  if (!input.busy || input.id === "") {
    return;
  }
  const submission = ++input.draft.submission;
  input.setSendError("");
  yield* input.prompt({ id: input.id, text: "$stop" }).pipe(Effect.andThen(input.refreshHistory), Effect.catch((err) => Effect.sync(() => {
    if (input.draft.submission === submission) input.setSendError(err instanceof Error ? err.message : "stop failed");
  })));
});

function pendingInputs(draft: ComposerDraft, items: QueueItem[]) {
  const consumed = draft.consumed ?? new Set<string>();
  const waiting = items.filter((item) => !consumed.has(item.id));
  const parked = new Map<string, Line>(waiting.filter((item) => item.delivery === "STEER").map((item) => [item.id, { ...item, role: "user" }]));
  for (const line of draft.parked ?? []) {
    if (!consumed.has(line.id)) parked.set(line.id, line);
  }
  return { parked: [...parked.values()], queued: waiting.filter((item) => item.delivery !== "STEER") };
}

function SessionComposer({
  id,
  draft,
  drafts,
  onDraftChange,
  busy,
  setBusy,
  setLines,
  refreshHistory,
}: {
  id: string;
  draft: ComposerDraft;
  drafts: Map<string, ComposerDraft>;
  onDraftChange: () => void;
  busy: boolean;
  setBusy: (value: boolean) => void;
  setLines: (update: (current: Line[]) => Line[]) => void;
  refreshHistory: () => Effect.Effect<unknown>;
}) {
  const [, setEditVersion] = useState(0);
  const { setCommand } = useContext(SessionCommands);
  const { scrollToEnd } = useMessageScroller();
  const prompt: typeof mutations.prompt = (input) => mutations.prompt(input).pipe(Effect.tap(() => Effect.sync(() => invalidate("queue"))));
  const agents = useRemote(queries.agents({ conversationId: id }));
  const queueQuery = useRemote(queries.queue({ id }), id !== "");
  const removeQueueItem = useAction({ action: mutations.removeQueueItem, onSuccess: () => invalidate("queue") });
  const steerQueueItem: typeof mutations.steerQueueItem = (input) => mutations.steerQueueItem(input).pipe(Effect.tap(() => Effect.sync(() => invalidate("queue"))));
  const popQueueItem = useAction({ action: mutations.popQueueItem, onSuccess: () => invalidate("queue") });
  const reorderQueue = useAction({ action: mutations.reorderQueue, onSuccess: () => invalidate("queue") });
  const invalidateSidebar = useContext(SidebarInvalidation);
  const create: typeof mutations.createSession = (input) => mutations.createSession(input).pipe(Effect.tap(() => Effect.sync(invalidateSidebar)));
  const { text, files, sending, agent } = draft;
  const setText = (value: string) => { draft.text = value; draft.edit++; setEditVersion((version) => version + 1); };
  const setFiles = (value: PendingFile[]) => { draft.files = value; draft.edit++; onDraftChange(); };
  const setAgent = (value: string) => { draft.agent = value; draft.edit++; onDraftChange(); };
  const [agentOpen, setAgentOpen] = useState(false);
  const [dollarOff, setDollarOff] = useState(false);
  const [dollarPick, setDollarPick] = useState("");
  const sendError = draft.error;
  const setSendError = (value: string) => { draft.error = value; onDraftChange(); };
  const currentAgent = id === "" ? "main" : agents.data?.currentAgent ?? "";
  const catalog = agents.data?.agents ?? [];
  const selected = catalog.some((item) => item.name === agent) ? agent : currentAgent || catalog[0]?.name || "";
  const skills = useRemote(queries.skills({ agent: selected }), selected !== "");
  const matches = dollarOff ? [] : dollarMatches(text, skills.data ?? []);
  const pick = Math.max(0, matches.findIndex((item) => item.invocation === dollarPick));
  const applyDollar = (invocation: string) => {
    setText(invocation);
    setDollarOff(invocation !== "$skill ");
    setAgentOpen(false);
  };
  const { queued, parked } = pendingInputs(draft, queueQuery.data ?? []);
  let placeholder = "Message a new session";
  if (busy) {
    placeholder = "Queue a follow-up · ⌘⏎ steers";
  } else if (id) {
    placeholder = "Message or $command";
  }
  const send = (delivery?: PromptDelivery) => {
    const command = /^\$(fork|handoff)\s*$/.exec(draft.text);
    if (delivery !== "STASH" && command) {
      if (!id) { setSendError("Open a session first."); return Promise.resolve(); }
      setCommand({ mode: command[1] as "fork" | "handoff", source: id });
      setText("");
      return Promise.resolve();
    }
    return Effect.runPromise(sendComposer({
      draft,
      onDraftChange,
      text: draft.text,
      files: draft.files,
      delivery,
      busy,
      sessionId: draft.sessionId,
      selected: id === "" ? selected : draft.agent,
      currentAgent,
      goSession: (sessionId) => {
        draft.sessionId = sessionId;
        drafts.set(sessionId, draft);
        onDraftChange();
      },
      prompt,
      create,
      scrollToEnd,
      setBusy,
      setAgentOpen,
      setSendError,
      setLines,
      refreshHistory,
    }));
  };
  const promoteQueued = (itemId: string) => Effect.runPromise(promoteComposer({ draft, id, itemId, busy, steerQueueItem, setBusy, setSendError }));
  const stop = () => Effect.runPromise(stopComposer({ draft, id, busy, prompt, refreshHistory, setSendError }));
  return (
    <>
      {sendError ? <p className="px-3 pb-2 text-sm text-destructive sm:px-5">{sendError}</p> : null}
      {parked.length > 0 ? <section aria-label="Pending steers" className="mx-auto w-full max-w-3xl px-3"><p className="text-xs text-muted-foreground">Waiting to steer</p>{parked.map((line) => <TranscriptLine key={line.id} line={line} conversationId={id} hasSandboxed={false} />)}</section> : null}
      <Composer
        files={files}
        setFiles={setFiles}
        sending={sending}
        sessionId={id}
        text={text}
        setText={setText}
        matches={matches}
        pick={pick}
        applyDollar={applyDollar}
        catalog={catalog}
        selected={selected}
        setAgent={setAgent}
        agentOpen={agentOpen}
        setAgentOpen={setAgentOpen}
        setDollarOff={setDollarOff}
        setDollarPick={setDollarPick}
        placeholder={placeholder}
        busy={busy}
        queued={queued}
        send={send}
        stop={stop}
        steerQueued={promoteQueued}
        popQueued={(itemId) => { setSendError(""); return popQueueItem.execute({ id, itemId }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "pop failed")); }}
        removeQueued={(itemId) => void removeQueueItem.execute({ id, itemId }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "remove failed"))}
        reorderQueued={(itemIds) => void reorderQueue.execute({ id, itemIds }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "reorder failed"))}
      />
    </>
  );
}

function ModEnterKeys({ mac }: { mac: boolean }) {
  const keys = mac ? ["⌘", "⏎"] : ["Ctrl", "Enter"];
  return (
    <span className="hidden items-center gap-0.5 sm:inline-flex">
      {keys.map((key) => (
        <kbd key={key} className="inline-flex h-3.5 min-w-3.5 items-center justify-center rounded-[2px] bg-muted px-1 text-[10px] font-medium text-muted-foreground">
          {key}
        </kbd>
      ))}
    </span>
  );
}

function SelectionQuote({ text, setText, messageInput, sending }: { text: string; setText: (value: string) => void; messageInput: React.RefObject<HTMLTextAreaElement | null>; sending: boolean }) {
  const [quote, setQuote] = useState<{ text: string; left: number; top: number }>();
  useEffect(() => {
    const clear = () => setQuote(undefined);
    const select = () => {
      const selection = window.getSelection();
      const transcript = document.getElementById("transcript-scroll");
      if (sending || !selection?.rangeCount || !selection.toString().trim()) return clear();
      const range = selection.getRangeAt(0);
      if (!transcript?.contains(range.startContainer) || !transcript.contains(range.endContainer)) return clear();
      const rect = range.getBoundingClientRect();
      setQuote({ text: selection.toString(), left: Math.max(8, Math.min(rect.left, window.innerWidth - 88)), top: Math.max(8, Math.min(rect.bottom + 6, window.innerHeight - 44)) });
    };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") clear(); };
    document.addEventListener("selectionchange", select);
    document.addEventListener("scroll", clear, true);
    document.addEventListener("keydown", escape);
    window.addEventListener("resize", clear);
    return () => {
      document.removeEventListener("selectionchange", select);
      document.removeEventListener("scroll", clear, true);
      document.removeEventListener("keydown", escape);
      window.removeEventListener("resize", clear);
    };
  }, [sending]);
  return quote && !sending ? <div className="fixed z-50" style={{ left: quote.left, top: quote.top }}>
    <Button type="button" size="sm" onMouseDown={(event) => { if (event.button === 0) event.preventDefault(); }} onClick={() => {
      const value = (text ? text + "\n\n" : "") + quote.text.replace(/\r\n?/g, "\n").split("\n").map((line) => `> ${line}`).join("\n") + "\n\n";
      flushSync(() => { setText(value); setQuote(undefined); });
      window.getSelection()?.removeAllRanges();
      messageInput.current!.focus();
      messageInput.current!.setSelectionRange(value.length, value.length);
    }}>Quote</Button>
  </div> : null;
}

function Composer({
  files,
  setFiles,
  sending,
  sessionId,
  text,
  setText,
  matches,
  pick,
  applyDollar,
  catalog,
  selected,
  setAgent,
  agentOpen,
  setAgentOpen,
  setDollarOff,
  setDollarPick,
  placeholder,
  busy,
  queued,
  send,
  stop,
  steerQueued,
  popQueued,
  removeQueued,
  reorderQueued,
}: {
  files: PendingFile[];
  setFiles: (files: PendingFile[]) => void;
  sending: boolean;
  sessionId: string;
  text: string;
  setText: (value: string) => void;
  matches: ReturnType<typeof dollarMatches>;
  pick: number;
  applyDollar: (invocation: string) => void;
  catalog: { name: string; model?: string; reasoning?: string }[];
  selected: string;
  setAgent: (name: string) => void;
  agentOpen: boolean;
  setAgentOpen: (open: boolean | ((value: boolean) => boolean)) => void;
  setDollarOff: (off: boolean) => void;
  setDollarPick: (pick: string) => void;
  placeholder: string;
  busy: boolean;
  queued: QueueItem[];
  send: (delivery?: PromptDelivery) => Promise<void>;
  stop: () => Promise<void>;
  steerQueued: (id: string, text: string) => Promise<void>;
  popQueued: (id: string) => Promise<unknown>;
  removeQueued: (id: string) => void;
  reorderQueued: (itemIds: string[]) => void;
}) {
  const mac = typeof navigator === "object" && /(Mac|iPod|iPhone|iPad)/.test(navigator.platform);
  const selectedButton = useRef<HTMLButtonElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const messageInput = useRef<HTMLTextAreaElement>(null);
  const { composer } = useContext(SessionCommands);
  useImperativeHandle(composer, () => (command) => {
    if (command === "stop") void stop();
    else if (command === "agent") setAgentOpen(true);
    else applyDollar(`$${command} ${text}`);
    messageInput.current?.focus();
  });
  const empty = text.trim() === "" && files.length === 0;
  const stopping = busy && empty;
  const pickerOpen = matches.length > 0;
  const selectedInvocation = matches[pick]?.invocation;
  useEffect(() => {
    selectedButton.current?.scrollIntoView({ block: "nearest" });
  }, [pickerOpen, selectedInvocation]);
  return (
    <div className="px-3 pb-4 sm:px-5">
      <SelectionQuote text={text} setText={setText} messageInput={messageInput} sending={sending} />
      <div className="relative mx-auto w-full max-w-3xl">
        {matches.length > 0 ? (
          <ul className="absolute inset-x-0 bottom-full z-10 mb-2 max-h-[50dvh] overflow-y-auto rounded-2xl border bg-popover text-popover-foreground shadow-md">
            {matches.map((cmd, index) => (
              <li key={cmd.invocation}>
                <button
                  ref={index === pick ? selectedButton : null}
                  type="button"
                  className={cn("flex w-full flex-col items-start gap-0.5 px-3 py-2 text-left text-sm", index === pick && "bg-accent")}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => applyDollar(cmd.invocation)}
                >
                  <span>
                    <span className="break-all font-medium">{cmd.invocation.trimEnd()}</span>
                    {cmd.hint ? <span className="text-muted-foreground"> {cmd.hint}</span> : null}
                  </span>
                  <span className="break-words text-xs text-muted-foreground">{cmd.desc}</span>
                </button>
              </li>
            ))}
          </ul>
        ) : null}
        <QueuePanel conversationId={sessionId} items={queued} busy={busy} onSteer={(id, itemText) => void steerQueued(id, itemText)} onPop={popQueued} onRemove={removeQueued} onReorder={reorderQueued} />
        <ComposerAttachments files={files} setFiles={setFiles} sending={sending} fileInput={fileInput}>
          <Textarea
            ref={messageInput}
            disabled={sending}
            value={text}
            onChange={(event) => {
              setDollarOff(false);
              setAgentOpen(false);
              setText(event.target.value);
            }}
            placeholder={placeholder}
            variant="embedded"
            className="min-h-16"
            onKeyDown={(event) => {
              if (event.key === "Enter" && event.altKey && (event.metaKey || event.ctrlKey) && !event.shiftKey) {
                event.preventDefault();
                void send("STASH");
                return;
              }
              if (matches.length > 0) {
                if (event.key === "ArrowDown") {
                  event.preventDefault();
                  setDollarPick(matches[(pick + 1) % matches.length].invocation);
                  return;
                }
                if (event.key === "ArrowUp") {
                  event.preventDefault();
                  setDollarPick(matches[(pick + matches.length - 1) % matches.length].invocation);
                  return;
                }
                if (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey)) {
                  event.preventDefault();
                  applyDollar(matches[pick].invocation);
                  return;
                }
                if (event.key === "Escape") {
                  event.preventDefault();
                  setDollarOff(true);
                  return;
                }
              }
              if (event.key === "Escape" && agentOpen) {
                event.preventDefault();
                setAgentOpen(false);
                return;
              }
              if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                void send(busy && (event.metaKey || event.ctrlKey) ? "STEER" : undefined);
              }
            }}
          />
          <div className="flex items-center gap-1 px-1 sm:gap-2">
            <div className="flex min-w-0 flex-1 items-center gap-1">
              <Tooltip><TooltipTrigger render={<Button type="button" variant="ghost" size="icon" className="size-11 sm:size-8" disabled={sending} />} aria-label="Add files" onClick={() => fileInput.current?.click()}><Plus /></TooltipTrigger><TooltipContent>Add files</TooltipContent></Tooltip>
              <Select items={catalog.map((item) => ({ value: item.name, label: item.name }))} value={selected} onValueChange={(value) => { if (value !== null) setAgent(value); }} open={agentOpen} onOpenChange={setAgentOpen} disabled={sending || catalog.length === 0}>
                <SelectTrigger aria-label="Choose agent" title={selected} className="min-h-11 min-w-0 max-w-36 sm:min-h-8"><Bot /><SelectValue className="min-w-0 overflow-hidden" placeholder="Choose agent" /></SelectTrigger>
                <SelectContent side="top" align="start" alignItemWithTrigger={false} className="w-[min(28rem,calc(100vw-2rem))]">
                  <SelectGroup>{catalog.map((item) => <SelectItem key={item.name} value={item.name} className="min-h-11 sm:min-h-8"><span className="flex min-w-0 max-w-[min(24rem,calc(100vw-5rem))] flex-col whitespace-normal"><span className="break-all">{item.name}</span><span className="break-all text-xs text-muted-foreground">{[item.model, item.reasoning].filter(Boolean).join(" · ")}</span></span></SelectItem>)}</SelectGroup>
                </SelectContent>
              </Select>
               <div className="hidden md:contents"><SessionHeaderActions id={sessionId} /></div>
            </div>
            <div className="flex shrink-0 items-center justify-end gap-1">
              <Button type="button" variant="ghost" className="h-11 sm:h-8" disabled={sending || empty} onClick={() => void send("STASH")}>Stash</Button>
              <Tooltip>
                <TooltipTrigger render={<Button type="button" variant="ghost" className="h-11 sm:h-8" disabled={!busy || sending || empty || isStopCommand(text)} />} aria-label="Steer" onClick={() => void send("STEER")}>
                  Steer
                </TooltipTrigger>
                <TooltipContent>Guide the current response <ModEnterKeys mac={mac} /></TooltipContent>
              </Tooltip>
              <Tooltip><TooltipTrigger render={<Button type="button" size="icon" className="size-11 sm:size-8" disabled={sending} />} aria-label={stopping ? "Stop" : "Send"} onClick={() => void (stopping ? stop() : send())}>
                {stopping ? <Square /> : <Send />}
              </TooltipTrigger><TooltipContent>{stopping ? "Stop response" : busy ? "Send to queue" : "Send message"}</TooltipContent></Tooltip>
            </div>
          </div>
        </ComposerAttachments>
      </div>
    </div>
  );
}

function ComposerAttachments({ files, setFiles, sending, fileInput, children }: {
  files: PendingFile[];
  setFiles: (files: PendingFile[]) => void;
  sending: boolean;
  fileInput: React.RefObject<HTMLInputElement | null>;
  children: ReactNode;
}) {
  const addFiles = (added: File[]) => setFiles([...files, ...added.map((file) => ({ id: crypto.getRandomValues(new Uint32Array(4)).join("-"), file }))]);
  return <div className="relative z-10 rounded-[22px] border bg-card p-2 shadow-[0_12px_28px_-18px_rgb(0_0_0/40%)]" onDragOver={(event) => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); }} onDrop={(event) => {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    if (!sending) addFiles(Array.from(event.dataTransfer.files));
  }}>
    <input ref={fileInput} type="file" multiple hidden aria-label="Attach files" disabled={sending} onChange={(event) => { addFiles(Array.from(event.target.files ?? [])); event.target.value = ""; }} />
    {files.length ? <AttachmentGroup aria-label="Pending attachments">{files.map(({ id, file }) => <Attachment key={id} state={sending ? "uploading" : "idle"}>
      <AttachmentMedia><FileIcon /></AttachmentMedia>
      <AttachmentContent><AttachmentTitle>{file.name}</AttachmentTitle><AttachmentDescription>{sending ? "Sending…" : `${file.size} bytes`}</AttachmentDescription></AttachmentContent>
      <AttachmentActions><AttachmentAction disabled={sending} aria-label={`Remove ${file.name}`} onClick={() => setFiles(files.filter((pending) => pending.id !== id))}><X /></AttachmentAction></AttachmentActions>
    </Attachment>)}</AttachmentGroup> : null}
    {children}
  </div>;
}

function cronWhen(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("en-US", { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" });
}

function cronAxisTime(ms: number) {
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function CronMarker({ label, tooltip, pct, ran, onClick }: { label: string; tooltip: string; pct: number; ran: boolean; onClick?: () => void }) {
  const [position, setPosition] = useState<{ left: number } | null>(null);
  const id = useId();
  function showTooltip(event: SyntheticEvent<HTMLElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    setPosition({ left: Math.max(8, Math.min(bounds.left, window.innerWidth - 232)) - bounds.left });
  }
  return (
    <span className={cn("absolute top-0 h-4 w-3 -translate-x-1/2", position && "z-50")} style={{ left: `${pct}%` }} onPointerEnter={(event) => { if (event.pointerType === "mouse") showTooltip(event); }} onPointerLeave={() => setPosition(null)}>
      <button type="button" aria-label={label} aria-describedby={position ? id : undefined} className="flex h-4 w-3 items-center justify-center rounded-sm focus-visible:outline-2"
        onFocus={showTooltip} onBlur={() => setPosition(null)} onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); setPosition(null); } }} onClick={(event) => { showTooltip(event); onClick?.(); }}>
        <span className={ran ? "h-3 w-2 rounded-sm bg-cyan-600 dark:bg-cyan-400" : "h-3 w-2 rounded-sm border-2 border-amber-600 dark:border-amber-400"} />
      </button>
      {position ? <span id={id} role="tooltip" style={position} className="absolute top-full z-50 w-max max-w-56 break-words rounded-md border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md">{tooltip}</span> : null}
    </span>
  );
}

function CronChatLink({ job, className, children }: { job: CronJob; className: string; children: ReactNode }) {
  const route = useRoute();
  const sidebar = useContext(Sidebar);
  const open = useAction({ action: mutations.createSession, onSuccess: (id) => {
    sidebar.invalidateQueries();
    invalidate("cronJobs");
    route.goSession(id);
  } });
  return job.nextRun ? <Link href={sessionPath(job.nextRun)} className={className}>{children}</Link> : <>
    <button type="button" className={className} disabled={open.isPending} onClick={() => open.fire({ sourceConversationId: job.origin })}>{open.isPending ? "Opening chat…" : children}</button>
    {open.error ? <p role="alert" className="text-sm text-destructive">{open.error.message}</p> : null}
  </>;
}

function CronRunPreview({ preview, onClose }: { preview: CronJob; onClose: () => void }) {
  const conversationId = preview.nextRun || preview.origin!;
  const history = useRemote(queries.history({ id: conversationId, sourceConversationId: preview.nextRun ? preview.origin : undefined }));
  const previewLines = useMemo(() => historyLines(history.data?.messages ?? []), [history.data]);
  const hasSandboxed = previewLines.some((line) => line.origin === "sandboxed");
  return (
    <section aria-label="Run preview" className="rounded-md border p-3">
      <div className="flex items-center justify-between gap-2">
        <CronChatLink job={preview} className="text-sm font-medium underline">{preview.stem} · {cronWhen(preview.lastRun)} UTC · Open chat →</CronChatLink>
        <Button variant="ghost" size="icon" aria-label="Close preview" onClick={onClose}><X className="h-4 w-4" /></Button>
      </div>
      {history.isLoading ? <p role="status" className="text-sm text-muted-foreground">Loading run…</p> : null}
      {history.error ? <p role="alert" className="text-sm text-destructive">{history.error.message}</p> : null}
      {!preview.nextRun ? <p className="text-sm text-muted-foreground">No delivered chat · Run traces</p> : null}
      <div className="mt-2 max-h-64 overflow-y-auto">
        {previewLines.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
        {history.data?.messages.length === 0 ? <p className="text-sm text-muted-foreground">No recorded messages for this run.</p> : null}
      </div>
    </section>
  );
}

function CronPage() {
  const route = useRoute();
  const jobs = useRemote(queries.cronJobs());
  const run = useAction({ action: mutations.runCron, onSuccess: (id, { stem }) => { invalidate("cronJobs"); if (id !== "") { notePendingCron(id, stem); route.goSession(id); } } });
  const [runQuery, setRunQuery] = useState("");
  const [confirmStem, setConfirmStem] = useState("");
  const confirmTrigger = useRef<HTMLButtonElement>(null);
  const [preview, setPreview] = useState<CronJob | null>(null);
  const rows = jobs.data ?? [];
  const defs = rows.filter((job) => job.status !== "ran");
  const runs = rows.filter((job) => job.status === "ran");
  const start = Date.now() - 12 * 3_600_000;
  const runNeedle = runQuery.trim().toLowerCase();
  const matchingStems = new Set(rows.filter((job) => runNeedle === "" || [
    ...Object.values(job).flat(),
    ...[job.lastRun, ...(job.upcoming ?? [])].filter(Boolean).map((at) => `${cronWhen(at)} UTC`),
    job.status === "ran" ? (job.nextRun ? "Open chat" : "No delivered chat") : "",
  ].join(" ").toLowerCase().includes(runNeedle)).map((job) => job.stem));
  const stems = [...new Set([...defs.map((job) => job.stem), ...runs.map((job) => job.stem)])].filter((stem) => matchingStems.has(stem));
  const groups = stems.map((stem) => ({
    stem,
    job: defs.find((item) => item.stem === stem),
    runs: runs.filter((item) => item.stem === stem),
  }));
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Cron</PageTitle>
      <Input aria-label="Search" value={runQuery} onChange={(event) => setRunQuery(event.target.value)} placeholder="Search" />
      {jobs.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {jobs.error ? <p className="text-sm text-destructive">{jobs.error.message}</p> : null}
      {run.error ? <p className="text-sm text-destructive">{run.error.message}</p> : null}
      <section className="flex flex-col gap-2">
        <div className="flex gap-4 text-xs text-muted-foreground"><span className="flex items-center gap-1"><span className="size-2 rounded-sm bg-cyan-600 dark:bg-cyan-400" />Ran</span><span className="flex items-center gap-1"><span className="size-2 rounded-sm border-2 border-amber-600 dark:border-amber-400" />Expected</span></div>
        <div className="flex items-end gap-2 text-[10px] tabular-nums text-muted-foreground">
          <span className="w-28 shrink-0" />
          <span className="relative h-4 min-w-0 flex-1">
            {[0, 6, 12, 18, 24, 30, 36].map((hour) => (
              <span key={hour} className="absolute -translate-x-1/2" style={{ left: `${(hour / 36) * 100}%` }}>
                {cronAxisTime(start + hour * 3_600_000)}
              </span>
            ))}
          </span>
        </div>
        {groups.map(({ stem, job, runs: jobRuns }) => (
          <div key={stem} className="flex items-center gap-2">
            <span className="w-28 shrink-0 truncate text-xs">{stem}</span>
            <div className="relative h-4 min-w-0 flex-1 rounded-sm bg-muted">
              <button type="button" className="absolute inset-0 rounded-sm focus-visible:outline-2" aria-label={`Preview latest run of ${stem}`} disabled={jobRuns.length === 0} onClick={() => setPreview(jobRuns[0] ?? null)} />
              {(job?.upcoming ?? []).map((at) => {
                const pct = ((Date.parse(at) - start) / (36 * 3_600_000)) * 100;
                if (!Number.isFinite(pct) || pct < 0 || pct > 100) {
                  return null;
                }
                return <CronMarker key={at} label={`Expected ${stem} at ${cronWhen(at)} UTC`} tooltip={`Expected ${stem} at ${cronWhen(at)} UTC`} pct={pct} ran={false} />;
              })}
              {jobRuns.map((item) => {
                const pct = ((Date.parse(item.lastRun) - start) / (36 * 3_600_000)) * 100;
                if (!Number.isFinite(pct) || pct < 0 || pct > 100) return null;
                return <CronMarker key={`${item.origin}:${item.nextRun}`} label={`Preview ${item.stem} at ${cronWhen(item.lastRun)} UTC`} tooltip={`Ran ${cronWhen(item.lastRun)} UTC${item.nextRun ? "" : " · No delivered chat"}`} pct={pct} ran onClick={() => setPreview(item)} />;
              })}
            </div>
          </div>
        ))}
      </section>
      {preview && matchingStems.has(preview.stem) ? <CronRunPreview preview={preview} onClose={() => setPreview(null)} /> : null}
      <section className="flex flex-col gap-1">
        {groups.map(({ stem, job, runs: jobRuns }) => (
          <section key={stem} aria-label={stem} className="border-b py-2">
            <details>
              <summary className="cursor-pointer text-sm font-medium">
              {job ? <Button
                size="icon"
                variant="ghost"
                className="mr-1 size-7 align-middle"
                aria-label={run.isPending && run.variables?.stem === stem ? `Running ${stem}` : `Run ${stem}`}
                title={`Run ${stem}`}
                disabled={run.isPending}
                onClick={(event) => {
                  event.preventDefault();
                  event.stopPropagation();
                  confirmTrigger.current = event.currentTarget;
                  setConfirmStem(stem);
                }}
              >
                {run.isPending && run.variables?.stem === job.stem ? <LoaderCircle aria-hidden="true" className="h-4 w-4 animate-spin motion-reduce:animate-none" /> : <Play aria-hidden="true" className="h-4 w-4" />}
              </Button> : null}
              {stem} <span className="text-xs text-muted-foreground">· {jobRuns.length} runs{job ? "" : " · Definition no longer available"}</span></summary>
            {job ? <details className="mt-2">
              <summary className="cursor-pointer text-xs text-muted-foreground">Definition</summary>
              <dl className="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 rounded-md border bg-muted/40 p-3 text-xs">
                {[["Schedule", job.schedule], ["Agent", job.agent], ["Channel", job.channel], ["Source", job.origin]].filter(([, value]) => value).map(([label, value]) => (
                  <div key={label} className="contents">
                    <dt className="font-medium text-muted-foreground">{label}</dt>
                    <dd className="whitespace-pre-wrap break-words font-mono">{value}</dd>
                  </div>
                ))}
              </dl>
              <pre className="mt-2 whitespace-pre-wrap text-xs text-muted-foreground">{job.body || "No body."}</pre>
            </details> : null}
            <ul className="mt-2 ml-3 flex flex-col gap-1 border-l pl-2">
              {jobRuns.map((item) => (
                <li key={`${item.origin}:${item.nextRun}`}>
                  <CronChatLink job={item} className="block rounded-md px-2 py-2 text-sm hover:bg-accent">
                    {cronWhen(item.lastRun)} UTC · {!item.nextRun ? "No delivered chat · " : ""}Open chat →
                  </CronChatLink>
                </li>
              ))}
            </ul>
            {jobRuns.length === 0 ? <p className="mt-2 text-xs text-muted-foreground">No runs yet.</p> : null}
            </details>
          </section>
        ))}
      </section>
      <Dialog open={confirmStem !== ""} onOpenChange={(open) => { if (!open) setConfirmStem(""); }}>
          <DialogContent finalFocus={confirmTrigger}>
            <DialogTitle>Run {confirmStem}?</DialogTitle>
            <DialogDescription>Start this cron job now?</DialogDescription>
            <div className="mt-6 flex justify-end gap-2">
              <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
              <Button onClick={() => {
                run.fire({ stem: confirmStem });
                setConfirmStem("");
              }}>Run</Button>
            </div>
          </DialogContent>
      </Dialog>
    </div>
  );
}

function AgentsPage() {
  const agents = useRemote(queries.agents());
  const [open, setOpen] = useState<string | null>(null);
  const rows = agents.data?.agents ?? [];
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Agents</PageTitle>
      {agents.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {agents.error ? <p className="text-sm text-destructive">{agents.error.message}</p> : null}
      {rows.length === 0 && !agents.isLoading && !agents.error ? <p className="text-sm text-muted-foreground">No agents configured.</p> : null}
      {rows.map((agent) => (
        <section key={agent.name} className="border-b pb-4">
          <button type="button" className="w-full text-left" onClick={() => setOpen(open === agent.name ? null : agent.name)}>
            <h2 className="text-sm font-medium">{agent.name}</h2>
            <p className="text-xs text-muted-foreground">{[agent.model, agent.reasoning, agent.verbosity, agent.origin].filter(Boolean).join(" · ")}</p>
            {agent.description ? <p className="mt-1 text-sm leading-relaxed">{agent.description}</p> : null}
          </button>
          {open === agent.name ? (
            <div className="mt-3 flex flex-col gap-3">
              {agent.permissions ? (
                <div>
                  <h3 className="text-xs font-medium text-muted-foreground">Permissions</h3>
                  <pre className="mt-1 whitespace-pre-wrap text-xs">{agent.permissions}</pre>
                </div>
              ) : null}
              {agent.prompt ? (
                <div>
                  <h3 className="text-xs font-medium text-muted-foreground">Prompt</h3>
                  <pre className="mt-1 whitespace-pre-wrap text-xs leading-relaxed">{agent.prompt}</pre>
                </div>
              ) : null}
            </div>
          ) : null}
        </section>
      ))}
    </div>
  );
}

function SkillsPage() {
  const skills = useRemote(queries.skills());
  const [open, setOpen] = useState<string | null>(null);
  const rows = skills.data ?? [];
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Skills</PageTitle>
      {skills.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {skills.error ? <p className="text-sm text-destructive">{skills.error.message}</p> : null}
      {rows.length === 0 && !skills.isLoading && !skills.error ? <p className="text-sm text-muted-foreground">No skills configured.</p> : null}
      {rows.map((skill) => (
        <section key={skill.name} className="border-b pb-4">
          <button type="button" className="w-full text-left" onClick={() => setOpen(open === skill.name ? null : skill.name)}>
            <h2 className="text-sm font-medium">{skill.name}</h2>
            <p className="text-xs text-muted-foreground">{[skill.license, skill.compatibility, skill.origin].filter(Boolean).join(" · ")}</p>
            {skill.description ? <p className="mt-1 text-sm leading-relaxed">{skill.description}</p> : null}
          </button>
          {open === skill.name && skill.content ? <pre className="mt-3 whitespace-pre-wrap text-xs leading-relaxed">{skill.content}</pre> : null}
        </section>
      ))}
    </div>
  );
}

type ConfigItem = { key: string; label: string; detail?: string[] };

function ConfigSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-1">
      <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</h2>
      <div className="flex flex-col">{children}</div>
    </section>
  );
}

function ConfigRow({ label, value }: { label: string; value: string }) {
  if (value === "") {
    return null;
  }
  return (
    <div className="flex items-center justify-between gap-4 border-b py-2">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span className="text-right text-sm">{value}</span>
    </div>
  );
}

function ConfigList({ items, empty }: { items: ConfigItem[]; empty: string }) {
  if (items.length === 0) {
    return <p className="py-2 text-sm text-muted-foreground">{empty}</p>;
  }
  return (
    <ul className="flex flex-col">
      {items.map((item) => (
        <li key={item.key} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b py-2">
          <span className="text-sm">{item.label}</span>
          {(item.detail ?? []).map((chip) => (
            <span key={chip} className="rounded-md bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
              {chip}
            </span>
          ))}
        </li>
      ))}
    </ul>
  );
}

function ConfigLoaded({ view }: { view: ConfigView }) {
  const identity = useRemote(queries.identity());
  const overlays: ConfigItem[] = (view.overlays ?? []).map((overlay) => ({ key: overlay, label: overlay }));
  const models: ConfigItem[] = (view.models ?? []).map((model, index) => ({
    key: `${model.name ?? ""}-${index}`,
    label: model.name ?? model.model ?? "",
    detail: model.name && model.model ? [model.model] : [],
  }));
  const channels: ConfigItem[] = (view.slackChannels ?? []).map((channel, index) => ({
    key: `${channel.channel ?? ""}-${index}`,
    label: channel.channel ?? "",
    detail: channel.agents ?? [],
  }));
  const servers: ConfigItem[] = (view.mcpServers ?? []).map((server) => ({ key: server, label: server }));
  return (
    <>
      <ConfigSection title="Runtime">
        <ConfigRow label="Workspace" value={view.workspace ?? ""} />
        <ConfigRow label="Logging" value={view.loggingLevel ?? ""} />
        <ConfigList items={overlays} empty="No overlays" />
      </ConfigSection>
      <ConfigSection title="Models">
        <ConfigRow label="Auto-approver" value={view.autoApproverModel ?? ""} />
        <ConfigList items={models} empty="No models" />
      </ConfigSection>
      <ConfigSection title="Web">
        <ConfigRow label="Configured user" value={identity.data ?? ""} />
        <ConfigRow label="Tailscale user" value={view.tailscaleUser || "Unavailable"} />
        <ConfigRow label="web.auto_settle_after" value={view.webAutoSettleAfter ?? ""} />
      </ConfigSection>
      <ConfigSection title="Slack">
        <ConfigList items={channels} empty="No channels" />
      </ConfigSection>
      <ConfigSection title="MCP">
        <ConfigList items={servers} empty="No servers" />
      </ConfigSection>
      <ConfigSection title="Flags">
        <ConfigRow label="Instrumentation" value={view.instrumentationEnabled ? "On" : "Off"} />
        <ConfigRow label="External MCP" value={view.mcpExternal ? "On" : "Off"} />
      </ConfigSection>
    </>
  );
}

function ConfigPage() {
  const config = useRemote(queries.config());
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Config</PageTitle>
      <ConfigSection title="Appearance">
        <div className="flex flex-wrap items-center justify-between gap-4 border-b py-2">
          <span className="text-sm text-muted-foreground">Theme</span>
          <PaletteChooser />
        </div>
      </ConfigSection>
      {config.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {config.error ? <p className="text-sm text-destructive">{config.error.message}</p> : null}
      {config.data ? <ConfigLoaded view={config.data} /> : null}
    </div>
  );
}
