import { useEffect, useMemo, useReducer, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Cable, History, Lock, Pencil, Plus, RotateCcw, Sparkles, TriangleAlert, X } from "lucide-react";
import { mutations, queries } from "@/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useTimelineDetail } from "@/timeline-detail";
import { comparisonRows, shadowHistory, ShadowCell, transcriptTurns, TurnContent, turnKey, useSessionStream } from "@/ui";
import type { AgentDraft, AgentDraftExternalMCP, AgentDraftMessage, AgentDraftOp, AgentDraftProblem, AgentSettingHelp, ShadowBinding } from "@/types";
import type { Fix } from "./assistant";
import { opId, type CoEdit } from "./coedit";
import { envName, fixed, mcpState, McpPanel, useMcpWrites, type Apply } from "./mcp";

type Turn = ReturnType<typeof transcriptTurns>[number];
type Found = { row: ReturnType<typeof comparisonRows>[number]; binding: ShadowBinding };
// A column is a variant by name, or the live agent with an empty key, as a message's problems name it.
type Column = { key: string; title: string };

const fileKinds = ["set_field", "edit_body", "add_variant", "rename_variant"];
// The revision of the last change to a variant's file, as a message's labels name the version each column ran.
const changedAt = (log: AgentDraftOp[], variant: string) => Math.max(0, ...log.filter((op) => ["create", "set_mode"].includes(op.kind)
  || (fileKinds.includes(op.kind) && (op.kind === "rename_variant" ? op.value : op.variant) === variant)).map((op) => Number(op.revision)));
export const sentence = (problem: AgentDraftProblem, help: AgentSettingHelp[]) => `${problem.key ? `${help.find(({ key }) => key === problem.key)?.label ?? problem.key}: ` : ""}${problem.sentence}`;
// The first variant is the agent itself; the others are its shadows.
export const variantTitle = (draft: AgentDraft, name: string) => name === draft.variants[0].name ? draft.name : `${draft.name}/${name}`;
const withoutUser = (turn: Turn) => ({ ...turn, items: turn.items.filter((line) => line.role !== "user") });

export function TestChat({ coedit, draft, messages, onFix }: { coedit: CoEdit; draft: AgentDraft; messages: AgentDraftMessage[]; onFix: (fix: Fix) => void }) {
  const client = useQueryClient();
  const help = useQuery(queries.agentSettings(draft.mode)).data ?? [];
  const { turns, found, viewport, busy, queued } = useTestChat(draft.testChat);
  // A refused change or restart says why until the next one goes through. A restart waits for the edits typed before
  // it to be in the draft, as a send does; the server replays the old chat's messages. One restart goes at a time.
  const [refused, setRefused] = useState("");
  const settle = (refusal = "") => { setRefused(refusal); return client.invalidateQueries({ queryKey: ["agentDraft", draft.id] }); };
  const fail = (error: Error) => setRefused(error.message);
  const apply: Apply = (op) => mutations.applyAgentDraftOp({ id: draft.id, op: { baseRevision: String(coedit.rev), opId: opId(), ...op } }).then(() => settle(), fail);
  const restart = useMutation({
    mutationFn: (replayEarlier: boolean) => coedit.drained().then(() => mutations.restartDraftChat({ id: draft.id, replay: [], replayEarlier })),
    onSuccess: (reply) => settle(reply.refused),
    onError: fail,
  });
  const columns: Column[] = [...draft.variants.map((variant) => ({ key: variant.name, title: variantTitle(draft, variant.name) })), ...draft.existing ? [{ key: "", title: "Live" }] : []];
  const state = mcpState(draft, messages[0]?.externalMcp), setMcp = useMcpWrites(draft, apply), grid = { gridTemplateColumns: `repeat(${columns.length}, minmax(0, 1fr))` };
  const added = "bcdefghijklmnopqrstuvwxyz".split("").find((name) => !draft.variants.some((variant) => variant.name === name));
  return <section aria-label="Test chat" className="flex min-h-96 min-w-0 flex-col border-b lg:min-h-0 lg:border-r lg:border-b-0">
    <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-1 border-b px-3 py-1">
      <h2 className="text-sm font-medium">Test chat</h2><span className="truncate text-xs text-muted-foreground">talks to the unsaved draft</span>
      <Button size="sm" variant="outline" className="ml-auto" aria-label="Add a variant" title="A new column: a copy of the draft to change on the left" disabled={!added} onClick={() => void apply({ kind: "add_variant", variant: added, value: draft.variants[0].name })}><Plus />Variant</Button>
      <Button size="sm" variant="ghost" title="A new, empty chat: the next message starts fresh" disabled={restart.isPending} onClick={() => restart.mutate(false)}><RotateCcw />Restart</Button>
      <Button size="sm" variant="ghost" title="A new chat that gets every earlier message again, answered by the current versions" disabled={restart.isPending} onClick={() => restart.mutate(true)}><History />Restart &amp; replay</Button>
    </div>
    {refused ? <p role="alert" className="shrink-0 border-b px-3 py-2 text-xs text-destructive">{refused}</p> : null}
    <Notices draft={draft} columns={columns.length} state={state} fresh={!messages.length} renamed={!draft.existing && coedit.log.some((op) => op.kind === "rename")} setMcp={setMcp} />
    <McpPanel draft={draft} first={messages[0]?.externalMcp} state={state} set={setMcp} />
    <div className="grid shrink-0 gap-5 border-b px-4 py-2" style={grid}>
      {columns.map((column, i) => <ColumnHeader key={column.key} column={column} current={changedAt(coedit.log, column.key)} last={messages.at(-1)} removable={i > 0 && !!column.key} apply={apply} />)}
    </div>
    <div ref={viewport} className="flex min-h-0 flex-1 flex-col overflow-y-auto">
      {turns.map((turn, index) => {
        const record = messages.find(({ inputId }) => turn.user.some((line) => line.inputId === inputId));
        return <Row key={turnKey(turn)} index={index} turn={turn} chat={draft.testChat} record={record} first={messages[0]?.externalMcp} grid={grid}>
          {columns.map((column, i) => <Cell key={column.key} column={column} draft={i === 0} running={busy && index === turns.length - 1} turn={turn} index={index} found={found.get(`${column.key}/${turnKey(turn)}`)} chat={draft.testChat} log={coedit.log} help={help} record={record} onFix={onFix} />)}
        </Row>;
      })}
      {queued.map((item) => <p key={item.id} role="status" className="border-b px-4 py-3 text-xs text-muted-foreground">Waiting its turn: {item.text}</p>)}
      {turns.length || queued.length ? null : <p role="status" className="m-auto max-w-sm p-8 text-center text-sm text-muted-foreground">Send a test message. Every column answers it side by side, before anything is saved.</p>}
    </div>
    <Composer coedit={coedit} id={draft.id} state={state} help={help} />
  </section>;
}

// One stream on the test chat drives every column: the variants and the live agent are its shadows, which wake it.
// Each shadow's answers are placed by the message whose turn they answer, never by position.
function useTestChat(chat: string) {
  const [, changed] = useReducer((count: number) => count + 1, 0);
  const draft = useMemo(() => ({ text: "", files: [], agent: "", sessionId: chat, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0 }), [chat]);
  const [children] = useState(() => new Map());
  const stream = useSessionStream(chat, draft, changed);
  const histories = useQueries({ queries: stream.shadows.map((binding) => shadowHistory(binding, children)) });
  // An External MCP message's metadata entry comes just before its turn, and shows with that message's answer.
  const turns = useMemo(() => {
    const joined: Turn[] = [];
    let carried: Turn["items"] = [];
    for (const turn of transcriptTurns(stream.lines, undefined, true)) {
      if (!turn.user.length) {
        carried = [...carried, ...turn.items];
        continue;
      }
      joined.push({ ...turn, items: [...carried, ...turn.items] });
      carried = [];
    }
    return joined;
  }, [stream.lines]);
  const found = new Map<string, Found>();
  stream.shadows.forEach((binding, i) => {
    const column = binding.shadowAgent === binding.parentAgent ? "" : binding.shadowAgent.slice(binding.parentAgent.length + 1);
    for (const row of comparisonRows(turns, transcriptTurns(histories[i].data?.lines ?? [], undefined, true), binding)) if (row.parent && row.correlation) found.set(`${column}/${turnKey(row.parent)}`, { row, binding });
  });
  const viewport = useRef<HTMLDivElement>(null);
  useEffect(() => { viewport.current?.scrollTo({ top: viewport.current.scrollHeight }); }, [turns.length]);
  const queued = useQuery({ ...queries.queue({ id: chat }), enabled: chat !== "", refetchInterval: 2000 }).data ?? [];
  return { turns, found, viewport, busy: stream.busy, queued };
}

function Notices({ draft, columns, state, fresh, renamed, setMcp }: { draft: AgentDraft; columns: number; state: ReturnType<typeof mcpState>; fresh: boolean; renamed: boolean; setMcp: ReturnType<typeof useMcpWrites> }) {
  return <>
    <p className="flex shrink-0 items-start gap-2 border-b bg-amber-50 px-3 py-2 text-xs text-amber-950 dark:bg-amber-950/40 dark:text-amber-100"><TriangleAlert className="mt-px size-3.5 shrink-0" />
      Test messages use real tools. Each message runs once in each of the {columns} {columns > 1 ? "columns" : "column"}, so side effects can repeat or collide.</p>
    {renamed ? <p role="status" className="shrink-0 border-b px-3 py-2 text-xs">Renamed: {draft.name} is a new agent. The original stays as it is, and its live version no longer answers here.</p> : null}
    {!state.on && state.allowed && state.required.length > 0 && fresh ? <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2 text-xs"><Cable className="size-3.5 shrink-0" />
      <span className="flex-1">This agent reads {state.required.map(envName).join(", ")}, which an External MCP caller sends as metadata.</span>
      <Button size="xs" onClick={() => setMcp(() => ({ on: true }))}>Test as External MCP</Button>
    </div> : null}
  </>;
}

// A variant's header names its version now, says when the next message runs a newer one than the last message did,
// and renames or removes the variant.
function ColumnHeader({ column, current, last, removable, apply }: { column: Column; current: number; last?: AgentDraftMessage; removable: boolean; apply: Apply }) {
  const [renaming, setRenaming] = useState(false);
  const label = last?.labels?.[column.key];
  const rename = (name: string) => { setRenaming(false); if (name && name !== column.key) void apply({ kind: "rename_variant", variant: column.key, value: name }); };
  return <div className="flex min-w-0 flex-col text-xs">
    <span className="flex min-w-0 items-center gap-1">
      {renaming ? <Input autoFocus aria-label={`New name of variant ${column.key}`} defaultValue={column.key} onBlur={(event) => rename(event.target.value.trim())} onKeyDown={(event) => event.key === "Enter" && rename(event.currentTarget.value.trim())} />
        : <span className="truncate font-medium">{column.title}</span>}
      {removable && !renaming ? <>
        <Button size="icon-xs" variant="ghost" aria-label={`Rename variant ${column.key}`} onClick={() => setRenaming(true)}><Pencil /></Button>
        <Button size="icon-xs" variant="ghost" aria-label={`Remove variant ${column.key}`} onClick={() => void apply({ kind: "remove_variant", variant: column.key })}><X /></Button>
      </> : null}
    </span>
    <span className="truncate text-[11px] text-muted-foreground">{column.key ? `r${current}` : "the saved live version"}{label && Number(label) !== current ? <> · <span className="text-foreground">{`next message uses r${current}`}</span></> : null}</span>
  </div>;
}

function Row({ index, turn, chat, record, first, grid, children }: { index: number; turn: Turn; chat: string; record?: AgentDraftMessage; first?: AgentDraftExternalMCP; grid: CSSProperties; children: ReactNode }) {
  const detail = useTimelineDetail(), mcp = record?.externalMcp;
  return <section aria-label={`Message ${index + 1}`} className="flex flex-col gap-2 border-b px-4 py-3">
    <TurnContent comparison turn={{ ...turn, items: turn.user }} detail={detail} conversationId={chat} hasSandboxed={false} />
    {mcp?.on ? <p className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground"><Cable className="size-3" /><span>External MCP from {mcp.caller || "no caller"}</span>
      {Object.entries(mcp.metadata ?? {}).sort().map(([key, value]) => fixed(first, key)
        ? <span key={key} className="flex items-center gap-0.5 font-mono"><Lock aria-label="fixed by the chat's first message" className="size-3" />{`${key}=${value}`}</span>
        : <span key={key} className="font-mono">{`${key}=${value} · this message only`}</span>)}
    </p> : null}
    <div className="grid gap-5" style={grid}>{children}</div>
  </section>;
}

// A column's answer to one message, with the version it ran, faded once that column's file has changed since. A
// variant's answer can be handed to the assistant to fix.
function Cell(props: { column: Column; draft: boolean; running: boolean; turn: Turn; index: number; found?: Found; chat: string; log: AgentDraftOp[]; help: AgentSettingHelp[]; record?: AgentDraftMessage; onFix: (fix: Fix) => void }) {
  const { column, log, record } = props;
  const label = column.key ? record?.labels?.[column.key] : record?.live;
  const faded = !!column.key && !!label && Number(label) < changedAt(log, column.key);
  const said = ((props.draft ? props.turn : props.found?.row.child)?.items ?? []).filter((line) => line.role === "assistant").map((line) => line.text).join("\n\n");
  return <div role="region" aria-label={`${column.title} answer`} className={cn("min-w-0", faded && "opacity-60")}>
    {label ? <p className="mb-1 text-[11px] text-muted-foreground">{`${column.key ? `r${label}` : `live ${label}`}${faded ? " · changed since" : ""}`}</p> : null}
    <Answer {...props} sent={!record || !!label} />
    {column.key && said && !props.running ? <Button size="xs" variant="ghost" onClick={() => props.onFix({ variant: column.key, title: column.title, text: said })}><Sparkles />Fix this</Button> : null}
  </div>;
}

function Answer({ column, draft, running, turn, index, found, chat, help, record, sent }: Parameters<typeof Cell>[0] & { sent: boolean }) {
  const detail = useTimelineDetail();
  const problem = record?.notRun?.find(({ variant }) => (variant ?? "") === column.key);
  if (problem) return <p className="text-xs text-muted-foreground">Not run. {sentence(problem, help)}</p>;
  if (!sent) return <p className="text-xs text-muted-foreground">Not sent: this column did not exist yet.</p>;
  if (draft) return <><TurnContent comparison turn={withoutUser(turn)} detail={detail} conversationId={chat} hasSandboxed={false} />{running ? <p role="status" className="text-xs text-muted-foreground">Working…</p> : null}</>;
  if (found) return <ShadowCell row={{ ...found.row, child: found.row.child && withoutUser(found.row.child) }} index={index} binding={found.binding} detail={detail} />;
  return <p role="status" className="text-xs text-muted-foreground">Waiting…</p>;
}

// One message goes to every column, once the edits typed before it are in the draft. A send that needs a new chat
// restarts with the message; a refused one stores nothing and says why, naming the settings to fix. A sent message
// leaves the box, unless something else was typed there meanwhile.
function Composer({ coedit, id, state, help }: { coedit: CoEdit; id: string; state: ReturnType<typeof mcpState>; help: AgentSettingHelp[] }) {
  const client = useQueryClient();
  const [text, setText] = useState("");
  const send = useMutation({
    mutationFn: async ({ restart, sent }: { restart: boolean; sent: string }) => {
      await coedit.drained();
      return restart ? mutations.restartDraftChat({ id, replay: sent.trim() ? [sent] : [] }) : mutations.draftPrompt({ id, text: sent });
    },
    onSuccess: (reply, { sent }) => { if (!reply.refused) setText((now) => now === sent ? "" : now); return client.invalidateQueries({ queryKey: ["agentDraft", id] }); },
  });
  const reply = send.data, restart = state.restart || (reply?.restart ? "Restart with new metadata" : "");
  const ready = !send.isPending && !state.missing.length && !state.refused && (!!restart || !!text.trim());
  const go = () => send.mutate({ restart: !!restart, sent: text });
  let note = "";
  if (state.refused) note = state.refused;
  else if (state.missing.length) note = `Fill ${state.missing.join(", ")} first: the agent reads ${state.missing.map(envName).join(", ")}.`;
  else if (!state.on && state.required.length) note = `${state.required.map(envName).join(", ")} comes only from External MCP: a Web test runs without it.`;
  return <div className="flex shrink-0 flex-col gap-2 border-t p-3">
    {reply?.refused || send.error ? <div role="alert" className="text-xs text-destructive">{reply?.refused ?? send.error?.message}
      {reply?.problems?.map((problem) => <p key={`${problem.variant}/${problem.key}`}>{sentence(problem, help)}</p>)}</div> : null}
    <Textarea aria-label="Test message" placeholder="Message every column" value={text} onChange={(event) => setText(event.target.value)}
      onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); if (ready) go(); } }} />
    <div className="flex items-center gap-2">
      <span className="min-w-0 flex-1 text-xs text-muted-foreground">{note}</span>
      <Button size="sm" disabled={!ready} onClick={go}>{restart || "Send"}</Button>
    </div>
  </div>;
}
