"use client";

import { QueryClient, QueryCache, MutationCache, QueryClientProvider, useQuery, useMutation } from "@tanstack/react-query";
import { captureException } from "@sentry/react";
import { Combobox } from "@base-ui/react/combobox";
import { Menu } from "@base-ui/react/menu";
import { Dialog, DialogContent, DialogTitle, DialogDescription, DialogClose, DialogHeader, DialogFooter, DialogTrigger } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Field, FieldGroup, FieldLabel, FieldError } from "@/components/ui/field";
import { queries, mutations, listSessions, rpc } from "./api";
import { draftContent } from "./drafts";
import type { ChatOrigin, HistoryView, MessageMatch, PromptDelivery, SearchMessagesResponse } from "./types";
import { Bot, Check, ChevronDown, CircleAlert, Clock, Command, Copy, CornerUpLeft, Download, Ellipsis, FileIcon, GitFork, GripVertical, Info, LoaderCircle, MessageSquare, PanelLeftClose, PanelLeftOpen, Pin, Play, Plus, Search, Send, Settings, Sparkles, Square, SquarePen, TextCursorInput, Undo2, X } from "lucide-react";
import Link, { usePathname, useSearch, navigate } from "./navigation";
import { createContext, memo, use, useCallback, useContext, useEffect, useId, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type Dispatch, type SetStateAction, type ReactNode, type SyntheticEvent, type RefObject, type ComponentProps } from "react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { flushSync } from "react-dom";
import { PaletteChooser, ThemeToggle } from "@/components/theme";
import { CodeBlock, InlineText, TranscriptText, copyText, useCleanText } from "./transcript-text";
import { projectTimeline, setTimelineRows, timelineLevels, useTimelineDetail, type TimelineRows } from "./timeline-detail";
import { TimelineDetailCard } from "./timeline-detail-card";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Attachment, AttachmentGroup, AttachmentMedia, AttachmentContent, AttachmentTitle, AttachmentDescription, AttachmentActions, AttachmentAction } from "@/components/ui/attachment";
import { Message, MessageContent } from "@/components/ui/message";
import { MessageScrollerProvider, MessageScroller, MessageScrollerViewport, MessageScrollerContent, MessageScrollerItem, MessageScrollerButton, useMessageScroller } from "@/components/ui/message-scroller";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { Agent, Attachment as AttachmentMeta, BackgroundJob, ConfigView, CronJob, QueueItem, Session, TranscriptEvent } from "@/types";
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

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError: (error) => { captureException(error); } }),
  mutationCache: new MutationCache({ onError: (error) => { captureException(error); } }),
});
type SessionCommand = { mode: "fork" | "handoff" | "name"; source: string; target?: MessageMatch };
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

function useMedia(query: string) {
  const subscribe = useCallback((listener: () => void) => {
    const media = matchMedia(query);
    media.addEventListener("change", listener);
    return () => media.removeEventListener("change", listener);
  }, [query]);
  return useSyncExternalStore(subscribe, () => matchMedia(query).matches);
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
  { name: "undo", label: "Undo message", hint: "", desc: "Restore the previous request for editing" },
  { name: "redo", label: "Redo history", hint: "", desc: "Restore the entire hidden conversation" },
  { name: "goal", label: "Start goal", hint: "<objective>", desc: "Start a goal loop" },
  { name: "stop", label: "Stop turn", hint: "", desc: "End the active turn" },
  { name: "cron", label: "Run cron", hint: "[job]", desc: "List or run a cron job" },
  { name: "workflow", label: "Run workflow", hint: "<name> [args]", desc: "Run a saved workflow" },
  { name: "agent", label: "Choose agent", hint: "[name]", desc: "List or switch agent" },
  { name: "enqueue", label: "Enqueue work", hint: "<text>", desc: "Queue work to run automatically" },
  { name: "stash", label: "Stash work", hint: "<text>", desc: "Hold work until explicitly sent" },
  { name: "steer", label: "Steer turn", hint: "<text>", desc: "Send now or guide the active turn" },
  { name: "queue", hint: "", desc: "List pending steers and later work" },
  { name: "skill", label: "Invoke skill", hint: "<name> [args]", desc: "Invoke a skill by name" },
];

function dollarMatches(text: string, skills: { name: string; description?: string }[], workflows: { name: string; description?: string }[]) {
  const workflowQuery = /^\$workflow(?:[\t ]+([^\s]*))?$/i.exec(text);
  if (workflowQuery) {
    return workflows.flatMap((workflow) => workflow.name.toLowerCase().startsWith((workflowQuery[1] ?? "").toLowerCase()) ? [{
      name: workflow.name,
      hint: "[args]",
      desc: workflow.description ?? "",
      invocation: `$workflow ${workflow.name} `,
    }] : []);
  }
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
  sending,
  onSteer,
  onPop,
  onRemove,
  onReorder,
  jobs,
}: {
  conversationId: string;
  items: QueueItem[];
  busy: boolean;
  sending: boolean;
  onSteer: (id: string, text: string) => void;
  onPop: (id: string) => Promise<unknown>;
  onRemove: (id: string) => void;
  onReorder: (itemIds: string[]) => void;
  jobs: ReactNode;
}) {
  const dragId = useRef<string | null>(null);
  const [order, setOrder] = useState<string[] | null>(null);
  const [poppingId, setPoppingId] = useState("");
  const queueActionLabel = busy ? "Steer" : "Send";
  if (items.length === 0 && !jobs) {
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
      {jobs}
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
            <div className="min-w-0 flex-1">
              <MessageAuthor principal={item.principal} />
              <span className="block truncate text-sm"><InlineText text={item.text.replace(/\s+/g, " ")} /></span>
            </div>
            <MessageAttachments attachments={item.attachments} conversationId={conversationId} />
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={sending || (item.delivery === "STASH" && poppingId !== "")}
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

const jobState = (job: BackgroundJob) => ({ completed: "finished", stopped: `stopped by ${job.stoppedBy}`, killed: "killed (server restarted)" } as Record<string, string | undefined>)[job.state] ?? job.state;

// jobsSlot is the composer's Background Jobs list, or nothing when the conversation has none.
function jobsSlot(id: string, jobs: BackgroundJob[] | undefined, onChange: () => Promise<unknown>, openCall: (callId: string) => void) {
  return jobs?.length ? <BackgroundJobs id={id} jobs={jobs} onChange={onChange} openCall={openCall} /> : null;
}

function BackgroundJobs({ id, jobs, openCall, onChange }: { id: string; jobs: BackgroundJob[]; openCall: (callId: string) => void; onChange: () => Promise<unknown> }) {
  const stop = useMutation({ mutationFn: mutations.stopBackgroundJob, onSuccess: onChange });
  return <ul aria-label="Background jobs" className={cn("flex flex-col gap-0.5", jobs.length > 3 && "max-h-32 overflow-y-auto")}>
    {jobs.map((job) => <li key={job.jobId} className="flex items-center gap-2 rounded-md px-2 py-1">
      <span className="min-w-0 flex-1 truncate text-sm">{job.label} · {jobState(job)}</span>
      {job.hidden ? null : <Button type="button" variant="ghost" size="sm" aria-label={`Open ${job.label}`} onClick={() => job.subagentKey ? navigate(delegationHref(id + job.subagentKey)) : openCall(job.toolCallId)}>Open</Button>}
      {job.state === "running" ? <Button type="button" variant="ghost" size="sm" aria-label={`Stop ${job.label}`} disabled={stop.isPending} onClick={() => stop.mutate({ conversationId: id, jobId: job.jobId })}>Stop</Button> : null}
    </li>)}
    {stop.error ? <li role="alert" className="px-2 text-xs text-destructive">{stop.error.message}</li> : null}
  </ul>;
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

function sessionTitle(session: Session, loading: boolean) {
  return session.name || rowPreview(session, loading).split("\n", 1)[0] || sessionLabel(session.id);
}

function useRoute() {
  const pathname = usePathname() || "/";
  const cron = pathname === "/cron";
  const agents = pathname === "/agents";
  const skills = pathname === "/skills";
  const config = pathname === "/config";
  const search = pathname === "/search";
  const id = pathname.startsWith("/s/") ? decodeSessionId(pathname.slice(3)) : "";
  return {
    cron,
    agents,
    skills,
    config,
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
const SidebarInvalidation = createContext<() => void>(() => {});

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
  const owner = identity.isSuccess ? identity.data.username : undefined;
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
    <SidebarInvalidation.Provider value={bump}>
      <Sidebar.Provider value={value}>{children}</Sidebar.Provider>
    </SidebarInvalidation.Provider>
  );
}

type PendingFile = { id: string; file: File };
type ComposerDraft = { text: string; files: PendingFile[]; agent: string; sessionId: string; sending: boolean; busy: boolean; lines: Line[]; parked?: Line[]; consumed?: Set<string>; revision?: string; origin?: ChatOrigin; terminal?: string; historyError?: string; historyRead?: Promise<void>; historyAgain?: boolean; start?: string; more?: boolean; earlier?: Promise<void>; delegations?: string[]; movable?: boolean; jobs?: BackgroundJob[]; error: string; edit: number; submission: number; historyEpoch?: number; revertEligible?: boolean; revertMessageId?: string; canUndo?: boolean; reverting?: boolean; focus?: number; hydrated?: boolean; persistenceKey?: string; persistedEdit?: number; persistenceError?: string };
const RevertActions = createContext<{ available: boolean; pending: boolean; run: (messageId?: string, redo?: boolean) => Promise<void> } | undefined>(undefined);
const DraftScope = createContext<string | undefined>(undefined);

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
      <div id="bottom-navigation" className={cn("min-h-0 items-center gap-[var(--navigation-gap)] py-1 md:grid-cols-[1fr_auto_1fr]", collapsed ? "hidden" : "grid")}>{children}</div>
    </footer>
  );
}

function SidebarToggle({ open, setOpen }: { open: boolean; setOpen: Dispatch<SetStateAction<boolean>> }) {
  const label = open ? "Hide sidebar" : "Show sidebar";
  const Icon = open ? PanelLeftClose : PanelLeftOpen;
  return <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)]" />} aria-label={label} aria-expanded={open} aria-controls="tab-sidebar" onClick={() => setOpen((value) => !value)}>
    <Icon className="size-[var(--navigation-icon)]" />
  </TooltipTrigger><TooltipContent side="top">{label}</TooltipContent></Tooltip>;
}

// Tabs resize from their right edge and delegation from its left one.
function ResizableAside({ side, className, children, ...props }: ComponentProps<"aside"> & { side: "tabs" | "delegation" }) {
  const [edge, label, fallback, min, max] = side === "tabs" ? [1, "Resize tabs", 224, 192, 512] : [-1, "Resize delegation panel", 384, 288, Math.round(innerWidth * 0.6)];
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

type Tab = { path: string; pinned?: boolean; preview?: boolean };
type TabState = { owner?: string; tabs: Tab[]; active: string };
const TabActions = createContext<ReturnType<typeof useTabs>>(null!);
const pageTabs: Record<string, [string, typeof Search]> = { "/": ["New session", SquarePen], "/search": ["Search", Search], "/cron": ["Cron", Clock], "/agents": ["Agents", Bot], "/skills": ["Skills", Sparkles], "/config": ["Settings", Settings] };

function pinnedFirst(tabs: Tab[]) {
  return tabs.toSorted((a, b) => Number(!!b.pinned) - Number(!!a.pinned));
}

// An opened location replaces the preview tab, or else opens right of the active tab.
function openTab(state: TabState, path: string): TabState {
  if (state.tabs.some((tab) => tab.path === path)) return { ...state, active: path };
  const preview = state.tabs.findIndex((tab) => tab.preview);
  const at = preview >= 0 ? preview : state.tabs.findIndex((tab) => tab.path === state.active) + 1 || state.tabs.length;
  return { ...state, active: path, tabs: pinnedFirst(state.tabs.toSpliced(at, Number(preview >= 0), { path, preview: true })) };
}

// The URL is the active tab, so a reload keeps it and any other URL opens as a permanent tab.
function loadTabs(owner: string | undefined, path: string): TabState {
  let tabs: Tab[] = [];
  if (owner !== undefined) {
    try {
      const stored = JSON.parse(localStorage.getItem(`tabs:${owner}`) ?? "") as Tab[];
      const flag = (value: unknown) => value === undefined || typeof value === "boolean";
      if (stored.every((tab) => typeof tab.path === "string" && flag(tab.pinned) && flag(tab.preview)) && new Set(stored.map((tab) => tab.path)).size === stored.length) tabs = stored;
    } catch { /* Missing or unreadable tabs reset to the current location. */ }
  }
  return { owner, tabs: tabs.some((tab) => tab.path === path) ? tabs : [...tabs, { path }], active: path };
}

function useTabs() {
  const identity = useQuery(queries.identity());
  const owner = identity.data?.username;
  const pathname = usePathname() || "/";
  const [state, setState] = useState(() => loadTabs(owner, pathname));
  const [placement, setPlacement] = useState<"top" | "left">(() => localStorage.getItem("tab-placement") === "left" ? "left" : "top");
  const place = (value: "top" | "left") => { localStorage.setItem("tab-placement", value); setPlacement(value); };
  if (state.owner !== owner) setState(loadTabs(owner, pathname));
  else if (state.active !== pathname) setState(openTab(state, pathname));
  useEffect(() => {
    if (state.owner !== undefined) localStorage.setItem(`tabs:${state.owner}`, JSON.stringify(state.tabs));
  }, [state.owner, state.tabs]);
  const update = (change: (tabs: Tab[]) => Tab[]) => setState((current) => ({ ...current, tabs: pinnedFirst(change(current.tabs)) }));
  // Closing the active tab activates the target, else its right neighbour, else its left one; no tabs leaves Home.
  const close = (paths: string[], target?: string) => {
    const doomed = new Set(paths), kept = (tab: Tab) => !doomed.has(tab.path);
    const rest = state.tabs.filter(kept);
    setState({ ...state, tabs: rest.length ? rest : [{ path: "/" }] });
    if (!doomed.has(pathname) || (!rest.length && pathname === "/")) return;
    const at = state.tabs.findIndex((tab) => tab.path === pathname);
    navigate(target ?? (state.tabs.slice(at + 1).find(kept) ?? state.tabs.slice(0, at).findLast(kept))?.path ?? "/");
  };
  const unpinned = (tabs: Tab[]) => tabs.flatMap((tab) => tab.pinned ? [] : [tab.path]);
  const next = placement === "left" ? "top" : "left";
  return {
    state, pathname, placement, place, close,
    promote: (path: string) => { if (state.tabs.some((tab) => tab.path === path && tab.preview)) update((tabs) => tabs.map((tab) => tab.path === path ? { ...tab, preview: false } : tab)); },
    move: (from: string, to: string) => update((tabs) => moveQueueId(tabs.map((tab) => tab.path), from, to).map((path) => tabs.find((tab) => tab.path === path)!)),
    create: (id: string) => update((tabs) => tabs.map((tab) => tab.path === "/" ? { path: sessionPath(id), pinned: tab.pinned } : tab)),
    commands: (path: string) => [
      { key: "close", label: "Close", run: () => close([path]) },
      { key: "close-others", label: "Close others", run: () => close(unpinned(state.tabs).filter((other) => other !== path), path) },
      { key: "close-right", label: "Close to the right", run: () => close(unpinned(state.tabs.slice(state.tabs.findIndex((tab) => tab.path === path) + 1)), path) },
      { key: "close-all", label: "Close all", run: () => close(unpinned(state.tabs)) },
      { key: "pin", label: state.tabs.find((tab) => tab.path === path)?.pinned ? "Unpin" : "Pin", run: () => update((tabs) => tabs.map((tab) => tab.path === path ? { path, pinned: !tab.pinned } : tab)) },
      { key: "placement", label: `Move tabs to ${next}`, run: () => place(next) },
    ],
  };
}

function TabStrip({ tabs: { state, pathname, close, promote, move, commands }, vertical, newChat }: { tabs: ReturnType<typeof useTabs>; vertical: boolean; newChat: () => void }) {
  const [menu, setMenu] = useState<{ path: string; anchor: Element | { getBoundingClientRect: () => DOMRect } }>();
  const [editing, setEditing] = useState<string>();
  const pathOf = (event: SyntheticEvent) => (event.target as Element).closest<HTMLElement>("[data-tab]")?.dataset.tab;
  const list = useRef<HTMLDivElement>(null), refocus = useRef("");
  // Closing from the strip unmounts the focused control, so focus moves to the selected tab, as in SearchTabs.
  const selected = () => list.current!.querySelector<HTMLElement>('[aria-selected="true"]')!;
  return <>
    <div ref={list} role="tablist" aria-label="Open tabs" aria-orientation={vertical ? "vertical" : "horizontal"} className={cn("flex shrink-0 [scrollbar-width:thin]", vertical ? "h-full flex-col gap-0.5 overflow-y-auto p-1" : "mr-12 overflow-x-auto overflow-y-hidden border-b")}
      onContextMenu={(event) => {
        const path = pathOf(event);
        if (!path) return;
        event.preventDefault();
        setMenu({ path, anchor: { getBoundingClientRect: () => DOMRect.fromRect({ x: event.clientX, y: event.clientY }) } });
      }}
      onMouseDown={(event) => { if (event.button === 1) event.preventDefault(); }}
      onDoubleClick={(event) => { if (event.target === event.currentTarget) newChat(); }}
      // A mouse wheel only scrolls vertically, so the top strip turns it sideways; Firefox reports wheel lines, not pixels.
      onWheel={(event) => { if (!vertical) event.currentTarget.scrollLeft += event.deltaY * (event.deltaMode ? 16 : 1); }}
      onAuxClick={(event) => {
        const path = pathOf(event);
        if (event.button !== 1 || !path) return;
        event.preventDefault();
        close([path]);
      }}
      onDragStart={(event) => event.dataTransfer.setData("application/x-rocketclaw-tab", pathOf(event)!)}
      onDragOver={(event) => event.preventDefault()}
      onDrop={(event) => {
        const path = pathOf(event);
        if (path) move(event.dataTransfer.getData("application/x-rocketclaw-tab"), path);
      }}
      onKeyDown={(event) => {
        const tab = event.target as HTMLElement, path = pathOf(event)!;
        if (tab.role !== "tab") return;
        if ((event.shiftKey && event.key === "F10") || event.key === "ContextMenu") {
          event.preventDefault();
          setMenu({ path, anchor: tab });
          return;
        }
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          if (path !== pathname) navigate(path);
          return;
        }
        const at = state.tabs.findIndex((item) => item.path === path);
        const step = ({ ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 } as Record<string, number>)[event.key];
        const position = event.key === "Home" ? 0 : event.key === "End" ? state.tabs.length - 1 : step ? at + step : -1;
        if (!state.tabs[position]) return;
        event.preventDefault();
        if (event.shiftKey && step) {
          flushSync(() => move(path, state.tabs[position].path));
          tab.focus();
        } else event.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]')[position].focus();
      }}>
      {state.tabs.map((tab) => <TabItem key={tab.path} tab={tab} active={tab.path === pathname} vertical={vertical} editing={tab.path === editing} endEdit={() => setEditing(undefined)} promote={() => promote(tab.path)} close={() => { flushSync(() => close([tab.path])); selected().focus(); }} openMenu={(anchor) => setMenu({ path: tab.path, anchor })} />)}
    </div>
    <Menu.Root open={!!menu} onOpenChange={(open) => { if (!open) setMenu(undefined); }}>
      <Menu.Portal><Menu.Positioner anchor={menu?.anchor} sideOffset={4} align="start" className="z-50 outline-none"><Menu.Popup finalFocus={() => { const key = refocus.current; refocus.current = ""; return key.startsWith("close") ? selected() : key !== "rename"; }} onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className="min-w-44 rounded-md border bg-popover p-1 text-popover-foreground shadow-md outline-none">
        {menu ? [...menu.path.startsWith("/s/") ? [{ key: "rename", label: "Rename session", run: () => setEditing(menu.path) }] : [], ...commands(menu.path)].map(({ key, label, run }) => <Menu.Item key={key} onClick={() => { refocus.current = key; run(); }} className="flex cursor-default items-center rounded-sm px-2 py-1.5 text-sm outline-none data-highlighted:bg-accent">{label}</Menu.Item>) : null}
      </Menu.Popup></Menu.Positioner></Menu.Portal>
    </Menu.Root>
  </>;
}

function TabItem({ tab, active, vertical, editing, endEdit, promote, close, openMenu }: { tab: Tab; active: boolean; vertical: boolean; editing: boolean; endEdit: () => void; promote: () => void; close: () => void; openMenu: (anchor: Element) => void }) {
  const sidebar = useContext(Sidebar);
  const id = tab.path.startsWith("/s/") ? decodeSessionId(tab.path.slice(3)) : "";
  const session = sidebar.rows.find((row) => row.id === id) ?? { id };
  const [label, Icon] = pageTabs[tab.path] ?? [sessionTitle(session, sidebar.loadingIds.has(id)), MessageSquare];
  const title = useCleanText(label);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (active) ref.current!.scrollIntoView({ block: "nearest", inline: "nearest" }); }, [active, title]);
  return <div data-tab={tab.path} draggable={!editing} className={cn("group flex shrink-0 items-center", vertical ? "rounded-md" : "border-r", active ? "bg-sidebar-row-active" : "hover:bg-accent")}>
    <Tooltip disabled={editing}><TooltipTrigger delay={500} render={<div ref={ref} role="tab" aria-selected={active} aria-label={title} tabIndex={active ? 0 : -1} className={cn("flex min-w-0 cursor-pointer items-center gap-1.5 py-2 pr-1 pl-3 text-sm outline-none last:pr-3 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring aria-[selected=false]:text-muted-foreground", vertical ? "flex-1" : "max-w-80", tab.preview && "italic")}
      onClick={() => { if (!active) navigate(tab.path); }} onDoubleClick={promote} />}>
      <Icon aria-hidden="true" className="size-4 shrink-0" />
      {session.running ? <span role="img" aria-label="Turn running" className="hidden size-2 shrink-0 rounded-full bg-foreground in-aria-selected:block [@media(hover:none)]:block" /> : null}
      {editing ? <TabRename session={session} placeholder={title} endEdit={endEdit} /> : tab.pinned && !vertical ? null : <span className="truncate">{title}</span>}
    </TooltipTrigger><TooltipContent side="bottom">{title}</TooltipContent></Tooltip>
    {active ? <button type="button" aria-label="Tab actions" className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground last:mr-1 hover:bg-accent hover:text-foreground" onClick={(event) => openMenu(event.currentTarget)}><Ellipsis className="size-3.5" /></button> : null}
    {tab.pinned ? null : <TabClose title={title} running={!!session.running} active={active} close={close} />}
  </div>;
}

// Enter and leaving the field save; Escape restores the old name first, so its blur saves nothing.
function TabRename({ session, placeholder, endEdit }: { session: Session; placeholder: string; endEdit: () => void }) {
  const sidebar = useContext(Sidebar);
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => { sidebar.invalidateQueries(); endEdit(); } });
  const name = session.name ?? "";
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current!.focus(), []);
  return <input ref={input} aria-label="Session name" defaultValue={name} placeholder={placeholder} aria-invalid={!!update.error} title={update.error?.message} className="min-w-0 flex-1 rounded-sm bg-background px-1 text-foreground not-italic outline-1 outline-ring aria-invalid:outline-destructive"
    onFocus={(event) => event.currentTarget.select()}
    onClick={(event) => event.stopPropagation()}
    onKeyDown={(event) => {
      if (event.key === "Escape") { event.preventDefault(); event.currentTarget.value = name; }
      if (event.key === "Enter" || event.key === "Escape") event.currentTarget.blur();
    }}
    onBlur={(event) => { const value = event.currentTarget.value; if (value === name) endEdit(); else update.mutate({ id: session.id, name: value }); }} />;
}

// A running turn's dot holds the close button's place until hover, focus, or activation.
// Where close owns the slot (active or touch), TabItem shows the dot before the title instead.
function TabClose({ title, running, active, close }: { title: string; running: boolean; active: boolean; close: () => void }) {
  const dot = running && !active;
  return <span className="relative mr-1 flex size-6 shrink-0 items-center justify-center">
    <button type="button" aria-label={`Close ${title}`} className={cn("flex size-6 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring", dot && "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 [@media(hover:none)]:opacity-100")} onClick={close}><X className="size-3.5" /></button>
    {dot ? <span role="img" aria-label="Turn running" className="pointer-events-none absolute size-2 rounded-full bg-foreground group-hover:opacity-0 group-focus-within:opacity-0 [@media(hover:none)]:hidden" /> : null}
  </span>;
}

function persistComposerDraft(draft: ComposerDraft) {
  if (!draft.hydrated || !draft.persistenceKey || draft.persistedEdit === draft.edit) return;
  draft.persistedEdit = draft.edit;
  return draftContent(draft.persistenceKey, { text: draft.text, files: draft.files, agent: draft.agent }).then(
    () => { draft.persistenceError = ""; },
    () => { draft.persistenceError = "Draft could not be saved locally. Keep this page open."; },
  );
}

export function App() {
  const identity = useQuery(queries.identity(), queryClient), config = useQuery(queries.config(), queryClient);
  const scope = identity.isSuccess && config.isSuccess ? JSON.stringify(["authenticated-username", location.origin, identity.data.username, config.data.workspace ?? ""]) : undefined;
  return <QueryClientProvider client={queryClient}><TooltipProvider><ProtocolGuard /><SidebarOwner>
    <SessionApp scope={scope} scopeError={identity.error?.message ?? config.error?.message} />
  </SidebarOwner></TooltipProvider></QueryClientProvider>;
}

function SessionApp({ scope, scopeError }: { scope?: string; scopeError?: string }) {
  const [command, setCommand] = useState<SessionCommand>();
  const composer = useRef<((command: string) => void) | null>(null);
  const commands = useMemo(() => ({ command, setCommand, composer }), [command]);
  const route = useRoute();
  const showChat = ![route.cron, route.agents, route.skills, route.config, route.search].some(Boolean);
  const [palette, setPalette] = useState<{ mode: "sessions" | "commands" | "cron" | undefined; key: number }>({ mode: undefined, key: 0 });
  const openPalette = useCallback((mode: "sessions" | "commands") => setPalette((current) => ({ mode, key: current.key + 1 })), []);
  const drafts = useMemo(() => new Map<string, ComposerDraft>(), [scope]);
  const [, setDraftVersion] = useState(0);
  const onDraftChange = useCallback(() => {
    setDraftVersion((version) => version + 1);
    for (const draft of new Set(drafts.values())) {
      void persistComposerDraft(draft)?.finally(() => setDraftVersion((version) => version + 1));
    }
  }, [drafts]);
  const [conversation, setConversation] = useState({ id: route.id, created: "", key: 0 });
  const newChatOwner = useRef<(() => unknown) | undefined>(undefined);
  const tabs = useTabs();
  const medium = useMedia("(min-width: 48rem)");
  const vertical = tabs.placement === "left" && medium;
  const [tabsOpen, setTabsOpen] = useState(true);
  const { close: closeTab, state: { tabs: openTabs }, pathname } = tabs;
  const newChat = useCallback(() => {
    const draft = drafts.get("") ?? drafts.get(conversation.id)!;
    if ([!scope, draft.reverting, !draft.hydrated].some(Boolean)) return;
    const owner = newChatOwner.current;
    draft.reverting = true;
    draft.persistenceError = "";
    onDraftChange();
    return draftContent(JSON.stringify([scope, ""]), { text: "", files: [], agent: "" }).then(() => {
      if (drafts.get("") === draft) { draft.text = ""; draft.files = []; draft.agent = ""; draft.edit++; }
      if (newChatOwner.current !== owner) return;
      drafts.delete("");
      setConversation((current) => ({ ...current, created: "", key: current.key + 1 }));
      navigate("/");
    }, () => { draft.persistenceError = "Local draft could not be cleared. Keep this page open."; }).finally(() => {
      draft.reverting = false;
      onDraftChange();
    });
  }, [drafts, scope, conversation.id, onDraftChange]);
  useLayoutEffect(() => {
    newChatOwner.current = newChat;
    return () => { newChatOwner.current = undefined; };
  }, [newChat]);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat) return;
      if ((event.metaKey || event.ctrlKey) && !event.altKey && event.code === "KeyP") {
        event.preventDefault();
        openPalette(event.shiftKey ? "commands" : "sessions");
        return;
      }
      if (vertical && (event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.code === "KeyB") {
        event.preventDefault();
        setTabsOpen((open) => !open);
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.altKey && !event.shiftKey && event.code === "KeyN") {
        event.preventDefault();
        newChat();
        return;
      }
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && (event.code === "BracketLeft" || event.code === "BracketRight")) {
        event.preventDefault();
        const at = openTabs.findIndex((tab) => tab.path === pathname);
        const next = openTabs[(at + (event.code === "BracketLeft" ? openTabs.length - 1 : 1)) % openTabs.length].path;
        if (next !== pathname) navigate(next);
      }
    };
    const onEscape = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || showChat || event.key !== "Escape") return;
      if (document.querySelector('[role="dialog"], [role="listbox"], [role="menu"], [role="tooltip"]')) return;
      event.preventDefault();
      closeTab([location.pathname]);
    };
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("keydown", onEscape);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("keydown", onEscape);
    };
  }, [newChat, showChat, openPalette, closeTab, openTabs, pathname, vertical]);
  if (showChat && conversation.id !== route.id) {
    // Creation assigns this conversation its ID; other navigation starts a fresh subtree.
    const created = conversation.id === "" && conversation.created === route.id;
    setConversation({ id: route.id, created: "", key: created ? conversation.key : conversation.key + 1 });
  }
  return (
          <DraftScope value={scope}><SessionCommands value={commands}><TabActions value={tabs}>
           {command ? <SessionCommandDialog key={`${command.mode}:${command.source}`} command={command} drafts={drafts} onDraftChange={onDraftChange} /> : null}
            <CommandPalette key={palette.key} drafts={drafts} mode={palette.mode} setMode={(mode) => setPalette((current) => ({ ...current, mode }))} newChat={newChat} />
         <div className="flex h-dvh min-h-0 flex-col overflow-hidden overscroll-y-none bg-background">
            <BottomNavigation>
              {vertical ? <SidebarToggle open={tabsOpen} setOpen={setTabsOpen} /> : null}
              <div className="min-w-0 max-w-full justify-self-center overflow-x-auto md:col-start-2 overflow-y-hidden scrollbar-none">
                <div className="flex w-max items-center gap-[var(--navigation-gap)]">
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="New session" onClick={newChat}>
                    <SquarePen className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">New session</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="Search sessions" onClick={() => openPalette("sessions")}>
                    <Search className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">Search sessions</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="Open command palette" onClick={() => openPalette("commands")}>
                    <Command className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">Open command palette</TooltipContent></Tooltip>
                </div>
              </div>
            </BottomNavigation>
            <div className="fixed top-2 right-2 z-40 rounded-md bg-background shadow-sm"><ThemeToggle /></div>
          {vertical ? null : <TabStrip tabs={tabs} vertical={false} newChat={newChat} />}
          <div className="flex min-h-0 min-w-0 flex-1">
            {vertical ? <ResizableAside side="tabs" id="tab-sidebar" className={cn("border-r", !tabsOpen && "hidden")}><TabStrip tabs={tabs} vertical newChat={newChat} /></ResizableAside> : null}
            <main className={cn("flex min-h-0 min-w-0 flex-1 flex-col md:min-w-[26rem]", command?.target && "pt-[min(75dvh,30rem)]")}>
              <WarmTabs cron={route.cron} agents={route.agents} skills={route.skills} config={route.config} />
              {route.search ? <SearchPage /> : null}
              <TabPane show={showChat}>
                <MessageScrollerProvider key={conversation.key} autoScroll scrollEdgeThreshold={48}>
                  <Transcript id={conversation.id} drafts={drafts} scopeError={scopeError} onDraftChange={onDraftChange} onCreated={(id) => { tabs.create(id); setConversation((current) => ({ ...current, created: id })); }} />
                </MessageScrollerProvider>
              </TabPane>
             </main>
             {showChat ? <DelegationPanel id={route.id} /> : null}
           </div>
          </div>
          </TabActions></SessionCommands></DraftScope>
  );
}

function SessionCommandDialog({ command, drafts, onDraftChange }: { command: SessionCommand; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  if (command.mode === "name") return <NameSessionDialog id={command.source} />;
  return command.mode === "fork" ? <ForkDialog source={command.source} drafts={drafts} onDraftChange={onDraftChange} /> : <HandoffDialog command={command} drafts={drafts} onDraftChange={onDraftChange} />;
}

function ForkDialog({ source, drafts, onDraftChange }: { source: string; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  const { setCommand } = useContext(SessionCommands);
  const sidebar = useContext(Sidebar);
  const [query, setQuery] = useState("");
  const history = useQuery(queries.history({ id: source }));
  const fork = useMutation({ mutationFn: async (message?: TranscriptEvent) => {
    const draft = await forkDraft(source, message);
    drafts.set(draft.sessionId, draft);
    onDraftChange();
    sidebar.invalidateQueries();
    navigate(sessionPath(draft.sessionId));
    setCommand(undefined);
  } });
  // OpenCode V2: packages/app/src/session/commands/fork-dialog.tsx lists user
  // messages newest first, forks BEFORE selection, and restores it for editing.
  const items = [{ key: "full", label: "Full session", detail: "Copy all recorded history", choose: () => fork.mutate(undefined) }, ...(history.data?.messages ?? []).filter((message) => message.role === "user" && message.messageId && message.text.toLowerCase().includes(query.toLowerCase())).toReversed().map((message) => ({ key: message.messageId!, label: message.text, detail: "Continue before this message", choose: () => fork.mutate(message) }))];
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
  const identity = useQuery(queries.identity());
  const { setCommand } = useContext(SessionCommands);
  const popup = useRef<HTMLDivElement>(null);
  const route = useRoute();
  const sidebar = useContext(Sidebar);
  const [query, setQuery] = useState("");
  const search = useQuery({ ...queries.searchMessages(query.trim()), enabled: query.trim() !== "" });
  const handoffRequest = { queryKey: ["handoff", command.source], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<{ document: string }>("Handoff", { id: command.source }, signal), retry: false, staleTime: Infinity, gcTime: 0 };
  const handoff = useQuery(handoffRequest);
  const action = useMutation({ mutationFn: async (kind: "copy" | "new" | "stash") => {
    const principal = identity.isSuccess && !identity.isFetching ? identity.data.principal : undefined;
    const { document } = await queryClient.fetchQuery(handoffRequest);
    if (kind === "copy") await copyText(document, popup.current!);
    if (kind === "new") {
      const id = await mutations.createSession({ agent: "main" });
      const optimistic: Line = { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), role: "user", text: document, principal };
      const draft: ComposerDraft = { text: "", files: [], agent: "", sessionId: id, sending: false, busy: true, lines: [optimistic], error: "", edit: 0, submission: 0 };
      drafts.set(id, draft);
      onDraftChange();
      sidebar.invalidateQueries();
      navigate(sessionPath(id));
      void mutations.prompt({ id, text: document, messageId: optimistic.id }).catch((err: Error) => {
        draft.text = [document, draft.text].filter(Boolean).join("\n\n");
        draft.lines = draft.lines.filter((line) => line.id !== optimistic.id);
        draft.error = err.message;
        if (draft.submission === 0) draft.busy = false;
        onDraftChange();
      });
    }
    if (kind === "stash") {
      await mutations.prompt({ id: command.target!.conversationId, text: document, delivery: "STASH" });
      void queryClient.invalidateQueries({ queryKey: ["queue"] });
    }
    setCommand(undefined);
  } });
  const target = command.target;
  const preview = useQuery({ ...queries.history({ id: target?.conversationId ?? "" }), enabled: !!target });
  const items = (query.trim() ? search.data ?? [] : []).map((match) => ({ key: `${match.conversationId}:${match.message.messageId}`, label: match.message.text, session: sidebar.rows.find((row) => row.id === match.conversationId) ?? { id: match.conversationId }, choose: () => {
      setQuery("");
      setCommand({ ...command, target: match });
      navigate(sessionPath(match.conversationId));
    } }));
  const pending = action.isPending;
  const choices = ([{ key: "copy", label: "Copy handoff", progress: "Copying handoff…" }, { key: "new", label: "Start new session", progress: "Starting session…" }] as const).map(({ key, label, progress }) => ({
    key, label: pending && action.variables === key ? progress : label, choose: () => action.mutate(key),
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
          <div className="min-w-0 flex-1"><SessionRowContent session={sidebar.rows.find((row) => row.id === target.conversationId) ?? { id: target.conversationId }} /><p className="truncate text-xs text-muted-foreground"><InlineText text={target.message.text.replace(/\s+/g, " ")} /></p></div>
          <Button variant="ghost" className="min-h-11 shrink-0" disabled={pending} onClick={() => setCommand({ ...command, target: undefined })}>Change</Button>
        </div> : null}
        {error ? <p role="alert" className="break-words text-destructive">{error.message}</p> : null}
        {handoff.isError ? <Button variant="outline" className="min-h-11" onClick={() => void handoff.refetch()}>Retry handoff</Button> : null}
      </div>
      {target && <DialogFooter className="shrink-0 flex-row flex-wrap items-center justify-between sm:justify-between">
        {handoff.data ? <CodeBlock text={handoff.data.document} label="Handoff" compact /> : <p hidden={pending} role="status" className="flex items-center gap-2 text-xs text-muted-foreground">{handoff.isPending ? <><LoaderCircle className="size-4 animate-spin" />Preparing handoff…</> : "Handoff not ready"}</p>}
        <Button className="ml-auto min-h-11" aria-label="Stash handoff here" disabled={[pending, !preview.data, preview.isError, route.id !== target.conversationId].some(Boolean)} onClick={() => action.mutate("stash")}>{pending && action.variables === "stash" ? "Stashing…" : "Stash"}</Button>
      </DialogFooter>}
    </DialogContent>
  </Dialog>;
}

async function forkDraft(source: string, message?: TranscriptEvent): Promise<ComposerDraft> {
  // Restore files before creating a session; a failed download must not leave a fork.
  const files = await restoreFiles(message?.attachments ?? []);
  return mutations.forkSession({ id: source, before: message?.messageId }).then((result) => ({ text: result.prompt.text, files, agent: "", sessionId: result.id, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0 }));
}

async function restoreFiles(attachments: AttachmentMeta[]): Promise<PendingFile[]> {
  return Promise.all(attachments.map(async (file) => {
    const response = await fetch(`/api/DownloadAttachment?${new URLSearchParams({ conversationId: file.conversationId, id: file.id })}`);
    if (!response.ok) throw new Error(`Could not restore ${file.name}`);
    return { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), file: new File([await response.blob()], file.name, { type: file.mimeType }) };
  }));
}

function SessionCommandPicker({ items, query, setQuery, forking, disabled }: { items: { key: string; label: string; detail?: string; session?: Session; choose: () => void }[]; query: string; setQuery: (query: string) => void; forking: boolean; disabled: boolean }) {
  const [pick, setPick] = useState(0);
  const active = useRef<HTMLButtonElement>(null);
  const selected = items.length ? pick % items.length : 0;
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [selected]);
  return <>
    <Input aria-label={forking ? "Search fork messages" : "Search messages"} placeholder="Search messages" value={query} disabled={disabled} onChange={(event) => { setPick(0); setQuery(event.target.value); }} onKeyDown={(event) => setPick(paletteMove(event, selected, items.length, () => items[selected]?.choose()))} />
    <ul className="max-h-[40vh] overflow-y-auto">
      {items.map((item, index) => <li key={item.key}><Button ref={index === selected ? active : null} variant={index === selected ? "secondary" : "ghost"} size="lg" className="min-h-11 h-auto w-full flex-col items-start" disabled={disabled} onClick={item.choose}>{item.session ? <SessionRowContent session={item.session} /> : null}<span aria-live="polite" className="line-clamp-2 text-left whitespace-normal break-words"><InlineText text={item.label.replace(/\s+/g, " ")} /></span>{item.detail ? <span className="max-w-full truncate text-xs">{item.detail}</span> : null}</Button></li>)}
    </ul>
  </>;
}

function paletteRows(
  mode: "sessions" | "commands" | "cron",
  needle: string,
  sidebar: { rows: Session[]; loadingIds: ReadonlySet<string> },
  jobs: { stem: string; status: string; schedule?: string; agent?: string; channel?: string }[] | undefined,
  newChat: () => void,
  openCron: () => void,
  runStem: (stem: string) => void,
  origins: ReadonlyMap<string, string>,
  actions: { key: string; label: string; detail?: string; keep?: boolean; disabled?: boolean; run: () => void }[],
  filters: ReturnType<typeof sessionSearchTerms>,
  agentFilter: string,
  roomFilter: string,
  recent: string[],
): { key: string; label?: string; detail?: string; session?: Session; loading?: boolean; keep?: boolean; disabled?: boolean; run: () => void }[] {
  if (mode === "sessions") {
    return sidebar.rows.filter((session) => sessionMatchesSearch(session, filters, agentFilter, roomFilter, origins.get(session.id) ?? "")).sort((a, b) => compareSessions(filters.sort, a, b)).map((session) => ({
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
    { key: "new", label: "Sessions: New", run: newChat },
    { key: "search", label: "Sessions: Search", run: () => navigate("/search") },
    { key: "run-cron", label: "Cron: Run", keep: true, run: openCron },
    ...([["cron", "Cron: Dashboard"], ["agents", "List Agents"], ["skills", "List Skills"], ["config", "Settings"]] as const).map(([key, label]) => ({ key, label, run: () => navigate(`/${key}`) })),
    ...timelineLevels.map(({ id, label, rows }) => ({ key: `timeline-${id}`, label: `Timeline: ${label}`, run: () => setTimelineRows(rows) })),
    // VS Code: src/vs/platform/quickinput/browser/commandsQuickAccess.ts, _getPicks.
  ].filter((item) => needle === "" || item.label.toLowerCase().includes(needle)).sort((a, b) => (recent.indexOf(a.key) + 1 || Infinity) - (recent.indexOf(b.key) + 1 || Infinity) || a.label.localeCompare(b.label));
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

// The server searches every visible chat origin in one request; values maps matching chats to their origin text.
function useSessionOrigins(rows: Session[], needle: string) {
  const identity = useQuery(queries.identity());
  const protocol = useQuery(queries.protocol());
  // New sidebar conversations start a fresh origin search for the same needle.
  const ids = useMemo(() => rows.map(({ id }) => id).toSorted(), [rows]);
  const [settled, setSettled] = useState(needle);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(needle), 250);
    return () => clearTimeout(timer);
  }, [needle]);
  const query = useQuery<Map<string, string>>({
    queryKey: ["searchOrigins", identity.data?.username, protocol.data, settled, ids],
    enabled: settled !== "",
    placeholderData: (previous) => previous,
    staleTime: 10_000,
    refetchOnWindowFocus: false,
    retry: false,
    queryFn: async ({ signal }) => new Map((await rpc<{ matches: { conversationId: string; text: string }[] }>("SearchOrigins", { query: settled }, signal)).matches.map(({ conversationId, text }) => [conversationId, text])),
  });
  return { values: query.data ?? new Map<string, string>(), pending: needle !== "" && (needle !== settled || query.isPending || query.isFetching), failed: needle !== "" && needle === settled && query.isError };
}

const paletteCopy = { sessions: { title: "Go to session", desc: "Search and open a session.", placeholder: "Search sessions" }, commands: { title: "Run command", desc: "Search and run a command.", placeholder: "Type a command" },
  cron: { title: "Run cron", desc: "Search and run a cron job.", placeholder: "Search cron jobs" } };

function CommandPalette({ drafts, mode, setMode, newChat }: { drafts: Map<string, ComposerDraft>; mode: "sessions" | "commands" | "cron" | undefined; setMode: (mode: "sessions" | "commands" | "cron" | undefined) => void; newChat: () => void }) {
  const sidebar = useContext(Sidebar);
  const { setCommand, composer } = useContext(SessionCommands);
  const { id } = useRoute();
  const tabCommands = useContext(TabActions).commands(usePathname() || "/").map((item) => ({ ...item, key: `tab-${item.key}`, label: `Tabs: ${item.label}` }));
  const actions = useSessionActions(sidebar.rows.find((row) => row.id === id), () => setMode(undefined));
  const choices = useQuery({ ...queries.agents({ conversationId: id }), enabled: id !== "" });
  const draft = drafts.get(id);
  const commands = id ? dollarCommands.filter(({ name }) => name !== "cron" && name !== "queue" && (name !== "undo" || draft?.revertEligible && draft.canUndo) && (name !== "redo" || draft?.revertEligible && draft.revertMessageId) && (name !== "stop" || (draft?.busy ?? sidebar.rows.find((row) => row.id === id)?.running)) && (name !== "agent" || !!choices.data?.agents.length)).map(({ name, label }) => ({ key: name, label: ["fork", "handoff", "stop", "agent"].includes(name) ? `Sessions: ${label}` : `Command: ${label} ($${name})`, disabled: [!draft?.hydrated, draft?.sending, draft?.reverting].some(Boolean), run: () => {
    if (name === "fork" || name === "handoff") setCommand({ mode: name, source: id });
    else composer.current!(name);
  } })) : [];
  const [query, setQuery] = useState("");
  const [recent, setRecent] = useState<string[]>(() => JSON.parse(localStorage.getItem("command-history") ?? "[]"));
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
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000, enabled: mode === "sessions" });
  const filters = sessionSearchTerms(query);
  const active = useRef<HTMLButtonElement>(null);
  const origins = useSessionOrigins(sidebar.rows, mode === "sessions" ? filters.needle : "");
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
  const items = mode === undefined ? [] : paletteRows(mode, query.trim().toLowerCase(), sidebar, jobs.data, newChat, () => { setQuery(""); setPick(0); setMode("cron"); }, (stem) => runCron.mutate({ stem }), origins.values, [...actions.items.map((item) => ({ ...item, label: `Sessions: ${item.label}` })), ...commands, ...tabCommands], filters, agentFilter, roomFilter, recent);
  const selected = items.length === 0 ? 0 : pick % items.length;
  const choose = (item: (typeof items)[number]) => {
    if (item.disabled) return;
    if (mode === "commands") {
      const next = [item.key, ...recent.filter((key) => key !== item.key)].slice(0, 50);
      localStorage.setItem("command-history", JSON.stringify(next));
      setRecent(next);
      setPick(0);
    }
    if (!item.keep) setMode(undefined);
    item.run();
  };
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [selected, mode]);
  const copy = paletteCopy[mode ?? "sessions"];
  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    const next = paletteMove(event, selected, items.length, () => { if (items[selected]) choose(items[selected]); });
    if (event.key !== "Enter") setPick(next);
  };
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

function SessionRowContent({ session, loading = false, age = relativeTime(session.updatedAt ?? "") }: { session: Session; loading?: boolean; age?: string }) {
  const title = sessionTitle(session, loading);
  const clean = useCleanText(title);
  const channel = slackSession(session.id) ? (session.title ?? "") : "";
  const meta = [channel, session.agent, ...(session.tags ?? [])].filter(Boolean).join(" · ");
  const updated = session.updatedAt ? `Updated ${new Date(session.updatedAt).toLocaleString(undefined, { timeZoneName: "short" })}` : "";
  return <span className="flex min-w-0 w-full flex-1 flex-col gap-0.5">
    <span className="flex items-center gap-1.5 text-sm font-medium">{session.forkedFrom ? <GitFork role="img" aria-label="Forked session" className="size-3.5 shrink-0" /> : null}<span data-slot="session-title" className="truncate" title={clean}><InlineText text={title} /></span></span>
    <span className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"><span className="min-w-0 flex-1 truncate" title={meta}>{meta}</span>{session.running ? <LoaderCircle role="img" aria-label="Turn running" className="size-3 shrink-0 animate-spin motion-reduce:animate-none" /> : null}{age ? <Tooltip><TooltipTrigger render={<time dateTime={session.updatedAt} />} aria-label={updated} className="shrink-0 tabular-nums">{age}</TooltipTrigger><TooltipContent>{updated}</TooltipContent></Tooltip> : null}</span>
  </span>;
}

function useSessionActions(session: Session | undefined, onSuccess?: () => void) {
  const invalidate = useContext(SidebarInvalidation);
  const { setCommand } = useContext(SessionCommands);
  const saved = () => { invalidate(); onSuccess?.(); };
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: saved });
  const id = session?.id ?? "";
  const items = session ? [
    ...(session.forkedFrom ? [{ key: "origin", label: "Open original conversation", icon: CornerUpLeft, run: () => navigate(sessionPath(session.forkedFrom!)) }] : []),
    { key: "name", label: "Name session", icon: TextCursorInput, run: () => setCommand({ mode: "name", source: id }) },
    { key: "pin", label: session.pinned ? "Unpin session" : "Pin session", icon: Pin, pressed: !!session.pinned, keep: true, run: () => update.mutate({ id, pinned: !session.pinned }) },
  ].map((item) => ({ ...item, disabled: update.isPending })) : [];
  return { items, error: update.error };
}

function SessionActions({ session }: { session?: Session }) {
  const { items, error } = useSessionActions(session);
  return <>{items.map(({ key, label, icon: Icon, pressed, disabled, run }) => <Tooltip key={key}>
    <TooltipTrigger render={<Button variant="ghost" size="icon" className="size-11 sm:size-8" disabled={disabled} />} aria-label={label} aria-pressed={pressed} onClick={run}><Icon className={cn(pressed && "fill-current")} /></TooltipTrigger><TooltipContent>{label}</TooltipContent>
  </Tooltip>)}{error ? <span role="alert" className="text-xs text-destructive">{error.message}</span> : null}</>;
}

function SessionHeaderActions({ id }: { id: string }) {
  const sidebar = useContext(Sidebar);
  return <SessionActions session={sidebar.rows.find((row) => row.id === id)} />;
}

function NameSessionDialog({ id }: { id: string }) {
  const sidebar = useContext(Sidebar);
  const session = sidebar.rows.find((row) => row.id === id);
  const { setCommand } = useContext(SessionCommands);
  const [value, setValue] = useState(session?.name ?? "");
  const nameId = useId();
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => { sidebar.invalidateQueries(); setCommand(undefined); } });
  return <Dialog open onOpenChange={(open) => { if (!open) setCommand(undefined); }}>
        <DialogContent>
          <DialogTitle>Name session</DialogTitle>
          <DialogDescription>Shared with everyone who can see this session. Leave blank to show the last message.</DialogDescription>
          <form className="mt-4 flex flex-col gap-3" action={() => update.mutate({ id, name: value })}>
            <FieldGroup>
              <Field data-invalid={!!update.error}>
                <FieldLabel htmlFor={nameId}>Session name</FieldLabel>
                <Input id={nameId} name="name" type="text" value={value} aria-invalid={!!update.error} onChange={(event) => setValue(event.target.value)} />
                {update.error ? <FieldError>{update.error.message}</FieldError> : null}
              </Field>
            </FieldGroup>
            <div className="flex justify-end gap-2">
              <DialogClose render={<Button variant="ghost" />}>Cancel</DialogClose>
              <Button type="submit" disabled={update.isPending}>{update.isPending ? "Saving…" : "Save"}</Button>
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
  const tags: string[] = [], cronNames: string[] = [], filterTerms: string[] = [];
  let pinnedOnly = false, forkedOnly = false, cronOnly = false, sort = "";
  const text = query.replace(/(?:^|\s)(is:(?:pinned|forked|cron)|sort:(?:newest|oldest)|(?:tag|cron):("(?:[^"\\]|\\.)*"|\S+))(?=\s|$)/gi, (term, filter: string, value?: string) => {
    const lower = filter.toLowerCase();
    if (value !== undefined) {
      let name = value;
      if (value.startsWith('"')) {
        try { name = JSON.parse(value); } catch { return term; }
      }
      if (name === "") return term;
      (lower.startsWith("cron:") ? cronNames : tags).push(name);
    } else if (lower.startsWith("sort:")) {
      sort = lower.slice(5);
    } else {
      pinnedOnly ||= lower === "is:pinned";
      forkedOnly ||= lower === "is:forked";
      cronOnly ||= lower === "is:cron";
    }
    filterTerms.push(filter);
    return "";
  }).trim();
  // A trailing operator prefix is being typed, not searched.
  const typing = /(?:^|\s)((?:is|sort|cron):\S*)$/i.exec(text);
  const rest = typing && " is:pinned is:forked is:cron sort:newest sort:oldest cron:".includes(` ${typing[1].toLowerCase()}`) ? text.slice(0, typing.index).trim() : text;
  const needle = typedPrefix(text, "agent:") === null && typedPrefix(text, "room:") === null ? rest.toLowerCase() : "";
  return { pinnedOnly, forkedOnly, cronOnly, cronNames, sort, tags, filterTerms, text, needle };
}

function sessionMatchesSearch(session: Session, filters: ReturnType<typeof sessionSearchTerms>, agentFilter: string, roomFilter: string, origin: string) {
  const tags = new Set(session.tags);
  return filters.tags.every((tag) => tags.has(tag)) && filters.cronNames.every((name) => session.cronName === name) && (!filters.pinnedOnly || session.pinned) && (!filters.forkedOnly || session.forkedFrom) && (!filters.cronOnly || session.cron) && matchesSession(session, "", agentFilter, roomFilter) && (matchesSession(session, filters.needle, "", "") || origin.includes(filters.needle));
}

// sort: orders by last activity (missing last, then ID); otherwise pinned rows come first.
function compareSessions(sort: string, a: Session, b: Session) {
  const time = (session: Session) => Date.parse(session.updatedAt ?? "") * (sort === "newest" ? -1 : 1) || Infinity;
  return sort ? time(a) - time(b) || a.id.localeCompare(b.id) : Number(!!b.pinned) - Number(!!a.pinned);
}

type SavedSearch = { id: string; name?: string; query: string; agentFilter: string; roomFilter: string };
type SavedSearches = { tabs: SavedSearch[]; active: string };

function SearchPage() {
  const identity = useQuery(queries.identity());
  if (!identity.isSuccess) return null;
  return <SearchTabs key={identity.data.username} owner={identity.data.username} />;
}

function SearchTabs({ owner }: { owner: string }) {
  const storageKey = `search-tabs:${owner}`;
  const [rename, setRename] = useState<string | null>(null);
  const [saved, setSaved] = useState<SavedSearches>(() => {
    const stored = JSON.parse(localStorage.getItem(storageKey) ?? '{"tabs":[]}') as SavedSearches;
    const params = new URLSearchParams(location.search);
    const link = { query: params.get("q") ?? "", agentFilter: params.get("agent") ?? "", roomFilter: params.get("room") ?? "" };
    const linked = !!(link.query || link.agentFilter || link.roomFilter);
    if (stored.tabs.length && !linked) return stored;
    const match = stored.tabs.toSorted((a, b) => Number(b.id === stored.active) - Number(a.id === stored.active)).find((item) => item.query === link.query && item.agentFilter === link.agentFilter && item.roomFilter === link.roomFilter);
    const id = match?.id ?? crypto.getRandomValues(new Uint32Array(4)).join("-");
    const next = { tabs: match ? stored.tabs : [...stored.tabs, { id, ...link }], active: id };
    if (linked) localStorage.setItem(storageKey, JSON.stringify(next));
    return next;
  });
  const draft = useRef(localStorage.getItem(storageKey) === null);
  const savedRef = useRef(saved);
  const focusAfterClose = useRef(false);
  const activeTab = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => { input.current?.focus(); }, []);
  const sidebar = useContext(Sidebar);
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000 });
  const selected = saved.tabs.findIndex((tab) => tab.id === saved.active);
  const tab = saved.tabs[selected];
  const search = useSearch();
  useEffect(() => {
    const params = new URLSearchParams(Object.entries({ q: tab.query, agent: tab.agentFilter, room: tab.roomFilter }).filter(([, value]) => value));
    history.replaceState(history.state, "", `/search${params.size ? `?${params}` : ""}`);
  }, [tab, search]);
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

function windowed(text: string, needle: string, tagIds: string[]) {
  if (!needle) return text;
  const lines = text.split("\n");
  let hit = lines.findIndex((line) => line.toLowerCase().includes(needle));
  if (hit < 0) hit = lines.findIndex((line) => tagIds.some((id) => line.includes("<@" + id) || line.includes("<!subteam^" + id)));
  if (hit < 0) return text;
  let start = Math.max(0, hit - 2), end = Math.min(lines.length, hit + 3), open = -1;
  for (let i = 0; i < lines.length; i++) {
    if (!/^\s*```/.test(lines[i])) continue;
    if (open < 0) open = i;
    else {
      if (open < end && i >= start) { start = Math.min(start, open); end = Math.max(end, i + 1); }
      open = -1;
    }
  }
  if (open >= 0 && open < end) { start = Math.min(start, open); end = lines.length; }
  return `${start ? "…\n" : ""}${lines.slice(start, end).join("\n")}${end < lines.length ? "\n…" : ""}`;
}

function SearchResultGroup({ session, hits, text, field, needle, tagIds }: { session: Session; hits: MessageMatch[]; text: string; field?: string; needle: string; tagIds: string[] }) {
  const count = hits.length + Number(!!text);
  const label = sessionTitle(session, false);
  const clean = useCleanText(label);
  const row = (href: string, kind: string, body: string, key?: string) => <li key={key}><Link href={href} className="flex min-h-11 min-w-0 items-start gap-3 px-2 py-1 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring sm:min-h-7"><span className="w-16 shrink-0 text-xs text-muted-foreground">{kind}</span><div className="min-w-0"><TranscriptText text={windowed(body, needle, tagIds)} needle={needle} interactive={false} /></div></Link></li>;
  return <li role="group" aria-label={clean} className="min-w-0">
    <h2 className="flex min-w-0 items-center gap-1 text-sm font-medium"><Link href={sessionPath(session.id)} title={clean} className="min-w-0 truncate py-1 hover:underline focus-visible:outline-2 focus-visible:outline-ring"><InlineText text={label} /></Link><span className="shrink-0 text-muted-foreground">({count})</span>{session.pinned ? <Pin aria-label="Pinned" className="size-3 shrink-0" /> : null}</h2>
    <ul className="mt-1 border-l pl-2 text-sm leading-5">
      {text ? row(sessionPath(session.id), field || "Preview", text) : null}
      {hits.map((match, i) => row(sessionPath(session.id) + (match.message.messageId ? `?message=${encodeURIComponent(match.message.messageId)}` : ""), match.message.role === "user" ? "You" : "Assistant", match.message.text, match.message.messageId || String(i)))}
    </ul>
  </li>;
}

function SearchMatches({ matching, messages, rows, origins, needle, sort, tagIds }: { matching: Session[]; messages: MessageMatch[]; rows: Session[]; origins: Map<string, string>; needle: string; sort: string; tagIds: string[] }) {
  const bySession = Map.groupBy(messages, (match) => match.conversationId);
  const matched = new Set(matching);
  const sessions = rows.filter((session) => bySession.has(session.id) || matched.has(session)).sort((a, b) => (!sort && Number(bySession.has(b.id)) - Number(bySession.has(a.id))) || compareSessions(sort, a, b));
  return <ul className="flex min-w-0 flex-col gap-4">
    {sessions.map((session) => {
      const origin = origins.get(session.id) ?? "";
      const hits = bySession.get(session.id) ?? [];
      const field = needle ? [["Name", session.name], ["Room", session.title], ["Agent", session.agent], ["Session", sessionLabel(session.id)], ["Origin", origin]].find(([, value]) => value?.toLowerCase().includes(needle)) : undefined;
      const text = field?.[1] || (!hits.length && matched.has(session) ? session.preview || sessionLabel(session.id) : "");
      return <SearchResultGroup key={session.id} session={session} hits={hits} text={text} field={field?.[0]} needle={needle} tagIds={tagIds} />;
    })}
  </ul>;
}

function SearchStatus({ pending, checking, error, originError, empty, incomplete, unindexed, retry }: { pending: boolean; checking: boolean; error?: string; originError: boolean; empty: boolean; incomplete: boolean; unindexed: boolean; retry: () => void }) {
  return <>
    {pending || checking ? <p role="status" className="text-muted-foreground">{pending ? "Searching…" : "Still checking chat origins…"}</p> : null}
    {error ? <p role="alert" className="text-destructive">Search failed: {error} <button type="button" className="underline" onClick={retry}>Retry</button></p> : null}
    {originError ? <p role="alert" className="text-destructive">Some chat origins could not be searched.</p> : null}
    {unindexed ? <p role="status" className="text-muted-foreground">Message results may be incomplete while chats are still being indexed.</p> : null}
    {empty && (incomplete || !unindexed) ? <p role="status" className="text-muted-foreground">{incomplete ? "Session search is still loading." : "No matches"}</p> : null}
  </>;
}

function SearchResults({ tab, rows, catalog, edit, input, onFirstSubmit }: { tab: SavedSearch; rows: Session[]; catalog: { name: string }[]; edit: (change: Partial<SavedSearch>) => void; input: React.Ref<HTMLInputElement>; onFirstSubmit: () => void }) {
  const sidebar = useContext(Sidebar);
  const filters = sessionSearchTerms(tab.query);
  const searchKey = filters.needle;
  const origins = useSessionOrigins(rows, searchKey);
  const [result, setResult] = useState<{ query: string; matches: MessageMatch[]; tagIds: string[]; indexComplete?: boolean; error?: string; pending: boolean }>({ query: "", matches: [], tagIds: [], pending: false });
  const request = useRef<AbortController>(null);
  const version = useRef(0);
  const pause = useRef<ReturnType<typeof setTimeout>>(undefined);
  const deadline = useRef<ReturnType<typeof setTimeout>>(undefined);
  const firstEdit = useRef(0);
  const submit = useCallback(() => {
    clearTimeout(pause.current);
    clearTimeout(deadline.current);
    firstEdit.current = 0;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    const current = ++version.current;
    setResult({ query: searchKey, matches: [], tagIds: [], pending: !!searchKey });
    if (!searchKey) return;
    void rpc<SearchMessagesResponse>("SearchMessages", { query: searchKey }, controller.signal).then(({ matches, tagIds, indexComplete }) => {
      if (current === version.current) setResult({ query: searchKey, matches: matches ?? [], tagIds: tagIds ?? [], indexComplete, pending: false });
    }).catch((error: Error) => {
      if (current === version.current && !controller.signal.aborted) setResult({ query: searchKey, matches: [], tagIds: [], error: error.message, pending: false });
    });
  }, [searchKey]);
  useEffect(() => {
    if (!firstEdit.current) firstEdit.current = Date.now();
    pause.current = setTimeout(submit, 250);
    deadline.current = setTimeout(submit, Math.max(0, 1000 - (Date.now() - firstEdit.current)));
    return () => { clearTimeout(pause.current); clearTimeout(deadline.current); };
  }, [submit]);
  useEffect(() => () => { request.current?.abort(); version.current++; }, []);
  const pending = result.pending || result.query !== searchKey;
  const current = !pending && !result.error;
  const matching = rows.filter((row) => sessionMatchesSearch(row, filters, tab.agentFilter, tab.roomFilter, origins.values.get(row.id) ?? ""));
  const visible = new Set(rows.filter((row) => sessionMatchesSearch(row, { ...filters, needle: "" }, tab.agentFilter, tab.roomFilter, "")).map((row) => row.id));
  const messages = result.query === searchKey ? result.matches.filter((match) => visible.has(match.conversationId)) : [];
  const searching = !!tab.query.trim() || !!tab.agentFilter || !!tab.roomFilter;
  return <>
    <div id="search-tab-panel" role="tabpanel" aria-label="Search" className="min-w-0">
      <SessionSearch rows={rows} catalog={catalog} query={tab.query} setQuery={(query) => edit({ query })} agentFilter={tab.agentFilter} setAgentFilter={(agentFilter) => edit({ agentFilter })} roomFilter={tab.roomFilter} setRoomFilter={(roomFilter) => edit({ roomFilter })} inputRef={input} onKeyDown={(event) => { if (event.key === "Enter") { onFirstSubmit(); submit(); } }} />
    </div>
    <div className="min-h-0 flex-1 overflow-y-auto text-sm" aria-label="Search results">
      <SearchStatus pending={pending} checking={origins.pending} error={result.query === searchKey ? result.error : undefined} originError={origins.failed} empty={searching && current && matching.length + messages.length === 0} incomplete={!searchIsAuthoritative(sidebar) || origins.failed || origins.pending} unindexed={result.query === searchKey && result.indexComplete === false} retry={submit} />
      {searching ? <SearchMatches matching={matching} messages={messages} rows={rows} origins={origins.values} needle={filters.needle} sort={filters.sort} tagIds={result.tagIds} /> : null}
      {!searching ? <p className="text-muted-foreground">Type to search messages and conversations.</p> : null}
    </div>
  </>;
}

// Pills are the committed filter terms; the input keeps free text and the term still being typed.
function searchInput(query: string, typed: string, rows: Session[], catalog: { name: string }[]) {
  const { text, filterTerms } = sessionSearchTerms(query);
  const input = query.trimEnd().endsWith(typed.trimEnd()) && sessionSearchTerms(typed).text === text ? typed : text;
  const pills = [...new Set(filterTerms.slice(0, filterTerms.length - sessionSearchTerms(input).filterTerms.length))];
  const token = input.slice(input.search(/\S*$/));
  const tagPrefix = typedPrefix(token, "tag:"), cronPrefix = typedPrefix(token, "cron:"), isPrefix = typedPrefix(token, "is:"), sortPrefix = typedPrefix(token, "sort:");
  const agentPrefix = typedPrefix(text, "agent:"), roomPrefix = typedPrefix(text, "room:");
  const needle = ((tagPrefix ?? cronPrefix)?.replace(/^"|"$/g, "") ?? isPrefix ?? sortPrefix)?.toLowerCase();
  const names = roomPrefix !== null ? slackRooms(rows) : cronPrefix !== null ? [...new Set(rows.flatMap((row) => row.cronName ? [row.cronName] : []))] : tagPrefix !== null ? [...new Set(rows.flatMap((row) => row.tags ?? []))] : sortPrefix !== null ? ["newest", "oldest"] : ["pinned", "forked", "cron"];
  const offered = overlayChoices(agentPrefix, roomPrefix ?? needle ?? null, catalog, names);
  const completed = cronPrefix !== null ? sessionSearchTerms(token).cronNames[0] : needle;
  // A completed filter term commits on space or Enter instead of offering itself again.
  const choices = agentPrefix === null && roomPrefix === null && offered.some((item) => (cronPrefix !== null ? item.key : item.key.toLowerCase()) === completed) ? [] : offered;
  return { text, filterTerms, input, pills, token, tagPrefix, cronPrefix, sortPrefix, agentPrefix, roomPrefix, choices };
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
  const [typed, setTyped] = useState("");
  const { text, filterTerms, input, pills, token, tagPrefix, cronPrefix, sortPrefix, agentPrefix, roomPrefix, choices } = searchInput(query, typed, rows, catalog);
  const pick = choices.length === 0 ? 0 : overlayPick % choices.length;
  const active = useRef<HTMLButtonElement>(null);
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [pick, text]);
  const change = (terms: string[], rest: string) => {
    setTyped(rest);
    setQuery([...terms, rest].filter(Boolean).join(" "));
  };
  const edit = (value: string, all = false) => {
    const cut = all ? value.length : value.search(/\s\S*$/) + 1;
    const head = sessionSearchTerms(value.slice(0, cut));
    change([...pills, ...head.filterTerms], head.filterTerms.length ? [head.text, value.slice(cut)].filter(Boolean).join(" ") : value);
  };
  const applyOverlay = (name: string) => {
    if (agentPrefix === null && roomPrefix === null) {
      change([...pills, tagPrefix !== null || cronPrefix !== null ? `${cronPrefix !== null ? "cron" : "tag"}:${/\s/.test(name) || name.startsWith('"') ? JSON.stringify(name) : name}` : `${sortPrefix !== null ? "sort" : "is"}:${name}`], input.slice(0, input.length - token.length).trim());
    } else if (agentPrefix !== null) {
      setAgentFilter(name);
      change(filterTerms, "");
    } else {
      setRoomFilter(name);
      change(filterTerms, "");
    }
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
            {pills.map((term) => <FilterPill key={term} label={term} onClear={() => change(pills.filter((pill) => pill !== term), input)} />)}
            <Input
              ref={inputRef}
              value={input}
              onChange={(event) => {
                setOverlayPick(0);
                edit(event.target.value);
              }}
              aria-label={placeholder ?? "Search sessions"}
              placeholder={placeholder ?? (agentFilter || roomFilter || pills.length ? "Search" : "Search or agent: or room:")}
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
                    change(pills, "");
                    return;
                  }
                }
                if (event.key === "Enter" && (agentPrefix !== null || roomPrefix !== null)) {
                  event.preventDefault();
                  return;
                }
                if (event.key === "Enter") edit(input, true);
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
  const { close } = useContext(TabActions);
  return <header className="flex w-full shrink-0 items-center gap-2">
    <h1 className="text-lg font-semibold">{children}</h1>
    <Button type="button" variant="ghost" size="icon-sm" className="ml-auto hidden md:inline-flex" aria-label="Close" onClick={() => close([location.pathname])}><X /></Button>
  </header>;
}

type Line = { id: string; text: string; role: "user" | "assistant" | "thinking" | "tool" | "developer"; complete?: boolean; entryKey?: string; inputId?: string; messageId?: string; turnId?: string; toolCallId?: string; toolName?: string; toolParts?: Line[]; attachments?: (AttachmentMeta & { file?: File })[] } & Pick<TranscriptEvent, "agent" | "model" | "reasoningEffort" | "origin" | "header" | "principal" | "state" | "parentId" | "completionNotes" | "delegation" | "review">;
type OriginFilter = { sandboxed: boolean; canonical: boolean };

function lineId(role: Line["role"], text: string, seen: Map<string, number>) {
  const base = `${role}:${text}`;
  const n = (seen.get(base) ?? 0) + 1;
  seen.set(base, n);
  return `${base}:${n}`;
}

function transcriptTurns(lines: Line[], filter: OriginFilter = { sandboxed: true, canonical: true }) {
  const turns: { user: Line[]; items: Line[]; lines: Line[] }[] = [];
  let current = { user: [] as Line[], items: [] as Line[], lines: [] as Line[] };
  const calls = new Map<string, Line & { toolParts: Line[] }>();
  const skills = new Map<string, Line & { toolParts: Line[] }>();
  const flush = () => {
    if (current.user.length > 0 || current.items.length > 0) {
      turns.push(current);
      current = { user: [], items: [], lines: [] };
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
    current.lines.push(line);
    const callKey = `${line.parentId ?? line.entryKey ?? ""}/${line.toolCallId ?? ""}`;
    const resultCall = line.role === "tool" ? calls.get(callKey) : undefined;
    const skillHeader = line.text.split("\n", 1)[0];
    const skillCall = line.role === "developer" ? skills.get(skillHeader) : undefined;
    if (line.role === "user") {
      current.user.push(line);
    } else if (line.role === "tool" && line.toolName) {
      const call = { ...line, toolParts: [] as Line[] };
      current.items.push(call);
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
      current.items.push(line);
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
  const revert = useContext(RevertActions);
  return <div data-slot="message-actions" className={cn("invisible flex max-w-full items-center gap-1 group-focus-visible/message:visible [@media(hover:hover)]:group-hover/message:visible [@media(hover:hover)]:group-has-[:focus-visible]/message:visible [@media(hover:none)]:group-focus-within/message:visible", line.role === "user" && "self-end")}>
    {line.role === "assistant" || line.header ? <MessageFooter line={line} hasSandboxed={hasSandboxed} /> : null}
    <Button type="button" size="icon-xs" variant="ghost" aria-label="Copy message" title="Copy message" onClick={async () => {
      setError(false);
      try {
        await copyText(line.text, document.body);
        setCopied(line.text);
      } catch {
        setError(true);
      }
    }}>{copied === line.text && !error ? <Check /> : <Copy />}</Button>
    {revert?.available && line.role === "user" && line.messageId && line.entryKey ? <Button type="button" size="icon-xs" variant="ghost" className="size-11 sm:size-6" aria-label="Revert message" title="Revert message" disabled={revert.pending} onClick={() => void revert.run(line.messageId)}><Undo2 /></Button> : null}
    <span role="status" className={error ? "text-xs text-destructive" : "sr-only"}>{error ? "Could not copy. Select and copy the text." : copied === line.text ? "Copied" : ""}</span>
  </div>;
}

// Without `open`, a line keeps the always-open look that cron and steer previews use.
function TraceLine({ line, hasSandboxed, open }: { line: Line; hasSandboxed: boolean; open?: boolean }) {
  const body = line.role === "thinking" ? (
    <div className="flex items-start gap-1.5 px-1 py-0.5 text-[12px] leading-5 text-muted-foreground">
      <Bot className="size-3.5 shrink-0 opacity-80" />
      <span className="min-w-0 whitespace-pre-wrap break-words">{line.text}</span>
    </div>
  ) : (
    <div className="min-w-0 px-1 pb-3">
      <CodeBlock label="Instructions" text={line.text} />
      {line.header ? <MessageFooter line={line} hasSandboxed={hasSandboxed} /> : null}
    </div>
  );
  return open === undefined ? body : (
    <details open={open} className="min-w-0">
      <summary className="cursor-pointer px-1 py-1 text-xs text-muted-foreground hover:text-foreground">{line.role === "thinking" ? "Thinking" : "Instructions"}</summary>
      {body}
    </details>
  );
}

function MessageAuthor({ principal, role = "user" }: { principal?: string; role?: Line["role"] }) {
  return role === "user" && principal ? <span data-slot="message-author" className="mb-1 block whitespace-pre-wrap wrap-anywhere text-xs text-muted-foreground">{principal}</span> : null;
}

// Each Completion Note is one row with its note, from its stored job, folded beneath.
function CompletionNotes({ notes }: { notes: BackgroundJob[] }) {
  return notes.map((note) => <details key={note.jobId} className="mb-3 min-w-0">
    <summary className="cursor-pointer px-3 py-2 text-xs font-medium">{note.label} · {jobState(note)}</summary>
    <CodeBlock label={note.label} text={note.note} />
  </details>);
}

// A task row opens the subagent its call recorded; older rows open the delegation ending in their call ID.
function delegationOf(line: Line, delegations?: string[]) {
  return line.delegation || delegations?.find((child) => child.slice(child.lastIndexOf("/") + 1) === line.toolCallId);
}

function ToolLine({ line, conversationId, hasSandboxed, open }: { line: Line; conversationId: string; hasSandboxed: boolean; open?: boolean }) {
  const title = toolTitle(line);
  const delegations = use(Delegations);
  const delegation = delegationOf(line, delegations);
  // A reviewed call's row opens its permission review once the delegations list it; an older task row, the one ending in its call ID.
  const review = (line.review && delegations?.includes(line.review) ? line.review : undefined) || line.delegation && delegations?.find((child) => child !== delegation && child.slice(child.lastIndexOf("/") + 1) === line.toolCallId);
  const parts = [line, ...(line.toolParts ?? [])];
  const text = [
    line.toolName ? `Arguments\n${line.text.slice(line.toolName.length + 1)}` : `Result\n${line.text}`,
    ...parts.slice(1).map((part) => `${part.role === "developer" ? "Skill instructions" : "Result"}\n${part.text}`),
  ].join("\n\n");
  return (
    <details open={open ?? true} data-tool-call-id={line.toolCallId} className="mb-3 min-w-0">
      <summary className="cursor-pointer px-3 py-2 text-xs font-medium" title={title}>
        <span className="ml-1 inline-block max-w-[calc(100%-1.5rem)] truncate align-middle font-mono">{title}</span>
        {line.state ? <span aria-live="polite" className="ml-2 text-muted-foreground">{line.state}</span> : null}
      </summary>
      {delegation ? <Link href={delegationHref(delegation)} aria-label={`Open delegation: ${title}`} className="block w-fit px-3 pb-2 text-xs text-muted-foreground underline hover:text-foreground">Open delegation</Link> : null}
      {review ? <Link href={delegationHref(review)} aria-label={`Open permission review: ${title}`} className="block w-fit px-3 pb-2 text-xs text-muted-foreground underline hover:text-foreground">Open permission review</Link> : null}
      {line.state ? <MessageFooter line={line} hasSandboxed={hasSandboxed} /> : null}
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

function TranscriptLine({ line, conversationId, hasSandboxed, open }: { line: Line; conversationId: string; hasSandboxed: boolean; open?: boolean }) {
  if (line.completionNotes?.length) return <CompletionNotes notes={line.completionNotes} />;
  if (["thinking", "developer"].includes(line.role)) return <TraceLine line={line} hasSandboxed={hasSandboxed} open={open} />;
  if (line.role === "tool") return <ToolLine line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} open={open} />;
  const align = line.role === "user" ? "end" : undefined;
  return (
    <Message data-message-id={line.messageId || undefined} align={align} className="mb-4" tabIndex={0} onPointerDown={(event) => {
      if (event.pointerType === "touch" && !(event.target as Element).closest("button, a, input, textarea, summary")) event.currentTarget.focus({ preventScroll: true });
    }}>
      <MessageContent>
        <Bubble variant={line.role === "user" ? "secondary" : "ghost"} align={align}>
          <BubbleContent>
            <MessageAuthor principal={line.principal} role={line.role} />
            <TranscriptText text={line.text} />
          </BubbleContent>
          <MessageAttachments attachments={line.attachments} conversationId={conversationId} />
        </Bubble>
        <MessageActions line={line} hasSandboxed={hasSandboxed} />
      </MessageContent>
    </Message>
  );
}

type Turn = ReturnType<typeof transcriptTurns>[number];

function turnKey(turn: Turn) {
  return turn.user[0]?.id ?? turn.items[0]?.id;
}

function TurnRailButton({ turn, index, onJump, preview }: { turn: Turn; index: number; onJump: (turn: Turn) => void; preview?: boolean }) {
  const text = (turn.user[0]?.text ?? turn.items.find((line) => line.role === "assistant")?.text ?? "Activity").split("\n").flatMap((line) => /^ {0,3}(`{3,}|~{3,})/.test(line) ? [] : [line.replace(/^[\t ]*(?:[-*•] |\d+\. |(?:>|&gt;) )/, "")]).join(" ").replace(/\s+/g, " ").trim() || "Activity";
  const clean = useCleanText(text).slice(0, 120);
  return <button type="button" aria-label={`${preview ? "Jump to turn" : "Turn"} ${index + 1}: ${clean}`} className={preview ? "block w-full rounded-sm px-3 py-2 text-left text-xs hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring" : "group flex min-h-6 w-full items-center justify-end rounded-sm text-left text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"} onClick={() => onJump(turn)}>
    {preview ? <span className="line-clamp-2 break-words">{index + 1}. <InlineText text={text} /></span> : <span aria-hidden="true" className="flex w-6 shrink-0 items-center justify-center"><span className="h-0.5 w-2 rounded-full bg-current transition-[width] group-hover:w-4 group-focus-visible:w-4" /></span>}
  </button>;
}

function useTranscriptPosition(conversationId: string, lines: Line[], turns: Turn[]) {
  const viewport = useRef<HTMLDivElement>(null);
  const identity = useQuery(queries.identity());
  const firstOwner = useRef<string | undefined>(identity.isSuccess ? identity.data.username : undefined);
  const { scrollToMessage } = useMessageScroller();
  const target = useContext(SessionCommands).command?.target;
  const search = useSearch();
  const messageId = conversationId && location.pathname === sessionPath(conversationId) ? new URLSearchParams(search).get("message") : null;
  const targetId = messageId ?? (target?.conversationId === conversationId ? target.message.messageId : null);
  const targetTurn = targetId ? turns.findIndex((turn) => turn.user.some((line) => line.messageId === targetId) || turn.items.some((line) => line.role === "assistant" && line.messageId === targetId)) : -1;
  const targetKey = targetTurn < 0 ? undefined : turnKey(turns[targetTurn]);
  const seen = useCallback(() => {
    const element = viewport.current;
    if (!element || !identity.isSuccess || !element.getClientRects().length) return;
    if (firstOwner.current === undefined) firstOwner.current = identity.data.username;
    if (firstOwner.current !== identity.data.username) return;
    const bounds = element.getBoundingClientRect();
    const visible = [...element.querySelectorAll<HTMLElement>('[data-slot="message"][data-message-id]')].filter((node) => {
      const rect = node.getBoundingClientRect();
      return rect.bottom > bounds.top && rect.top < bounds.bottom;
    });
    const last = visible.at(-1)?.dataset.messageId;
    if (last) localStorage.setItem(`last-seen:${identity.data.username}`, `${sessionPath(conversationId)}?message=${encodeURIComponent(last)}`);
  }, [conversationId, identity.isSuccess, identity.data]);
  useEffect(() => {
    const frame = requestAnimationFrame(() => requestAnimationFrame(seen));
    return () => cancelAnimationFrame(frame);
  }, [lines, seen]);
  useEffect(() => {
    if (!targetKey) return;
    scrollToMessage(`turn-${targetKey}`, { align: "center", behavior: "instant" });
    const frame = requestAnimationFrame(() => {
      const line = [...(viewport.current?.querySelectorAll<HTMLElement>("[data-message-id]") ?? [])].find((node) => node.dataset.messageId === targetId);
      line?.scrollIntoView({ block: "center", behavior: "instant" });
      seen();
    });
    return () => cancelAnimationFrame(frame);
  }, [targetKey, targetId, scrollToMessage, seen, viewport]);
  return { viewport, seen, scrollToMessage };
}

// Finished turns keep their line objects between updates, so only changed turns and the live one re-render.
const TranscriptTurn = memo(function TranscriptTurn({ turn, index, detail, conversationId, hasSandboxed }: { turn: Turn; index: number; live: boolean; detail: TimelineRows; conversationId: string; hasSandboxed: boolean }) {
  return (
    <MessageScrollerItem messageId={`turn-${turnKey(turn)}`} data-turn-key={turnKey(turn)} role="region" aria-label={`Turn ${index + 1}`} tabIndex={-1}>
      {turn.user.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
      {projectTimeline(turn.items, detail).map((row) => row.kind === "line" ? <TranscriptLine key={row.line.id} line={row.line} open={row.open} conversationId={conversationId} hasSandboxed={hasSandboxed} /> : (
        <details key={row.key} className="group pb-3">
          <summary className="flex w-fit cursor-pointer list-none items-center gap-1 px-1 py-2 text-xs text-muted-foreground hover:text-foreground [&::-webkit-details-marker]:hidden">
            {row.label} <span aria-hidden="true" className="transition-transform group-open:rotate-90">▸</span>
          </summary>
          <div className="ml-1 border-l pl-3">
            {row.members.map(({ line, open }) => <TranscriptLine key={line.id} line={line} open={open} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
          </div>
        </details>
      ))}
    </MessageScrollerItem>
  );
}, (previous, next) => !next.live && previous.index === next.index && previous.detail === next.detail && previous.conversationId === next.conversationId && previous.hasSandboxed === next.hasSandboxed
  && previous.turn.lines.length === next.turn.lines.length && previous.turn.lines.every((line, index) => line === next.turn.lines[index]));

function WorkingStatus({ conversationId, movable, onChange }: { conversationId: string; movable: boolean; onChange: () => Promise<unknown> }) {
  const move = useMutation({ mutationFn: mutations.moveToBackground, onSuccess: onChange });
  return <MessageScrollerItem><div className="flex flex-wrap items-center gap-2 px-1 pb-4">
    <p role="status" className="text-sm text-muted-foreground">Working…</p>
    {movable ? <Button type="button" size="xs" variant="outline" disabled={move.isPending} onClick={() => move.mutate({ conversationId })}>Move to background</Button> : null}
    {move.error ? <span role="alert" className="text-xs text-destructive">{move.error.message}</span> : null}
  </div></MessageScrollerItem>;
}

function TranscriptLog({
  conversationId,
  lines,
  working,
  terminal,
  origin,
  filter,
  hasSandboxed,
  more,
  loadEarlier,
  movable,
  refreshHistory,
}: {
  lines: Line[];
  conversationId: string;
  working: boolean;
  movable: boolean;
  refreshHistory: () => Promise<unknown>;
  terminal?: string;
  origin?: ChatOrigin;
  filter: OriginFilter;
  hasSandboxed: boolean;
  more: boolean;
  loadEarlier: () => Promise<void>;
}) {
  const turns = transcriptTurns(lines, filter);
  const detail = useTimelineDetail();
  const { viewport, seen, scrollToMessage } = useTranscriptPosition(conversationId, lines, turns);
  const anchor = useRef<{ key: string; top: number }>(undefined);
  const [loading, setLoading] = useState(false);
  // Scrolling near the top loads one earlier page and keeps the turns on screen still.
  const earlier = useCallback(() => {
    const first = viewport.current?.querySelector<HTMLElement>("[data-turn-key]");
    if (first) anchor.current = { key: first.dataset.turnKey!, top: first.getBoundingClientRect().top };
    setLoading(true);
    void loadEarlier().finally(() => { setLoading(false); });
  }, [loadEarlier, viewport]);
  useLayoutEffect(() => {
    const saved = anchor.current;
    const node = saved && [...viewport.current?.querySelectorAll<HTMLElement>("[data-turn-key]") ?? []].find((item) => item.dataset.turnKey === saved.key);
    if (node) viewport.current!.scrollTop += node.getBoundingClientRect().top - saved.top;
    if (!loading) anchor.current = undefined;
  }, [lines, loading, viewport]);
  const nearTop = useCallback(() => {
    const element = viewport.current;
    if (more && !loading && element && element.scrollTop < element.clientHeight) earlier();
  }, [more, loading, earlier, viewport]);
  useEffect(() => {
    const frame = requestAnimationFrame(nearTop);
    return () => cancelAnimationFrame(frame);
  }, [lines, nearTop]);
  const running = usePendingCron(conversationId);
  const emptyMessage = lines.length === 0
    ? (running ? `${running} is running` : "Send a message to start the conversation.")
    : "No messages for selected origins.";
  useEffect(() => { if (running && lines.length > 0) notePendingCron(conversationId, ""); }, [running, lines.length, conversationId]);
  const jumpToTurn = (turn: Turn) => {
    scrollToMessage(`turn-${turnKey(turn)}`, { align: "start", behavior: "instant" });
    viewport.current?.querySelector<HTMLElement>(`[data-turn-key="${CSS.escape(turnKey(turn))}"]`)?.focus({ preventScroll: true });
  };
  return (
    <MessageScroller className="flex-1">
    <MessageScrollerViewport ref={viewport} id="transcript-scroll" preserveScrollOnPrepend={false} onScroll={() => { seen(); nearTop(); }} className="overflow-x-hidden [overflow-anchor:none]">
      <div className="min-h-full pl-3 pr-8 pt-3 pb-4 sm:pl-5 sm:pr-10 sm:pt-4">
      <MessageScrollerContent className="mx-auto w-full min-w-0 max-w-3xl">
      {origin && (origin.kind === "cron" || origin.kind === "external_mcp") ? <MessageScrollerItem messageId="origin"><OriginCard origin={origin} /></MessageScrollerItem> : null}
      {turns.length === 0 && !working ? (
        <MessageScrollerItem className="flex flex-1 items-center justify-center">
          <p role="status" className="text-sm text-muted-foreground">{emptyMessage}</p>
        </MessageScrollerItem>
      ) : (
        <>
          {loading ? <MessageScrollerItem><p role="status" className="px-1 text-sm text-muted-foreground">Loading earlier messages…</p></MessageScrollerItem> : null}
          {turns.map((turn, index) => <TranscriptTurn key={turnKey(turn)} turn={turn} index={index} live={index === turns.length - 1} detail={detail} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
          {working ? <WorkingStatus conversationId={conversationId} movable={movable} onChange={refreshHistory} /> : null}
          {terminal ? <MessageScrollerItem><p role="status" className="px-1 pb-4 text-sm text-muted-foreground">Turn {terminal}.</p></MessageScrollerItem> : null}
        </>
      )}
      </MessageScrollerContent>
      </div>
    </MessageScrollerViewport>
    {turns.length > 0 ? (
      <nav aria-label="Conversation turns" className="group/rail absolute top-12 bottom-0 right-[6px] flex w-8 items-center justify-end py-3">
        <div role="group" aria-label="Message previews" className="absolute right-full top-1/2 hidden max-h-[calc(100%-1.5rem)] w-[min(20rem,calc(100vw-4rem))] -translate-y-1/2 overflow-y-auto overscroll-contain rounded-md border bg-popover p-1 text-popover-foreground shadow-lg group-hover/rail:block group-focus-within/rail:block">
          {turns.map((turn, index) => <TurnRailButton key={turnKey(turn)} turn={turn} index={index} onJump={jumpToTurn} preview />)}
        </div>
        <div className="max-h-full w-8 overflow-y-auto">
          {turns.map((turn, index) => <TurnRailButton key={turnKey(turn)} turn={turn} index={index} onJump={jumpToTurn} />)}
        </div>
      </nav>
    ) : null}
    <MessageScrollerButton aria-label="Scroll to latest" title="Scroll to latest" behavior="instant" />
    </MessageScroller>
  );
}

function applyHistoryDelta(draft: ComposerDraft, view: HistoryView) {
  const cutoffChanged = (draft.revertMessageId ?? "") !== (view.revertMessageId ?? "");
  if (view.reset) draft.historyEpoch = (draft.historyEpoch ?? 0) + 1;
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
    const reset = view.reset && (!draft.revision || cutoffChanged);
    const changed = new Set([...(view.reset ? view.entryKeys : view.replacedKeys), ...view.removedKeys]);
    const groups = Map.groupBy([...draft.lines.filter((line) => !reset && line.entryKey && !changed.has(line.entryKey)), ...historyLines(view.messages)], (line) => line.entryKey!);
    // Earlier pages are not followed; they stay until removed or a reset restarts the view.
    const followed = new Set([...view.entryKeys, ...view.removedKeys]);
    const keys = [...new Set(draft.lines.flatMap((line) => !view.reset && line.entryKey && !followed.has(line.entryKey) ? [line.entryKey] : [])), ...view.entryKeys];
    const retained = new Set(keys);
    let anchor = "";
    for (const line of draft.lines) {
      if (line.entryKey && retained.has(line.entryKey)) anchor = line.entryKey;
      if (!reset && !line.entryKey && line.complete && !consumed.has(line.inputId || line.id)) {
        if (!groups.has(anchor)) groups.set(anchor, []);
        groups.get(anchor)!.push(line);
      }
    }
    const pending = cutoffChanged ? [] : draft.lines.filter((line) => !line.entryKey && line.role === "user" && !line.complete && !consumed.has(line.inputId || line.id));
    draft.lines = [...(groups.get("") ?? []), ...keys.flatMap((key) => groups.get(key) ?? []), ...pending];
  }
  if (view.reset || draft.start === undefined) {
    draft.start = view.start;
    draft.more = view.more;
    draft.delegations = undefined;
  }
  draft.busy = view.running || draft.lines.some((line) => !line.entryKey && line.role === "user" && !line.complete);
  draft.terminal = view.terminal;
  draft.origin = view.origin;
  draft.revertEligible = view.revertEligible;
  draft.revertMessageId = view.revertMessageId;
  draft.canUndo = view.canUndo;
  draft.movable = view.movable;
  draft.jobs = view.backgroundJobs;
  draft.revision = view.revision;
  draft.historyError = "";
  return newlyConsumed;
}

function readHistoryDelta(id: string, draft: ComposerDraft, onDraftChange: () => void): Promise<void> {
  draft.historyAgain = true;
  if (draft.historyRead) return draft.historyRead;
  draft.historyRead = (async () => {
    do {
      draft.historyAgain = false;
      const epoch = draft.historyEpoch ?? 0;
      try {
        const key = queries.history({ id }).queryKey;
        const cached = queryClient.getQueryData<HistoryView>(key);
        const view = await queries.history({ id, revision: cached ? draft.revision : undefined, limit: historyPage }).queryFn({});
        if (epoch !== (draft.historyEpoch ?? 0)) { draft.historyAgain = true; continue; }
        if (!view.reset && view.revision === draft.revision
          && draft.busy === (view.running || draft.lines.some((line) => !line.entryKey && line.role === "user" && !line.complete))
          && draft.terminal === view.terminal && JSON.stringify(draft.origin) === JSON.stringify(view.origin)
          && JSON.stringify(cached?.delegations) === JSON.stringify(view.delegations)
          && draft.movable === view.movable && JSON.stringify(draft.jobs) === JSON.stringify(view.backgroundJobs)
          && !draft.historyError) continue;
        if (applyHistoryDelta(draft, view)) void queryClient.invalidateQueries({ queryKey: ["queue"] });
        // Main's delegation panel reads a full durable parent view from this cache.
        const changed = new Set([...view.replacedKeys, ...view.removedKeys]);
        const groups = Map.groupBy([...(view.reset ? [] : cached?.messages ?? []).filter((message) => !changed.has(message.entryKey)), ...view.messages], (message) => message.entryKey);
        queryClient.setQueryData<HistoryView>(key, { ...view, reset: true, replacedKeys: [], removedKeys: [], messages: view.entryKeys.flatMap((entry) => groups.get(entry) ?? []) });
      } catch (err) {
        // A history failure must not turn an accepted Prompt into a failed send.
        captureException(err);
        draft.historyError = err instanceof Error ? err.message : "history failed";
      }
      onDraftChange();
    } while (draft.historyAgain);
  })().finally(() => { draft.historyRead = undefined; });
  return draft.historyRead;
}

const historyPage = 50;

// Prepends one settled page: the previous historyPage entries, or every entry from a linked message onward.
function readEarlierHistory(id: string, draft: ComposerDraft, onDraftChange: () => void, from?: string): Promise<void> {
  const before = draft.start;
  const epoch = draft.historyEpoch ?? 0;
  if (draft.earlier || !draft.more || !before) return draft.earlier ?? Promise.resolve();
  draft.earlier = (async () => {
    try {
      const view = await queries.history({ id, before, ...(from ? { from } : { limit: historyPage }) }).queryFn({});
      if (draft.start === before && epoch === (draft.historyEpoch ?? 0)) {
        const known = new Set(draft.lines.map((line) => line.entryKey));
        draft.lines = [...historyLines(view.messages).filter((line) => !known.has(line.entryKey)), ...draft.lines];
        draft.start = view.start;
        draft.more = view.more;
        draft.delegations = [...new Set([...(draft.delegations ?? []), ...view.delegations])];
      }
    } catch (err) {
      captureException(err);
      draft.historyError = err instanceof Error ? err.message : "history failed";
    }
    onDraftChange();
  })().finally(() => { draft.earlier = undefined; });
  return draft.earlier;
}

// Open scrolls to a script's call, loading earlier pages until it is rendered or none remain.
async function openCall(draft: ComposerDraft, onDraftChange: () => void, scrollToMessage: ReturnType<typeof useMessageScroller>["scrollToMessage"], callId: string) {
  const find = () => document.querySelector<HTMLElement>(`#transcript-scroll [data-tool-call-id="${CSS.escape(callId)}"]`);
  for (let start: string | undefined; !find() && draft.more && start !== draft.start;) {
    start = draft.start;
    await readEarlierHistory(draft.sessionId, draft, onDraftChange);
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  }
  const node = find();
  if (!node) return;
  for (let group = node.parentElement?.closest("details"); group; group = group.parentElement?.closest("details")) group.open = true;
  scrollToMessage(`turn-${node.closest<HTMLElement>("[data-turn-key]")!.dataset.turnKey}`, { align: "start", behavior: "instant" });
  node.scrollIntoView({ block: "center", behavior: "instant" });
}

function historyLines(messages: TranscriptEvent[]): Line[] {
  const seen = new Map<string, number>();
  return messages.map((message) => {
    const role = message.role === "thinking" || message.role === "user" || message.role === "tool" || message.role === "developer" ? message.role : "assistant";
    return { ...message, id: message.inputId || message.itemId || message.messageId || lineId(role, message.text, seen), role };
  });
}

function useSessionStream(id: string, draft: ComposerDraft, onDraftChange: () => void) {
  const scope = useContext(DraftScope);
  const history = useQuery({ ...queries.history({ id }), enabled: false });
  const refreshHistory = useCallback(() => { draft.historyEpoch = (draft.historyEpoch ?? 0) + 1; return readHistoryDelta(draft.sessionId, draft, onDraftChange); }, [draft, onDraftChange]);
  const loadEarlier = useCallback((from?: string) => readEarlierHistory(draft.sessionId, draft, onDraftChange, from), [draft, onDraftChange]);
  const followed = history.data?.delegations, earlier = draft.delegations;
  const delegations = useMemo(() => earlier ? [...new Set([...(followed ?? []), ...earlier])] : followed, [followed, earlier]);
  const setBusy = useCallback((value: boolean) => { draft.busy = value; onDraftChange(); }, [draft, onDraftChange]);
  const setLines = useCallback((update: (current: Line[]) => Line[]) => { draft.lines = update(draft.lines); onDraftChange(); }, [draft, onDraftChange]);
  useEffect(() => {
    if (!id || !scope) return;
    const stream = new EventSource(`/stream?${new URLSearchParams({ id })}`);
    void refreshHistory();
    stream.onopen = () => { void refreshHistory(); };
    stream.onmessage = (event) => {
      const change = JSON.parse(String(event.data)) as { conversationId: string; revision: string };
      if (change.conversationId === id) void refreshHistory();
    };
    return () => {
      stream.close();
    };
  }, [id, scope, refreshHistory]);
  return { busy: draft.busy, setBusy, lines: draft.lines, setLines, refreshHistory, opening: id !== "" && !draft.revision, historyError: draft.historyError, origin: draft.origin, terminal: draft.terminal, delegations, more: draft.more ?? false, start: draft.start, loadEarlier, movable: draft.movable ?? false, hasSandboxed: draft.lines.some((line) => line.origin === "sandboxed") };
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

// Own hydration separately from transcript/history requests. A remounted session
// reuses its in-memory draft; identity/workspace changes replace only the draft map.
function useComposerDraft(id: string, drafts: Map<string, ComposerDraft>, changed: () => void) {
  const scope = useContext(DraftScope);
  const draft = useMemo(() => drafts.get(id) ?? { text: "", files: [], agent: "", sessionId: id, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0 }, [id, drafts]);
  useLayoutEffect(() => { drafts.set(id, draft); }, [id, drafts, draft]);
  useEffect(() => {
    if (!scope || draft.hydrated) return;
    draft.persistenceKey = JSON.stringify([scope, id]);
    let current = true;
    void draftContent(draft.persistenceKey).then((content) => {
      if (current && content && !draft.edit && !draft.text && !draft.files.length) Object.assign(draft, content);
    }, () => { draft.persistenceError = "Local draft storage is unavailable. Keep this page open."; }).finally(() => {
      if (current) { draft.hydrated = true; changed(); }
    });
    return () => { current = false; };
  }, [scope, id, draft, changed]);
  return draft;
}

function Transcript({ id, drafts, scopeError, onDraftChange, onCreated }: { id: string; drafts: Map<string, ComposerDraft>; scopeError?: string; onDraftChange: () => void; onCreated: (id: string) => void }) {
  const [filter, setFilter] = useState<OriginFilter>({ sandboxed: true, canonical: true });
  const search = useSearch();
  const target = useContext(SessionCommands).command?.target;
  const previewing = target?.conversationId === id;
  const preview = useQuery({ ...queries.history({ id }), enabled: previewing });
  const previewLines = useMemo(() => previewing && preview.data ? historyLines(preview.data.messages) : undefined, [previewing, preview.data]);
  const draft = useComposerDraft(id, drafts, onDraftChange);
  const route = useRoute();
  useLayoutEffect(() => {
    if (id === "" && draft.sessionId !== "" && drafts.get("") === draft) {
      drafts.delete("");
      onCreated(draft.sessionId);
      route.goSession(draft.sessionId);
    }
  });
  const { busy, setBusy, lines, setLines, refreshHistory, opening, historyError, origin, terminal, delegations, more, start, loadEarlier, movable, hasSandboxed } = useSessionStream(id, draft, onDraftChange);
  const messageId = location.pathname === sessionPath(id) ? new URLSearchParams(search).get("message") : null;
  const matchedOrigin = (previewLines ?? lines).find((line) => line.messageId === messageId)?.origin;
  // A linked message older than the loaded entries loads every entry from it onward.
  const linkedEntry = messageId?.split(":")[0];
  useEffect(() => {
    if (!previewing && more && linkedEntry && /^\d+$/.test(linkedEntry) && BigInt(linkedEntry) < BigInt(start!)) void loadEarlier(linkedEntry);
  }, [previewing, more, linkedEntry, start, loadEarlier]);
  const visibleFilter = { sandboxed: filter.sandboxed || matchedOrigin === "sandboxed", canonical: filter.canonical || matchedOrigin === "canonical" };
  const runRevert = useCallback((messageId?: string, redo?: boolean) => revertComposer(draft, messageId, !!redo, refreshHistory, onDraftChange), [draft, refreshHistory, onDraftChange]);
  const available = !!draft.revertEligible && !previewing, pending = [draft.reverting, draft.sending, !draft.hydrated].some(Boolean);
  const revert = useMemo(() => ({ available, pending, run: runRevert }), [available, pending, runRevert]);
  const error = [scopeError, historyError].find(Boolean);
  return (
    <RevertActions value={revert}>
      {draft.revertMessageId ? <div role="status" className="flex flex-wrap items-center gap-2 px-12 py-2 text-xs text-muted-foreground md:px-3"><span>History reverted. Files and external effects are unchanged.</span><Button size="sm" variant="outline" className="min-h-11 sm:min-h-8" disabled={revert.pending} onClick={() => void revert.run(undefined, true)}>Redo</Button></div> : null}
      <Delegations value={previewing ? preview.data?.delegations : delegations}><TranscriptLog conversationId={id} lines={previewLines ?? lines} working={!previewing && busy} terminal={previewing ? undefined : terminal} origin={origin} filter={visibleFilter} hasSandboxed={hasSandboxed} more={!previewing && more} loadEarlier={loadEarlier} movable={movable} refreshHistory={refreshHistory} /></Delegations>
      {error ? <p role="alert" className="px-3 text-sm text-destructive">{error}</p> : null}
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
      <fieldset disabled={[opening, previewing, !draft.hydrated].some(Boolean)} className={previewing ? "hidden" : "contents"}>
        <SessionComposer id={id} draft={draft} drafts={drafts} onDraftChange={onDraftChange} busy={busy} setBusy={setBusy} setLines={setLines} refreshHistory={refreshHistory} />
      </fieldset>
    </RevertActions>
  );
}

function DelegationPanel({ id }: { id: string }) {
  const child = new URLSearchParams(useSearch()).get("delegation") ?? "";
  const wide = useMedia("(min-width: 64rem)");
  const main = useQuery({ ...queries.history({ id }), enabled: false });
  const history = useQuery({ ...queries.history({ id: child }), enabled: child !== "" });
  useEffect(() => {
    if (child && main.data?.revertMessageId && !main.data.delegations.some((level) => child === level || child.startsWith(`${level}/`))) navigate(delegationHref());
  }, [child, main.data]);
  if (!child) return null;
  const opened = historyLines(main.data?.messages ?? []).map((line) => delegationOf(line, main.data?.delegations));
  const first = opened.find((level) => level && (child === level || child.startsWith(`${level}/`))) ?? child;
  const levels = [...child.matchAll(/\/|$/g)].map((match) => child.slice(0, match.index)).filter((level) => level.length >= first.length).map((level, index, all) => {
    const parent = queryClient.getQueryData<HistoryView>(queries.history({ id: index ? all[index - 1] : id }).queryKey);
    const row = historyLines(parent?.messages ?? []).find((line) => line.toolName && [delegationOf(line, parent?.delegations), line.review].includes(level));
    return { level, label: row ? toolTitle(row) : level.slice(level.lastIndexOf("/") + 1) };
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
        {transcriptTurns(lines).flatMap((turn) => [...turn.user, ...turn.items]).map((line) => <TranscriptLine key={line.id} line={line} conversationId={child} hasSandboxed={sandboxed} />)}
      </Delegations>
      {history.isSuccess ? <Link href={delegationHref(parent?.level)} className="block w-fit text-xs text-muted-foreground underline hover:text-foreground">{parent ? `Back to ${parent.label}` : "Back to conversation"}</Link> : null}
    </div></ScrollArea>
  </>;
  return wide ? <ResizableAside side="delegation" aria-label="Delegation" className="flex flex-col border-l bg-background">{body}</ResizableAside> : <Sheet open onOpenChange={(open) => { if (!open) close(); }}>
    <SheetContent side="right" showCloseButton={false} className="data-[side=right]:w-full data-[side=right]:sm:max-w-none"><SheetTitle className="sr-only">Delegation</SheetTitle>{body}</SheetContent>
  </Sheet>;
}

async function sendComposer(input: {
  draft: ComposerDraft;
  onDraftChange: () => void;
  text: string;
  files: PendingFile[];
  delivery?: PromptDelivery;
  principal?: string;
  busy: boolean;
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
  const delivery = input.delivery ?? (input.busy ? "QUEUE" : "STEER");
  // Match strings.Fields/unicode.IsSpace in frontend/rpc/server.go, not JavaScript's whitespace.
  const command = input.delivery === "STASH" ? null : /^\p{White_Space}*\$(enqueue|stash|steer)(?=\p{White_Space}|$)\p{White_Space}*/u.exec(input.text);
  const followUp: PromptDelivery = command ? command[1] === "enqueue" ? "QUEUE" : command[1] === "stash" ? "STASH" : "STEER" : delivery;
  const stashing = followUp === "STASH";
  const stopping = !stashing && isStopCommand(input.text);
  if (draft.sending || (input.text.trim() === "" && input.files.length === 0)) {
    return;
  }
  draft.sending = true;
  const submission = ++draft.submission;
  input.onDraftChange();
  const agent = draft.agent;
  let dispatchedEdit: number | undefined;
  const enqueue = followUp !== "STEER" && !stopping;
  if (!input.busy && !enqueue) {
    input.setBusy(true);
  }
  input.scrollToEnd();
  input.setSendError("");
  const optimistic: Line = { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), role: "user", text: command ? input.text.slice(command[0].length) : input.text, principal: input.principal };
  try {
    let sessionId = input.sessionId;
    if (sessionId === "") {
      sessionId = await input.create.mutateAsync({ agent: input.selected });
      input.goSession(sessionId);
    }
    optimistic.attachments = input.files.map(({ id, file }) => ({ id, name: file.name, mimeType: file.type, size: String(file.size), conversationId: sessionId, file }));
    if (!enqueue && input.busy && !stopping) {
      draft.parked = [...(draft.parked ?? []), optimistic];
      input.onDraftChange();
    } else if (!enqueue) input.setLines((current) => [...current, optimistic]);
    const attachments: AttachmentMeta[] = await Promise.all(input.files.map(async ({ file }) => {
      const response = await fetch(`/api/UploadAttachment?${new URLSearchParams({ conversationId: sessionId, name: file.name })}`, { method: "POST", body: file });
      if (!response.ok) throw new Error(`Upload failed: ${file.name}`);
      return response.json();
    }));
    if (!stashing && !stopping && !/^\p{White_Space}*\$agent(?=\p{White_Space}|$)/u.test(optimistic.text) && input.sessionId !== "" && input.currentAgent !== "" && input.selected !== "" && input.selected !== input.currentAgent) {
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
    const response = input.prompt.mutateAsync({ id: sessionId, text: input.text, delivery, messageId: optimistic.id, ...(attachments.length ? { attachmentIds: attachments.map((file) => file.id) } : {}) });
    draft.sending = false;
    input.onDraftChange();
    input.setAgentOpen(false);
    const privateText = await response;
    optimistic.complete = true;
    void queryClient.invalidateQueries({ queryKey: ["agents"] });
    if (enqueue) {
      await queryClient.invalidateQueries({ queryKey: ["queue"] });
      return;
    }
    await input.refreshHistory();
    if (privateText) {
      const parked = draft.parked?.some((line) => line.id === optimistic.id);
      draft.parked = draft.parked?.filter((line) => line.id !== optimistic.id);
      input.setLines((current) => [...current, ...(parked ? [optimistic] : []), { id: `${optimistic.id}:reply`, role: "assistant", text: privateText, complete: true }]);
    }
  } catch (err) {
    draft.parked = draft.parked?.filter((line) => line.id !== optimistic.id);
    captureException(err);
    if (dispatchedEdit === undefined) draft.sending = false;
    else if (draft.edit === dispatchedEdit && draft.submission === submission) {
      draft.text = input.text;
      draft.files = input.files;
      draft.agent = agent;
      draft.edit++;
    }
    input.setLines((current) => current.filter((line) => line.id !== optimistic.id));
    if (draft.submission === submission) {
      input.setSendError(err instanceof Error ? err.message : "send failed");
      if (!stashing) input.setBusy(input.busy);
    }
  }
}

// OpenCode V2 packages/app/src/session/revert.ts: only the dispatched session
// owns restored content. Redo changes history, never the composer.
async function revertComposer(draft: ComposerDraft, messageId: string | undefined, redo: boolean, refresh: () => Promise<unknown>, changed: () => void) {
  if (draft.reverting || draft.sending) return;
  const edit = draft.edit, submission = draft.submission;
  draft.reverting = true;
  draft.historyEpoch = (draft.historyEpoch ?? 0) + 1;
  draft.error = "";
  changed();
  try {
    await queryClient.cancelQueries({ queryKey: ["history"] });
    if (redo) await mutations.clearRevert({ id: draft.sessionId });
    else {
      const result = await mutations.stageRevert({ id: draft.sessionId, messageId });
      if (result.prompt) {
        const files = await restoreFiles(result.prompt.attachments ?? []);
        if (draft.edit === edit && draft.submission === submission) {
          draft.text = result.prompt.text;
          draft.files = files;
          draft.edit++;
          draft.focus = (draft.focus ?? 0) + 1;
        }
      }
    }
  } catch (err) {
    draft.error = err instanceof Error ? err.message : "History change failed";
  } finally {
    draft.historyEpoch = (draft.historyEpoch ?? 0) + 1;
    draft.reverting = false;
    changed();
    await queryClient.invalidateQueries({ queryKey: ["queue"] });
    await refresh();
  }
}

type QueueAgentSwitch = {
  draft: Pick<ComposerDraft, "sending">;
  onDraftChange: () => void;
  id: string;
  selected: string;
  currentAgent: string;
  prompt: { mutateAsync: (value: { id: string; text: string }) => Promise<string> };
};

// Queue actions switch an unlisted agent first, holding the draft's sending flag so a send cannot switch at the same time.
async function switchQueueAgent(input: QueueAgentSwitch) {
  if (input.currentAgent === "" || input.selected === "" || input.selected === input.currentAgent) return;
  input.draft.sending = true;
  input.onDraftChange();
  try {
    await input.prompt.mutateAsync({ id: input.id, text: `$agent ${input.selected}` });
    await queryClient.invalidateQueries({ queryKey: ["agents"] });
  } finally {
    input.draft.sending = false;
    input.onDraftChange();
  }
}

async function promoteComposer(input: QueueAgentSwitch & {
  draft: Pick<ComposerDraft, "sending" | "submission">;
  itemId: string;
  busy: boolean;
  steerQueueItem: { mutateAsync: (value: { id: string; itemId: string }) => Promise<unknown> };
  setBusy: (value: boolean) => void;
  setSendError: (value: string) => void;
}) {
  if (input.id === "" || input.draft.sending) {
    return;
  }
  const submission = ++input.draft.submission;
  if (!input.busy) {
    input.setBusy(true);
  }
  input.setSendError("");
  try {
    await switchQueueAgent(input);
    await input.steerQueueItem.mutateAsync({ id: input.id, itemId: input.itemId });
  } catch (err) {
    if (input.draft.submission === submission) {
      input.setSendError(err instanceof Error ? err.message : "steer failed");
      input.setBusy(input.busy);
    }
  }
}

async function popComposer(input: QueueAgentSwitch & {
  itemId: string;
  popQueueItem: { mutateAsync: (value: { id: string; itemId: string }) => Promise<unknown> };
  setSendError: (value: string) => void;
}) {
  if (input.draft.sending) return;
  input.setSendError("");
  try {
    await switchQueueAgent(input);
    await input.popQueueItem.mutateAsync({ id: input.id, itemId: input.itemId });
  } catch (err) {
    input.setSendError(err instanceof Error ? err.message : "pop failed");
  }
}

async function stopComposer(input: {
  draft: ComposerDraft;
  id: string;
  busy: boolean;
  prompt: { mutateAsync: (value: { id: string; text: string }) => Promise<unknown> };
  refreshHistory: () => Promise<unknown>;
  setSendError: (value: string) => void;
}) {
  if (!input.busy || input.id === "") {
    return;
  }
  const submission = ++input.draft.submission;
  input.setSendError("");
  try {
    await input.prompt.mutateAsync({ id: input.id, text: "$stop" });
    await input.refreshHistory();
  } catch (err) {
    if (input.draft.submission === submission) input.setSendError(err instanceof Error ? err.message : "stop failed");
  }
}

function pendingInputs(draft: ComposerDraft, items: QueueItem[]) {
  if (draft.revertMessageId) return { parked: [], queued: [] };
  const consumed = draft.consumed ?? new Set<string>();
  const waiting = items.filter((item) => !consumed.has(item.id));
  const parked = new Map<string, Line>(waiting.filter((item) => item.delivery === "STEER").map((item) => [item.id, { ...item, role: "user" }]));
  for (const line of draft.parked ?? []) {
    if (!consumed.has(line.id) && !parked.has(line.id)) parked.set(line.id, line);
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
  refreshHistory: () => Promise<unknown>;
}) {
  const identity = useQuery(queries.identity());
  const { setCommand } = useContext(SessionCommands);
  const { promote } = useContext(TabActions);
  const scope = useContext(DraftScope)!;
  const revert = useContext(RevertActions)!;
  const { scrollToEnd, scrollToMessage } = useMessageScroller();
  const prompt = useMutation({ mutationFn: mutations.prompt, onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["queue"] }); } });
  const agents = useQuery({ ...queries.agents({ conversationId: id }), refetchInterval: 2000 });
  const queueQuery = useQuery({ ...queries.queue({ id }), enabled: id !== "", refetchInterval: 2000 });
  const removeQueueItem = useMutation({ mutationFn: mutations.removeQueueItem, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const steerQueueItem = useMutation({ mutationFn: mutations.steerQueueItem, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const popQueueItem = useMutation({ mutationFn: mutations.popQueueItem, onSuccess: () => queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const reorderQueue = useMutation({ mutationFn: mutations.reorderQueue, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const invalidateSidebar = useContext(SidebarInvalidation);
  const create = useMutation({ mutationFn: mutations.createSession, onSuccess: invalidateSidebar });
  const { text, files, sending, agent } = draft;
  const [, setTyped] = useState(0);
  // Typing redraws only this composer; a whole-app redraw per keystroke made long chats lag.
  // A draft or a send keeps the preview tab.
  const setText = (value: string) => {
    draft.text = value;
    draft.edit++;
    setTyped((count) => count + 1);
    const error = draft.persistenceError ?? "";
    void persistComposerDraft(draft)?.finally(() => { if (draft.persistenceError !== error) onDraftChange(); });
    promote(id ? sessionPath(id) : "/");
  };
  const setFiles = (value: PendingFile[]) => { draft.files = value; draft.edit++; onDraftChange(); };
  const setAgent = (value: string) => { draft.agent = value; draft.edit++; onDraftChange(); };
  const [agentOpen, setAgentOpen] = useState(false);
  const [dollarOff, setDollarOff] = useState(false);
  const [dollarPick, setDollarPick] = useState("");
  const sendError = draft.error;
  const setSendError = (value: string) => { draft.error = value; onDraftChange(); };
  const currentAgent = id === "" ? "main" : agents.data?.currentAgent ?? "";
  const catalog = agents.data?.agents ?? [];
  // Sends switch to the shown agent, so an unlisted current agent moves to the first listed one.
  const selected = [agent, currentAgent].find((name) => catalog.some((item) => item.name === name)) ?? catalog[0]?.name ?? currentAgent;
  const skills = useQuery({ ...queries.skills({ agent: selected }), enabled: selected !== "", placeholderData: undefined });
  const workflows = useQuery({ ...queries.workflows(), staleTime: 60_000 });
  const matches = dollarOff ? [] : dollarMatches(text, skills.data ?? [], workflows.data ?? []);
  const pick = Math.max(0, matches.findIndex((item) => item.invocation === dollarPick));
  const applyDollar = (invocation: string) => {
    setText(invocation);
    setDollarOff(invocation !== "$skill " && invocation !== "$workflow ");
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
    const command = /^\p{White_Space}*\$(fork|handoff|undo|redo)\p{White_Space}*$/u.exec(draft.text);
    if (delivery !== "STASH" && command) {
      if (["undo", "redo"].includes(command[1])) return revert.run(undefined, command[1] === "redo");
      if (!id) { setSendError("Open a session first."); return Promise.resolve(); }
      setCommand({ mode: command[1] as "fork" | "handoff", source: id });
      setText("");
      return Promise.resolve();
    }
    promote(id ? sessionPath(id) : "/");
    return sendComposer({
      draft,
      onDraftChange,
      text: draft.text,
      files: draft.files,
      delivery,
      principal: identity.isSuccess && !identity.isFetching ? identity.data.principal : undefined,
      busy,
      sessionId: draft.sessionId,
      selected,
      currentAgent,
      goSession: (sessionId) => {
        void draftContent(JSON.stringify([scope, ""]), { text: "", files: [], agent: "" }).catch(() => { draft.persistenceError = "Draft could not be saved locally. Keep this page open."; onDraftChange(); });
        draft.sessionId = sessionId;
        draft.persistenceKey = JSON.stringify([scope, sessionId]);
        draft.persistedEdit = undefined;
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
  };
  const promoteQueued = (itemId: string) => promoteComposer({ draft, onDraftChange, id, itemId, busy, selected, currentAgent, prompt, steerQueueItem, setBusy, setSendError });
  const stop = () => stopComposer({ draft, id, busy, prompt, refreshHistory, setSendError });
  return (
    <>
      {sendError ? <p className="px-3 pb-2 text-sm text-destructive sm:px-5">{sendError}</p> : null}
      {draft.persistenceError ? <p role="alert" className="px-3 pb-2 text-sm text-destructive">{draft.persistenceError}</p> : null}
      {parked.length > 0 ? <section aria-label="Pending steers" className="mx-auto w-full max-w-3xl px-3"><p className="text-xs text-muted-foreground">Waiting to steer</p>{parked.map((line) => <TranscriptLine key={line.id} line={line} conversationId={id} hasSandboxed={false} />)}</section> : null}
      <Composer
        files={files}
        setFiles={setFiles}
        sending={[sending, draft.reverting].some(Boolean)}
        focus={draft.focus}
        historyCommand={(redo) => void revert.run(undefined, redo)}
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
        popQueued={(itemId) => popComposer({ draft, onDraftChange, id, itemId, selected, currentAgent, prompt, popQueueItem, setSendError })}
        removeQueued={(itemId) => void removeQueueItem.mutateAsync({ id, itemId }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "remove failed"))}
        reorderQueued={(itemIds) => void reorderQueue.mutateAsync({ id, itemIds }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "reorder failed"))}
        jobs={jobsSlot(id, draft.jobs, refreshHistory, (callId) => void openCall(draft, onDraftChange, scrollToMessage, callId))}
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
  const [quote, setQuote] = useState<{ left: number; top: number }>();
  useEffect(() => {
    const clear = () => setQuote(undefined);
    const select = () => {
      const selection = window.getSelection();
      const transcript = document.getElementById("transcript-scroll");
      if (sending || !selection?.rangeCount || !selection.toString().trim()) return clear();
      const range = selection.getRangeAt(0);
      if (!transcript?.contains(range.startContainer) || !transcript.contains(range.endContainer)) return clear();
      const rect = range.getBoundingClientRect();
      setQuote({ left: Math.max(8, Math.min(rect.left, window.innerWidth - 88)), top: Math.max(8, Math.min(rect.bottom + 6, window.innerHeight - 44)) });
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
      // Read the selection now: a click can arrive before the last selectionchange has rendered.
      const value = (text ? text + "\n\n" : "") + window.getSelection()!.toString().replace(/\r\n?/g, "\n").split("\n").map((line) => `> ${line}`).join("\n") + "\n\n";
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
  focus,
  historyCommand,
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
  jobs,
}: {
  files: PendingFile[];
  setFiles: (files: PendingFile[]) => void;
  sending: boolean;
  focus?: number;
  historyCommand: (redo: boolean) => void;
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
  jobs: ReactNode;
}) {
  const mac = typeof navigator === "object" && /(Mac|iPod|iPhone|iPad)/.test(navigator.platform);
  const selectedButton = useRef<HTMLButtonElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const messageInput = useRef<HTMLTextAreaElement>(null);
  const { composer } = useContext(SessionCommands);
  useEffect(() => {
    if (focus) { const input = messageInput.current!; input.focus(); input.setSelectionRange(input.value.length, input.value.length); }
  }, [focus]);
  useImperativeHandle(composer, () => (command) => {
    // The agent picker keeps focus so desktop typing searches agents.
    if (command === "agent") return setAgentOpen(true);
    if (command === "undo" || command === "redo") return historyCommand(command === "redo");
    if (command === "stop") void stop();
    else applyDollar(`$${command} ${text}`);
    messageInput.current?.focus();
  });
  const empty = text.trim() === "" && files.length === 0;
  const stopping = busy && empty;
  const steerOff = !busy || sending || empty || isStopCommand(text);
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
                  disabled={sending}
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
        <QueuePanel conversationId={sessionId} items={queued} busy={busy} sending={sending} onSteer={(id, itemText) => void steerQueued(id, itemText)} onPop={popQueued} onRemove={removeQueued} onReorder={reorderQueued} jobs={jobs} />
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
              <AgentPicker catalog={catalog} selected={selected} setAgent={setAgent} open={agentOpen} setOpen={setAgentOpen} disabled={sending || catalog.length === 0} />
               <div className="hidden md:contents"><SessionHeaderActions id={sessionId} /></div>
            </div>
            <div className="flex shrink-0 items-center justify-end gap-1">
              <Button type="button" variant={steerOff ? "quiet" : "ghost"} className="h-11 sm:h-8" disabled={sending || empty} onClick={() => void send("STASH")}>Stash</Button>
              <Tooltip>
                <TooltipTrigger render={<Button type="button" variant="ghost" className="h-11 sm:h-8" disabled={steerOff} />} aria-label="Steer" onClick={() => void send("STEER")}>
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

function AgentPicker({ catalog, selected, setAgent, open, setOpen, disabled }: { catalog: Agent[]; selected: string; setAgent: (name: string) => void; open: boolean; setOpen: (open: boolean) => void; disabled: boolean }) {
  return (
    <Combobox.Root items={catalog} value={catalog.find((item) => item.name === selected) ?? null} onValueChange={(item) => { if (item) setAgent(item.name); }} itemToStringLabel={(item) => item.name} filter={(item, query) => `${item.name} ${item.model ?? ""}`.toLowerCase().includes(query.toLowerCase())} autoHighlight open={open} onOpenChange={setOpen} disabled={disabled}>
      <Combobox.Trigger aria-label="Choose agent" title={selected} className="flex min-h-11 w-fit min-w-0 max-w-36 items-center gap-1.5 rounded-lg border border-input bg-transparent py-2 pr-2 pl-2.5 text-sm whitespace-nowrap outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 sm:h-8 sm:min-h-8 dark:bg-input/30 dark:hover:bg-input/50 [&_svg]:size-4 [&_svg]:shrink-0"><Bot /><span className={cn("min-w-0 flex-1 overflow-hidden text-left", !selected && "text-muted-foreground")}>{selected || "Choose agent"}</span><ChevronDown className="text-muted-foreground" /></Combobox.Trigger>
      <Combobox.Portal><Combobox.Positioner side="top" align="start" sideOffset={4} className="isolate z-50">
        <Combobox.Popup data-slot="combobox-content" initialFocus={() => !window.matchMedia("(pointer: coarse)").matches} className="flex max-h-(--available-height) w-[min(28rem,calc(100vw-2rem))] flex-col gap-1 overflow-hidden rounded-lg bg-popover p-1 text-popover-foreground shadow-md ring-1 ring-foreground/10">
          <Combobox.Input render={<Input />} aria-label="Search agents" placeholder="Search agents" />
          <Combobox.Empty className="px-2 py-2 text-sm text-muted-foreground empty:p-0">No matching agents</Combobox.Empty>
          <Combobox.List className="min-h-0 overflow-y-auto">{(item: Agent) => <Combobox.Item key={item.name} value={item} className="flex min-h-11 cursor-default items-center gap-1.5 rounded-md py-1 pr-2 pl-1.5 text-sm outline-hidden select-none data-highlighted:bg-accent data-highlighted:text-accent-foreground sm:min-h-8"><span className="flex min-w-0 flex-1 flex-col whitespace-normal"><span className="break-all">{item.name}</span><span className="break-all text-xs text-muted-foreground">{[item.model, item.reasoning].filter(Boolean).join(" · ")}</span></span><Combobox.ItemIndicator><Check className="size-4" /></Combobox.ItemIndicator></Combobox.Item>}</Combobox.List>
        </Combobox.Popup>
      </Combobox.Positioner></Combobox.Portal>
    </Combobox.Root>
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
  }} onPaste={(event) => {
    if (!event.clipboardData.files.length) return;
    event.preventDefault();
    if (!sending) addFiles(Array.from(event.clipboardData.files));
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
        <ConfigRow label="Configured user" value={identity.data?.username ?? ""} />
        <ConfigRow label="Tailscale user" value={view.tailscaleUser || "Unavailable"} />
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

const tabPlacements = [{ value: "top", label: "Top" }, { value: "left", label: "Left sidebar" }];

function ConfigPage() {
  const config = useQuery({ ...queries.config(), staleTime: 60_000 });
  const tabs = useContext(TabActions);
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Config</PageTitle>
      <ConfigSection title="Appearance">
        <div className="flex flex-wrap items-center justify-between gap-4 border-b py-2">
          <span className="text-sm text-muted-foreground">Theme</span>
          <PaletteChooser />
        </div>
        <div className="flex flex-wrap items-center justify-between gap-4 border-b py-2">
          <span className="text-sm text-muted-foreground">Tabs</span>
          <Select items={tabPlacements} value={tabs.placement} onValueChange={(value) => tabs.place(value === "left" ? "left" : "top")}>
            <SelectTrigger aria-label="Tab placement" className="min-w-40"><SelectValue /></SelectTrigger>
            <SelectContent align="end"><SelectGroup>{tabPlacements.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectGroup></SelectContent>
          </Select>
        </div>
      </ConfigSection>
      <ConfigSection title="Timeline">
        <TimelineDetailCard />
      </ConfigSection>
      {config.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {config.error ? <p className="text-sm text-destructive">{config.error.message}</p> : null}
      {config.data ? <ConfigLoaded view={config.data} /> : null}
    </div>
  );
}
