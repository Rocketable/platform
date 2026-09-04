"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { httpLink } from "@trpc/client";
import { createTRPCReact } from "@trpc/react-query";
import { Bot, Calendar, Check, GripVertical, Menu, Search, Send, Settings, Sparkles, Square, SquarePen, Undo2, X } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { ThemeToggle } from "@/components/theme";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import type { AppRouter } from "@/router";
import { runPreload } from "@/preload";
import { decodeSessionId, encodeSessionId } from "@/session-id";

const trpc = createTRPCReact<AppRouter>();
const queryClient = new QueryClient();
const trpcClient = trpc.createClient({ links: [httpLink({ url: "/trpc" })] });

function sessionPath(id: string) {
  return `/s/${encodeSessionId(id)}`;
}

function slackSession(id: string) {
  return id.startsWith("slack-thread:");
}

function composerAgents(
  sessionId: string,
  title: string,
  current: string,
  catalog: { name: string; model?: string; reasoning?: string }[],
  channels: { channel?: string; agents?: string[] }[],
) {
  if (!slackSession(sessionId)) {
    return catalog;
  }
  let names = channels.find((channel) => channel.channel === title)?.agents ?? [];
  if (names.length === 0) {
    names = channels.find((channel) => channel.channel === "@")?.agents ?? [];
  }
  const allowed = new Set(names);
  const filtered = catalog.filter((item) => allowed.has(item.name));
  if (current !== "" && !allowed.has(current)) {
    const extra = catalog.find((item) => item.name === current);
    if (extra) {
      return [extra, ...filtered];
    }
  }
  return filtered;
}

function typedPrefix(query: string, prefix: string) {
  if (!query.toLowerCase().startsWith(prefix)) {
    return null;
  }
  return query.slice(prefix.length);
}

function slackRooms(sessions: { id: string; title?: string }[]) {
  const rooms: string[] = [];
  const seen = new Set<string>();
  for (const session of sessions) {
    if (!slackSession(session.id)) {
      continue;
    }
    const name = session.title ?? "";
    if (name !== "" && !seen.has(name)) {
      seen.add(name);
      rooms.push(name);
    }
  }
  return rooms;
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

function PrefixOverlay({
  items,
  pick,
  onPick,
}: {
  items: { key: string; label: string; detail?: string }[];
  pick: number;
  onPick: (key: string) => void;
}) {
  if (items.length === 0) {
    return null;
  }
  return (
    <ul className="absolute inset-x-0 top-full z-10 mt-1 overflow-hidden rounded-lg border bg-popover text-popover-foreground shadow-md">
      {items.map((item, index) => (
        <li key={item.key}>
          <button
            type="button"
            className={cn("flex w-full flex-col items-start px-3 py-2 text-left text-sm", index === pick && "bg-accent")}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onPick(item.key)}
          >
            <span className="font-medium">{item.label}</span>
            {item.detail ? <span className="text-xs text-muted-foreground">{item.detail}</span> : null}
          </button>
        </li>
      ))}
    </ul>
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
];

function dollarMatches(text: string) {
  if (!text.startsWith("$") || text.includes("\n") || text.includes(" ")) {
    return [];
  }
  const query = text.slice(1).toLowerCase();
  if (dollarCommands.some((cmd) => cmd.name === query && cmd.hint === "")) {
    return [];
  }
  return dollarCommands.filter((cmd) => cmd.name.startsWith(query));
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
  items,
  busy,
  onSteer,
  onRemove,
  onReorder,
}: {
  items: { id: string; text: string }[];
  busy: boolean;
  onSteer: (id: string, text: string) => void;
  onRemove: (id: string) => void;
  onReorder: (itemIds: string[]) => void;
}) {
  const dragId = useRef<string | null>(null);
  const [order, setOrder] = useState<string[] | null>(null);
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
            <span className="min-w-0 flex-1 truncate text-sm">{item.text}</span>
            <button
              type="button"
              className="shrink-0 rounded-md px-2 py-0.5 text-xs text-muted-foreground hover:bg-accent"
              onClick={() => onSteer(item.id, item.text)}
            >
              {busy ? "Steer" : "Send"}
            </button>
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
  const router = useRouter();
  const cron = pathname === "/cron";
  const agents = pathname === "/agents";
  const skills = pathname === "/skills";
  const config = pathname === "/config";
  const id = pathname.startsWith("/s/") ? decodeSessionId(pathname.slice(3)) : "";
  return {
    cron,
    agents,
    skills,
    config,
    id,
    goHome: () => router.push("/"),
    goCron: () => router.push("/cron"),
    goSession: (sessionId: string) => router.push(sessionPath(sessionId)),
  };
}

function TabPane({ show, children }: { show: boolean; children: ReactNode }) {
  return <div className={cn("min-h-0 flex-1 flex-col", show ? "flex" : "hidden")}>{children}</div>;
}

function WarmTabs({ cron, agents, skills, config }: { cron: boolean; agents: boolean; skills: boolean; config: boolean }) {
  const utils = trpc.useUtils();
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
      () => utils.agents.prefetch(),
      () => utils.skills.prefetch(),
      () => utils.cronJobs.prefetch(),
      () => utils.config.prefetch(),
      () => setWarm({ cron: true, agents: true, skills: true, config: true }),
    );
  }, [utils]);
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
  const proto = trpc.protocol.useQuery(undefined, { refetchInterval: 2000 });
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

export function App() {
  const route = useRoute();
  return (
    <trpc.Provider client={trpcClient} queryClient={queryClient}>
      <QueryClientProvider client={queryClient}>
        <ProtocolGuard />
        <div className="flex h-dvh min-h-0 overflow-hidden overscroll-y-none bg-background">
          <aside className="hidden w-64 shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground md:flex lg:w-72">
            <SessionList />
          </aside>
          <div className="flex min-h-0 min-w-0 flex-1 flex-col">
            <header className="flex h-12 items-center gap-2 border-b px-3 md:hidden">
              <Sheet>
                <SheetTrigger asChild>
                  <Button variant="ghost" size="icon" aria-label="Sessions">
                    <Menu className="h-4 w-4" />
                  </Button>
                </SheetTrigger>
                <SheetContent className="w-72 p-0">
                  <SessionList />
                </SheetContent>
              </Sheet>
              <span className="text-sm font-medium">RocketClaw</span>
            </header>
            <main className="flex min-h-0 min-w-0 flex-1 flex-col">
              <WarmTabs cron={route.cron} agents={route.agents} skills={route.skills} config={route.config} />
              <TabPane show={!route.cron && !route.agents && !route.skills && !route.config}>
                <Transcript />
              </TabPane>
            </main>
          </div>
        </div>
      </QueryClientProvider>
    </trpc.Provider>
  );
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
  onSettle,
}: {
  session: { id: string; title?: string; preview?: string; updatedAt?: string; agent?: string; settled?: boolean };
  current: boolean;
  onSettle: (settled: boolean) => void;
}) {
  const title = (session.preview ?? "").split("\n", 1)[0] || sessionLabel(session.id);
  const channel = slackSession(session.id) ? (session.title ?? "") : "";
  const meta = [channel, session.agent, relativeTime(session.updatedAt ?? "")].filter(Boolean).join(" · ");
  return (
    <li className="group flex list-none items-stretch py-0.5">
      <Link
        href={sessionPath(session.id)}
        className={cn(
          "relative flex min-w-0 flex-1 cursor-pointer overflow-hidden rounded-md px-2.5 py-2 text-left outline-none select-none",
          current ? "bg-sidebar-row-active text-sidebar-foreground" : "text-sidebar-foreground hover:bg-sidebar-row-hover",
        )}
      >
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate text-sm font-medium">{title}</span>
          <span className="truncate text-xs text-muted-foreground">{meta}</span>
        </span>
      </Link>
      <button
        type="button"
        className={cn(
          "flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-sidebar-row-hover hover:text-sidebar-foreground",
          current ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100",
        )}
        aria-label={session.settled ? "Unsettle" : "Settle"}
        onClick={() => onSettle(!session.settled)}
      >
        {session.settled ? <Undo2 className="h-3.5 w-3.5" /> : <Check className="h-3.5 w-3.5" />}
      </button>
    </li>
  );
}

function SessionInbox({
  sessions,
  currentId,
  onSettle,
}: {
  sessions: { id: string; title?: string; preview?: string; updatedAt?: string; agent?: string; settled?: boolean }[];
  currentId: string;
  onSettle: (id: string, settled: boolean) => void;
}) {
  const active: typeof sessions = [];
  const settled: typeof sessions = [];
  for (const session of sessions) {
    if (session.settled) {
      settled.push(session);
    } else {
      active.push(session);
    }
  }
  return (
    <ul className="flex min-h-0 flex-1 flex-col overflow-y-auto px-2 pb-2">
      {active.map((session) => (
        <SessionRow key={session.id} session={session} current={currentId === session.id} onSettle={(next) => onSettle(session.id, next)} />
      ))}
      {settled.length > 0 ? <li className="list-none px-2.5 pt-3 pb-1 text-xs font-medium text-muted-foreground">Settled</li> : null}
      {settled.map((session) => (
        <SessionRow key={session.id} session={session} current={currentId === session.id} onSettle={(next) => onSettle(session.id, next)} />
      ))}
    </ul>
  );
}

function SessionList() {
  const utils = trpc.useUtils();
  const sessions = trpc.sessions.useQuery(undefined, { refetchInterval: 2000 });
  const agents = trpc.agents.useQuery(undefined, { staleTime: 60_000 });
  const create = trpc.createSession.useMutation();
  const settle = trpc.settleSession.useMutation({ onSuccess: () => void utils.sessions.invalidate() });
  const route = useRoute();
  const [query, setQuery] = useState("");
  const [agentFilter, setAgentFilter] = useState("");
  const [roomFilter, setRoomFilter] = useState("");
  const [overlayPick, setOverlayPick] = useState(0);
  const catalog = agents.data ?? [];
  const rows = sessions.data ?? [];
  const agentPrefix = typedPrefix(query, "agent:");
  const roomPrefix = typedPrefix(query, "room:");
  const choices = overlayChoices(agentPrefix, roomPrefix, catalog, slackRooms(rows));
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
  const needle = agentPrefix === null && roomPrefix === null ? query.trim().toLowerCase() : "";
  const filtered = rows.filter((session) => {
    if (agentFilter !== "" && (session.agent ?? "") !== agentFilter) {
      return false;
    }
    if (roomFilter !== "" && (slackSession(session.id) ? (session.title ?? "") : "") !== roomFilter) {
      return false;
    }
    if (needle === "") {
      return true;
    }
    return `${session.title ?? ""} ${session.preview ?? ""} ${session.agent ?? ""} ${sessionLabel(session.id)}`.toLowerCase().includes(needle);
  });
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex h-12 shrink-0 items-center justify-between gap-2 px-3">
        <Link href="/" className="truncate text-sm font-medium tracking-tight">
          RocketClaw
        </Link>
        <ThemeToggle />
      </div>
      <div className="flex shrink-0 items-center gap-1 px-2 pb-2">
        <Button
          variant="ghost"
          size="icon"
          className="size-8 shrink-0"
          aria-label="New session"
          onClick={async () => {
            route.goSession(await create.mutateAsync({}));
          }}
        >
          <SquarePen className="h-4 w-4" />
        </Button>
        <div className="relative min-w-0 flex-1">
          <PrefixOverlay items={choices} pick={pick} onPick={applyOverlay} />
          <div className="flex h-8 min-w-0 items-center gap-1 rounded-md border border-sidebar-border bg-background pr-2 pl-2">
            <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            {agentFilter ? <FilterPill label={`agent:${agentFilter}`} onClear={() => setAgentFilter("")} /> : null}
            {roomFilter ? <FilterPill label={`room:${roomFilter}`} onClear={() => setRoomFilter("")} /> : null}
            <input
              value={query}
              onChange={(event) => {
                setOverlayPick(0);
                setQuery(event.target.value);
              }}
              placeholder={agentFilter || roomFilter ? "Search" : "Search or agent: or room:"}
              className="min-w-0 flex-1 bg-transparent text-sm outline-none"
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
      </div>
      <SessionInbox sessions={filtered} currentId={route.id} onSettle={(id, next) => void settle.mutateAsync({ id, settled: next })} />
      <div className="flex shrink-0 flex-wrap items-center gap-1 border-t border-sidebar-border p-2">
        <Button variant={route.cron ? "secondary" : "ghost"} size="icon" className="size-8" aria-label="Cron" asChild>
          <Link href="/cron" onMouseEnter={() => void utils.cronJobs.prefetch()}>
            <Calendar className="h-4 w-4" />
          </Link>
        </Button>
        <Button variant={route.agents ? "secondary" : "ghost"} size="icon" className="size-8" aria-label="Agents" asChild>
          <Link href="/agents" onMouseEnter={() => void utils.agents.prefetch()}>
            <Bot className="h-4 w-4" />
          </Link>
        </Button>
        <Button variant={route.skills ? "secondary" : "ghost"} size="icon" className="size-8" aria-label="Skills" asChild>
          <Link href="/skills" onMouseEnter={() => void utils.skills.prefetch()}>
            <Sparkles className="h-4 w-4" />
          </Link>
        </Button>
        <Button variant={route.config ? "secondary" : "ghost"} size="icon" className="size-8" aria-label="Config" asChild>
          <Link href="/config" onMouseEnter={() => void utils.config.prefetch()}>
            <Settings className="h-4 w-4" />
          </Link>
        </Button>
      </div>
    </div>
  );
}

type Line = { id: string; text: string; role: "user" | "assistant" | "thinking" };

function thinkingRows(text: string, seen: Map<string, number>) {
  const rows: Line[] = [];
  for (const row of text.split("\n")) {
    const line = row.trim();
    if (line === "") {
      continue;
    }
    rows.push({ id: lineId("thinking", line, seen), text: line, role: "thinking" });
  }
  return rows;
}

function appendThinking(current: Line[], text: string) {
  const seen = new Map<string, number>();
  for (const line of current) {
    seen.set(`${line.role}:${line.text}`, (seen.get(`${line.role}:${line.text}`) ?? 0) + 1);
  }
  return [...current, ...thinkingRows(text, seen)];
}

function replaceThinking(current: Line[], text: string) {
  let i = current.length;
  while (i > 0 && current[i - 1].role === "thinking") {
    i -= 1;
  }
  const seen = new Map<string, number>();
  for (const line of current.slice(0, i)) {
    seen.set(`${line.role}:${line.text}`, (seen.get(`${line.role}:${line.text}`) ?? 0) + 1);
  }
  return [...current.slice(0, i), ...thinkingRows(text, seen)];
}

function lineId(role: Line["role"], text: string, seen: Map<string, number>) {
  const base = `${role}:${text}`;
  const n = (seen.get(base) ?? 0) + 1;
  seen.set(base, n);
  return `${base}:${n}`;
}

function appendLine(current: Line[], role: Line["role"], text: string) {
  const last = current[current.length - 1];
  if (last && last.role === role && last.text === text) {
    return current;
  }
  const seen = new Map<string, number>();
  for (const line of current) {
    seen.set(`${line.role}:${line.text}`, (seen.get(`${line.role}:${line.text}`) ?? 0) + 1);
  }
  return [...current, { id: lineId(role, text, seen), text, role }];
}

function replaceAssistant(current: Line[], text: string) {
  let i = current.length;
  if (i > 0 && current[i - 1].role === "assistant") {
    i -= 1;
  }
  const seen = new Map<string, number>();
  for (const line of current.slice(0, i)) {
    seen.set(`${line.role}:${line.text}`, (seen.get(`${line.role}:${line.text}`) ?? 0) + 1);
  }
  if (text.trim() === "") {
    return current.slice(0, i);
  }
  const line: Line = { id: lineId("assistant", text, seen), text, role: "assistant" };
  return [...current.slice(0, i), line];
}

function TranscriptLog({
  lines,
  thinking,
  scroller,
}: {
  lines: Line[];
  thinking: boolean;
  scroller: RefObject<HTMLDivElement | null>;
}) {
  return (
    <div ref={scroller} className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain px-3 [overflow-anchor:none] sm:px-5">
      {lines.length === 0 && !thinking ? (
        <div className="flex h-full items-center justify-center">
          <p className="text-sm text-muted-foreground">Send a message to start the conversation.</p>
        </div>
      ) : (
        <div className="mx-auto w-full min-w-0 max-w-3xl pt-3 pb-4 sm:pt-4">
          {lines.map((line) =>
            line.role === "user" ? (
              <div key={line.id} className="flex flex-col items-end gap-1 pb-4">
                <div className="min-w-0 max-w-[80%] rounded-2xl bg-message px-4 py-3 text-sm leading-relaxed text-message-foreground">
                  <div className="whitespace-pre-wrap break-words">{line.text}</div>
                </div>
              </div>
            ) : line.role === "thinking" ? (
              <div key={line.id} className="flex items-center gap-1.5 px-1 py-0.5 text-[12px] leading-5 text-muted-foreground">
                <Bot className="size-3.5 shrink-0 opacity-80" />
                <span className="min-w-0 truncate">{line.text}</span>
              </div>
            ) : (
              <div key={line.id} className="min-w-0 px-1 pb-4">
                <div className="whitespace-pre-wrap break-words text-[15px] leading-7">{line.text}</div>
              </div>
            ),
          )}
          {thinking ? <p className="px-1 pb-4 text-sm text-muted-foreground">Thinking…</p> : null}
        </div>
      )}
    </div>
  );
}

function nextLines(current: Line[], payload: { text: string; snapshot: boolean; role?: string }) {
  if (payload.role === "thinking") {
    return payload.snapshot ? appendThinking(current, payload.text) : replaceThinking(current, payload.text);
  }
  const role = payload.role === "assistant" || (!payload.snapshot && payload.role !== "user") ? "assistant" : "user";
  if (role === "assistant" && !payload.snapshot) {
    return replaceAssistant(current, payload.text);
  }
  return appendLine(current, role, payload.text);
}

function applyStreamEvent(
  payload: { text: string; snapshot: boolean; role?: string; complete?: boolean },
  setBusy: (value: boolean) => void,
  setLines: (update: (current: Line[]) => Line[]) => void,
  reset: boolean,
) {
  if (payload.complete) {
    setBusy(false);
  }
  setLines((current) => nextLines(reset ? [] : current, payload));
}

function useSessionStream(id: string) {
  const [busy, setBusy] = useState(false);
  const [lines, setLines] = useState<Line[]>([]);
  const scroller = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  useEffect(() => {
    follow.current = true;
  }, [id]);
  useEffect(() => {
    if (!id) {
      setLines([]);
      return;
    }
    let cancelled = false;
    let replace = true;
    setLines([]);
    const stream = new EventSource(`/stream?id=${encodeSessionId(id)}`);
    stream.onopen = () => {
      replace = true;
    };
    stream.onmessage = (event) => {
      if (cancelled) {
        return;
      }
      const payload = JSON.parse(String(event.data)) as { text: string; snapshot: boolean; role?: string; complete?: boolean };
      const reset = payload.snapshot && replace;
      if (reset) {
        replace = false;
      }
      applyStreamEvent(payload, setBusy, setLines, reset);
    };
    return () => {
      cancelled = true;
      stream.close();
    };
  }, [id]);
  useEffect(() => {
    const el = scroller.current;
    if (!el) {
      return;
    }
    const nearEnd = () => el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    const onNavigate = () => {
      if (!nearEnd()) {
        follow.current = false;
      }
    };
    const onScroll = () => {
      if (nearEnd()) {
        follow.current = true;
      }
    };
    el.addEventListener("wheel", onNavigate, { passive: true });
    el.addEventListener("touchmove", onNavigate, { passive: true });
    el.addEventListener("pointerdown", onNavigate, { passive: true });
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      el.removeEventListener("wheel", onNavigate);
      el.removeEventListener("touchmove", onNavigate);
      el.removeEventListener("pointerdown", onNavigate);
      el.removeEventListener("scroll", onScroll);
    };
  }, [id]);
  useEffect(() => {
    const el = scroller.current;
    if (!el || !follow.current) {
      return;
    }
    const frame = requestAnimationFrame(() => {
      if (follow.current) {
        el.scrollTop = el.scrollHeight;
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [lines]);
  return { busy, setBusy, lines, setLines, scroller, follow };
}

function Transcript() {
  const route = useRoute();
  const { busy, setBusy, lines, setLines, scroller, follow } = useSessionStream(route.id);
  return (
    <>
      <TranscriptLog lines={lines} thinking={busy && lines.at(-1)?.role !== "thinking"} scroller={scroller} />
      <SessionComposer id={route.id} busy={busy} setBusy={setBusy} lines={lines} setLines={setLines} follow={follow} />
    </>
  );
}

async function sendComposer(input: {
  text: string;
  delivery?: "STEER" | "QUEUE";
  busy: boolean;
  working: boolean;
  sessionId: string;
  selected: string;
  currentAgent: string;
  goSession: (id: string) => void;
  prompt: { mutateAsync: (value: { id: string; text: string; delivery?: "STEER" | "QUEUE" }) => Promise<string> };
  create: { mutateAsync: (value: { agent?: string }) => Promise<string> };
  utils: { queue: { invalidate: () => Promise<unknown> } };
  follow: RefObject<boolean>;
  setBusy: (value: boolean) => void;
  setText: (value: string) => void;
  setAgentOpen: (value: boolean) => void;
  setSendError: (value: string) => void;
  setLines: (update: (current: Line[]) => Line[]) => void;
}) {
  const stopping = isStopCommand(input.text);
  if (input.text.trim() === "") {
    return;
  }
  const followUp = input.delivery ?? (input.working ? "QUEUE" : "STEER");
  const enqueue = followUp === "QUEUE" && !stopping;
  if (!input.busy && !enqueue) {
    input.setBusy(true);
  }
  input.follow.current = true;
  input.setSendError("");
  try {
    let sessionId = input.sessionId;
    if (sessionId === "") {
      sessionId = await input.create.mutateAsync({ agent: input.selected });
      input.goSession(sessionId);
    } else if (input.selected !== "" && input.selected !== input.currentAgent) {
      await input.prompt.mutateAsync({ id: sessionId, text: `$agent ${input.selected}` });
    }
    const privateText = await input.prompt.mutateAsync({ id: sessionId, text: input.text, delivery: followUp });
    input.setText("");
    input.setAgentOpen(false);
    if (enqueue) {
      await input.utils.queue.invalidate();
      return;
    }
    if (privateText) {
      input.setLines((current) => appendLine(current, "assistant", privateText));
    }
    if (privateText || stopping) {
      input.setBusy(false);
    }
  } catch (err) {
    input.setSendError(err instanceof Error ? err.message : "send failed");
    input.setBusy(false);
  }
}

async function promoteComposer(input: {
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
  if (!input.busy) {
    input.setBusy(true);
  }
  input.setSendError("");
  try {
    await input.steerQueueItem.mutateAsync({ id: input.id, itemId: input.itemId });
  } catch (err) {
    input.setSendError(err instanceof Error ? err.message : "steer failed");
    input.setBusy(false);
  }
}

async function stopComposer(input: {
  id: string;
  busy: boolean;
  prompt: { mutateAsync: (value: { id: string; text: string }) => Promise<unknown> };
  setBusy: (value: boolean) => void;
  setSendError: (value: string) => void;
}) {
  if (!input.busy || input.id === "") {
    return;
  }
  input.setSendError("");
  try {
    await input.prompt.mutateAsync({ id: input.id, text: "$stop" });
    input.setBusy(false);
  } catch (err) {
    input.setSendError(err instanceof Error ? err.message : "stop failed");
  }
}

function SessionComposer({
  id,
  busy,
  setBusy,
  lines,
  setLines,
  follow,
}: {
  id: string;
  busy: boolean;
  setBusy: (value: boolean) => void;
  lines: Line[];
  setLines: (update: (current: Line[]) => Line[]) => void;
  follow: RefObject<boolean>;
}) {
  const route = useRoute();
  const utils = trpc.useUtils();
  const prompt = trpc.prompt.useMutation();
  const sessions = trpc.sessions.useQuery(undefined, { refetchInterval: 2000 });
  const agents = trpc.agents.useQuery(undefined, { staleTime: 60_000 });
  const config = trpc.config.useQuery(undefined, { staleTime: 60_000 });
  const queueQuery = trpc.queue.useQuery({ id }, { enabled: id !== "", refetchInterval: 2000 });
  const removeQueueItem = trpc.removeQueueItem.useMutation({ onSuccess: () => void utils.queue.invalidate() });
  const steerQueueItem = trpc.steerQueueItem.useMutation({ onSuccess: () => void utils.queue.invalidate() });
  const reorderQueue = trpc.reorderQueue.useMutation({ onSuccess: () => void utils.queue.invalidate() });
  const create = trpc.createSession.useMutation();
  const [text, setText] = useState("");
  const [agent, setAgent] = useState("");
  const [agentOpen, setAgentOpen] = useState(false);
  const [dollarOff, setDollarOff] = useState(false);
  const [dollarPick, setDollarPick] = useState(0);
  const [sendError, setSendError] = useState("");
  const currentSession = sessions.data?.find((session) => session.id === route.id);
  const currentAgent = currentSession?.agent ?? "";
  const catalog = composerAgents(route.id, currentSession?.title ?? "", currentAgent, agents.data ?? [], config.data?.slackChannels ?? []);
  const selected = catalog.some((item) => item.name === agent) ? agent : currentAgent || catalog[0]?.name || "";
  useEffect(() => {
    setAgent(currentAgent);
  }, [id, currentAgent]);
  const matches = dollarOff ? [] : dollarMatches(text);
  const pick = matches.length === 0 ? 0 : dollarPick % matches.length;
  const applyDollar = (name: string) => {
    setText(`$${name} `);
    setDollarOff(true);
    setAgentOpen(false);
  };
  const working = busy || lines.at(-1)?.role === "thinking";
  let placeholder = "Message a new session";
  if (working) {
    placeholder = "Queue a follow-up · ⌘⏎ steers";
  } else if (id) {
    placeholder = "Message or $command";
  }
  const send = (delivery?: "STEER" | "QUEUE") =>
    sendComposer({
      text,
      delivery,
      busy,
      working,
      sessionId: id,
      selected,
      currentAgent,
      goSession: route.goSession,
      prompt,
      create,
      utils,
      follow,
      setBusy,
      setText,
      setAgentOpen,
      setSendError,
      setLines,
    });
  const promoteQueued = (itemId: string) => promoteComposer({ id, itemId, busy, steerQueueItem, setBusy, setSendError });
  const stop = () => stopComposer({ id, busy, prompt, setBusy, setSendError });
  return (
    <>
      {sendError ? <p className="px-3 pb-2 text-sm text-destructive sm:px-5">{sendError}</p> : null}
      <Composer
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
        queued={queueQuery.data ?? []}
        send={send}
        stop={stop}
        steerQueued={promoteQueued}
        removeQueued={(itemId) => void removeQueueItem.mutateAsync({ id: route.id, itemId }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "remove failed"))}
        reorderQueued={(itemIds) => void reorderQueue.mutateAsync({ id, itemIds })}
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

function Composer({
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
  removeQueued,
  reorderQueued,
}: {
  text: string;
  setText: (value: string) => void;
  matches: typeof dollarCommands;
  pick: number;
  applyDollar: (name: string) => void;
  catalog: { name: string; model?: string; reasoning?: string }[];
  selected: string;
  setAgent: (name: string) => void;
  agentOpen: boolean;
  setAgentOpen: (open: boolean | ((value: boolean) => boolean)) => void;
  setDollarOff: (off: boolean) => void;
  setDollarPick: (pick: number) => void;
  placeholder: string;
  busy: boolean;
  queued: { id: string; text: string }[];
  send: (delivery?: "STEER" | "QUEUE") => Promise<void>;
  stop: () => Promise<void>;
  steerQueued: (id: string, text: string) => Promise<void>;
  removeQueued: (id: string) => void;
  reorderQueued: (itemIds: string[]) => void;
}) {
  const mac = typeof navigator === "object" && /(Mac|iPod|iPhone|iPad)/.test(navigator.platform);
  return (
    <div className="px-3 pb-4 sm:px-5">
      <div className="relative mx-auto w-full max-w-3xl">
        {matches.length > 0 ? (
          <ul className="absolute inset-x-0 bottom-full z-10 mb-2 overflow-hidden rounded-2xl border bg-popover text-popover-foreground shadow-md">
            {matches.map((cmd, index) => (
              <li key={cmd.name}>
                <button
                  type="button"
                  className={cn("flex w-full flex-col items-start gap-0.5 px-3 py-2 text-left text-sm", index === pick && "bg-accent")}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => applyDollar(cmd.name)}
                >
                  <span>
                    <span className="font-medium">${cmd.name}</span>
                    {cmd.hint ? <span className="text-muted-foreground"> {cmd.hint}</span> : null}
                  </span>
                  <span className="text-xs text-muted-foreground">{cmd.desc}</span>
                </button>
              </li>
            ))}
          </ul>
        ) : agentOpen && catalog.length > 0 ? (
          <ul className="absolute inset-x-0 bottom-full z-10 mb-2 max-h-64 overflow-y-auto rounded-2xl border bg-popover text-popover-foreground shadow-md">
            {catalog.map((item) => (
              <li key={item.name}>
                <button
                  type="button"
                  className={cn("flex w-full flex-col items-start px-3 py-2 text-left text-sm", item.name === selected && "bg-accent")}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => {
                    setAgent(item.name);
                    setAgentOpen(false);
                  }}
                >
                  <span className="font-medium">{item.name}</span>
                  <span className="text-xs text-muted-foreground">{[item.model, item.reasoning].filter(Boolean).join(" · ")}</span>
                </button>
              </li>
            ))}
          </ul>
        ) : null}
        <QueuePanel items={queued} busy={busy} onSteer={(id, itemText) => void steerQueued(id, itemText)} onRemove={removeQueued} onReorder={reorderQueued} />
        <div className="relative z-10 rounded-[22px] border bg-card p-2 shadow-[0_12px_28px_-18px_rgb(0_0_0/40%)]">
          <Textarea
            value={text}
            onChange={(event) => {
              setDollarOff(false);
              setAgentOpen(false);
              setText(event.target.value);
            }}
            placeholder={placeholder}
            className="min-h-16 border-0 bg-transparent shadow-none focus-visible:ring-0"
            onKeyDown={(event) => {
              if (matches.length > 0) {
                if (event.key === "ArrowDown") {
                  event.preventDefault();
                  setDollarPick(pick + 1);
                  return;
                }
                if (event.key === "ArrowUp") {
                  event.preventDefault();
                  setDollarPick(pick + matches.length - 1);
                  return;
                }
                if (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey)) {
                  event.preventDefault();
                  applyDollar(matches[pick].name);
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
          <div className="flex items-center gap-2 px-1">
            <button
              type="button"
              className="max-w-[12rem] truncate rounded-full px-2 py-1 text-xs text-muted-foreground hover:bg-accent"
              onClick={() => setAgentOpen((open) => !open)}
            >
              {selected || "agent"}
            </button>
            <span className="min-w-0 flex-1" />
            {busy && text.trim() !== "" && !isStopCommand(text) ? (
              <button
                type="button"
                className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-xs text-muted-foreground hover:bg-accent"
                aria-label={mac ? "Steer ⌘⏎" : "Steer Ctrl+Enter"}
                suppressHydrationWarning
                onClick={() => void send("STEER")}
              >
                Steer
                <span suppressHydrationWarning>
                  <ModEnterKeys mac={mac} />
                </span>
              </button>
            ) : null}
            <Button
              type="button"
              size="icon"
              className="rounded-full"
              aria-label={busy && text.trim() === "" ? "Stop" : "Send"}
              onClick={() => void (busy && text.trim() === "" ? stop() : send())}
            >
              {busy && text.trim() === "" ? <Square className="h-4 w-4" /> : <Send className="h-4 w-4" />}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

function cronWhen(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("en-US", { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" });
}

function cronDay(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "Unknown" : date.toLocaleDateString("en-US", { weekday: "short", month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });
}

function cronTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleTimeString("en-US", { timeStyle: "short", timeZone: "UTC" });
}

function cronAxisStart(ms: number) {
  const d = new Date(ms);
  d.setSeconds(0, 0);
  d.setMinutes(d.getMinutes() < 30 ? 0 : 30);
  return d.getTime();
}

function cronAxisTime(ms: number) {
  const d = new Date(ms);
  const h = d.getHours();
  const m = d.getMinutes();
  return `${h < 10 ? `0${h}` : h}:${m < 10 ? `0${m}` : m}`;
}

function CronPage() {
  const route = useRoute();
  const jobs = trpc.cronJobs.useQuery(undefined, { staleTime: 10_000 });
  const run = trpc.runCron.useMutation();
  const [open, setOpen] = useState<string | null>(null);
  const [runQuery, setRunQuery] = useState("");
  const rows = jobs.data ?? [];
  const defs = rows.filter((job) => job.status !== "ran");
  const runs = rows.filter((job) => job.status === "ran");
  const start = cronAxisStart(Date.now());
  const runNeedle = runQuery.trim().toLowerCase();
  const matchedRuns =
    runNeedle === ""
      ? runs
      : runs.filter((job) => `${job.stem} ${job.lastRun} ${cronWhen(job.lastRun)}`.toLowerCase().includes(runNeedle));
  const runDays: { day: string; items: typeof runs }[] = [];
  for (const job of matchedRuns) {
    const day = cronDay(job.lastRun);
    const existing = runDays.find((group) => group.day === day);
    if (existing) {
      existing.items.push(job);
      continue;
    }
    runDays.push({ day, items: [job] });
  }
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <h1 className="text-lg font-semibold">Cron</h1>
      {jobs.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {jobs.error ? <p className="text-sm text-destructive">{jobs.error.message}</p> : null}
      {run.error ? <p className="text-sm text-destructive">{run.error.message}</p> : null}
      <section className="flex flex-col gap-2">
        <div className="flex items-end gap-2 text-[10px] tabular-nums text-muted-foreground">
          <span className="w-28 shrink-0" />
          <span className="relative h-4 min-w-0 flex-1">
            {[0, 4, 8, 12, 16, 20, 24].map((hour) => (
              <span key={hour} className="absolute -translate-x-1/2" style={{ left: `${(hour / 24) * 100}%` }}>
                {cronAxisTime(start + hour * 3_600_000)}
              </span>
            ))}
          </span>
        </div>
        {defs.map((job) => (
          <div key={job.stem} className="flex items-center gap-2">
            <span className="w-28 shrink-0 truncate text-xs">{job.stem}</span>
            <div className="relative h-4 min-w-0 flex-1 rounded-sm bg-muted">
              {(job.upcoming ?? []).map((at) => {
                const pct = ((Date.parse(at) - start) / 86_400_000) * 100;
                if (!Number.isFinite(pct) || pct < 0 || pct > 100) {
                  return null;
                }
                return <span key={at} className="absolute top-0.5 h-3 w-1 rounded-sm bg-primary" style={{ left: `${pct}%` }} />;
              })}
            </div>
          </div>
        ))}
      </section>
      <section className="flex flex-col gap-1">
        <h2 className="text-sm font-medium">Definitions</h2>
        {defs.map((job) => (
          <div key={job.stem} className="border-b py-2">
            <div className="flex items-center gap-2">
              <button type="button" className="min-w-0 flex-1 text-left" onClick={() => setOpen(open === job.stem ? null : job.stem)}>
                <span className="block truncate text-sm font-medium">{job.stem}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  {[job.schedule, job.agent, job.channel, job.origin].filter(Boolean).join(" · ")}
                </span>
              </button>
              <Button
                size="sm"
                variant="outline"
                disabled={run.isPending}
                onClick={() =>
                  run.mutate(
                    { stem: job.stem },
                    {
                      onSuccess: (id) => {
                        if (id !== "") {
                          route.goSession(id);
                        }
                      },
                    },
                  )
                }
              >
                {run.isPending && run.variables?.stem === job.stem ? "Starting…" : "Run"}
              </Button>
            </div>
            {open === job.stem ? <pre className="mt-2 whitespace-pre-wrap text-xs text-muted-foreground">{job.body || "No body."}</pre> : null}
          </div>
        ))}
      </section>
      <section className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-medium">Runs</h2>
          <label className="sr-only" htmlFor="cron-run-search">
            Search runs
          </label>
          <input
            id="cron-run-search"
            value={runQuery}
            onChange={(event) => setRunQuery(event.target.value)}
            placeholder="Search runs"
            className="h-8 min-w-0 flex-1 rounded-md border border-input bg-background px-2 text-sm outline-none"
          />
        </div>
        {runs.length === 0 && !jobs.isLoading && !jobs.error ? <p className="text-sm text-muted-foreground">No runs yet.</p> : null}
        {runs.length > 0 && matchedRuns.length === 0 ? <p className="text-sm text-muted-foreground">No matching runs.</p> : null}
        {runDays.map((group) => (
          <div key={group.day} className="flex flex-col gap-1">
            <h3 className="px-2 text-xs font-medium text-muted-foreground">{group.day}</h3>
            <ul className="flex flex-col gap-1">
              {group.items.map((job) => (
                <li key={job.nextRun}>
                  <Link href={sessionPath(job.nextRun)} className="flex flex-col rounded-md px-2 py-2 text-left hover:bg-accent">
                    <span className="truncate text-sm font-medium">{job.stem}</span>
                    <span className="text-xs text-muted-foreground">{cronTime(job.lastRun)}</span>
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </section>
    </div>
  );
}

function AgentsPage() {
  const agents = trpc.agents.useQuery(undefined, { staleTime: 60_000 });
  const [open, setOpen] = useState<string | null>(null);
  const rows = agents.data ?? [];
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <h1 className="text-lg font-semibold">Agents</h1>
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
  const skills = trpc.skills.useQuery(undefined, { staleTime: 60_000 });
  const [open, setOpen] = useState<string | null>(null);
  const rows = skills.data ?? [];
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <h1 className="text-lg font-semibold">Skills</h1>
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

function ConfigLoaded({ view }: { view: {
  workspace?: string;
  overlays?: string[];
  models?: { name?: string; model?: string }[];
  slackChannels?: { channel?: string; agents?: string[] }[];
  mcpServers?: string[];
  loggingLevel?: string;
  autoApproverModel?: string;
  instrumentationEnabled?: boolean;
  mcpExternal?: boolean;
  mcpDevelopment?: boolean;
} }) {
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
      <ConfigSection title="Slack">
        <ConfigList items={channels} empty="No channels" />
      </ConfigSection>
      <ConfigSection title="MCP">
        <ConfigList items={servers} empty="No servers" />
      </ConfigSection>
      <ConfigSection title="Flags">
        <ConfigRow label="Instrumentation" value={view.instrumentationEnabled ? "On" : "Off"} />
        <ConfigRow label="External MCP" value={view.mcpExternal ? "On" : "Off"} />
        <ConfigRow label="Development MCP" value={view.mcpDevelopment ? "On" : "Off"} />
      </ConfigSection>
    </>
  );
}

function ConfigPage() {
  const config = trpc.config.useQuery(undefined, { staleTime: 60_000 });
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <h1 className="text-lg font-semibold">Config</h1>
      {config.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {config.error ? <p className="text-sm text-destructive">{config.error.message}</p> : null}
      {config.data ? <ConfigLoaded view={config.data} /> : null}
    </div>
  );
}
