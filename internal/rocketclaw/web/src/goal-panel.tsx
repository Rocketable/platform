import { useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, CircleCheck, CircleDashed, CircleSlash, CircleX, LoaderCircle, NotebookPen, Target } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { TraceCollapsible, TraceCollapsibleContent, TraceCollapsibleTrigger, TraceIcon, TraceLine, TraceTitle } from "@/components/ui/trace";
import { cn } from "@/lib/utils";
import { mutations, queries } from "./api";
import Link from "./navigation";
import { CodeBlock } from "./transcript-text";
import type { Goal, GoalEvent } from "./types";

type Outcome = "note" | "silent" | "failed" | "complete" | "blocked" | "exhausted" | "running";
type GoalTurn = { turn: number; updates: GoalEvent[]; end?: GoalEvent; outcome: Outcome };
type GoalRun = { resume?: GoalEvent; stops: GoalEvent[]; turns: GoalTurn[] };

const worst: (Outcome | "left")[] = ["left", "note", "complete", "running", "silent", "exhausted", "blocked", "failed"];
const shownTurns = 25;
const maxMarks = 60;

const outcomes: Record<Outcome, { label: string; mark: string; icon: typeof Target }> = {
  note: { label: "Progress noted", mark: "bg-primary", icon: NotebookPen },
  silent: { label: "No progress note", mark: "border border-muted-foreground", icon: CircleDashed },
  failed: { label: "Check failed", mark: "bg-destructive", icon: CircleX },
  complete: { label: "Complete", mark: "bg-green-600", icon: CircleCheck },
  blocked: { label: "Blocked", mark: "bg-amber-500", icon: Ban },
  exhausted: { label: "Out of turns", mark: "bg-muted-foreground", icon: CircleSlash },
  running: { label: "Running", mark: "bg-primary/50 motion-safe:animate-pulse", icon: LoaderCircle },
};

const statusLabels: Record<string, string> = { active: "Active", complete: "Complete", blocked: "Blocked", stopped: "Stopped", budget_exhausted: "Out of turns" };

function outcomeOf(turn: Omit<GoalTurn, "outcome">): Outcome {
  if (turn.updates.some((update) => update.check === "GOAL_CHECK_RESULT_FAILED")) return "failed";
  const status = turn.end?.status === "budget_exhausted" ? "exhausted" : turn.updates.at(-1)?.status;
  if (status === "complete" || status === "blocked" || status === "exhausted") return status;
  if (turn.updates.length) return "note";
  return turn.end ? "silent" : "running";
}

export function goalRuns(events: GoalEvent[]) {
  const runs: GoalRun[] = [{ stops: [], turns: [] }];
  for (const event of events) {
    const run = runs.at(-1)!;
    if (event.kind === "GOAL_EVENT_KIND_RESUME") runs.push({ resume: event, stops: [], turns: [] });
    if (event.kind === "GOAL_EVENT_KIND_STOP") run.stops.push(event);
    if (event.kind !== "GOAL_EVENT_KIND_UPDATE" && event.kind !== "GOAL_EVENT_KIND_TURN") continue;
    const number = event.turn ?? 0;
    let turn = run.turns.find((item) => item.turn === number);
    if (!turn) run.turns.push(turn = { turn: number, updates: [], outcome: "running" });
    if (event.kind === "GOAL_EVENT_KIND_TURN") turn.end = event;
    else turn.updates.push(event);
  }
  for (const run of runs) for (const turn of run.turns) turn.outcome = outcomeOf(turn);
  return runs;
}

function streak(turns: GoalTurn[], same: (turn: GoalTurn) => boolean) {
  const last = turns.findLastIndex((turn) => !same(turn));
  return turns.length - 1 - last;
}

export function goalWarnings(turns: GoalTurn[]) {
  const finished = turns.filter((turn) => turn.outcome !== "running");
  const notes = finished.map((turn) => turn.updates.at(-1)?.note?.trim().toLowerCase() ?? "");
  const lastNote = notes.at(-1) ?? "";
  const failed = streak(finished, (turn) => turn.outcome === "failed");
  const silent = streak(finished, (turn) => turn.outcome === "silent");
  const repeated = lastNote ? streak(finished, (turn) => (turn.updates.at(-1)?.note?.trim().toLowerCase() ?? "") === lastNote) : 0;
  return [
    failed >= 2 && `Check failed ${failed} turns in a row`,
    silent >= 3 && `No progress note for ${silent} turns`,
    repeated >= 3 && `Same note ${repeated} turns running`,
  ].filter((warning): warning is string => !!warning);
}

export function goalMarks(turns: GoalTurn[], maxTurns: number) {
  const slots: (Outcome | "left")[] = turns.map((turn) => turn.outcome);
  while (slots.length < maxTurns) slots.push("left");
  const size = Math.max(1, Math.ceil(slots.length / maxMarks));
  const marks: (Outcome | "left")[] = [];
  for (let i = 0; i < slots.length; i += size) marks.push(slots.slice(i, i + size).reduce((a, b) => worst.indexOf(b) > worst.indexOf(a) ? b : a));
  return { marks, size };
}

function TurnBar({ turns, goal }: { turns: GoalTurn[]; goal: Goal }) {
  const max = goal.maxTurns ?? 0;
  const { marks, size } = goalMarks(turns, max);
  const counts = Object.entries(Object.groupBy(turns, (turn) => turn.outcome)).map(([outcome, group]) => `${group!.length} ${outcomes[outcome as Outcome].label.toLowerCase()}`);
  const used = `${turns.length}${max ? ` / ${max}` : ""} turns`;
  return <div className="flex flex-col gap-1">
    <div role="img" aria-label={[used, ...counts].join(", ")} className="flex h-3 items-stretch gap-0.5">
      {marks.map((mark, index) => <span key={index} className={cn("min-w-1 max-w-4 flex-1 rounded-sm", mark === "left" ? "border border-dashed border-muted-foreground/40" : outcomes[mark].mark)} />)}
    </div>
    <span className="text-xs text-muted-foreground">{used}{size > 1 ? ` · each mark ≈ ${size} turns` : ""}</span>
  </div>;
}

function useGoalControl(id: string) {
  const client = useQueryClient();
  const [reply, setReply] = useState("");
  const control = useMutation({
    mutationFn: (command: "stop" | "resume") => mutations.prompt({ id, text: `/goal ${command}` }),
    onSuccess: async (response) => { setReply(response.privateText ?? ""); await client.invalidateQueries({ queryKey: ["goal"] }); },
    onError: (error) => setReply(error.message),
  });
  return { reply, pending: control.isPending, run: (command: "stop" | "resume") => control.mutate(command) };
}

function GoalControls({ id, status }: { id: string; status: string }) {
  const control = useGoalControl(id);
  const resumable = ["stopped", "blocked", "budget_exhausted"].includes(status);
  return <div className="flex items-center gap-2">
    {status === "active" ? <Button size="sm" variant="outline" disabled={control.pending} onClick={() => control.run("stop")}>Stop goal</Button> : null}
    {resumable ? <Button size="sm" variant="outline" disabled={control.pending} onClick={() => control.run("resume")}>Resume goal</Button> : null}
    {control.reply ? <span role="status" className="text-xs text-muted-foreground">{control.reply}</span> : null}
  </div>;
}

function goalSettings(goal: Goal) {
  return [goal.check && `check: ${goal.check}`, goal.force && "force: cannot end as blocked"].filter(Boolean).join(" · ");
}

function GoalHeader({ id, goal, turns }: { id: string; goal: Goal; turns: GoalTurn[] }) {
  const set = [goal.principalName && `Set by ${goal.principalName}`, goal.createdAt && when(goal.createdAt)].filter(Boolean).join(" · ");
  const settings = goalSettings(goal);
  return <header className="flex shrink-0 flex-col gap-2 border-b p-3 lg:pr-12">
    <p className="line-clamp-3 text-sm font-medium whitespace-pre-wrap wrap-anywhere">{goal.objective}</p>
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <Badge role="status" variant={goal.status === "active" ? "default" : "secondary"}>{statusLabels[goal.status] ?? goal.status}</Badge>
      {set ? <span>{set}</span> : null}
    </div>
    {settings ? <p className="font-mono text-xs wrap-anywhere text-muted-foreground">{settings}</p> : null}
    <TurnBar turns={turns} goal={goal} />
    {goalWarnings(turns).map((warning) => <p key={warning} className="text-xs font-medium text-amber-700 dark:text-amber-300">{warning}</p>)}
    <GoalControls id={id} status={goal.status} />
  </header>;
}

const when = (at: string) => new Date(at).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });

function TurnSummary({ turn }: { turn: GoalTurn }) {
  const note = turn.updates.findLast((update) => update.note)?.note?.split("\n", 1)[0];
  return note || outcomes[turn.outcome].label;
}

function TurnDetail({ turn, check, href }: { turn: GoalTurn; check: string; href?: string }) {
  return <div className="flex flex-col gap-2 text-xs">
    {turn.updates.map((update) => <div key={update.id} className="flex flex-col gap-1">
      <span className="font-medium text-foreground">Marked {update.status}{update.check === "GOAL_CHECK_RESULT_PASSED" ? `; check ${check} passed` : ""}{update.check === "GOAL_CHECK_RESULT_FAILED" ? `; check ${check} failed` : ""}</span>
      {update.note ? <p className="whitespace-pre-wrap wrap-anywhere">{update.note}</p> : null}
      {update.checkOutput ? <CodeBlock size="sm" label="Check output" text={update.checkOutput} /> : null}
    </div>)}
    {turn.updates.length ? null : <p>{turn.outcome === "running" ? "This turn is still running." : "The agent recorded no progress note this turn."}</p>}
    {href ? <Link href={href} className="w-fit text-primary underline-offset-4 hover:underline">Open turn in chat</Link> : null}
  </div>;
}

function TurnRow({ turn, open, check, href }: { turn: GoalTurn; open: boolean; check: string; href?: string }) {
  const { icon: Icon, label } = outcomes[turn.outcome];
  return <li>
    <TraceCollapsible defaultOpen={open}>
      <TraceCollapsibleTrigger title={label}>
        <TraceLine status={turn.outcome === "failed" ? "error" : undefined}>
          <TraceIcon><Icon className={turn.outcome === "running" ? "motion-safe:animate-spin" : undefined} /></TraceIcon>
          <TraceTitle>Turn {turn.turn} · <TurnSummary turn={turn} /></TraceTitle>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent><TurnDetail turn={turn} check={check} href={href} /></TraceCollapsibleContent>
    </TraceCollapsible>
  </li>;
}

function runLabel(run: GoalRun, number: number) {
  const end = run.turns.at(-1)?.outcome;
  return [`Run ${number}`, `${run.turns.length} turns`, end && end !== "running" && end !== "note" && end !== "silent" ? outcomes[end].label.toLowerCase() : "", run.stops.length ? "stopped" : ""].filter(Boolean).join(" · ");
}

function RunTurns({ run, check, link }: { run: GoalRun; check: string; link: (turn: GoalTurn) => string | undefined }) {
  const [limit, setLimit] = useState(shownTurns);
  const turns = run.turns.toReversed();
  const open = turns.find((turn) => turn.outcome !== "running");
  return <ol aria-label="Goal turns, newest first" className="flex flex-col gap-1">
    {turns.slice(0, limit).map((turn) => <TurnRow key={turn.turn} turn={turn} open={turn === open} check={check} href={link(turn)} />)}
    {turns.length > limit ? <li><Button size="sm" variant="ghost" onClick={() => setLimit(limit + shownTurns)}>Show {Math.min(shownTurns, turns.length - limit)} earlier turns</Button></li> : null}
  </ol>;
}

function GoalLedger({ runs, goal, link }: { runs: GoalRun[]; goal: Goal; link: (turn: GoalTurn) => string | undefined }) {
  const check = goal.check ?? "";
  const current = runs.at(-1)!;
  return <div className="flex flex-col gap-3 p-3">
    {current.turns.length ? null : <p className="text-sm text-muted-foreground">Waiting for the first goal turn to finish.</p>}
    {runs.toReversed().map((run, index) => {
      const number = runs.length - index;
      const resumed = run.resume ? <p className="text-xs text-muted-foreground">Resumed{run.resume.principal ? ` by ${run.resume.principal}` : ""} · {when(run.resume.at)} · turns count from 1 again</p> : null;
      if (run === current) return <section key={number} aria-label={`Run ${number}`} className="flex flex-col gap-2"><RunTurns run={run} check={check} link={link} />{resumed}</section>;
      return <TraceCollapsible key={number}>
        <TraceCollapsibleTrigger><TraceLine><TraceTitle>{runLabel(run, number)}</TraceTitle></TraceLine></TraceCollapsibleTrigger>
        <TraceCollapsibleContent><RunTurns run={run} check={check} link={link} />{resumed}</TraceCollapsibleContent>
      </TraceCollapsible>;
    })}
  </div>;
}

export function GoalView({ id, revision, active, link }: { id: string; revision?: string; active: boolean; link: (messageId: string) => string }) {
  const goal = useQuery({ ...queries.goal({ id }, revision, active), placeholderData: keepPreviousData, retry: false });
  if (goal.isError) return <p role="alert" className="p-3 text-sm text-destructive">{goal.error.message}</p>;
  if (!goal.data) return <p className="p-3 text-sm text-muted-foreground">Loading the goal…</p>;
  if (!goal.data.goal) return <div className="flex flex-col gap-2 p-3 text-sm text-muted-foreground"><p>This chat has no goal.</p><p>Start one with <code>/goal &lt;objective&gt;</code>. Options go before the objective: <code>maxTurns: 10</code>, <code>check: &quot;make test&quot;</code>, <code>force: yes</code>.</p></div>;
  const runs = goalRuns(goal.data.events ?? []);
  const turnLink = (turn: GoalTurn) => turn.end?.messageId ? link(turn.end.messageId) : undefined;
  return <div className="flex min-h-0 flex-1 flex-col">
    <GoalHeader id={id} goal={goal.data.goal} turns={runs.at(-1)!.turns} />
    <ScrollArea className="min-h-0 flex-1"><GoalLedger runs={runs} goal={goal.data.goal} link={turnLink} /></ScrollArea>
  </div>;
}
