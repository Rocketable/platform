"use client";

import { QueryClient, QueryClientProvider, useQuery, useQueries, useMutation } from "@tanstack/react-query";
import { Dialog, DialogTrigger, DialogContent, DialogTitle, DialogDescription, DialogClose } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Field, FieldGroup, FieldLabel, FieldError } from "@/components/ui/field";
import { queries, mutations, listSessions } from "./api";
import type { ChatOrigin, PromptDelivery } from "./types";
import { Bot, Calendar, Check, CircleAlert, Download, FileIcon, GripVertical, LoaderCircle, Mail, PanelLeftClose, PanelLeftOpen, Pin, Play, Plus, Search, Send, Settings, Sparkles, Square, SquarePen, TextCursorInput, Undo2, X } from "lucide-react";
import Link, { usePathname, navigate } from "./navigation";
import { createContext, useCallback, useContext, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode, type SyntheticEvent } from "react";
import { flushSync } from "react-dom";
import { PaletteChooser, ThemeToggle } from "@/components/theme";
import { CodeBlock, TranscriptText } from "./transcript-text";
import { Button } from "@/components/ui/button";
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

const queryClient = new QueryClient();
const tabReturnTo = { current: "/" };

function sessionPath(id: string) {
  return `/s/${encodeSessionId(id)}`;
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
    <button type="button" className="flex h-5 max-w-[8rem] shrink-0 items-center rounded-full bg-sidebar-row-active px-2 text-xs" onClick={onClear}>
      <span className="truncate">{label}</span>
    </button>
  );
}

const dollarCommands = [
  { name: "goal", hint: "<objective>", desc: "Start a goal loop" },
  { name: "stop", hint: "", desc: "End the active turn" },
  { name: "cron", hint: "[job]", desc: "List or run a cron job" },
  { name: "workflow", hint: "<name> [args]", desc: "Run a saved workflow" },
  { name: "agent", hint: "[name]", desc: "List or switch agent" },
  { name: "enqueue", hint: "<text>", desc: "Stash later work" },
  { name: "queue", hint: "", desc: "List pending steers and later work" },
  { name: "skill", hint: "<name> [args]", desc: "Invoke a skill by name" },
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
  const id = pathname.startsWith("/s/") ? decodeSessionId(pathname.slice(3)) : "";
  return {
    cron,
    agents,
    skills,
    config,
    settled,
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
      () => queryClient.prefetchQuery(queries.agents()),
      () => queryClient.prefetchQuery(queries.skills()),
      () => queryClient.prefetchQuery(queries.cronJobs()),
      () => queryClient.prefetchQuery(queries.config()),
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
  const proto = useQuery({ ...queries.protocol(), refetchInterval: 2000 });
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

function delay(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const finish = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      resolve();
    };
    const timer = setTimeout(finish, ms);
    signal.addEventListener("abort", finish, { once: true });
  });
}

function SidebarOwner({ children }: { children: ReactNode }) {
  const identity = useQuery({ ...queries.identity(), refetchInterval: 2000, retry: false });
  const protocol = useQuery({ ...queries.protocol(), retry: false });
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
    let stopped = false;
    void loadSavedSessions(currentOwner, currentProtocol).then((saved) => {
      if (stopped || captured !== gen.current || ownerRef.current !== currentOwner || committed.current || saved === undefined) {
        return;
      }
      const merged = mergeSessionRows(saved, rowsRef.current);
      rowsRef.current = merged;
      setView((current) => ({ ...current, rows: merged }));
    }, () => {});
    return () => { stopped = true; };
  }, [owner, protocol.data, identityRejected, identityGeneration]);
  useEffect(() => {
    if (owner === undefined || protocol.data === undefined || identityRejected) {
      return;
    }
    const captured = gen.current;
    const currentOwner = owner;
    const currentProtocol = protocol.data;
    const ac = new AbortController();
    const tick = async () => {
      if (ac.signal.aborted || captured !== gen.current) return;
      setView((current) => ({ ...current, refreshing: true, enumerationComplete: false, summariesComplete: true }));
      const snapshotGeneration = await loadSnapshotGeneration(currentOwner, currentProtocol).catch(() => undefined);
      if (ac.signal.aborted || captured !== gen.current) return;
      readSessionEnumeration(currentOwner, Promise.resolve(listSessions(ac.signal)), () => rowsRef.current, (merged, summaries, complete) => {
        if (ac.signal.aborted || captured !== gen.current) {
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
      }, ac.signal).then((result) => {
        if (ac.signal.aborted || captured !== gen.current) {
          return;
        }
        if (result.mismatch) {
          reset();
          ownerRef.current = undefined;
          // A fast same-owner refetch can hide the intermediate pending state.
          void queryClient.resetQueries({ queryKey: ["identity"] }).then(() => setIdentityGeneration((value) => value + 1));
        } else if (shouldCommitSnapshot(result)) {
          committed.current = true;
          rowsRef.current = result.received;
          setView({ rows: result.received, enumerationComplete: true, summariesComplete: true, refreshing: false, loadingIds: new Set() });
          void saveCompleteSessions(currentOwner, currentProtocol, result.received, snapshotGeneration).catch(() => {});
        } else {
          setView((current) => ({ ...current, enumerationComplete: result.exhausted && result.upstreamSuccess && !result.mismatch, refreshing: false }));
        }
      }).then(() => {
        if (!ac.signal.aborted && captured === gen.current) void delay(2000, ac.signal).then(tick);
      });
    };
    tick();
    return () => {
      ac.abort();
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
    <Sidebar.Provider value={value}>
      {children}
    </Sidebar.Provider>
  );
}

type PendingFile = { id: string; file: File };
type ComposerDraft = { text: string; files: PendingFile[]; agent: string; sessionId: string; sending: boolean; busy: boolean; lines: Line[]; parked?: Line[]; consumed?: Set<string>; error: string; edit: number; submission: number };

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

export function App() {
  const route = useRoute();
  const showChat = !route.cron && !route.agents && !route.skills && !route.config && !route.settled;
  const [sidebarOpen, setSidebarOpen] = useState(true);
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
      if (showChat || event.key !== "Escape") return;
      if (document.querySelector('[role="dialog"], [role="listbox"], [role="tooltip"]')) return;
      event.preventDefault();
      navigate(tabReturnTo.current);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [newChat, showChat]);
  if (showChat && conversation.id !== route.id) {
    // Creation assigns this conversation its ID; other navigation starts a fresh subtree.
    const created = conversation.id === "" && conversation.created === route.id;
    setConversation({ id: route.id, created: "", key: created ? conversation.key : conversation.key + 1 });
  }
  return (
      <QueryClientProvider client={queryClient}><TooltipProvider>
        <ProtocolGuard />
        <SidebarOwner>
          <CommandPalette newChat={newChat} sidebarOpen={sidebarOpen} onToggleSidebar={() => setSidebarOpen((open) => !open)} />
         <MobileSidebar chat={showChat}>
            <BottomNavigation>
              <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="hidden size-[var(--navigation-button)] md:inline-flex" />} aria-label={sidebarOpen ? "Hide sidebar" : "Show sidebar"} aria-expanded={sidebarOpen} aria-controls="session-sidebar" onClick={() => setSidebarOpen((open) => !open)}>
                {sidebarOpen ? <PanelLeftClose className="size-[var(--navigation-icon)]" /> : <PanelLeftOpen className="size-[var(--navigation-icon)]" />}
              </TooltipTrigger><TooltipContent side="top">{sidebarOpen ? "Hide sidebar" : "Show sidebar"}</TooltipContent></Tooltip>
              <SessionTabs returnTo={returnTo}>
                <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="New session" onClick={newChat}>
                  <SquarePen className="size-[var(--navigation-icon)]" />
                </TooltipTrigger><TooltipContent side="top">New session</TooltipContent></Tooltip>
              </SessionTabs>
            </BottomNavigation>
            <div className="fixed top-2 right-2 z-40 rounded-md bg-background shadow-sm"><ThemeToggle /></div>
          <div className="flex min-h-0 min-w-0 flex-1">
            <aside id="session-sidebar" className={cn("hidden w-64 shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground lg:w-72", sidebarOpen && "md:flex")}>
              <SessionList />
            </aside>
            <main className="flex min-h-0 min-w-0 flex-1 flex-col" data-sidebar-swipe="open">
              <WarmTabs cron={route.cron} agents={route.agents} skills={route.skills} config={route.config} />
              {route.settled ? <SessionList settledOnly /> : null}
              <TabPane show={showChat}>
                <MessageScrollerProvider key={conversation.key} autoScroll scrollEdgeThreshold={48}>
                  <Transcript id={conversation.id} drafts={drafts.current} onDraftChange={onDraftChange} onCreated={(id) => setConversation((current) => ({ ...current, created: id }))} />
                </MessageScrollerProvider>
              </TabPane>
             </main>
           </div>
         </MobileSidebar>
        </SidebarOwner>
      </TooltipProvider></QueryClientProvider>
  );
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
  origins: string[],
  id: string,
  updateSession: (input: { id: string; unread: boolean }) => void,
): { key: string; label: string; detail: string; keep?: boolean; run: () => void }[] {
  if (mode === "sessions") {
    return sidebar.rows.filter((session, index) => matchesSession(session, needle, "", "") || origins[index]?.includes(needle)).sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned)).map((session) => ({
      key: session.id,
      label: session.name || rowPreview(session, sidebar.loadingIds.has(session.id)).split("\n", 1)[0] || sessionLabel(session.id),
      detail: [session.settled ? "Settled" : "", session.agent, relativeTime(session.updatedAt ?? "")].filter(Boolean).join(" · "),
      run: () => navigate(sessionPath(session.id)),
    }));
  }
  if (mode === "cron") {
    return (jobs ?? []).filter((job) => job.status !== "ran").filter((job) => needle === "" || `${job.stem} ${job.schedule ?? ""} ${job.agent ?? ""} ${job.channel ?? ""}`.toLowerCase().includes(needle)).map((job) => ({
      key: job.stem,
      label: job.stem,
      detail: [job.schedule, job.agent, job.channel].filter(Boolean).join(" · "),
      keep: true,
      run: () => runStem(job.stem),
    }));
  }
  return [
    ...sidebar.rows.filter((session) => session.id === id).map((session) => ({ key: "toggle-unread", label: session.unread ? "Mark read" : "Mark unread", detail: "Current chat", keep: true, run: () => updateSession({ id, unread: !session.unread }) })),
    { key: "new", label: "New session", detail: "", run: newChat },
    { key: "run-cron", label: "Run cron", detail: "", keep: true, run: openCron },
    { key: "settled", label: "Settled", detail: "", run: () => navigate("/settled") },
    { key: "cron", label: "Cron", detail: "", run: () => navigate("/cron") },
    { key: "agents", label: "Agents", detail: "", run: () => navigate("/agents") },
    { key: "skills", label: "Skills", detail: "", run: () => navigate("/skills") },
    { key: "config", label: "Config", detail: "", run: () => navigate("/config") },
    { key: "sidebar", label: sidebarOpen ? "Hide sidebar" : "Show sidebar", detail: "", run: onToggleSidebar },
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

function CommandPalette({ newChat, sidebarOpen, onToggleSidebar }: { newChat: () => void; sidebarOpen: boolean; onToggleSidebar: () => void }) {
  const sidebar = useContext(Sidebar);
  const { id } = useRoute();
  const [mode, setMode] = useState<"sessions" | "commands" | "cron">();
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => { sidebar.invalidateQueries(); setMode(undefined); } });
  const [query, setQuery] = useState("");
  const [pick, setPick] = useState(0);
  const active = useRef<HTMLButtonElement>(null);
  const identity = useQuery(queries.identity());
  const protocol = useQuery(queries.protocol());
  const selectOrigin = useCallback(({ origin }: { origin?: ChatOrigin }) => !origin ? "" : (origin.kind === "cron"
    ? `Cron Source: ${origin.sourcePath} Stem: ${origin.stem} Run kind: ${origin.runKind} Run ID: ${origin.runId} Agent: ${origin.agent} Ran at: ${origin.ranAt}`
    : `External MCP External conversation: ${origin.externalConversationId} Agent: ${origin.agent} ${origin.pairs?.map(({ key, value }) => `${key}=${value}`).join(" ") ?? ""}`).toLowerCase(), []);
  // Ponytail: first search reads each visible chat's stored entries for its origin.
  // A selective origin projection can replace these reads if search volume demands it.
  const origins = useQueries({ queries: mode === "sessions" && query.trim() !== "" ? sidebar.rows.map(({ id }) => ({
    ...queries.history({ id, originOnly: true }),
    queryKey: ["sessionOrigin", identity.data, protocol.data, id],
    staleTime: 10_000,
    retry: false,
    select: selectOrigin,
  })) : [] });
  const jobs = useQuery({ ...queries.cronJobs(), staleTime: 10_000, enabled: mode === "commands" || mode === "cron" });
  const runCron = useMutation({
    mutationFn: mutations.runCron,
    onSuccess: (id, input) => {
      void queryClient.invalidateQueries({ queryKey: ["cronJobs"] });
      sidebar.invalidateQueries();
      if (id === "") return;
      notePendingCron(id, input.stem);
      navigate(sessionPath(id));
      setMode(undefined);
    },
  });
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || event.altKey || !(event.metaKey || event.ctrlKey) || event.code !== "KeyP") return;
      event.preventDefault();
      setQuery("");
      setPick(0);
      setMode(event.shiftKey ? "commands" : "sessions");
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);
  const items = mode === undefined ? [] : paletteRows(mode, query.trim().toLowerCase(), sidebar, jobs.data, newChat, sidebarOpen, onToggleSidebar, () => { setQuery(""); setPick(0); setMode("cron"); }, (stem) => runCron.mutate({ stem }), origins.map((origin) => origin.data ?? ""), id, update.mutate);
  const selected = items.length === 0 ? 0 : pick % items.length;
  const choose = (item: (typeof items)[number]) => {
    if (!item.keep) setMode(undefined);
    item.run();
  };
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [selected, mode]);
  const copy = paletteCopy(mode);
  const empty = (mode === "cron" && jobs.isLoading) || origins.some((origin) => origin.isPending) ? "Loading…" : "No matches";
  return (
    <Dialog open={mode !== undefined} onOpenChange={(open) => { if (!open) setMode(undefined); }}>
      <DialogContent showCloseButton={false} className="top-[20%] translate-y-0 overflow-hidden sm:max-w-lg">
        <DialogTitle className="sr-only">{copy.title}</DialogTitle>
        <DialogDescription className="sr-only">{copy.desc}</DialogDescription>
        <Input
          value={query}
          onChange={(event) => { setPick(0); setQuery(event.target.value); }}
          placeholder={copy.placeholder}
          variant="embedded"
          onKeyDown={(event) => setPick(paletteMove(event, selected, items.length, () => { if (items[selected]) choose(items[selected]); }))}
        />
        {runCron.error || update.error ? <p role="alert" className="px-3 text-sm text-destructive">{(runCron.error ?? update.error)?.message}</p> : null}
        {origins.some((origin) => origin.isError) ? <p role="alert" className="px-3 text-sm text-destructive">Could not search all chat origins.</p> : null}
        <ul className="max-h-[min(24rem,50vh)] overflow-y-auto p-1 [scrollbar-width:thin] [scrollbar-color:var(--muted-foreground)_transparent]">
          {items.length === 0 ? <li className="px-3 py-2 text-sm text-muted-foreground">{empty}</li> : items.map((item, index) => (
            <li key={item.key}>
              <button ref={index === selected ? active : null} type="button" className={cn("flex w-full flex-col items-start rounded-md px-3 py-2 text-left text-sm", index === selected && "bg-accent")} onMouseDown={(event) => event.preventDefault()} onClick={() => choose(item)}>
                <span className="font-medium">{item.label}</span>
                {item.detail ? <span className="text-xs text-muted-foreground">{item.detail}</span> : null}
              </button>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  );
}

function paletteCopy(mode: "sessions" | "commands" | "cron" | undefined) {
  if (mode === "cron") return { title: "Run cron", desc: "Search and run a cron job.", placeholder: "Search cron jobs" };
  if (mode === "commands") return { title: "Run command", desc: "Search and run a command.", placeholder: "Type a command" };
  return { title: "Go to session", desc: "Search and open a session.", placeholder: "Search sessions" };
}

function paletteMove(event: { key: string; preventDefault: () => void }, selected: number, count: number, enter: () => void) {
  if (event.key === "ArrowDown") {
    event.preventDefault();
    return selected + 1;
  }
  if (event.key === "ArrowUp") {
    event.preventDefault();
    return selected + Math.max(count, 1) - 1;
  }
  if (event.key === "Enter") {
    event.preventDefault();
    enter();
  }
  return selected;
}

function relativeTime(iso: string) {
  if (iso === "") {
    return "";
  }
  const ms = Date.now() - Date.parse(iso);
  if (!Number.isFinite(ms) || ms < 60_000) {
    return "now";
  }
  if (ms < 3_600_000) {
    return `${Math.floor(ms / 60_000)}m`;
  }
  if (ms < 86_400_000) {
    return `${Math.floor(ms / 3_600_000)}h`;
  }
  return `${Math.floor(ms / 86_400_000)}d`;
}

function SessionRow({
  session,
  current,
  loading,
  onSettle,
}: {
  session: Session;
  current: boolean;
  loading: boolean;
  onSettle: (settled: boolean) => void;
}) {
  const title = session.name || rowPreview(session, loading).split("\n", 1)[0] || sessionLabel(session.id);
  const channel = slackSession(session.id) ? (session.title ?? "") : "";
  const meta = [channel, session.agent, relativeTime(session.updatedAt ?? "")].filter(Boolean).join(" · ");
  return (
    <li className="group relative flex list-none items-stretch py-0.5">
      <Link
        href={sessionPath(session.id)}
        className={cn(
          "relative flex min-w-0 flex-1 cursor-pointer overflow-hidden rounded-md px-2.5 py-2 text-left outline-none select-none",
          current ? "bg-sidebar-row-active text-sidebar-foreground" : "text-sidebar-foreground hover:bg-sidebar-row-hover",
        )}
      >
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex items-center gap-1.5 text-sm font-medium">{session.unread ? <span role="img" aria-label="Unread" className="size-1.5 shrink-0 rounded-full bg-primary" /> : null}<span className="truncate">{title}</span></span>
          <span className="flex items-center gap-1 text-xs text-muted-foreground">
            <span className="inline-flex size-3 shrink-0">{session.running ? <LoaderCircle role="img" aria-label="Turn running" className="size-3 animate-spin motion-reduce:animate-none" /> : null}</span>
            <span className="truncate">{meta}</span>
          </span>
        </span>
      </Link>
      <div className="absolute top-1 right-1 flex rounded-md bg-sidebar shadow-sm opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto group-focus-within:opacity-100 group-focus-within:pointer-events-auto [@media(hover:none)]:opacity-100 [@media(hover:none)]:pointer-events-auto">
      <SessionDetails id={session.id} compact />
      <button
        type="button"
        className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-sidebar-row-hover hover:text-sidebar-foreground"
        aria-label={session.settled ? "Unsettle" : "Settle"}
        onClick={() => onSettle(!session.settled)}
      >
        {session.settled ? <Undo2 className="h-3.5 w-3.5" /> : <Check className="h-3.5 w-3.5" />}
      </button>
      </div>
    </li>
  );
}

function SessionPin({ session, compact = false }: { session: Session; compact?: boolean }) {
  const sidebar = useContext(Sidebar);
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => sidebar.invalidateQueries() });
  return <>
    <Tooltip>
      <TooltipTrigger render={<Button type="button" variant="ghost" size="icon" className={compact ? "" : "size-11 sm:size-8"} disabled={update.isPending} />} aria-label={session.pinned ? "Unpin session" : "Pin session"} aria-pressed={!!session.pinned} onClick={() => update.mutate({ id: session.id, pinned: !session.pinned })}>
        <Pin className={cn(session.pinned && "fill-current")} />
      </TooltipTrigger>
      <TooltipContent>{session.pinned ? "Unpin session" : "Pin session"}</TooltipContent>
    </Tooltip>
    {update.error ? <span role="alert" className="text-xs text-destructive">{update.error.message}</span> : null}
  </>;
}

function SessionDetails({ id, compact = false }: { id: string; compact?: boolean }) {
  const sidebar = useContext(Sidebar);
  const session = sidebar.rows.find((row) => row.id === id);
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const nameId = useId();
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => { sidebar.invalidateQueries(); setOpen(false); } });
  if (!session) return null;
  return <>
    <Dialog open={open} onOpenChange={setOpen}>
      <Tooltip>
        <TooltipTrigger render={<DialogTrigger render={<Button variant="ghost" size={compact ? "icon-sm" : "icon"} className={compact ? "" : "size-11 sm:size-8"} />} />} aria-label="Name session" onClick={() => { setName(session.name ?? ""); update.reset(); }}><TextCursorInput /></TooltipTrigger>
        <TooltipContent>Rename session</TooltipContent>
      </Tooltip>
        <DialogContent>
          <DialogTitle>Name session</DialogTitle>
          <DialogDescription>Shared with everyone who can see this session. Leave blank to show the last message.</DialogDescription>
          <form className="mt-4 flex flex-col gap-3" action={() => update.mutate({ id, name })}>
            <FieldGroup>
              <Field data-invalid={!!update.error}>
                <FieldLabel htmlFor={nameId}>Session name</FieldLabel>
                <Input id={nameId} name="name" value={name} aria-invalid={!!update.error} onChange={(event) => setName(event.target.value)} />
                {update.error ? <FieldError>{update.error.message}</FieldError> : null}
              </Field>
            </FieldGroup>
            <div className="flex justify-end gap-2">
              <DialogClose render={<Button variant="ghost" />}>Cancel</DialogClose>
              <Button type="submit" disabled={update.isPending}>{update.isPending ? "Saving…" : "Save"}</Button>
            </div>
          </form>
        </DialogContent>
    </Dialog>
    <SessionPin session={session} compact={compact} />
    <Tooltip>
      <TooltipTrigger render={<Button variant="ghost" size={compact ? "icon-sm" : "icon"} className={compact ? "" : "size-11 sm:size-8"} disabled={update.isPending} />} aria-label="Mark unread" onClick={() => update.mutate({ id, unread: true })}><Mail /></TooltipTrigger>
      <TooltipContent>Mark unread</TooltipContent>
    </Tooltip>
    {update.error && !open ? <span role="alert" className="text-xs text-destructive">{update.error.message}</span> : null}
  </>;
}

function matchesSession(session: Session, needle: string, agentFilter: string, roomFilter: string) {
  if (agentFilter !== "" && (session.agent ?? "") !== agentFilter) return false;
  if (roomFilter !== "" && (slackSession(session.id) ? (session.title ?? "") : "") !== roomFilter) return false;
  return needle === "" || `${session.name ?? ""} ${session.title ?? ""} ${session.preview ?? ""} ${session.agent ?? ""} ${sessionLabel(session.id)}`.toLowerCase().includes(needle);
}

function SidebarFreshness({ sidebar, emptySearch, emptyLabel = "No matches" }: { sidebar: SidebarView; emptySearch: boolean; emptyLabel?: string }) {
  const authoritative = searchIsAuthoritative(sidebar);
  return <>
    {emptySearch ? <p role="status" className="px-3 pb-1 text-xs text-muted-foreground">{authoritative ? emptyLabel : "loading..."}</p> : null}
  </>;
}

function SessionSearch({ rows, catalog, query, setQuery, agentFilter, setAgentFilter, roomFilter, setRoomFilter }: {
  rows: Session[];
  catalog: { name: string }[];
  query: string;
  setQuery: (value: string) => void;
  agentFilter: string;
  setAgentFilter: (value: string) => void;
  roomFilter: string;
  setRoomFilter: (value: string) => void;
}) {
  const sidebar = useContext(Sidebar);
  const stale = !sidebar.refreshing && rows.length > 0 && !searchIsAuthoritative(sidebar);
  const [overlayPick, setOverlayPick] = useState(0);
  const agentPrefix = typedPrefix(query, "agent:");
  const roomPrefix = typedPrefix(query, "room:");
  const choices = overlayChoices(agentPrefix, roomPrefix, catalog, roomPrefix === null ? [] : slackRooms(rows));
  const pick = choices.length === 0 ? 0 : overlayPick % choices.length;
  const applyOverlay = (name: string) => {
    if (agentPrefix !== null) {
      setAgentFilter(name);
    } else {
      setRoomFilter(name);
    }
    setQuery("");
    setOverlayPick(0);
  };
  return (
        <div className="relative min-w-0 flex-1">
          {choices.length > 0 ? <ul className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-lg border bg-popover text-popover-foreground shadow-md">
            {choices.map((item, index) => (
              <li key={item.key}>
                <button type="button" className={cn("flex w-full flex-col items-start px-3 py-2 text-left text-sm", index === pick && "bg-accent")} onMouseDown={(event) => event.preventDefault()} onClick={() => applyOverlay(item.key)}>
                  <span className="font-medium">{item.label}</span>
                  {item.detail ? <span className="text-xs text-muted-foreground">{item.detail}</span> : null}
                </button>
              </li>
            ))}
          </ul> : null}
          <div className="flex h-8 min-w-0 items-center gap-1 rounded-md border border-sidebar-border bg-background pr-2 pl-2">
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
              value={query}
              onChange={(event) => {
                setOverlayPick(0);
                setQuery(event.target.value);
              }}
              placeholder={agentFilter || roomFilter ? "Search" : "Search or agent: or room:"}
              variant="embedded"
              className="h-full min-w-0 flex-1"
              onKeyDown={(event) => {
                if (choices.length > 0) {
                  if (event.key === "ArrowDown") {
                    event.preventDefault();
                    setOverlayPick(pick + 1);
                    return;
                  }
                  if (event.key === "ArrowUp") {
                    event.preventDefault();
                    setOverlayPick(pick + choices.length - 1);
                    return;
                  }
                  if (event.key === "Tab" || event.key === "Enter") {
                    event.preventDefault();
                    applyOverlay(choices[pick].key);
                    return;
                  }
                  if (event.key === "Escape") {
                    event.preventDefault();
                    setQuery("");
                  }
                }
              }}
            />
          </div>
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

function SessionList({ settledOnly = false }: { settledOnly?: boolean }) {
  const sidebar = useContext(Sidebar);
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000 });
  const settle = useMutation({ mutationFn: mutations.settleSession, onSuccess: () => sidebar.invalidateQueries() });
  const route = useRoute();
  const [query, setQuery] = useState("");
  const [agentFilter, setAgentFilter] = useState("");
  const [roomFilter, setRoomFilter] = useState("");
  const catalog = agents.data?.agents ?? [];
  const rows = sidebar.rows;
  const includeSettled = /(?:^|\s)is:settled(?=\s|$)/i.test(query);
  const pinnedOnly = /(?:^|\s)is:pinned(?=\s|$)/i.test(query);
  const unreadOnly = /(?:^|\s)is:unread(?=\s|$)/i.test(query);
  const text = query.replace(/(?:^|\s)is:(?:settled|pinned|unread)(?=\s|$)/gi, " ").trim();
  const needle = typedPrefix(text, "agent:") === null && typedPrefix(text, "room:") === null ? text.toLowerCase() : "";
  const filtered = rows.filter((session) => (settledOnly ? session.settled : includeSettled || pinnedOnly || unreadOnly || !session.settled) && (!pinnedOnly || session.pinned) && (!unreadOnly || session.unread) && matchesSession(session, needle, agentFilter, roomFilter)).sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned));
  const searching = [needle, agentFilter, roomFilter, includeSettled, pinnedOnly, unreadOnly].some(Boolean);
  return (
    <div className={cn("flex h-full min-h-0 flex-col", settledOnly && "mx-auto w-full max-w-3xl gap-6 p-4")}>
      {settledOnly ? <PageTitle>Settled</PageTitle> : null}
      <div className={cn("flex shrink-0 items-center gap-1", !settledOnly && "p-2")}>
        <SessionSearch rows={rows} catalog={catalog} query={query} setQuery={setQuery} agentFilter={agentFilter} setAgentFilter={setAgentFilter} roomFilter={roomFilter} setRoomFilter={setRoomFilter} />
      </div>
      <SidebarFreshness sidebar={sidebar} emptySearch={filtered.length === 0 && (settledOnly || searching)} emptyLabel={searching ? "No matches" : "No settled chats"} />
      {settle.error ? <p role="alert" className="text-sm text-destructive">{settle.error.message}</p> : null}
      <ul className="flex min-h-0 flex-1 flex-col overflow-y-auto px-2 pb-2">
        {filtered.map((session) => (
          <SessionRow key={session.id} session={session} current={route.id === session.id} loading={sidebar.loadingIds.has(session.id)} onSettle={(next) => settle.mutate({ id: session.id, settled: next })} />
        ))}
      </ul>
    </div>
  );
}

function SessionTabs({ returnTo, children }: { returnTo: string; children: ReactNode }) {
  const route = useRoute();
  return (
      <div className="min-w-0 max-w-full justify-self-center overflow-x-auto overflow-y-hidden scrollbar-none">
      <div className="flex w-max items-center gap-[var(--navigation-gap)]">
        {children}
        <Tooltip><TooltipTrigger render={<Button variant={route.settled ? "default" : "ghost"} aria-current={route.settled} size="icon" className="size-[var(--navigation-button)]" role="link" nativeButton={false} render={<Link href={route.settled ? returnTo : "/settled"} />} />} aria-label="Settled">
          <Check className="size-[var(--navigation-icon)]" />
        </TooltipTrigger><TooltipContent side="top">Settled</TooltipContent></Tooltip>
        <Tooltip><TooltipTrigger render={<Button variant={route.cron ? "default" : "ghost"} aria-current={route.cron} size="icon" className="size-[var(--navigation-button)]" role="link" nativeButton={false} render={<Link href={route.cron ? returnTo : "/cron"} />} />} aria-label="Cron" onMouseEnter={() => void queryClient.prefetchQuery(queries.cronJobs())}>
            <Calendar className="size-[var(--navigation-icon)]" />
        </TooltipTrigger><TooltipContent side="top">Cron</TooltipContent></Tooltip>
        <Tooltip><TooltipTrigger render={<Button variant={route.agents ? "default" : "ghost"} aria-current={route.agents} size="icon" className="size-[var(--navigation-button)]" role="link" nativeButton={false} render={<Link href={route.agents ? returnTo : "/agents"} />} />} aria-label="Agents" onMouseEnter={() => void queryClient.prefetchQuery(queries.agents())}>
            <Bot className="size-[var(--navigation-icon)]" />
        </TooltipTrigger><TooltipContent side="top">Agents</TooltipContent></Tooltip>
        <Tooltip><TooltipTrigger render={<Button variant={route.skills ? "default" : "ghost"} aria-current={route.skills} size="icon" className="size-[var(--navigation-button)]" role="link" nativeButton={false} render={<Link href={route.skills ? returnTo : "/skills"} />} />} aria-label="Skills" onMouseEnter={() => void queryClient.prefetchQuery(queries.skills())}>
            <Sparkles className="size-[var(--navigation-icon)]" />
        </TooltipTrigger><TooltipContent side="top">Skills</TooltipContent></Tooltip>
        <Tooltip><TooltipTrigger render={<Button variant={route.config ? "default" : "ghost"} aria-current={route.config} size="icon" className="size-[var(--navigation-button)]" role="link" nativeButton={false} render={<Link href={route.config ? returnTo : "/config"} />} />} aria-label="Config" onMouseEnter={() => void queryClient.prefetchQuery(queries.config())}>
            <Settings className="size-[var(--navigation-icon)]" />
        </TooltipTrigger><TooltipContent side="top">Config</TooltipContent></Tooltip>
      </div>
      </div>
  );
}

type Line = { id: string; text: string; role: "user" | "assistant" | "thinking" | "tool" | "developer"; turnId?: string; streamText?: string; toolCallId?: string; toolName?: string; toolParts?: Line[]; attachments?: (AttachmentMeta & { file?: File })[] };

function lineId(role: Line["role"], text: string, seen: Map<string, number>) {
  const base = `${role}:${text}`;
  const n = (seen.get(base) ?? 0) + 1;
  seen.set(base, n);
  return `${base}:${n}`;
}

function appendLine(current: Line[], role: Line["role"], text: string, tool: Pick<Line, "toolCallId" | "toolName" | "attachments"> = {}) {
  const seen = new Map<string, number>();
  for (const line of current) {
    seen.set(`${line.role}:${line.text}`, (seen.get(`${line.role}:${line.text}`) ?? 0) + 1);
  }
  const rows = role === "thinking" ? text.split("\n").map((row) => row.trim()).filter(Boolean) : [text];
  return [...current, ...rows.map((row) => ({ id: lineId(role, row, seen), text: row, role, ...(role === "thinking" ? {} : tool) }))];
}

function transcriptTurns(lines: Line[]) {
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
    const resultCall = line.role === "tool" ? calls.get(line.toolCallId ?? "") : undefined;
    const skillHeader = line.text.split("\n", 1)[0];
    const skillCall = line.role === "developer" ? skills.get(skillHeader) : undefined;
    if (line.role === "user") {
      flush();
      current.user.push(line);
    } else if (line.role === "assistant") {
      current.replies.push(line);
    } else if (line.role === "tool" && line.toolName) {
      const call = { ...line, toolParts: [] as Line[] };
      current.traces.push(call);
      if (line.toolCallId) calls.set(line.toolCallId, call);
    } else if (resultCall) {
      resultCall.toolParts.push(line);
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

function TranscriptLine({ line, conversationId }: { line: Line; conversationId: string }) {
  if (line.role === "thinking") {
    return (
      <div className="flex items-center gap-1.5 px-1 py-0.5 text-[12px] leading-5 text-muted-foreground">
        <Bot className="size-3.5 shrink-0 opacity-80" />
        <span className="min-w-0 whitespace-pre-wrap break-words">{line.text}</span>
      </div>
    );
  }
  if (line.role === "tool") {
    const title = toolTitle(line);
    const parts = [line, ...(line.toolParts ?? [])];
    const text = parts.map((part, index) => {
      const label = index === 0 ? line.toolName ? "Arguments" : "Result" : part.role === "developer" ? "Skill instructions" : "Result";
      const body = index === 0 && line.toolName ? line.text.slice(line.toolName.length + 1) : part.text;
      return `${label}\n${body}`;
    }).join("\n\n");
    return (
      <details open className="mb-3 min-w-0">
        <summary className="cursor-pointer px-3 py-2 text-xs font-medium" title={title}>
          <span className="ml-1 inline-block max-w-[calc(100%-1.5rem)] truncate align-middle font-mono">{title}</span>
        </summary>
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
      </div>
    );
  }
  return (
    <Message align={line.role === "user" ? "end" : undefined} className="mb-4">
      <MessageContent>
        <Bubble variant={line.role === "user" ? "secondary" : "ghost"} align={line.role === "user" ? "end" : undefined}>
          <BubbleContent><TranscriptText text={line.text} /></BubbleContent>
          <MessageAttachments attachments={line.attachments} conversationId={conversationId} />
        </Bubble>
      </MessageContent>
    </Message>
  );
}

function TranscriptLog({
  conversationId,
  lines,
  thinking,
  origin,
}: {
  lines: Line[];
  conversationId: string;
  thinking: boolean;
  origin?: ChatOrigin;
}) {
  const turns = transcriptTurns(lines);
  const { scrollToMessage } = useMessageScroller();
  const turnNodes = useRef<(HTMLElement | null)[]>([]);
  const running = usePendingCron(conversationId);
  useEffect(() => { if (running && lines.length > 0) notePendingCron(conversationId, ""); }, [running, lines.length, conversationId]);
  const jumpToTurn = (index: number) => {
    scrollToMessage(`turn-${index}`, { align: "start", behavior: "instant" });
    turnNodes.current[index]?.focus({ preventScroll: true });
  };
  return (
    <MessageScroller className="flex-1">
    <MessageScrollerViewport id="transcript-scroll" className="overflow-x-hidden [overflow-anchor:none]">
      <div className="min-h-full pl-3 pr-8 pt-3 pb-4 sm:pl-5 sm:pr-10 sm:pt-4">
      <MessageScrollerContent className="mx-auto w-full min-w-0 max-w-3xl">
      {origin && (origin.kind === "cron" || origin.kind === "external_mcp") ? <MessageScrollerItem messageId="origin"><OriginCard origin={origin} /></MessageScrollerItem> : null}
      {lines.length === 0 && !thinking ? (
        <MessageScrollerItem className="flex flex-1 items-center justify-center">
          <p role="status" className="text-sm text-muted-foreground">{running ? `${running} is running` : "Send a message to start the conversation."}</p>
        </MessageScrollerItem>
      ) : (
        <>
          {turns.map((turn, index) => (
              <MessageScrollerItem key={turn.user[0]?.id ?? turn.traces[0]?.id ?? turn.replies[0]?.id} messageId={`turn-${index}`} ref={(node) => { turnNodes.current[index] = node; }} role="region" aria-label={`Turn ${index + 1}`} tabIndex={-1}>
                {turn.user.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} />)}
                {turn.traces.length > 0 ? (
                  <details open className="group pb-3">
                    <summary className="flex w-fit cursor-pointer list-none items-center gap-1 px-1 py-2 text-xs text-muted-foreground hover:text-foreground [&::-webkit-details-marker]:hidden">
                      Thinking <span aria-hidden="true" className="transition-transform group-open:rotate-90">▸</span>
                    </summary>
                    <div className="ml-1 border-l pl-3">
                      {turn.traces.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} />)}
                    </div>
                  </details>
                ) : null}
                {turn.replies.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} />)}
              </MessageScrollerItem>
          ))}
          {thinking ? <MessageScrollerItem><p className="px-1 pb-4 text-sm text-muted-foreground">Thinking…</p></MessageScrollerItem> : null}
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

function nextLines(current: Line[], payload: TranscriptEvent): Line[] {
  if (payload.role === "user" && payload.messageId) {
    if (current.some((line) => line.id === payload.messageId)) return current;
    return [...current, { id: payload.messageId, role: "user", text: payload.text, attachments: payload.attachments }];
  }
  const role = payload.role === "thinking" || payload.role === "user" || payload.role === "tool" || payload.role === "developer" ? payload.role : "assistant";
  const boundary = current.findLastIndex((line) => line.role === "user");
  const matches = (line: Line) => !!payload.turnId && line.turnId === payload.turnId && line.role === role;
  const prefix = boundary < 0 ? "" : current.slice(0, boundary).findLast(matches)?.streamText ?? "";
  const text = prefix && payload.text.startsWith(prefix) ? payload.text.slice(prefix.length).trimStart() : payload.text;
  const index = current.findIndex((line, i) => i > boundary && matches(line));
  const retained = index < 0 ? current : current.filter((line, i) => i <= boundary || !matches(line));
  const attachments = [...new Map([...(current[index]?.attachments ?? []), ...(payload.attachments ?? [])].map((file) => [file.id, file])).values()];
  if (text.trim() === "" && attachments.length === 0) return retained;
  const added = appendLine(retained, role, text, { toolCallId: payload.toolCallId, toolName: payload.toolName, attachments });
  const updated = added.slice(retained.length).map((line) => ({ ...line, turnId: payload.turnId, streamText: payload.text }));
  const position = index < 0 ? retained.length : index;
  return [...retained.slice(0, position), ...updated, ...retained.slice(position)];
}

function applyStreamEvent(
  payload: TranscriptEvent,
  setBusy: (value: boolean) => void,
  setLines: (update: (current: Line[]) => Line[]) => void,
) {
  if (payload.role === "user" && payload.messageId) {
    setBusy(true);
  } else if (payload.complete) {
    setBusy(false);
  }
  setLines((current) => nextLines(current, payload));
}

async function readTranscriptHistory(draft: ComposerDraft, request: Promise<TranscriptEvent[]>, onDraftChange: () => void, preserveLive = false) {
  const before = draft.lines;
  const messages = await request;
  // Saved history has no live IDs: it cannot safely replace or merge a newer stream.
  if (draft.lines !== before || draft.sending || preserveLive) {
    // Attachment IDs can confirm stored files without guessing input identity.
    const files = new Map(messages.flatMap((message) => message.attachments ?? []).map((file) => [file.id, file]));
    if (files.size === 0) return messages;
    draft.lines = draft.lines.map((line) => ({ ...line, attachments: line.attachments?.map((file) => files.get(file.id) ?? file) }));
    onDraftChange();
  } else {
    const seen = new Map<string, number>();
    draft.lines = messages.map((message): Line => {
      const role = message.role === "thinking" || message.role === "user" || message.role === "tool" || message.role === "developer" ? message.role : "assistant";
      return { id: lineId(role, message.text, seen), role, text: message.text, toolCallId: message.toolCallId, toolName: message.toolName, attachments: message.attachments };
    });
    onDraftChange();
  }
  return messages;
}

function useSessionStream(id: string, draft: ComposerDraft, onDraftChange: () => void) {
  const sidebar = useContext(Sidebar);
  const active = useRoute().id === id && id !== "";
  const opened = useRef(false);
  // Let an initial sidebar load finish; later reads can refresh its completed list.
  const read = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => { if (sidebar.enumerationComplete) sidebar.invalidateQueries(); } });
  const historyQuery = queries.history({ id });
  const pendingCron = usePendingCron(id);
  const history = useQuery({ ...historyQuery, queryFn: async ({ signal }) => {
    const view = historyQuery.queryFn({ signal });
    await readTranscriptHistory(draft, view.then((value) => value.messages), onDraftChange, draft.busy && draft.lines.length > 0);
    return view;
  }, enabled: id !== "", refetchOnWindowFocus: false, retry: false, refetchInterval: pendingCron ? 2000 : false });
  const historyReady = history.data !== undefined;
  useEffect(() => {
    if (!active) opened.current = false;
    else if (history.isSuccess && !opened.current) {
      opened.current = true;
      read.mutate({ id, unread: false });
    }
  }, [active, history.isSuccess, id, read.mutate]);
  const reconnectHistory = history.refetch;
  // History errors belong to the query; a confirmed Prompt must not become a retry.
  const refreshHistory = useCallback(() => readTranscriptHistory(draft, queryClient.fetchQuery(queries.history({ id })).then((view) => view.messages), onDraftChange).catch(() => {}), [id, draft, onDraftChange]);
  const setBusy = useCallback((value: boolean) => { draft.busy = value; onDraftChange(); }, [draft, onDraftChange]);
  const setLines = useCallback((update: (current: Line[]) => Line[]) => { draft.lines = update(draft.lines); onDraftChange(); }, [draft, onDraftChange]);
  useEffect(() => {
    if (!id || !historyReady) return;
    const stream = new EventSource(`/stream?${new URLSearchParams({ id })}`);
    let connected = false;
    stream.onopen = () => {
      if (connected) void reconnectHistory();
      connected = true;
    };
    stream.onmessage = (event) => {
      const payload = JSON.parse(String(event.data)) as TranscriptEvent;
      if (payload.role === "user" && payload.messageId) {
        if (draft.consumed?.has(payload.messageId)) return;
        (draft.consumed ??= new Set()).add(payload.messageId);
        draft.parked = draft.parked?.filter((line) => line.id !== payload.messageId);
        void queryClient.invalidateQueries({ queryKey: ["queue"] });
      }
      applyStreamEvent(payload, setBusy, setLines);
    };
    return () => {
      stream.close();
    };
  }, [id, historyReady, reconnectHistory, draft, setBusy, setLines]);
  return { busy: draft.busy, setBusy, lines: draft.lines, setLines, refreshHistory, opening: id !== "" && !history.data, historyError: history.error?.message ?? read.error?.message, origin: history.data?.origin };
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
  const { busy, setBusy, lines, setLines, refreshHistory, opening, historyError, origin } = useSessionStream(id, draft, onDraftChange);
  return (
    <>
      <TranscriptLog conversationId={id} lines={lines} thinking={busy && lines.at(-1)?.role !== "thinking"} origin={origin} />
      {historyError ? <p role="alert" className="px-3 text-sm text-destructive">{historyError}</p> : null}
      <fieldset disabled={opening} className="contents">
        <SessionComposer id={id} draft={draft} drafts={drafts} onDraftChange={onDraftChange} busy={busy} setBusy={setBusy} lines={lines} setLines={setLines} refreshHistory={refreshHistory} />
      </fieldset>
    </>
  );
}

async function sendComposer(input: {
  draft: ComposerDraft;
  onDraftChange: () => void;
  text: string;
  files: PendingFile[];
  delivery?: PromptDelivery;
  busy: boolean;
  working: boolean;
  sessionId: string;
  selected: string;
  currentAgent: string;
  goSession: (id: string) => void;
  prompt: { mutateAsync: (value: { id: string; text: string; delivery?: PromptDelivery; attachmentIds?: string[]; messageId?: string }) => Promise<string> };
  create: { mutateAsync: (value: { agent?: string }) => Promise<string> };
  scrollToEnd: () => boolean;
  setBusy: (value: boolean) => void;
  setAgentOpen: (value: boolean) => void;
  setSendError: (value: string) => void;
  setLines: (update: (current: Line[]) => Line[]) => void;
  refreshHistory: () => Promise<unknown>;
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
  const followUp = input.delivery ?? (input.working ? "QUEUE" : "STEER");
  const enqueue = stashing || (followUp === "QUEUE" || /^\s*\$enqueue(?:\s|$)/.test(input.text)) && !stopping;
  if (!input.busy && !enqueue) {
    input.setBusy(true);
  }
  input.scrollToEnd();
  input.setSendError("");
  const optimistic: Line = { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), role: "user", text: input.text };
  try {
    let sessionId = input.sessionId;
    if (sessionId === "") {
      sessionId = await input.create.mutateAsync({ agent: input.selected });
      input.goSession(sessionId);
    }
    optimistic.attachments = input.files.map(({ id, file }) => ({ id, name: file.name, mimeType: file.type, size: String(file.size), conversationId: sessionId, file }));
    if (!enqueue && input.working && !stopping) {
      draft.parked = [...(draft.parked ?? []), optimistic];
      input.onDraftChange();
    } else if (!enqueue) input.setLines((current) => [...current, optimistic]);
    const attachments: AttachmentMeta[] = await Promise.all(input.files.map(async ({ file }) => {
      const response = await fetch(`/api/UploadAttachment?${new URLSearchParams({ conversationId: sessionId, name: file.name })}`, { method: "POST", body: file });
      if (!response.ok) throw new Error(`Upload failed: ${file.name}`);
      return response.json();
    }));
    if (!stashing && input.sessionId !== "" && input.selected !== "" && input.selected !== input.currentAgent) {
      await input.prompt.mutateAsync({ id: sessionId, text: `$agent ${input.selected}` });
      await queryClient.invalidateQueries({ queryKey: ["agents"] });
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
    const response = input.prompt.mutateAsync({ id: sessionId, text: input.text, delivery: followUp, messageId: optimistic.id, ...(attachments.length ? { attachmentIds: attachments.map((file) => file.id) } : {}) });
    draft.sending = false;
    input.onDraftChange();
    input.setAgentOpen(false);
    const privateText = await response;
    void queryClient.invalidateQueries({ queryKey: ["agents"] });
    if (enqueue) {
      await queryClient.invalidateQueries({ queryKey: ["queue"] });
      return;
    }
    if (draft.parked?.some((line) => line.id === optimistic.id)) {
      await input.refreshHistory();
      draft.parked = draft.parked.filter((line) => line.id !== optimistic.id);
      (draft.consumed ??= new Set()).add(optimistic.id);
      input.onDraftChange();
    }
    if (privateText) {
      input.setLines((current) => appendLine(current, "assistant", privateText));
    }
    if ((privateText || stopping) && draft.submission === submission) {
      input.setBusy(false);
    }
  } catch (err) {
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
  }
}

async function promoteComposer(input: {
  draft: ComposerDraft;
  id: string;
  itemId: string;
  busy: boolean;
  steerQueueItem: { mutateAsync: (value: { id: string; itemId: string }) => Promise<unknown> };
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
  try {
    await input.steerQueueItem.mutateAsync({ id: input.id, itemId: input.itemId });
  } catch (err) {
    if (input.draft.submission === submission) {
      input.setSendError(err instanceof Error ? err.message : "steer failed");
      input.setBusy(input.busy);
    }
  }
}

async function stopComposer(input: {
  draft: ComposerDraft;
  id: string;
  busy: boolean;
  prompt: { mutateAsync: (value: { id: string; text: string }) => Promise<unknown> };
  setBusy: (value: boolean) => void;
  setSendError: (value: string) => void;
}) {
  if (!input.busy || input.id === "") {
    return;
  }
  const submission = ++input.draft.submission;
  input.setSendError("");
  try {
    await input.prompt.mutateAsync({ id: input.id, text: "$stop" });
    if (input.draft.submission === submission) input.setBusy(false);
  } catch (err) {
    if (input.draft.submission === submission) input.setSendError(err instanceof Error ? err.message : "stop failed");
  }
}

function pendingInputs(draft: ComposerDraft, items: QueueItem[]) {
  const consumed = new Set([...(draft.consumed ?? []), ...draft.lines.map((line) => line.id)]);
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
  lines,
  setLines,
  refreshHistory,
}: {
  id: string;
  draft: ComposerDraft;
  drafts: Map<string, ComposerDraft>;
  onDraftChange: () => void;
  busy: boolean;
  setBusy: (value: boolean) => void;
  lines: Line[];
  setLines: (update: (current: Line[]) => Line[]) => void;
  refreshHistory: () => Promise<unknown>;
}) {
  const { scrollToEnd } = useMessageScroller();
  const prompt = useMutation({ mutationFn: mutations.prompt, onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["queue"] }); } });
  const agents = useQuery({ ...queries.agents({ conversationId: id }), refetchInterval: 2000 });
  const queueQuery = useQuery({ ...queries.queue({ id }), enabled: id !== "", refetchInterval: 2000 });
  const removeQueueItem = useMutation({ mutationFn: mutations.removeQueueItem, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const steerQueueItem = useMutation({ mutationFn: mutations.steerQueueItem, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const popQueueItem = useMutation({ mutationFn: mutations.popQueueItem, onSuccess: () => queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const reorderQueue = useMutation({ mutationFn: mutations.reorderQueue, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const sidebar = useContext(Sidebar);
  const create = useMutation({ mutationFn: mutations.createSession, onSuccess: () => sidebar.invalidateQueries() });
  const { text, files, sending, agent } = draft;
  const setText = (value: string) => { draft.text = value; draft.edit++; onDraftChange(); };
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
  const skills = useQuery({ ...queries.skills({ agent: selected }), enabled: selected !== "", placeholderData: undefined });
  const matches = dollarOff ? [] : dollarMatches(text, skills.data ?? []);
  const pick = Math.max(0, matches.findIndex((item) => item.invocation === dollarPick));
  const applyDollar = (invocation: string) => {
    setText(invocation);
    setDollarOff(invocation !== "$skill ");
    setAgentOpen(false);
  };
  const working = busy || lines.at(-1)?.role === "thinking";
  const { queued, parked } = pendingInputs(draft, queueQuery.data ?? []);
  let placeholder = "Message a new session";
  if (working) {
    placeholder = "Queue a follow-up · ⌘⏎ steers";
  } else if (id) {
    placeholder = "Message or $command";
  }
  const send = (delivery?: PromptDelivery) => sendComposer({
      draft,
      onDraftChange,
      text: draft.text,
      files: draft.files,
      delivery,
      busy,
      working,
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
    });
  const promoteQueued = (itemId: string) => promoteComposer({ draft, id, itemId, busy, steerQueueItem, setBusy, setSendError });
  const stop = () => stopComposer({ draft, id, busy, prompt, setBusy, setSendError });
  return (
    <>
      {sendError ? <p className="px-3 pb-2 text-sm text-destructive sm:px-5">{sendError}</p> : null}
      {parked.length > 0 ? <section aria-label="Pending steers" className="mx-auto w-full max-w-3xl px-3"><p className="text-xs text-muted-foreground">Waiting to steer</p>{parked.map((line) => <TranscriptLine key={line.id} line={line} conversationId={id} />)}</section> : null}
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
        busy={working}
        queued={queued}
        send={send}
        stop={stop}
        steerQueued={promoteQueued}
        popQueued={(itemId) => { setSendError(""); return popQueueItem.mutateAsync({ id, itemId }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "pop failed")); }}
        removeQueued={(itemId) => void removeQueueItem.mutateAsync({ id, itemId }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "remove failed"))}
        reorderQueued={(itemIds) => void reorderQueue.mutateAsync({ id, itemIds }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "reorder failed"))}
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
               <div className="hidden md:contents"><SessionDetails id={sessionId} /></div>
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
  const open = useMutation({ mutationFn: mutations.createSession, onSuccess: (id) => {
    sidebar.invalidateQueries();
    void queryClient.invalidateQueries({ queryKey: ["cronJobs"] });
    route.goSession(id);
  } });
  return job.nextRun ? <Link href={sessionPath(job.nextRun)} className={className}>{children}</Link> : <>
    <button type="button" className={className} disabled={open.isPending} onClick={() => open.mutate({ sourceConversationId: job.origin })}>{open.isPending ? "Opening chat…" : children}</button>
    {open.error ? <p role="alert" className="text-sm text-destructive">{open.error.message}</p> : null}
  </>;
}

function CronRunPreview({ preview, onClose }: { preview: CronJob; onClose: () => void }) {
  const conversationId = preview.nextRun || preview.origin!;
  const history = useQuery(queries.history({ id: conversationId, sourceConversationId: preview.nextRun ? preview.origin : undefined }));
  const previewLines = useMemo(() => (history.data?.messages ?? []).reduce(nextLines, []), [history.data]);
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
        {previewLines.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} />)}
        {history.data?.messages.length === 0 ? <p className="text-sm text-muted-foreground">No recorded messages for this run.</p> : null}
      </div>
    </section>
  );
}

function CronPage() {
  const route = useRoute();
  const jobs = useQuery({ ...queries.cronJobs(), staleTime: 10_000 });
  const run = useMutation({ mutationFn: mutations.runCron, onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["cronJobs"] }); } });
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
                run.mutate({ stem: confirmStem }, { onSuccess: (id) => { if (id !== "") { notePendingCron(id, confirmStem); route.goSession(id); } } });
                setConfirmStem("");
              }}>Run</Button>
            </div>
          </DialogContent>
      </Dialog>
    </div>
  );
}

function AgentsPage() {
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000 });
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
  const skills = useQuery({ ...queries.skills(), staleTime: 60_000 });
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
  const identity = useQuery(queries.identity());
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
  const config = useQuery({ ...queries.config(), staleTime: 60_000 });
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
