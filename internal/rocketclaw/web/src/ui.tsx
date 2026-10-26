"use client";

import { QueryClient, QueryCache, MutationCache, QueryClientProvider, queryOptions, useQuery, useMutation, useIsMutating } from "@tanstack/react-query";
import { captureException } from "@sentry/react";
import { Menu } from "@base-ui/react/menu";
import { Popover } from "@base-ui/react/popover";
import { Drawer } from "@base-ui/react/drawer";
import { Tabs } from "@base-ui/react/tabs";
import { Dialog, DialogContent, DialogTitle, DialogDescription, DialogClose, DialogHeader, DialogFooter } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupTextarea } from "@/components/ui/input-group";
import { DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Field, FieldGroup, FieldLabel, FieldError } from "@/components/ui/field";
import { queries, mutations, listSessions, rpc, RPCError } from "./api";
import { draftContent } from "./drafts";
import { holdWakeLock, setVoiceSticky, startVoice, stopVoice, subscribeVoice, useVoice, voiceChoice, voiceLevel, voiceView, type VoiceCaption, type VoiceState, type VoiceView } from "./voice";
import type { ChatOrigin, HistoryView, MessageMatch, PendingQuestion, PromptDelivery, SearchSessionsResponse, ShadowBinding } from "./types";
import { BellOff, BellRing, Bot, Brain, Captions, Check, ChevronDown, ChevronRight, CircleAlert, Clock, Copy, Download, Ellipsis, FileIcon, Files, FileText, GitFork, GripVertical, Hourglass, Keyboard, Layers, ListChecks, LoaderCircle, Magnet, MessageSquare, Mic, MicOff, PanelLeftClose, PanelLeftOpen, Pin, Play, Plus, RotateCcw, ScrollText, Search, Send, Settings, Share, ShieldCheck, Sparkles, Square, SquarePen, SquareTerminal, Undo2, Users, Wrench, X } from "lucide-react";
import Link, { usePathname, useSearch, navigate } from "./navigation";
import { createContext, memo, use, useCallback, useContext, useEffect, useEffectEvent, useId, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type Dispatch, type SetStateAction, type ReactNode, type ReactElement, type SyntheticEvent, type RefObject, type ComponentProps, type CSSProperties } from "react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { flushSync } from "react-dom";
import { PaletteChooser, ThemeToggle } from "@/components/theme";
import { CodeBlock, InlineText, MarkdownText, OUTPUT_KEY, TranscriptText, cleanText, copyText, mentionIds, setPreference, useCleanText, usePreference, useSlackNames } from "./transcript-text";
import { projectTimeline, setTimelineRows, timelineFailure, timelineLevel, timelineLevels, useTimelineDetail, type TimelineRows } from "./timeline-detail";
import { TimelineDetailCard } from "./timeline-detail-card";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Attachment, AttachmentGroup, AttachmentMedia, AttachmentContent, AttachmentTitle, AttachmentDescription, AttachmentActions, AttachmentAction } from "@/components/ui/attachment";
import { Message, MessageContent } from "@/components/ui/message";
import { MessageScrollerProvider, MessageScroller, MessageScrollerViewport, MessageScrollerContent, MessageScrollerItem, MessageScrollerButton, useMessageScroller } from "@/components/ui/message-scroller";
import { Questionnaire, QuestionnaireActions, QuestionnaireChoice, QuestionnaireChoiceDescription, QuestionnaireChoices, QuestionnaireDescription, QuestionnaireError, QuestionnaireItem, QuestionnaireSubmit, QuestionnaireTitle } from "@/components/ui/questionnaire";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Trace, TraceCollapsible, TraceCollapsibleContent, TraceCollapsibleTrigger, TraceDescription, TraceIcon, TraceLine, TraceTitle } from "@/components/ui/trace";
import { cn } from "@/lib/utils";
import type { Agent, Attachment as AttachmentMeta, BackgroundJob, CodeModeCall, ConfigView, ConversationKind, CronJob, QueueItem, Remote, Session, SavedTabs, SearchPage as SearchPageState, TranscriptEvent, VoiceSettings } from "@/types";
import { runPreload } from "@/preload";
import { decodeSessionId, encodeSessionId } from "@/session-id";
import type { MountOptions, Mounted } from "../../oc/host/api";
import {
  invalidatePendingSaves,
  loadSavedSessions,
  loadSnapshotGeneration,
  listedRows,
  mergeSessionRows,
  readSessionEnumeration,
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
type SessionCommand = { mode: "fork" | "handoff" | "name" | "tag"; source: string; target?: MessageMatch };
const SessionCommands = createContext<{ command?: SessionCommand; setCommand: Dispatch<SetStateAction<SessionCommand | undefined>>; composer: RefObject<((command: string) => void) | null> }>(null!);

function sessionPath(id: string) {
  return `/s/${encodeSessionId(id)}`;
}

// A remote OpenCode session opens at /oc/, so its path says what it is before the session list arrives.
function rowPath(session: { id: string; kind?: ConversationKind }) {
  return session.kind === "REMOTE_OPENCODE" ? `/oc/${encodeSessionId(session.id)}` : sessionPath(session.id);
}

// pathSession is the session a /s/ or /oc/ path opens, else "".
function pathSession(path: string) {
  return path.startsWith("/s/") ? decodeSessionId(path.slice(3)) : path.startsWith("/oc/") ? decodeSessionId(path.slice(4)) : "";
}

const Delegations = createContext<Pick<HistoryView, "delegations" | "reviewRationales"> | undefined>(undefined);

// The right panel shows one delegation or one skill, so opening either replaces the other.
// A permission review can open at the approver's answer that gave a reason: the one saved under its
// review turn, or, for reviews saved before turns were named, the first that gave the reason.
function panelHref(id?: string, panel: "delegation" | "skill" | "shadow" = "delegation", reason?: string, turn?: string) {
  const params = new URLSearchParams(location.search);
  params.delete("delegation");
  params.delete("skill");
  params.delete("reason");
  params.delete("answer");
  if (id) params.set(panel, id);
  if (reason !== undefined) params.set("reason", reason);
  if (turn) params.set("answer", turn);
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

function slackSession(session: { kind?: ConversationKind }) {
  return session.kind === "SLACK_THREAD";
}

function slackRooms(sessions: { id: string; title?: string; kind?: ConversationKind }[]) {
  return [...new Set(sessions.flatMap((session) =>
    slackSession(session) && session.title ? [session.title] : []
  ))];
}

// The query's last word, and whether it is an unfinished operator: a bare key, a strict prefix of an is:/sort: keyword, or an unclosed quote.
function typingWord(query: string) {
  const word = /(?:^|\s)((?:tag|cron|agent|room|principal|server|model):"(?:[^"\\]|\\.)*\\?|\S*)$/i.exec(query)![1];
  const lower = word.toLowerCase();
  return { word, unfinished: /^(?:tag|cron|agent|room|principal|server|model):(?:"(?:[^"\\]|\\.)*\\?)?$/.test(lower) || /^(?:is|sort):/.test(lower) && ["is:pinned", "is:forked", "is:cron", "is:unread", "sort:newest", "sort:oldest"].some((keyword) => keyword !== lower && keyword.startsWith(lower)) };
}

function termValue(value: string) {
  return /\s/.test(value) || value.startsWith('"') ? JSON.stringify(value) : value;
}

// cutTerms removes each term, with the whitespace after it, from text.
function cutTerms(text: string, terms: { start: number; end: number }[]) {
  return terms.reduceRight((rest, term) => rest.slice(0, term.start) + rest.slice(term.end + 1), text);
}

function FilterPill({ label, onClear }: { label: string; onClear: () => void }) {
  return (
    <button type="button" className="flex min-h-5 max-w-full shrink-0 items-center rounded-full bg-sidebar-row-active px-2 text-xs" onMouseDown={(event) => event.preventDefault()} onClick={onClear}>
      <span className="min-w-0 text-left whitespace-normal [overflow-wrap:anywhere]">{label}</span>
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
  onSteer: (id: string) => Promise<unknown>;
  onPop: (id: string) => Promise<unknown>;
  onRemove: (id: string) => void;
  onReorder: (itemIds: string[]) => void;
  jobs: ReactNode;
}) {
  const dragId = useRef<string | null>(null);
  const [order, setOrder] = useState<string[] | null>(null);
  const [poppingId, setPoppingId] = useState("");
  const [steeringId, setSteeringId] = useState("");
  const [expanded, setExpanded] = useState(() => new Set<string>());
  const [edit, setEdit] = useState<{ id: string; text: string }>();
  const saving = useRef(false);
  const update = useMutation({ mutationKey: ["editQueueItem", conversationId], mutationFn: mutations.editQueueItem, onSettled: () => queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const settling = useIsMutating({ mutationKey: ["editQueueItem", conversationId] }) > 0;
  if (edit && !settling && !items.some((item) => item.id === edit.id && item.delivery !== "STEER")) setEdit(undefined);
  const queueActionLabel = busy ? "Steer" : "Send";
  if (items.length === 0 && !jobs && !update.error) {
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
    <div className="mb-2 rounded-lg bg-muted p-2 shadow-[inset_0_0_0_1px_var(--border)]">
      {jobs}
      {update.error ? <p role="alert" className="px-2 text-sm text-destructive">{update.error instanceof RPCError && update.error.code === 5 ? "This message is no longer waiting." : update.error.message}</p> : null}
      <ul className={cn("flex flex-col gap-0.5 overflow-y-auto", edit ? "max-h-[50dvh]" : "max-h-32")}>
        {rows.map((item) => (
          <li key={item.id} data-queue-id={item.id} className="flex flex-wrap items-center gap-2 rounded-md px-2 py-1">
            <button
              type="button"
              className="cursor-grab touch-none p-1 text-muted-foreground"
              aria-label="Reorder"
              disabled={settling || edit?.id === item.id}
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
            <span className="min-w-0 flex-1 truncate text-sm">
              {item.principal ? <span data-slot="message-author" className="text-muted-foreground after:content-['_·_']">{item.principal}</span> : null}
              {expanded.has(item.id) ? null : <span><InlineText text={item.text.replace(/\s+/g, " ")} /></span>}
            </span>
            <button type="button" disabled={edit?.id === item.id} className="shrink-0 rounded-md p-1 text-muted-foreground hover:bg-accent" aria-label={expanded.has(item.id) ? "Collapse message" : "Expand message"} aria-expanded={expanded.has(item.id)} onClick={() => setExpanded((current) => {
              const next = new Set(current);
              if (next.has(item.id)) next.delete(item.id); else next.add(item.id);
              return next;
            })}>
              <ChevronRight className={cn("size-3.5", expanded.has(item.id) && "rotate-90")} />
            </button>
            <MessageAttachments attachments={item.attachments} conversationId={conversationId} />
            {item.delivery !== "STEER" ? <Button type="button" variant="ghost" size="sm" disabled={sending || settling || !!edit || poppingId !== "" || steeringId !== ""} onClick={() => { update.reset(); setEdit({ id: item.id, text: item.editText ?? item.text }); }}>Edit</Button> : null}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={sending || settling || edit?.id === item.id || (item.delivery === "STASH" && poppingId !== "") || item.id === steeringId}
              onClick={async () => {
                if (item.delivery !== "STASH") {
                  // The first steer claims the row; a second click would find it gone.
                  setSteeringId(item.id);
                  try { await onSteer(item.id); } finally { setSteeringId(""); }
                  return;
                }
                setPoppingId(item.id);
                try { await onPop(item.id); } finally { setPoppingId(""); }
              }}
            >
              {item.id === poppingId ? "Popping…" : item.delivery === "STASH" ? "Pop" : queueActionLabel}
            </Button>
            <button type="button" disabled={settling || edit?.id === item.id} className="shrink-0 rounded-md p-1 text-muted-foreground hover:bg-accent" aria-label="Remove" onClick={() => onRemove(item.id)}>
              <X className="h-3.5 w-3.5" />
            </button>
            {edit?.id === item.id ? <form className="w-full" ref={(form) => { form?.scrollIntoView({ block: "nearest" }); }} onSubmit={async (event) => {
              event.preventDefault();
              if (sending || settling || saving.current) return;
              saving.current = true;
              try { await update.mutateAsync({ id: conversationId, itemId: edit.id, text: edit.text }); setEdit(undefined); } catch { /* The mutation keeps the error and edited text visible. */ } finally { saving.current = false; }
            }}><FieldGroup>
              <Field data-disabled={settling}>
                <FieldLabel className="sr-only" htmlFor={`queue-edit-${item.id}`}>Edit queued message</FieldLabel>
                <Textarea id={`queue-edit-${item.id}`} autoFocus value={edit.text} disabled={settling} className="max-h-[min(12rem,20dvh)] overflow-y-auto" onChange={(event) => setEdit({ ...edit, text: event.target.value })} onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing && event.nativeEvent.keyCode !== 229) { event.preventDefault(); event.currentTarget.form!.requestSubmit(); }
                }} />
              </Field>
              <div className="flex gap-2">
                <Button type="submit" size="sm" disabled={sending || settling}>{settling ? "Saving…" : "Save"}</Button>
                <Button type="button" variant="ghost" size="sm" disabled={settling} onClick={() => { setEdit(undefined); update.reset(); }}>Cancel</Button>
              </div>
            </FieldGroup></form> : expanded.has(item.id) ? <p className="w-full whitespace-pre-wrap wrap-anywhere text-sm">{item.text}</p> : null}
          </li>
        ))}
      </ul>
    </div>
  );
}

const jobState = (job: BackgroundJob) => ({ completed: "finished", stopped: `stopped by ${job.stoppedBy}`, killed: "killed (server restarted)" } as Record<string, string | undefined>)[job.state] ?? job.state;
const jobStatus = (job: BackgroundJob) => job.state === "running" ? "running" : ["failed", "killed"].includes(job.state) ? "error" : undefined;

// jobsSlot is the composer's question card and Background Jobs list, or nothing when the conversation has neither.
function jobsSlot(id: string, jobs: BackgroundJob[] | undefined, onChange: () => Promise<unknown>, openCall: (callId: string) => void, card: ReactNode) {
  return jobs?.length || card ? <>{card}{jobs?.length ? <BackgroundJobs id={id} jobs={jobs} onChange={onChange} openCall={openCall} /> : null}</> : null;
}

function BackgroundJobs({ id, jobs, openCall, onChange }: { id: string; jobs: BackgroundJob[]; openCall: (callId: string) => void; onChange: () => Promise<unknown> }) {
  const stop = useMutation({ mutationFn: mutations.stopBackgroundJob, onSuccess: onChange });
  return <ul aria-label="Background jobs" className={cn("flex flex-col gap-0.5", jobs.length > 3 && "max-h-32 overflow-y-auto")}>
    {jobs.map((job) => <li key={job.jobId} className="flex items-center gap-2 rounded-md px-2 py-1">
      <TraceLine status={jobStatus(job)} className="flex-1">
        <TraceStatusIcon status={jobStatus(job)} icon={<Check />} />
        <TraceTitle>{job.label}</TraceTitle>
        <TraceDescription>{jobState(job)}</TraceDescription>
      </TraceLine>
      {job.hidden ? null : <Button type="button" variant="ghost" size="sm" aria-label={`Open ${job.label}`} onClick={() => job.subagentKey ? navigate(panelHref(id + job.subagentKey)) : openCall(job.toolCallId)}>Open</Button>}
      {job.state === "running" ? <Button type="button" variant="ghost" size="sm" aria-label={`Stop ${job.label}`} disabled={stop.isPending} onClick={() => stop.mutate({ conversationId: id, jobId: job.jobId })}>Stop</Button> : null}
    </li>)}
    {stop.error ? <li role="alert" className="px-2 text-xs text-destructive">{stop.error.message}</li> : null}
  </ul>;
}

// QuestionCard asks a Web turn's pending question as a Questionnaire
// (https://ui.shadcn.com/docs/components/base/questionnaire): Submit sends the picked option, or every checked one.
function QuestionCard({ question, sending, answer }: { question: PendingQuestion; sending: boolean; answer: (selected: string[]) => void }) {
  return <Questionnaire aria-label="Question" className="my-1" onSubmit={(event) => {
    event.preventDefault();
    answer(new FormData(event.currentTarget).getAll("answer").map(String));
  }}>
    <QuestionnaireItem name="answer" multiple={question.multiple} required disabled={sending}>
      <QuestionnaireTitle>{question.question}</QuestionnaireTitle>
      {question.details ? <QuestionnaireDescription>{question.details}</QuestionnaireDescription> : null}
      <QuestionnaireChoices>
        {question.options.map((option) => <QuestionnaireChoice key={option.value} value={option.value}>
          {option.label}
          {option.description ? <QuestionnaireChoiceDescription>{option.description}</QuestionnaireChoiceDescription> : null}
        </QuestionnaireChoice>)}
      </QuestionnaireChoices>
      <QuestionnaireError />
    </QuestionnaireItem>
    <QuestionnaireActions>
      <p className="text-xs text-muted-foreground">Or send your own answer from the message box.</p>
      <QuestionnaireSubmit size="sm" />
    </QuestionnaireActions>
  </Questionnaire>;
}

// useQuestion answers the conversation's first pending Web question, from its card or with Send's
// text, unless that stops the turn. A question no longer pending leaves its card when History refreshes.
function useQuestion(id: string, draft: ComposerDraft, onChange: () => Promise<unknown>, setSendError: (value: string) => void) {
  const mutation = useMutation({ mutationFn: mutations.answerQuestion, onSettled: onChange });
  const question = draft.questions?.[0];
  const answer = (input: { selected?: string[]; custom?: string }) => mutation.mutateAsync({ conversationId: id, askId: question!.id, ...input })
    .then(() => { setSendError(""); return true; }, (err: unknown) => { setSendError(err instanceof RPCError && err.code === 5 ? "This question was already answered or ended" : err instanceof Error ? err.message : "answer failed"); return false; });
  const card = question ? <QuestionCard key={question.id} question={question} sending={mutation.isPending} answer={(selected) => void answer({ selected })} /> : null;
  const take = (delivery: PromptDelivery | undefined, setText: (value: string) => void) => question && delivery === undefined && draft.text.trim() && !isStopCommand(draft.text)
    ? answer({ custom: draft.text }).then((saved) => { if (saved) setText(""); }) : undefined;
  return { answering: mutation.isPending, pending: !!question, take, card };
}

// composerLabels says what Send does: answer the pending question, queue behind the running turn, or send.
function composerLabels(asking: boolean, busy: boolean, id: string) {
  if (asking) return { placeholder: "Answer the question · ⌘⏎ steers", sendLabel: "Send answer" };
  if (busy) return { placeholder: "Queue a follow-up · ⌘⏎ steers", sendLabel: "Send to queue" };
  return { placeholder: id ? "Message or $command" : "Message a new session", sendLabel: "Send message" };
}


// A session created here is titled by its first message, or a cron run by its cron's name, until the session list has it.
const firstMessages = new Map<string, string>();

function sessionTitle(session: Session) {
  return session.name || (session.preview ?? "").split("\n", 1)[0] || firstMessages.get(session.id) || session.label || session.id;
}

function useRoute() {
  const pathname = usePathname() || "/";
  const cron = pathname === "/cron";
  const agents = pathname === "/agents";
  const skills = pathname === "/skills";
  // Skill names are lowercase letters, digits and hyphens (internal/rocketcode skills.go), so paths hold them as-is.
  const skill = pathname.startsWith("/skills/") ? pathname.slice("/skills/".length) : "";
  const config = pathname === "/config";
  const search = isSearch(pathname);
  const voice = pathname === "/voice";
  const pages = pathname === "/pages";
  const shortcuts = pathname === "/shortcuts";
  // A page path keeps its escaped agent and file path after the space segment.
  const page = pathname.startsWith("/pages/") ? pathname.split("/").slice(3).join("/") : "";
  const id = pathname.startsWith("/s/") ? decodeSessionId(pathname.slice(3)) : "";
  const remote = pathname.startsWith("/oc/") ? decodeSessionId(pathname.slice(4)) : "";
  return {
    cron,
    agents,
    skills,
    skill,
    config,
    search,
    voice,
    pages,
    shortcuts,
    page,
    id,
    remote,
    goHome: () => navigate("/"),
    goCron: () => navigate("/cron"),
    goSession: (sessionId: string) => navigate(sessionPath(sessionId)),
  };
}

function TabPane({ show, children }: { show: boolean; children: ReactNode }) {
  return <div className={cn("min-h-0 flex-1 flex-col", show ? "flex" : "hidden")}>{children}</div>;
}

// Memoized so app-level state changes, like opening the palette, do not redraw the hidden pages.
const WarmTabs = memo(function WarmTabs({ cron, agents, skills, config }: { cron: boolean; agents: boolean; skills: boolean; config: boolean }) {
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
});

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

// The index page names the hashed script and stylesheet of the build the server runs; once it stops naming this page's, a newer (or rolled-back) build is live.
function WebAppUpdate() {
  const [{ script, styles }] = useState(() => ({ script: document.querySelector('script[type="module"]')!.getAttribute("src")!, styles: document.querySelector('link[rel="stylesheet"]')!.getAttribute("href")! }));
  const index = useQuery({ queryKey: ["web-build"], queryFn: async ({ signal }) => {
    const response = await fetch("/", { signal });
    if (!response.ok) throw new Error(`Index page check failed: HTTP ${response.status}`);
    return response.text();
  }, refetchInterval: 60_000, retry: false });
  const updated = index.data !== undefined && !(index.data.includes(script) && index.data.includes(styles));
  return <div className={cn("flex shrink-0 items-center gap-3 pr-12 pl-3 text-sm", updated && "border-b bg-muted py-1.5")}>
    <p role="status">{updated ? "A new version of RocketClaw is available." : ""}</p>
    {updated ? <Button size="sm" onClick={() => window.location.reload()}>Refresh</Button> : null}
  </div>;
}

type SidebarView = {
  rows: Session[];
  refreshing: boolean;
  enumerationComplete: boolean;
  remote?: Remote;
};

type SidebarState = SidebarView & {
  invalidateQueries: () => void;
};

const Sidebar = createContext<SidebarState>({
  rows: [],
  refreshing: false,
  enumerationComplete: false,
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
  const [view, setView] = useState<SidebarView>({ rows: [], refreshing: false, enumerationComplete: false });
  const gen = useRef(0);
  const rowsRef = useRef<Session[]>([]);
  const committed = useRef(false);
  // The last complete listing's version and rows; the next poll asks for only what changed since.
  const listing = useRef<{ version: string; rows: Session[] }>({ version: "", rows: [] });
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
    listing.current = { version: "", rows: [] };
    setView({ rows: [], refreshing: false, enumerationComplete: false });
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
      setView((current) => ({ ...current, refreshing: true, enumerationComplete: false }));
      const snapshotGeneration = await loadSnapshotGeneration(currentOwner, currentProtocol).catch(() => undefined);
      if (ac.signal.aborted || captured !== gen.current) return;
      const viewing = document.hasFocus() ? pathSession(location.pathname) : "";
      const held = listing.current;
      readSessionEnumeration(currentOwner, Promise.resolve(listSessions(ac.signal, `/api/ListSessions?${new URLSearchParams({ viewing, since: held.version })}`)), () => rowsRef.current, (merged) => {
        // Only changed rows arrive since a held version; they show in place once the listing completes.
        if (ac.signal.aborted || captured !== gen.current || held.version) {
          return;
        }
        rowsRef.current = merged;
        setView((current) => ({ ...current, rows: merged }));
      }, ac.signal).then((result) => {
        if (ac.signal.aborted || captured !== gen.current) {
          return;
        }
        const rows = shouldCommitSnapshot(result) ? listedRows(held.rows, result.received, result.order) : undefined;
        if (result.mismatch) {
          reset();
          ownerRef.current = undefined;
          // A fast same-owner refetch can hide the intermediate pending state.
          void queryClient.resetQueries({ queryKey: ["identity"] }).then(() => setIdentityGeneration((value) => value + 1));
        } else if (rows) {
          listing.current = { version: result.version ?? "", rows };
          committed.current = true;
          rowsRef.current = rows;
          setView({ rows, enumerationComplete: true, refreshing: false, remote: result.remote });
          void saveCompleteSessions(currentOwner, currentProtocol, rows, snapshotGeneration).catch(() => {});
        } else {
          // A listing that could not be rebuilt leaves the version, so the next poll gets every row.
          if (shouldCommitSnapshot(result)) listing.current = { version: "", rows: [] };
          setView((current) => ({ ...current, enumerationComplete: false, refreshing: false, remote: result.remote ?? current.remote }));
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
    listing.current = { version: "", rows: [] };
    rowsRef.current = stripSessionHistory(rowsRef.current, id);
    setView((current) => ({ ...current, rows: rowsRef.current }));
  }, [owner, protocol.data, bump]);
  useEffect(() => {
    const channel = new BroadcastChannel(SESSION_HISTORY_CHANNEL);
    channel.onmessage = ({ data }: MessageEvent<{ owner: string; protocol: string; id: string }>) => {
      if (data.owner === owner && data.protocol === protocol.data) forgetHistory(data.id);
    };
    return () => channel.close();
  }, [owner, protocol.data, forgetHistory]);
  const visible = owner !== undefined && ownerRef.current === owner && protocolRef.current === protocol.data;
  const value = useMemo(() => ({ ...view, rows: visible ? view.rows : [], enumerationComplete: visible && view.enumerationComplete, invalidateQueries: bump }), [view, visible, bump]);
  return (
    <SidebarInvalidation.Provider value={bump}>
      <Sidebar.Provider value={value}>{children}</Sidebar.Provider>
    </SidebarInvalidation.Provider>
  );
}

type PendingFile = { id: string; file: File };
type ComposerDraft = { text: string; files: PendingFile[]; agent: string; sessionId: string; sending: boolean; busy: boolean; lines: Line[]; parked?: Line[]; consumed?: Set<string>; revision?: string; origin?: ChatOrigin; terminal?: string; historyError?: string; missing?: boolean; historyRead?: Promise<void>; historyAgain?: boolean; start?: string; more?: boolean; earlier?: Promise<void>; delegations?: string[]; reviewRationales?: Record<string, string>; shadows?: ShadowBinding[]; earlierShadows?: { from: string; before: string; shadows: ShadowBinding[] }[]; movable?: boolean; jobs?: BackgroundJob[]; questions?: PendingQuestion[]; error: string; edit: number; submission: number; historyEpoch?: number; revertEligible?: boolean; revertMessageId?: string; revertFloor?: string; canUndo?: boolean; reverting?: boolean; focus?: number; hydrated?: boolean; persistenceKey?: string; persistedEdit?: number; persistenceError?: string };
// latest is the newest turn's entry key: its unsaved messages revert by input ID.
const RevertActions = createContext<{ available: boolean; pending: boolean; latest?: string; floor?: string; run: (messageId?: string, redo?: boolean) => Promise<void> } | undefined>(undefined);
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

// Wide screens show the left tabs beside the page until hidden; narrow ones show them in a drawer that closes when a tab is picked or the address changes.
function useTabSidebar(pathname: string) {
  const medium = useMedia("(min-width: 48rem)");
  const [shown, setShown] = useState(true);
  const [drawerAt, setDrawerAt] = useState<string>();
  if (drawerAt && drawerAt !== pathname) setDrawerAt(undefined);
  const open = medium ? shown : drawerAt === pathname;
  return { medium, open, toggle: () => medium ? setShown(!shown) : setDrawerAt(open ? undefined : pathname) };
}

// The drawer opens from the top-left button or a rightward swipe that starts anywhere except text fields, sliders,
// sideways scrollers, and open popups; a leftward swipe closes it. While it listens, the page claims sideways pans:
// a browser that takes one over (such as Chrome on Linux) cancels the pointer and can replay a stale touch from the
// previous gesture, which reads as a vertical scroll.
function TabDrawer({ medium, open, toggle, children }: ReturnType<typeof useTabSidebar> & { children: ReactNode }) {
  const swiped = useEffectEvent(toggle);
  useEffect(() => {
    if (medium || open) return;
    document.documentElement.style.touchAction = "pan-y pinch-zoom";
    let start: { x: number; y: number } | undefined;
    const begin = (event: TouchEvent) => {
      start = undefined;
      if (event.touches.length !== 1 || document.querySelector('[role="dialog"], [role="listbox"], [role="menu"]')) return;
      for (let node = event.target as Element | null; node; node = node.parentElement) {
        if (node.matches('input, textarea, select, [contenteditable], [data-slot="slider"]') || (node.scrollWidth > node.clientWidth && /auto|scroll/.test(getComputedStyle(node).overflowX))) return;
      }
      start = { x: event.touches[0].clientX, y: event.touches[0].clientY };
    };
    const move = (event: TouchEvent) => {
      if (!start) return;
      const dx = event.touches[0].clientX - start.x, dy = Math.abs(event.touches[0].clientY - start.y);
      // A touch that moves mostly vertically first is a scroll, so its later sideways movement is ignored.
      if (dy > 8 && dy > dx) start = undefined;
      else if (dx >= 48) { start = undefined; swiped(); }
    };
    addEventListener("touchstart", begin, { passive: true });
    addEventListener("touchmove", move, { passive: true });
    return () => {
      document.documentElement.style.touchAction = "";
      removeEventListener("touchstart", begin);
      removeEventListener("touchmove", move);
    };
  }, [medium, open]);
  if (medium) return null;
  return <>
    <div className="flex h-12 shrink-0 items-center border-b px-1 [--navigation-button:2.5rem] [--navigation-icon:1.25rem]"><SidebarToggle open={open} toggle={toggle} /></div>
    <Drawer.Root open={open} onOpenChange={toggle} swipeDirection="left">
      <Drawer.Portal>
        <Drawer.Backdrop className="fixed inset-0 z-50 bg-black/10 opacity-[calc(1-var(--drawer-swipe-progress))] transition-opacity duration-200 data-swiping:duration-0 data-starting-style:opacity-0 data-ending-style:opacity-0 supports-backdrop-filter:backdrop-blur-xs" />
        <Drawer.Viewport className="fixed inset-0 z-50 flex">
          {/* Picking the tab already shown leaves the address alone, so the pick itself closes the drawer. A mouse
              click lands on the popup, which holds the pointer for its swipe, so the tab is found under the pointer. */}
          <Drawer.Popup id="tab-sidebar" onClick={(event) => { if (document.elementFromPoint(event.clientX, event.clientY)?.closest('[role="tab"]')) toggle(); }} className="flex h-full w-3/4 max-w-sm flex-col border-r bg-popover text-sm text-popover-foreground shadow-lg outline-none touch-auto [transform:translateX(var(--drawer-swipe-movement-x))] transition-transform duration-200 ease-in-out data-swiping:select-none data-starting-style:[transform:translateX(-100%)] data-ending-style:[transform:translateX(-100%)]">
            <Drawer.Title className="sr-only">Tabs</Drawer.Title>
            {children}
          </Drawer.Popup>
        </Drawer.Viewport>
      </Drawer.Portal>
    </Drawer.Root>
  </>;
}

function SidebarToggle({ open, toggle }: { open: boolean; toggle: () => void }) {
  const label = open ? "Hide sidebar" : "Show sidebar";
  const Icon = open ? PanelLeftClose : PanelLeftOpen;
  return <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)]" />} aria-label={label} aria-expanded={open} aria-controls="tab-sidebar" onClick={toggle}>
    <Icon className="size-[var(--navigation-icon)]" />
  </TooltipTrigger><TooltipContent side="top">{label}</TooltipContent></Tooltip>;
}

function CopyPageLink() {
  const pathname = usePathname();
  const [result, setResult] = useState<{ pathname: string; status?: "copied" | "error" }>();
  if (result && result.pathname !== pathname) setResult(undefined);
  useEffect(() => {
    if (result?.status !== "copied") return;
    const timer = setTimeout(() => setResult(undefined), 2000);
    return () => clearTimeout(timer);
  }, [result]);
  const status = result?.status;
  const Icon = status === "copied" ? Check : status === "error" ? CircleAlert : Share;
  const message = status === "copied" ? "Page link copied" : status === "error" ? "Could not copy page link. Copy the address from your browser." : "";
  return <div className="shrink-0">
    <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)]" />} aria-label="Copy page link" onClick={async () => {
      const pending = { pathname };
      setResult(pending);
      try {
        await copyText(location.href, document.body);
        setResult((current) => current === pending ? { pathname, status: "copied" } : current);
      } catch {
        setResult((current) => current === pending ? { pathname, status: "error" } : current);
      }
    }}>
      <Icon className={cn("size-[var(--navigation-icon)]", status === "error" && "text-destructive")} />
    </TooltipTrigger><TooltipContent side="top">{message || "Copy page link"}</TooltipContent></Tooltip>
    <span role="status" className="sr-only">{message}</span>
  </div>;
}

// Comparison columns share the existing panels' pointer/keyboard width policy.
function useAsideWidth(side: "tabs" | "delegation" | "shadow") {
  const [edge, label, fallback, min, max] = side === "tabs" ? [1, "Resize tabs", 224, 192, 512] : [-1, `Resize ${side} panel`, 384, 288, Math.round(innerWidth * 0.6)];
  const clamp = (value: number) => Math.round(Math.min(Math.max(value, min), max));
  const [stored, setStored] = useState(() => Number(localStorage.getItem(`${side}-width`)));
  const width = clamp(stored || fallback);
  const resize = (value: number) => {
    const room = document.querySelector("main")!.getBoundingClientRect().width - 26 * parseFloat(getComputedStyle(document.documentElement).fontSize);
    const next = clamp(Math.min(value, side === "shadow" ? room : width + room));
    setStored(next);
    localStorage.setItem(`${side}-width`, String(next));
  };
  return { edge, label, width, min, max, resize };
}

function AsideResizeHandle({ edge, label, width, min, max, resize }: ReturnType<typeof useAsideWidth>) {
  return <div role="separator" aria-orientation="vertical" aria-label={label} aria-valuenow={width} aria-valuemin={min} aria-valuemax={max} tabIndex={0}
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
      }} />;
}

// Tabs resize from their right edge and delegation from its left one.
function ResizableAside({ side, className, children, ...props }: ComponentProps<"aside"> & { side: "tabs" | "delegation" }) {
  const sizing = useAsideWidth(side);
  return <aside {...props} className={cn("relative", className)} style={{ width: sizing.width, minWidth: sizing.min }}>{children}<AsideResizeHandle {...sizing} /></aside>;
}

// An unplaced tab is a session opened before its sidebar row told which space it was born in.
type Tab = { path: string; preview?: boolean; group?: string; space?: string; unplaced?: boolean; search?: SearchPageState };
type TabGroup = { id: string; name: string; color: string; collapsed?: boolean };
type Space = { id: string; name: string };
// last names each space's last active tab.
type TabState = { owner?: string; tabs: Tab[]; active: string; groups: TabGroup[]; spaces: Space[]; space: string; last: Record<string, string> };
const TabActions = createContext<ReturnType<typeof useTabs>>(null!);
const pageTabs: Record<string, [string, typeof Search]> = { "/": ["New session", SquarePen], "/voice": ["Voice", Mic], "/cron": ["Cron", Clock], "/agents": ["Agents", Bot], "/skills": ["Skills", Sparkles], "/config": ["Settings", Settings], "/pages": ["Pages", Files], "/shortcuts": ["Shortcuts", Keyboard] };

// A Search page is /search or a location under it, each its own tab; it is titled with its name, else the query it shows.
const isSearch = (path: string) => /^\/search(\/|$)/.test(path);
const searchTitle = (search?: SearchPageState) => search?.name || search?.searches.find((item) => item.id === search.active)?.query || "Search";

// A hosted page, from its escaped agent and file path, is labeled "<agent> · <path>".
function pageLabel(page: string) {
  const [agent, ...path] = page.split("/").map(decodeURIComponent);
  return `${agent} · ${path.join("/")}`;
}

// A skill's page tab is titled with its name, and a hosted page's tab with its label.
function pageTab(path: string): [string, typeof Search] | undefined {
  if (path.startsWith("/pages/")) return [pageLabel(path.split("/").slice(3).join("/")), FileText];
  return pageTabs[path] ?? (path.startsWith("/skills/") ? [path.slice("/skills/".length), Sparkles] : undefined);
}

// A hosted page opens in the space named by its link's space segment, ignoring case, else in the current space.
// A segment naming a space, or "-", becomes that space's exact name, so one page has one tab per space.
function pageSpace(state: Pick<TabState, "spaces" | "space">, path: string): [string, string] {
  const [, root, segment, ...rest] = path.split("/");
  const space = root === "pages" && rest.length ? state.spaces.find((item) => item.name.toLowerCase() === decodeURIComponent(segment).toLowerCase()) ?? (segment === "-" ? state.spaces.find((item) => item.id === state.space) : undefined) : undefined;
  return space ? [`/pages/${encodeURIComponent(space.name)}/${rest.join("/")}`, space.id] : [path, state.space];
}

// Group colors light enough for black text in both themes.
const groupColors: Record<string, string> = { Grey: "bg-gray-400", Blue: "bg-blue-400", Red: "bg-red-400", Yellow: "bg-yellow-400", Green: "bg-green-400", Pink: "bg-pink-400", Purple: "bg-purple-400", Cyan: "bg-cyan-400", Orange: "bg-orange-400" };
const groupName = (group: TabGroup) => group.name || `${group.color} group`;

// Each group sits where its first member is, so its tabs stay adjacent.
function arrange(tabs: Tab[]) {
  const rank = (tab: Tab) => tabs.findIndex((other) => other === tab || (!!tab.group && other.group === tab.group));
  return tabs.toSorted((a, b) => rank(a) - rank(b));
}

// A tab joining a group moves after its last member, into the group's space; a tab from another space becomes permanent.
function join(tabs: Tab[], path: string, group: string, space: string) {
  const rest = tabs.filter((tab) => tab.path !== path), tab = tabs.find((item) => item.path === path);
  return rest.toSpliced(rest.findLastIndex((item) => item.group === group) + 1, 0, { ...tab, path, preview: tab?.space === space && tab.preview, group, space });
}

// New session is one location, so the space that opens it takes its tab, out of any group.
// Like every tab moved between spaces, it becomes permanent, so each space keeps at most one preview tab.
function home(tabs: Tab[], space: string) {
  return tabs.some((tab) => tab.path === "/") ? tabs.map((tab) => tab.path === "/" && tab.space !== space ? { path: "/", space } : tab) : [...tabs, { path: "/", space }];
}

// An opened location replaces the current space's preview tab, or else opens right of the active tab, inside the active tab's group.
// A permanent open leaves the preview tab alone and makes an already open tab permanent.
// Activating any other tab enters its space, where it becomes the last active tab, and expands its group.
// A session opens where the link points, marked unplaced until TabSettling moves it to the space it was born in.
// A location shared in the current space opens there without a tab of this person's, even if they have one elsewhere.
// A Search page always opens permanent, so opening one of its results never replaces it.
function openTab(state: TabState, link: string, shared: SavedTabs["shared"], preview = true): TabState {
  const [path, target] = pageSpace(state, link);
  preview &&= !isSearch(path);
  if (isShared(shared, state.space, path)) return { ...state, active: path, last: { ...state.last, [state.space]: path } };
  const open = state.tabs.find((tab) => tab.path === path), current = open && path !== "/" ? open.space! : target;
  const slot = preview ? state.tabs.findIndex((tab) => tab.preview && tab.space === current) : -1;
  const group = state.tabs.find((tab) => tab.path === state.active && tab.space === current)?.group;
  const at = slot >= 0 ? slot : state.tabs.findIndex((tab) => tab.path === state.active) + 1 || state.tabs.length;
  const added = state.tabs.toSpliced(at, Number(slot >= 0), { path, preview, group, space: current, unplaced: pathSession(path) !== "" || undefined });
  return { ...state, active: path, space: current, last: { ...state.last, [current]: path },
    groups: state.groups.map((item) => item.id === open?.group && path !== state.active ? { ...item, collapsed: false } : item),
    tabs: !open ? arrange(group && state.tabs[at]?.group !== group ? join(added, path, group, current) : added) : path === "/" ? home(state.tabs, current) : preview ? state.tabs : state.tabs.map((tab) => tab.path === path ? { ...tab, preview: false } : tab) };
}

// Entering a space returns to its last active tab, which may be a Shared one, else its first tab, else New session.
function entry(state: Pick<TabState, "tabs" | "last">, space: string, shared: SavedTabs["shared"]) {
  const there = state.tabs.filter((tab) => tab.space === space), last = state.last[space];
  return there.some((tab) => tab.path === last) || isShared(shared, space, last) ? last : there[0]?.path ?? "/";
}

// Leaving deleted space doomed moves its tabs, made permanent, and their groups to the first remaining space, as the server
// does its Shared tabs.
function leave(state: TabState, doomed: string, spaces: Space[]): TabState {
  return { ...state, spaces, space: spaces[0].id, tabs: state.tabs.map((tab) => tab.space === doomed ? permanent(tab, spaces[0].id) : tab) };
}

// A tab moved to space as a permanent tab keeps everything else it holds.
const permanent = ({ preview: _preview, unplaced: _unplaced, ...tab }: Tab, space: string): Tab => ({ ...tab, space });

// The server's saved tabs; tabs left in a lost space or group belong to the first space and no group.
function savedTabs(owner?: string, saved?: Pick<SavedTabs, "spaces" | "state">): TabState {
  const spaces = saved?.spaces ?? [{ id: "main", name: "Main" }], known = (id?: string) => spaces.some((space) => space.id === id);
  const groups = saved?.state.groups ?? [];
  const tabs = (saved?.state.tabs ?? []).map(({ group, space, ...tab }) => ({ ...tab, group: groups.some((item) => item.id === group) ? group : undefined, space: known(space) ? space : spaces[0].id }));
  return { owner, tabs: arrange(tabs), active: "", groups, spaces, space: known(saved?.state.space) ? saved!.state.space : spaces[0].id, last: saved?.state.last ?? {} };
}

// The URL is the active tab, so a reload keeps it and any other URL not shared in the space opens as a permanent tab.
function loadTabs(path: string, owner?: string, saved?: SavedTabs, shared: SavedTabs["shared"] = []): TabState {
  const state = savedTabs(owner, saved);
  const [link, target] = pageSpace(state, path);
  const tabs = state.tabs.some((tab) => tab.path === link) || isShared(shared, state.space, link) ? state.tabs : arrange([...state.tabs, { path: link, space: target, unplaced: pathSession(link) !== "" || undefined }]);
  return openTab({ ...state, tabs, active: link }, link, shared);
}

// The saved form drops groups without members, except "shared", which holds whether this person collapsed Shared.
const savedState = (state: TabState): SavedTabs["state"] => ({
  tabs: state.tabs.map(({ path, preview, group, space, search }) => ({ path, preview, group, space, search })), groups: state.groups.filter((group) => group.id === "shared" || state.tabs.some((tab) => tab.group === group.id)),
  space: state.space, last: state.last,
});

const noShared: SavedTabs["shared"] = [], noMutes: SavedTabs["mutes"] = [];
const sharedTab = (shared: SavedTabs["shared"], space: string, path: string) => shared.find((tab) => tab.space === space && tab.path === path);
const isShared = (shared: SavedTabs["shared"], space: string, path: string) => !!sharedTab(shared, space, path);
// A tab's mute, or its snooze until it ends; the next Tabs reply drops an ended snooze.
const muteOf = (mutes: SavedTabs["mutes"], path: string) => mutes.find((mute) => mute.path === path && (!mute.until || Date.parse(mute.until) > Date.now()));

// Every Tabs reply carries every space's Shared tabs and this person's mutes, which replace the cached ones when they
// changed, as a cache write redraws the app; this person's tabs are left to the revision merge in useTabs.
// received sees the reply before the cache takes it.
async function tabsCall(input: Parameters<typeof mutations.tabs>[0], received?: (reply: SavedTabs) => void) {
  const reply = await mutations.tabs(input);
  received?.(reply);
  queryClient.setQueriesData<SavedTabs>({ queryKey: ["tabs"] }, (old) => old && !reply.stale && JSON.stringify([old.shared, old.mutes]) !== JSON.stringify([reply.shared, reply.mutes]) ? { ...old, shared: reply.shared, mutes: reply.mutes } : undefined);
  return reply;
}

// A three-way merge of one list: what this window opened, closed, or changed since base wins; the rest is the server's.
function mergeList<T>(base: T[], local: T[], server: T[], key: (item: T) => string, sig: (item: T) => string, localOrder: boolean) {
  const find = (list: T[], item: T) => list.find((other) => key(other) === key(item));
  const all = localOrder ? [...local, ...server] : [...server, ...local];
  return all.filter((item, i) => all.findIndex((other) => key(other) === key(item)) === i)
    .filter((item) => find(server, item) ? find(local, item) || !find(base, item) : find(local, item) && !find(base, item))
    .map((item) => {
      const mine = find(local, item), was = find(base, item);
      return mine && (!was || sig(mine) !== sig(was)) ? mine : find(server, item) ?? item;
    });
}

// Tabs saved elsewhere since base, with this window's own changes since base on top. This window keeps its order, tabs
// opened elsewhere going last, unless only the other window reordered tabs.
function mergeTabs(base: SavedTabs["state"], local: SavedTabs["state"], server: SavedTabs["state"]): SavedTabs["state"] {
  const shared = (list: SavedTabs["state"]["tabs"], other: SavedTabs["state"]["tabs"]) => list.flatMap((tab) => other.some((item) => item.path === tab.path) ? [tab.path] : []).join("\n");
  return {
    tabs: mergeList(base.tabs, local.tabs, server.tabs, (tab) => tab.path, (tab) => [tab.path, !!tab.preview, tab.group ?? "", tab.space ?? "", JSON.stringify(tab.search ?? null)].join("\n"), shared(local.tabs, base.tabs) !== shared(base.tabs, local.tabs) || shared(server.tabs, base.tabs) === shared(base.tabs, server.tabs)),
    groups: mergeList(base.groups, local.groups, server.groups, (group) => group.id, (group) => [group.id, group.name, group.color, !!group.collapsed].join("\n"), false),
    space: local.space !== base.space ? local.space : server.space,
    last: { ...server.last, ...Object.fromEntries(Object.entries(local.last).filter(([space, path]) => base.last[space] !== path)) },
  };
}

function useTabs() {
  const identity = useQuery(queries.identity());
  const owner = identity.data?.username;
  const saved = useQuery({ ...queries.tabs(owner ?? ""), enabled: owner !== undefined });
  const shared = saved.data?.shared ?? noShared, mutes = saved.data?.mutes ?? noMutes;
  const pathname = usePathname() || "/";
  const [state, setState] = useState(() => loadTabs(pathname));
  const here = shared.flatMap((tab) => tab.space === state.space ? [tab.path] : []);
  const [editor, setEditor] = useState<{ id: string; open: boolean }>();
  // The tab whose notifications Pick date & time snoozes.
  const [snoozing, setSnoozing] = useState<string>();
  // base is the saved state this window last saved or took in, at revision; at stamps the read last taken in.
  const sync = useRef<{ saving: boolean; dirty: boolean; next?: TabState; applied?: TabState; base?: SavedTabs["state"]; revision: number; at?: number }>({ saving: false, dirty: false, revision: 0 });
  const spaceChange = useMutation({ mutationFn: (input: Parameters<typeof tabsCall>[0]) => tabsCall(input), onSuccess: (view) => setState((current) => ({ ...current, spaces: view.spaces })), onError: () => queryClient.invalidateQueries({ queryKey: ["tabs"] }) });
  const sharing = useMutation({ mutationFn: (input: Parameters<typeof tabsCall>[0]) => tabsCall(input), onError: () => queryClient.invalidateQueries({ queryKey: ["tabs"] }) });
  if (state.active !== pageSpace(state, pathname)[0]) setState(openTab(state, pathname, shared));
  // A page link whose space segment was rewritten shows the rewritten path, once the owner's spaces are known.
  useEffect(() => {
    if (state.owner === undefined || state.active === pathname) return;
    history.replaceState(history.state, "", state.active + location.search);
    dispatchEvent(new PopStateEvent("popstate"));
  }, [state.owner, state.active, pathname]);
  // The query refetches when the page becomes visible; a window that stayed visible reloads when it regains focus.
  useEffect(() => {
    const reload = () => void saved.refetch({ cancelRefetch: false });
    addEventListener("focus", reload);
    return () => removeEventListener("focus", reload);
  }, [saved.refetch]);
  // A newer saved state merges tabs, groups, and spaces changed elsewhere with this window's own unsaved changes, which
  // are then saved. The window stays on its tab; when that tab closed elsewhere, or left the space's Shared tabs, it
  // enters its space as if switching to it. A tab shared in the space stays open there. A space deleted elsewhere is
  // left as if this window deleted it.
  useEffect(() => {
    const queue = sync.current;
    if (!saved.data || (state.owner === owner && queue.at === saved.dataUpdatedAt)) return;
    queue.at = saved.dataUpdatedAt;
    const alive = saved.data.spaces.some((item) => item.id === state.space);
    const base = queue.base, revision = Number(saved.data.revision), inShared = alive && isShared(shared, state.space, state.active);
    // A first load saves the tab its URL added.
    if (state.owner !== owner) {
      [queue.base, queue.revision] = [saved.data.state, revision];
      return setState(loadTabs(pathname, owner, saved.data, shared));
    }
    if (revision <= queue.revision) {
      if (!alive) return setState(openTab(leave(state, state.space, saved.data.spaces), state.active, shared));
      if (!inShared && !state.tabs.some((tab) => tab.path === state.active && tab.space === state.space)) navigate(entry(state, state.space, shared));
      return;
    }
    [queue.base, queue.revision] = [saved.data.state, revision];
    const next = savedTabs(owner, { spaces: saved.data.spaces, state: mergeTabs(base!, savedState(state), saved.data.state) }), open = inShared ? { space: state.space } : next.tabs.find((tab) => tab.path === state.active);
    const merged = { ...next, tabs: next.tabs.map((tab) => ({ ...tab, unplaced: state.tabs.find((item) => item.path === tab.path)?.unplaced })), active: state.active, space: open?.space ?? (next.spaces.some((item) => item.id === state.space) ? state.space : next.spaces[0].id) };
    queue.applied = queue.dirty ? undefined : merged;
    setState(merged);
    if (!open) navigate(entry(merged, merged.space, shared));
  }, [saved.data, saved.dataUpdatedAt, owner, state, pathname, shared]);
  // Each change saves the whole state, made from the saved revision, one save at a time, the newest waiting change next.
  // A failed save is not undone: the next change or reload saves it again. A save refused because another window saved
  // first answers with that newer state, which the reload above merges in and saves again.
  const flush = () => {
    const queue = sync.current, sent = savedState(queue.next!);
    queue.saving = true;
    queue.next = undefined;
    void tabsCall({ state: sent, revision: queue.revision }).then((reply) => {
      if (reply.stale) return void queryClient.setQueriesData({ queryKey: ["tabs"] }, reply);
      [queue.base, queue.revision] = [sent, Number(reply.revision)];
      if (!queue.next) queue.dirty = false;
    }, () => undefined).finally(() => {
      queue.saving = false;
      if (queue.next) flush();
    });
  };
  useEffect(() => {
    const queue = sync.current;
    if (state.owner === undefined || state === queue.applied) return;
    queue.dirty = true;
    queue.next = state;
    if (!queue.saving) flush();
  }, [state]);
  // Moving a tab makes it permanent and takes it out of its group; moving the active tab follows it to its new space.
  const moveTo = (paths: Set<string>, space: string) => setState((current) => openTab({ ...current, tabs: current.tabs.map((tab) => paths.has(tab.path) ? { ...permanent(tab, space), group: undefined } : tab) }, current.active, shared));
  // An unplaced session moves to the space it was born in once its row names it; an unknown new space is fetched first.
  // A background window holds stale tabs, so it waits until it is focused and has reloaded.
  const settle = (rows: Session[]) => {
    if (!document.hasFocus() || saved.isFetching) return;
    for (const tab of state.tabs.filter((item) => item.unplaced)) {
      const birth = rows.find((row) => rowPath(row) === tab.path)?.spaceId;
      if (birth === undefined) continue;
      if (birth && !state.spaces.some((space) => space.id === birth)) {
        void tabsCall({}).then((view) => setState((current) => view.spaces.some((space) => space.id === birth) ? { ...current, spaces: view.spaces } : current));
        return;
      }
      if (birth && birth !== tab.space) moveTo(new Set([tab.path]), birth);
      else setState((current) => ({ ...current, tabs: current.tabs.map((item) => item.path === tab.path ? { ...item, unplaced: undefined } : item) }));
    }
  };
  const update = (change: (tabs: Tab[]) => Tab[]) => setState((current) => ({ ...current, tabs: arrange(change(current.tabs)) }));
  const regroup = (match: (tab: Tab) => boolean, group?: string) => update((tabs) => tabs.map((tab) => match(tab) ? { ...tab, group } : tab));
  const edit = (id: string, change: Partial<TabGroup>) => setState((current) => ({ ...current, groups: current.groups.map((group) => group.id === id ? { ...group, ...change } : group) }));
  // The strip and menus show only the current space's tabs and groups.
  const shown = state.tabs.filter((tab) => tab.space === state.space);
  const groups = state.groups.filter((group) => shown.some((tab) => tab.group === group.id));
  // Closing the active tab activates the target, else its right neighbour, else its left one, in this space; an emptied space gets New session.
  // Only this person's tabs here close, so a tab only in Shared stays open.
  const close = (paths: string[], target?: string) => {
    const doomed = new Set(paths.filter((path) => shown.some((tab) => tab.path === path))), kept = (tab: Tab) => !doomed.has(tab.path);
    const rest = state.tabs.filter(kept), empty = !shown.some(kept);
    setState({ ...state, tabs: empty ? home(rest, state.space) : rest });
    if (!doomed.has(pathname) || (empty && pathname === "/")) return;
    const at = shown.findIndex((tab) => tab.path === pathname);
    navigate(target ?? (shown.slice(at + 1).find(kept) ?? shown.slice(0, at).findLast(kept))?.path ?? "/");
  };
  const enter = (id: string, spaces = state.spaces) => {
    const path = entry(state, id, shared);
    setState(openTab({ ...state, spaces, space: id }, path, shared));
    if (path !== pathname) navigate(path);
  };
  const mute = (path: string, until: string) => sharing.mutate({ mute: { path, until } });
  // A Shared Search page's edit saves only onto the page it was made from. Refused because someone changed the page
  // meanwhile, it is made again onto theirs, keeping their searches and this person's change.
  const saveShared = (space: string, path: string, base: SearchPageState | undefined, page: SearchPageState) => sharing.mutate({ editShared: { space, path, base, search: page } }, { onSuccess: (reply) => {
    const theirs = sharedTab(reply.shared, space, path)?.search;
    if (!reply.stale) return;
    queryClient.setQueriesData<SavedTabs>({ queryKey: ["tabs"] }, (old) => old && { ...old, shared: reply.shared });
    if (theirs) saveShared(space, path, theirs, rebase(base, page, theirs));
  } });
  return {
    state, shown, groups, here, shared, mutes, pathname, close, enter, edit, editor, setEditor, settle, snoozing, setSnoozing, mute, pending: spaceChange.isPending,
    swapShared: (path: string, other: string) => sharing.mutate({ swapShared: { space: state.space, path, with: other } }),
    // Collapsing Shared is this person's choice for every space, kept as the member-less group "shared".
    toggleShared: () => setState((current) => ({ ...current, groups: [...current.groups.filter((group) => group.id !== "shared"), { id: "shared", name: "", color: "Grey", collapsed: !current.groups.some((group) => group.id === "shared" && group.collapsed) }] })),
    // A new space takes the first free "Space N" name; the server keeps names unique ignoring case.
    addSpace: async () => {
      const taken = new Set(state.spaces.map((space) => space.name.toLowerCase()));
      let n = state.spaces.length + 1;
      while (taken.has(`space ${n}`)) n++;
      const spaces = (await spaceChange.mutateAsync({ createSpace: `Space ${n}` })).spaces, id = spaces.find((space) => space.name === `Space ${n}`)?.id;
      // A new space has no tabs, so entering it opens New session.
      setState((current) => openTab({ ...current, spaces, space: id ?? current.space }, "/", shared));
      navigate("/");
    },
    // A rejected name keeps the old one.
    renameSpace: (name: string) => spaceChange.mutate({ renameSpace: { id: state.space, name } }),
    deleteSpace: async () => {
      const doomed = state.space, reply = await spaceChange.mutateAsync({ deleteSpace: doomed });
      setState((current) => openTab(leave(current, doomed, reply.spaces), current.active, reply.shared));
    },
    promote: (path: string) => { if (state.tabs.some((tab) => tab.path === path && tab.preview)) update((tabs) => tabs.map((tab) => tab.path === path ? { ...tab, preview: false } : tab)); },
    // Cmd/Ctrl+[ and ], and their palette commands, step through this space's Shared rows, then its tabs, each tab once,
    // wrapping.
    step: (back: boolean) => {
      const order = [...new Set([...here, ...shown.map((tab) => tab.path)])], at = order.indexOf(pathname);
      const target = order[at < 0 ? (back ? order.length - 1 : 0) : (at + (back ? order.length - 1 : 1)) % order.length];
      if (target !== pathname) navigate(target);
    },
    // The tab is opened with the navigation, in one render, so the URL never shows it as a preview first.
    open: (path: string, query = "", space?: string) => flushSync(() => { setState((current) => openTab(space ? { ...current, space } : current, path, shared, false)); navigate(path + query); }),
    // A Search page's change merges into the person's own page or, inShared, into the one page everyone in this space
    // shares, which the cache takes at once so the next change merges into it.
    editSearch: (path: string, change: Partial<SearchPageState>, inShared = false) => {
      const merge = <T extends { path: string; search?: SearchPageState }>(tab: T) => tab.path === path ? { ...tab, search: { searches: [], active: "", ...tab.search, ...change } } : tab;
      if (!inShared) return setState((current) => ({ ...current, tabs: current.tabs.map(merge) }));
      const next = shared.map((tab) => tab.space === state.space ? merge(tab) : tab);
      queryClient.setQueriesData<SavedTabs>({ queryKey: ["tabs"] }, (old) => old && { ...old, shared: next });
      saveShared(state.space, path, sharedTab(shared, state.space, path)?.search, sharedTab(next, state.space, path)!.search!);
    },
    // A moved tab takes the group of the tab it lands on.
    move: (from: string, to: string) => update((tabs) => moveQueueId(tabs.map((tab) => tab.path), from, to).map((path) => tabs.find((tab) => tab.path === path)!).map((tab) => tab.path === from ? { ...tab, group: tabs.find((other) => other.path === to)?.group } : tab)),
    // A moved group lands whole before or after the tab it lands on, or that tab's whole group, the way a moved tab does.
    moveGroup: (id: string, to: string) => update((tabs) => {
      const members = tabs.filter((tab) => tab.group === id), rest = tabs.filter((tab) => tab.group !== id), target = tabs.find((tab) => tab.path === to)!;
      if (target.group === id) return tabs;
      const block = rest.filter((tab) => tab === target || (!!target.group && tab.group === target.group));
      return rest.toSpliced(tabs.indexOf(target) > tabs.indexOf(members[0]) ? rest.indexOf(block.at(-1)!) + 1 : rest.indexOf(block[0]), 0, ...members);
    }),
    // A new session's tab waits for its row to learn the space it was born in.
    create: (id: string) => update((tabs) => tabs.map((tab) => tab.path === "/" ? { path: sessionPath(id), group: tab.group, space: tab.space, unplaced: true } : tab)),
    // Close, the group commands, and the moves act on paths, the selected tabs; sub names an item's submenu and its label there.
    commands: (path: string, rows: Session[], paths = new Set([path])): { key: string; label: string; sub?: { menu: string; label: string }; run: () => void }[] => {
      const tab = state.tabs.find((item) => item.path === path), selected = state.tabs.filter((item) => paths.has(item.path));
      const birth = paths.size === 1 && state.spaces.find((space) => space.id === rows.find((row) => rowPath(row) === path)?.spaceId);
      // Shared changes act on this one tab, for everyone. A Shared tab that is not this person's offers only its removal
      // and its notifications.
      // Taking a Search page out of Shared keeps the page it held, as the server returns it, as this person's own, with
      // any searches of their own page there, so nobody's searches are lost.
      const keep = ({ unshared: page }: SavedTabs) => page && setState((current) => ({ ...current, tabs: current.tabs.some((item) => item.path === path)
        ? current.tabs.map((item) => item.path === path ? { ...item, search: { ...page, searches: [...page.searches, ...item.search?.searches.filter((own) => !page.searches.some((other) => other.id === own.id)) ?? []] } } : item)
        : [...current.tabs, { path, space: state.space, search: page }] }));
      const share = here.includes(path) ? [{ key: "unshare", label: "Remove from Shared", run: () => void tabsCall({ unshare: { space: state.space, path } }, keep).catch(() => queryClient.invalidateQueries({ queryKey: ["tabs"] })) }]
        : path === "/" ? [] : [{ key: "share", label: "Add to Shared", run: () => sharing.mutate({ share: { space: state.space, path, search: tab?.search } }) }];
      // Muting and snoozing are this person's, and only sessions notify. Snooze ends are reckoned when chosen, in local time.
      const nine = (days: number) => new Date().setHours(24 * days + 9, 0, 0, 0);
      const notify = !pathSession(path) ? [] : muteOf(mutes, path) ? [{ key: "notify-unmute", label: "Unmute notifications", run: () => sharing.mutate({ unmute: path }) }] : [
        { key: "notify-mute", label: "Mute notifications", run: () => mute(path, "") },
        ...([["1h", "for 1 hour", "1 hour", () => Date.now() + 3_600_000], ["4h", "for 4 hours", "4 hours", () => Date.now() + 14_400_000], ["tomorrow", "until tomorrow, 9:00", "Tomorrow, 9:00", () => nine(1)],
          ["monday", "until next Monday, 9:00", "Next Monday, 9:00", () => nine((8 - new Date().getDay()) % 7 || 7)]] as const)
          .map(([key, label, sub, until]) => ({ key: `notify-${key}`, label: `Snooze notifications ${label}`, sub: { menu: "Snooze notifications", label: sub }, run: () => mute(path, new Date(until()).toISOString()) })),
        { key: "notify-pick", label: "Snooze notifications until a picked time…", sub: { menu: "Snooze notifications", label: "Pick date & time…" }, run: () => setSnoozing(path) },
      ];
      if (!shown.some((item) => item.path === path)) return [...share, ...notify];
      return [
        { key: "close", label: "Close", run: () => close([...paths]) },
        { key: "close-others", label: "Close others", run: () => close(shown.flatMap((tab) => tab.path === path ? [] : [tab.path]), path) },
        { key: "close-right", label: "Close to the right", run: () => close(shown.slice(shown.findIndex((tab) => tab.path === path) + 1).map((tab) => tab.path), path) },
        { key: "close-all", label: "Close all", run: () => close(shown.map((tab) => tab.path)) },
        ...share,
        ...notify,
        { key: "group-new", label: "Add to new group", sub: { menu: "Add to group", label: "New group" }, run: () => {
          const id = crypto.getRandomValues(new Uint32Array(2)).join("-");
          setState((current) => ({ ...current, groups: [...current.groups, { id, name: "", color: Object.keys(groupColors)[groups.length % Object.keys(groupColors).length] }] }));
          regroup((item) => paths.has(item.path), id);
          setEditor({ id, open: true });
        } },
        ...groups.filter((group) => selected.some((item) => item.group !== group.id)).map((group) => ({ key: `group-${group.id}`, label: `Add to group "${groupName(group)}"`, sub: { menu: "Add to group", label: groupName(group) }, run: () => update((tabs) => selected.reduce((joined, item) => join(joined, item.path, group.id, state.space), tabs)) })),
        ...selected.some((item) => item.group) ? [{ key: "group-remove", label: "Remove from group", run: () => regroup((item) => paths.has(item.path)) }] : [],
        // A session moved to another space can go back to the space it was born in.
        ...birth && birth.id !== tab?.space ? [{ key: "restore", label: `Restore to "${birth.name}"`, run: () => moveTo(paths, birth.id) }] : [],
        ...state.spaces.filter((space) => space.id !== state.space).map((space) => ({ key: `space-${space.id}`, label: `Move to "${space.name}"`, sub: { menu: "Move to space", label: space.name }, run: () => moveTo(paths, space.id) })),
      ];
    },
    // New session is one location, so New tab in group moves it into the group and its space.
    groupCommands: (id: string) => [
      { key: "new", label: "New tab in group", run: () => { update((tabs) => join(tabs, "/", id, state.space)); navigate("/"); } },
      { key: "ungroup", label: "Ungroup", run: () => regroup((tab) => tab.group === id) },
      { key: "close", label: "Close group", run: () => close(state.tabs.flatMap((tab) => tab.group === id ? [tab.path] : [])) },
      // A group moves whole, keeping its name, color, and collapsed state; its tabs become permanent, and the active tab follows.
      ...state.spaces.filter((space) => space.id !== state.space).map((space) => ({ key: `space-${space.id}`, label: `Move group to "${space.name}"`, run: () => setState(openTab({ ...state, tabs: state.tabs.map((tab) => tab.group === id ? permanent(tab, space.id) : tab) }, state.active, shared)) })),
    ],
  };
}

// Sessions follow their sidebar rows to the spaces they were born in. Only this and TabNotifications read the rows
// at the top, so their updates do not redraw the whole app.
function TabSettling() {
  const { rows } = useContext(Sidebar), { settle } = useContext(TabActions);
  useEffect(() => settle(rows), [rows, settle]);
  return null;
}

// An open session tab whose unread mark moves past the one this window last saw raises a desktop notification that
// opens it, unless it is the tab in view or this person muted or snoozed it. A Shared tab counts as open. Marks come only
// from this person's own turns, or anyone's in a session in some space's Shared group.
// The first complete live list after a page load or owner change only records the marks; restored rows never count.
// It is titled by the session's name, else its agent, or a remote OpenCode session by its tab title, and shows
// the start of the reply: a RocketClaw row's preview is its last message, an OpenCode row's reply its last answer.
// ponytail: rows refresh on the sidebar poll, so a hidden window's throttled timers can delay the notification;
// server push would fix it. Every window that saw the mark notifies under the session's tag, so the system shows
// one notification, even while another window has the tab in view.
function TabNotifications() {
  const { rows, enumerationComplete } = useContext(Sidebar), { state, shared, mutes } = useContext(TabActions);
  const owner = useQuery(queries.identity()).data?.username;
  const seen = useRef<{ owner?: string; unread?: Map<string, string> }>({});
  useEffect(() => {
    if (!enumerationComplete) return;
    const last = seen.current.owner === owner ? seen.current.unread : undefined;
    seen.current = { owner, unread: new Map(rows.map((row) => [row.id, row.unreadAt ?? ""])) };
    if (!last || !("Notification" in window) || Notification.permission !== "granted") return;
    for (const row of rows) {
      if (!row.unreadAt || row.unreadAt === last.get(row.id)) continue;
      const path = rowPath(row);
      if (![...state.tabs, ...shared].some((tab) => tab.path === path) || muteOf(mutes, path) || (document.hasFocus() && state.active === path)) continue;
      // A notification replacing one with its tag alerts again in Chromium only with renotify, which TypeScript's DOM types omit.
      const reply = cleanText((row.server ? row.reply : row.preview) ?? "", {}).replace(/\s+/g, " ").trim();
      const options = { body: reply.length > 200 ? `${reply.slice(0, 199)}…` : reply || "Finished answering", tag: row.id, renotify: true };
      const title = row.server || !row.agent ? sessionTitle(row) : row.name || row.agent;
      let notification: Notification;
      // Android Chrome shows notifications only from a service worker, which this app lacks: its constructor throws.
      try { notification = new Notification(cleanText(title, {}), options); } catch { return; }
      notification.onclick = () => { focus(); navigate(path); notification.close(); };
    }
  }, [rows, enumerationComplete, owner, state.tabs, state.active, shared, mutes]);
  // Notifications are on by default: the browser asks for its permission on the first click or key press, the
  // earliest a browser lets a page ask.
  useEffect(() => {
    if (!("Notification" in window) || Notification.permission !== "default") return;
    const ask = () => { stop(); void Notification.requestPermission(); };
    const stop = () => { removeEventListener("click", ask, true); removeEventListener("keydown", ask, true); };
    addEventListener("click", ask, true);
    addEventListener("keydown", ask, true);
    return stop;
  }, []);
  return null;
}

// Agents and cron jobs without a space show in every space; the rest only in the space of that name, ignoring case.
function useInSpace() {
  const { state } = useContext(TabActions);
  const name = state.spaces.find((space) => space.id === state.space)?.name.toLowerCase();
  return (item: { space?: string }) => !item.space || item.space.toLowerCase() === name;
}

const arrows: Record<string, number> = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 };
type MenuAnchor = Element | { getBoundingClientRect: () => DOMRect };
const popupClass = "min-w-44 rounded-md border bg-popover p-1 text-popover-foreground shadow-md outline-none", itemClass = "flex cursor-default items-center rounded-sm px-2 py-1.5 text-sm outline-none data-highlighted:bg-accent";

// Each tab list takes Shift+F10 or the menu key for a tab's menu, and Enter or Space to open it. Arrows, Home, and End
// walk the rendered tabs of both lists, as collapsed groups hide some; Shift+Arrow moves the tab within its list instead.
function tabKeys(event: React.KeyboardEvent<HTMLElement>, pathname: string, openMenu: (path: string, anchor: MenuAnchor) => void, move: (from: string, to: string) => void) {
  const tab = event.target as HTMLElement;
  if (tab.role !== "tab") return;
  const path = tab.closest<HTMLElement>("[data-tab]")!.dataset.tab!;
  if ((event.shiftKey && event.key === "F10") || event.key === "ContextMenu") {
    event.preventDefault();
    return openMenu(path, tab);
  }
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    if (path !== pathname) navigate(path);
    return;
  }
  const rendered = [...(event.shiftKey ? event.currentTarget : event.currentTarget.closest("[data-tab-lists]")!).querySelectorAll<HTMLElement>('[role="tab"]')], step = arrows[event.key];
  const position = event.key === "Home" ? 0 : event.key === "End" ? rendered.length - 1 : step ? rendered.indexOf(tab) + step : -1;
  if (!rendered[position]) return;
  event.preventDefault();
  if (event.shiftKey && step) {
    flushSync(() => move(path, rendered[position].closest<HTMLElement>("[data-tab]")!.dataset.tab!));
    tab.focus();
  } else rendered[position].focus();
}

// Sessions and Search pages can be renamed from their tab, and a Search page from its Shared row, which otherwise offers
// only its Shared and notification items.
function menuItems(commands: ReturnType<typeof useTabs>["commands"], menu: { path: string; shared?: boolean }, rows: Session[], targets: Set<string>, rename: () => void) {
  const renaming = targets.size === 1 && (isSearch(menu.path) || (pathSession(menu.path) && !menu.shared)) ? [{ key: "rename", label: isSearch(menu.path) ? "Rename search" : "Rename session", run: rename }] : [];
  return [...renaming, ...menu.shared ? commands(menu.path, rows).filter(({ key }) => key === "unshare" || key.startsWith("notify")) : commands(menu.path, rows, targets)];
}

function TabStrip({ tabs, newChat }: { tabs: ReturnType<typeof useTabs>; newChat: () => void }) {
  const { rows } = useContext(Sidebar);
  const { state, shown, groups, pathname, close, promote, move, moveGroup, commands, edit, editor, setEditor, groupCommands } = tabs, space = state.spaces.find((item) => item.id === state.space)?.name;
  const [menu, setMenu] = useState<{ path: string; anchor: MenuAnchor; shared?: boolean }>();
  const [editing, setEditing] = useState<string>();
  const pathOf = (event: SyntheticEvent) => (event.target as Element).closest<HTMLElement>("[data-tab]")?.dataset.tab;
  const list = useRef<HTMLDivElement>(null), refocus = useRef("");
  // Closing from the strip unmounts the focused control, so focus moves to the selected tab, as in SearchTabs.
  const selected = () => list.current!.querySelector<HTMLElement>('[role="tab"][tabindex="0"]')!;
  const header = () => list.current!.querySelector<HTMLElement>(`[data-group="${editor?.id}"]`);
  const group = groups.find((item) => item.id === editor?.id);
  // Cmd/Ctrl+click and Shift+click select tabs besides the active one; activating a tab or Escape drops them.
  const [picked, setPicked] = useState({ at: pathname, paths: new Set<string>() });
  const selection = new Set(shown.flatMap((tab) => tab.path === pathname || (picked.at === pathname && picked.paths.has(tab.path)) ? [tab.path] : []));
  // The menu of a selected tab acts on every selected tab; any other tab's menu acts on that tab alone.
  const targets = menu && !selection.has(menu.path) ? new Set([menu.path]) : selection;
  const items: ReturnType<typeof commands> = !menu ? [] : menuItems(commands, menu, rows, targets, () => setEditing(`${menu.shared ? "shared:" : ""}${menu.path}`));
  // The active tab is the list's keyboard stop, else, while it shows only in Shared, the first rendered tab.
  const stop = shown.some((tab) => tab.path === pathname) ? pathname : shown.find((tab) => !groups.find((item) => item.id === tab.group)?.collapsed)?.path;
  const option = ({ key, label, run }: (typeof items)[number]) => <Menu.Item key={key} onClick={() => { refocus.current = key; run(); }} className={itemClass}>{label}</Menu.Item>;
  return <div data-tab-lists className="flex h-full shrink-0 flex-col">
    <SpaceMenu tabs={tabs} name={space} />
    <SharedTabs tabs={tabs} space={space} editing={editing} endEdit={() => setEditing(undefined)} openMenu={(path, anchor) => setMenu({ path, anchor, shared: true })} />
    <div ref={list} role="tablist" aria-label="Open tabs" aria-orientation="vertical" className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto p-1 [scrollbar-width:thin]"
      // A selecting click keeps the active tab; Shift+click selects the rendered tabs from the active one to it.
      onClickCapture={(event) => {
        const path = pathOf(event);
        if (!path || !(event.target as Element).closest('[role="tab"]')) return;
        const rendered = [...event.currentTarget.querySelectorAll<HTMLElement>("[data-tab]")].map((node) => node.dataset.tab!);
        const [from, to] = [rendered.indexOf(pathname), rendered.indexOf(path)].toSorted((a, b) => a - b);
        if (event.shiftKey) setPicked({ at: pathname, paths: new Set(rendered.slice(from, to + 1)) });
        else if (event.metaKey || event.ctrlKey) setPicked({ at: pathname, paths: selection.symmetricDifference(new Set([path])) });
        else return setPicked({ at: pathname, paths: new Set() });
        event.stopPropagation();
      }}
      onMouseDown={(event) => { if (event.button === 1) event.preventDefault(); }}
      onDoubleClick={(event) => { if (event.target === event.currentTarget) newChat(); }}
      onAuxClick={(event) => {
        const path = pathOf(event);
        if (event.button !== 1 || !path) return;
        event.preventDefault();
        close([path]);
      }}
      onDragStart={(event) => {
        const group = (event.target as Element).closest<HTMLElement>("[data-group]")?.dataset.group;
        event.dataTransfer.setData(group ? "application/x-rocketclaw-group" : "application/x-rocketclaw-tab", group ?? pathOf(event)!);
      }}
      onDragOver={(event) => event.preventDefault()}
      // A group header stands for its first tab, the only drop target a collapsed group shows.
      onDrop={(event) => {
        const header = (event.target as Element).closest<HTMLElement>("[data-group]")?.dataset.group, group = event.dataTransfer.getData("application/x-rocketclaw-group");
        const path = pathOf(event) ?? shown.find((tab) => !!header && tab.group === header)?.path;
        if (path && group) moveGroup(group, path);
        else if (path) move(event.dataTransfer.getData("application/x-rocketclaw-tab"), path);
      }}
      onKeyDown={(event) => {
        if ((event.target as HTMLElement).role === "tab" && event.key === "Escape" && selection.size > 1 && !menu) {
          event.preventDefault();
          return setPicked({ at: pathname, paths: new Set() });
        }
        tabKeys(event, pathname, (path, anchor) => setMenu({ path, anchor }), move);
      }}>
      {shown.flatMap((tab, index) => {
        const group = groups.find((item) => item.id === tab.group);
        const openEditor = () => setEditor({ id: group!.id, open: true }), chosen = tab.path !== pathname && selection.has(tab.path);
        // A collapsed group's name goes bold, like an unread tab's title, while any of its tabs is unread.
        const unread = group?.collapsed && shown.some((item) => item.group === group.id && rows.some((row) => row.id === pathSession(item.path) && row.unreadAt));
        return [
          group && shown[index - 1]?.group !== group.id ? <div key={group.id} data-group={group.id} draggable className="flex shrink-0 items-center pt-1" onContextMenu={(event) => { event.preventDefault(); openEditor(); }}>
            <button type="button" aria-expanded={!group.collapsed} aria-label={groupName(group)} className="flex h-7 min-w-0 items-center rounded-md px-1.5 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" onClick={() => edit(group.id, { collapsed: !group.collapsed })}
              onKeyDown={(event) => {
                if ((event.shiftKey && event.key === "F10") || event.key === "ContextMenu") { event.preventDefault(); openEditor(); }
                // Shift+Arrow moves the group past the tab or group next to it.
                const button = event.currentTarget, step = event.shiftKey ? arrows[event.key] : 0;
                const neighbor = step && shown[step < 0 ? shown.findIndex((item) => item.group === group.id) - 1 : shown.findLastIndex((item) => item.group === group.id) + 1];
                if (!neighbor) return;
                event.preventDefault();
                flushSync(() => moveGroup(group.id, neighbor.path));
                button.focus();
              }}>
              <span className={cn("rounded-full", group.name ? cn("max-w-48 truncate px-2 py-0.5 text-xs text-black", unread ? "font-bold" : "font-medium") : "size-3", groupColors[group.color])}>{group.name}</span>
            </button>
            <button type="button" aria-label={`Edit ${groupName(group)}`} className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-foreground" onClick={openEditor}><Ellipsis className="size-3.5" /></button>
          </div> : null,
          // A collapsed group still shows its active tab.
          group?.collapsed && tab.path !== pathname ? null : <TabItem key={tab.path} tab={tab} description={[chosen && "In selection", `${group ? `Group ${groupName(group)}, ` : ""}Space: ${space}`].filter(Boolean).join(", ")} chosen={chosen} rule={group?.color} active={tab.path === pathname} stop={tab.path === stop} editing={tab.path === editing} endEdit={() => setEditing(undefined)} promote={() => promote(tab.path)} close={() => { flushSync(() => close([tab.path])); selected().focus(); }} openMenu={(anchor) => setMenu({ path: tab.path, anchor })} />,
        ];
      })}
    </div>
    {/* The name field lives in a popover because menus take typed letters for type-ahead. */}
    <Popover.Root open={!!editor?.open && !!group} onOpenChange={(open) => setEditor((current) => current && { ...current, open })}>
      <Popover.Portal><Popover.Positioner anchor={header} sideOffset={4} align="start" className="z-50 outline-none"><Popover.Popup aria-label="Edit group" finalFocus={() => header()?.querySelector("button") ?? selected()} onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className="flex w-56 flex-col gap-2 rounded-md border bg-popover p-2 text-popover-foreground shadow-md outline-none">
        {group ? <>
          {/* Leaving the field saves the name, as tab state redraws the whole app; a save per key made typing lag. */}
          <Input aria-label="Group name" placeholder="Name this group" defaultValue={group.name} onBlur={(event) => { if (event.target.value !== group.name) edit(group.id, { name: event.target.value }); }}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === "Escape") event.currentTarget.blur();
              if (event.key === "Enter") setEditor({ id: group.id, open: false });
            }} />
          <div role="radiogroup" aria-label="Group color" className="flex flex-wrap gap-2 px-1 py-1">
            {Object.entries(groupColors).map(([color, background]) => <input key={color} type="radio" name="group-color" aria-label={color} checked={group.color === color} onChange={() => edit(group.id, { color })} className={cn("size-5 cursor-pointer appearance-none rounded-full checked:outline-2 checked:outline-offset-2 checked:outline-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring", background)} />)}
          </div>
          {groupCommands(group.id).map(({ key, label, run }) => <button key={key} type="button" className="flex rounded-sm px-2 py-1.5 text-sm hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" onClick={() => { setEditor({ id: group.id, open: false }); run(); }}>{label}</button>)}
        </> : null}
      </Popover.Popup></Popover.Positioner></Popover.Portal>
    </Popover.Root>
    <Menu.Root open={!!menu} onOpenChange={(open) => { if (!open) setMenu(undefined); }}>
      <Menu.Portal><Menu.Positioner anchor={menu?.anchor} sideOffset={4} align="start" className="z-50 outline-none"><Menu.Popup finalFocus={() => { const key = refocus.current; refocus.current = ""; return key.startsWith("close") ? selected() : !["rename", "group-new", "notify-pick"].includes(key); }} onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className={popupClass}>
        {/* Items sharing a submenu, the group adds, the moves to other spaces, and the snoozes, sit in it where its first item would be. */}
        {items.map((item) => !item.sub ? option(item) : item !== items.find((other) => other.sub?.menu === item.sub!.menu) ? null : <Menu.SubmenuRoot key={item.sub.menu}>
          <Menu.SubmenuTrigger className={cn(itemClass, "justify-between gap-4 data-popup-open:bg-accent")}>{item.sub.menu}<ChevronRight className="size-4" /></Menu.SubmenuTrigger>
          <Menu.Portal><Menu.Positioner className="z-50 outline-none"><Menu.Popup className={popupClass}>{items.filter((other) => other.sub?.menu === item.sub!.menu).map((other) => option({ ...other, label: other.sub!.label }))}</Menu.Popup></Menu.Positioner></Menu.Portal>
        </Menu.SubmenuRoot>)}
      </Menu.Popup></Menu.Positioner></Menu.Portal>
    </Menu.Root>
  </div>;
}

// Shared shows the space's Shared tabs, the same for everyone, above this person's tabs, and hides while it has none. It
// is a list of its own, so the
// handlers of theirs never act on a Shared row; only its menu and Shift+Arrow, which reorders it for everyone, change it.
function SharedTabs({ tabs: { state, here, shared, pathname, promote, toggleShared, swapShared }, space, editing, endEdit, openMenu }: { tabs: ReturnType<typeof useTabs>; space?: string; editing?: string; endEdit: () => void; openMenu: (path: string, anchor: MenuAnchor) => void }) {
  const collapsed = state.groups.some((group) => group.id === "shared" && group.collapsed), stop = here.includes(pathname) ? pathname : here[0];
  // Like a collapsed group, collapsed Shared goes bold while any of its tabs is unread for this person.
  const { rows } = useContext(Sidebar), unread = collapsed && here.some((path) => rows.some((row) => row.id === pathSession(path) && row.unreadAt));
  const inert = () => undefined, list = useRef<HTMLDivElement>(null), moved = useRef(""), order = here.join("\n");
  // A moved row takes its new place when the reply arrives, which drops its focus, so it takes the focus back then.
  useEffect(() => { if (moved.current) list.current!.querySelector<HTMLElement>(`[data-tab="${CSS.escape(moved.current)}"] [role="tab"]`)?.focus(); moved.current = ""; }, [order]);
  if (!here.length) return null;
  return <>
    <div className="flex max-h-1/2 shrink-0 flex-col gap-0.5 overflow-y-auto px-1 pt-1 [scrollbar-width:thin]">
      <div className="flex shrink-0 items-center pt-1">
        <button type="button" aria-expanded={!collapsed} className="flex h-7 items-center rounded-md px-1.5 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" onClick={toggleShared}>
          <span className={cn("flex items-center gap-1 rounded-full px-2 py-0.5 text-xs text-black", unread ? "font-bold" : "font-medium", groupColors.Grey)}><Users aria-hidden="true" className="size-3" />Shared</span>
        </button>
      </div>
      <div ref={list} role="tablist" aria-label="Shared tabs" aria-orientation="vertical" className="flex flex-col gap-0.5" onKeyDown={(event) => tabKeys(event, pathname, openMenu, (path, other) => { moved.current = path; swapShared(path, other); })}>
        {here.map((path) => collapsed && path !== pathname ? null : <TabItem key={path} tab={{ path, search: sharedTab(shared, state.space, path)?.search }} shared description={`Shared, Space: ${space}`} chosen={false} rule="Grey" active={path === pathname} stop={path === stop} editing={editing === `shared:${path}`} endEdit={endEdit} promote={() => promote(path)} close={inert} openMenu={(anchor) => openMenu(path, anchor)} />)}
      </div>
    </div>
    <div className="mx-2.5 mt-1 h-px shrink-0 bg-border" />
  </>;
}

// The space button opens the space menu; renaming edits the name in place of the button, as Base UI's Menu takes printable keys.
function SpaceMenu({ tabs: { state, pending, enter, addSpace, renameSpace, deleteSpace }, name }: { tabs: ReturnType<typeof useTabs>; name?: string }) {
  const [renaming, setRenaming] = useState(false);
  const box = "m-1 mb-0 flex shrink-0 items-center gap-1.5 rounded-md px-3 py-2 text-sm font-medium";
  const item = "flex cursor-default items-center rounded-md px-1.5 py-1 text-sm outline-none focus:bg-accent focus:text-accent-foreground";
  return <DropdownMenu>
    {renaming ? <input ref={(node) => node?.focus()} aria-label="Space name" defaultValue={name} className={cn(box, "w-36 bg-background font-normal outline-1 -outline-offset-1 outline-ring")}
      onFocus={(event) => event.currentTarget.select()}
      onKeyDown={(event) => {
        // Escape empties the field, and leaving it blank keeps the old name.
        if (event.key === "Escape") { event.preventDefault(); event.currentTarget.value = ""; }
        if (event.key === "Enter" || event.key === "Escape") event.currentTarget.blur();
      }}
      onBlur={(event) => { const value = event.currentTarget.value.trim(); if (value) renameSpace(value); setRenaming(false); }} />
      : <Menu.Trigger aria-label={`Space: ${name}`} disabled={state.owner === undefined} className={cn(box, "cursor-pointer hover:bg-accent focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring")}><Layers aria-hidden="true" className="size-4" /><span className="max-w-32 truncate">{name}</span><ChevronDown aria-hidden="true" className="size-3.5 text-muted-foreground" /></Menu.Trigger>}
    <DropdownMenuContent finalFocus={!renaming} onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className="w-auto min-w-44">
      <DropdownMenuRadioGroup value={state.space} onValueChange={(id: string) => enter(id)}>
        {state.spaces.map((space) => <DropdownMenuRadioItem key={space.id} value={space.id} closeOnClick>{space.name}</DropdownMenuRadioItem>)}
      </DropdownMenuRadioGroup>
      <Menu.Group className="mt-1 border-t pt-1">
        <Menu.Item className={item} disabled={pending} onClick={() => void addSpace().then(() => setRenaming(true))}>New space</Menu.Item>
        <Menu.Item className={item} onClick={() => setRenaming(true)}>Rename space</Menu.Item>
        {state.spaces.length > 1 ? <Menu.Item className={item} onClick={deleteSpace}>Delete space</Menu.Item> : null}
      </Menu.Group>
    </DropdownMenuContent>
  </DropdownMenu>;
}

// A running turn wins over background work, which can wake the session after its turn.
function tabActivity(session: Session) {
  const [activity, Icon] = session.running ? ["Turn running", MessageSquare] as const : session.background ? ["Background work running", Hourglass] as const : ["", MessageSquare] as const;
  return [[activity, session.unreadAt && "Unread"].filter(Boolean).join(", "), Icon] as const;
}

function TabItem({ tab, description, chosen, rule, active, stop, shared, editing, endEdit, promote, close, openMenu }: { tab: Tab; description: string; chosen: boolean; rule?: string; active: boolean; stop: boolean; shared?: boolean; editing: boolean; endEdit: () => void; promote: () => void; close: () => void; openMenu: (anchor: MenuAnchor) => void }) {
  const sidebar = useContext(Sidebar);
  const id = pathSession(tab.path);
  const session = sidebar.rows.find((row) => row.id === id) ?? { id };
  const [activity, SessionIcon] = tabActivity(session);
  const [label, Icon] = isSearch(tab.path) ? [searchTitle(tab.search), Search] : pageTab(tab.path) ?? [sessionTitle(session), SessionIcon];
  const title = useCleanText(label), mute = muteOf(useContext(TabActions).mutes, tab.path);
  const quiet = mute && (mute.until ? `Snoozed until ${new Date(mute.until).toLocaleString([], { dateStyle: "medium", timeStyle: "short" })}` : "Muted");
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (active) ref.current!.scrollIntoView({ block: "nearest", inline: "nearest" }); }, [active, title]);
  return <div data-tab={tab.path} data-running={session.running} data-unread={!!session.unreadAt} draggable={!editing && !shared} className={cn("group flex shrink-0 items-center rounded-md", active ? "bg-sidebar-row-active" : "hover:bg-accent", { "bg-accent": chosen })}
    onContextMenu={(event) => { event.preventDefault(); openMenu({ getBoundingClientRect: () => DOMRect.fromRect({ x: event.clientX, y: event.clientY }) }); }}>
    {rule && <span className={cn("pointer-events-none my-1.5 ml-1 w-0.5 shrink-0 self-stretch", groupColors[rule])} />}
    <Tooltip disabled={editing}><TooltipTrigger delay={500} render={<div className="flex min-w-0 flex-1" />}>
      <div ref={ref} role="tab" aria-selected={active} aria-label={title} aria-description={[description, activity, quiet].filter(Boolean).join(", ")} tabIndex={stop ? 0 : -1} className={cn("flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 py-2 pr-1 pl-3 text-sm [:last-child>&]:pr-3 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring aria-[selected=false]:text-muted-foreground", tab.preview && "italic")}
        onClick={() => { if (!active) navigate(tab.path); }} onDoubleClick={promote}>
        <Icon aria-hidden="true" className="size-4 shrink-0" />
        {editing ? <TabRename session={session} tab={tab} shared={shared} placeholder={title} endEdit={endEdit} /> : <span className="truncate group-data-[unread=true]:font-bold motion-safe:group-data-[running=true]:shimmer motion-reduce:group-data-[running=true]:shimmer-still dark:in-aria-selected:shimmer-color-foreground/40">{title}</span>}
      </div>
    </TooltipTrigger><TooltipContent side="bottom">{title}</TooltipContent></Tooltip>
    {quiet ? <span title={quiet} className="flex size-6 shrink-0 items-center justify-center text-muted-foreground"><BellOff aria-hidden="true" className="size-3.5" /></span> : null}
    {active ? <button type="button" aria-label="Tab actions" className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground last:mr-1 hover:bg-accent hover:text-foreground" onClick={(event) => openMenu(event.currentTarget)}><Ellipsis className="size-3.5" /></button> : null}
    {shared ? <span aria-hidden="true" title="In Shared: everyone in this space sees it" className="mr-1 flex size-6 shrink-0 items-center justify-center text-muted-foreground"><Users className="size-3.5" /></span>
      : <button type="button" aria-label={`Close ${title}`} className="mr-1 flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring" onClick={close}><X className="size-3.5" /></button>}
  </div>;
}

// Enter and leaving the field save; Escape restores the old name first, so its blur saves nothing.
function TabRename({ session, tab, shared, placeholder, endEdit }: { session: Session; tab: Tab; shared?: boolean; placeholder: string; endEdit: () => void }) {
  const sidebar = useContext(Sidebar), { editSearch } = useContext(TabActions), search = isSearch(tab.path);
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => { sidebar.invalidateQueries(); endEdit(); } });
  const name = (search ? tab.search?.name : session.name) ?? "";
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current!.focus(), []);
  return <input ref={input} aria-label={search ? "Search page name" : "Session name"} defaultValue={name} placeholder={placeholder} aria-invalid={!!update.error} title={update.error?.message} className="min-w-0 flex-1 rounded-sm bg-background px-1 text-foreground not-italic outline-1 outline-ring aria-invalid:outline-destructive"
    onFocus={(event) => event.currentTarget.select()}
    onClick={(event) => event.stopPropagation()}
    onKeyDown={(event) => {
      if (event.key === "Escape") { event.preventDefault(); event.currentTarget.value = name; }
      if (event.key === "Enter" || event.key === "Escape") event.currentTarget.blur();
    }}
    onBlur={(event) => {
      const value = event.currentTarget.value;
      if (value === name) endEdit();
      else if (search) { editSearch(tab.path, { name: value }, shared); endEdit(); }
      else update.mutate({ id: session.id, name: value });
    }} />;
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
  return <QueryClientProvider client={queryClient}><TooltipProvider><ProtocolGuard /><div className="flex h-dvh flex-col"><WebAppUpdate /><SidebarOwner>
    <SessionApp scope={scope} scopeError={identity.error?.message ?? config.error?.message} />
  </SidebarOwner></div></TooltipProvider></QueryClientProvider>;
}

function SessionApp({ scope, scopeError }: { scope?: string; scopeError?: string }) {
  const [command, setCommand] = useState<SessionCommand>();
  const composer = useRef<((command: string) => void) | null>(null);
  const commands = useMemo(() => ({ command, setCommand, composer }), [command]);
  const route = useRoute();
  const showChat = ![route.cron, route.agents, route.skills, route.skill, route.config, route.search, route.voice, route.pages, route.shortcuts, route.page, route.remote].some(Boolean);
  // The screen stays on while the Voice page is open or a call runs.
  const awake = useSyncExternalStore(subscribeVoice, () => route.voice || !["idle", "error"].includes(voiceView().state));
  useEffect(() => awake ? holdWakeLock() : undefined, [awake]);
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
  const [conversation, setConversation] = useState({ id: "", created: "", key: 0 });
  const newChatOwner = useRef<(() => unknown) | undefined>(undefined);
  const tabs = useTabs();
  const { close: closeTab, pathname } = tabs;
  const sidebar = useTabSidebar(pathname);
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
  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (event.defaultPrevented) return;
    if ((event.metaKey || event.ctrlKey) && !event.altKey && event.code === "KeyP") {
      event.preventDefault();
      event.stopPropagation();
      if (!event.repeat) openPalette(event.shiftKey ? "commands" : "sessions");
      return;
    }
    if (sidebar.medium && (event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.code === "KeyB") {
      event.preventDefault();
      event.stopPropagation();
      if (!event.repeat) sidebar.toggle();
      return;
    }
    if ((event.metaKey || event.ctrlKey) && event.altKey && !event.shiftKey && event.code === "KeyN") {
      event.preventDefault();
      event.stopPropagation();
      if (!event.repeat) newChat();
      return;
    }
    if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && (event.code === "BracketLeft" || event.code === "BracketRight")) {
      event.preventDefault();
      event.stopPropagation();
      if (!event.repeat) tabs.step(event.code === "BracketLeft");
    }
  });

  const onEscape = useEffectEvent((event: KeyboardEvent) => {
    // Escape closes pages that hold nothing, so never a session or a Search page, whose searches would go with it.
    if (event.defaultPrevented || event.repeat || showChat || event.key !== "Escape" || route.remote || route.search) return;
    if (document.querySelector('[role="dialog"], [role="listbox"], [role="menu"], [role="tooltip"]')) return;
    event.preventDefault();
    closeTab([location.pathname]);
  });
  useEffect(() => {
    const key = (event: KeyboardEvent) => onKey(event);
    const escape = (event: KeyboardEvent) => onEscape(event);
    window.addEventListener("keydown", key, true);
    window.addEventListener("keydown", escape);
    return () => {
      window.removeEventListener("keydown", key, true);
      window.removeEventListener("keydown", escape);
    };
  }, []);
  if (showChat && conversation.id !== route.id) {
    // Creation assigns this conversation its ID; other navigation starts a fresh subtree.
    const created = conversation.id === "" && conversation.created === route.id;
    setConversation({ id: route.id, created: "", key: created ? conversation.key : conversation.key + 1 });
  }
  return (
          <DraftScope value={scope}><SessionCommands value={commands}><TabActions value={tabs}><TabSettling /><TabNotifications />
           {command ? <SessionCommandDialog key={`${command.mode}:${command.source}`} command={command} drafts={drafts} onDraftChange={onDraftChange} /> : null}
            {tabs.snoozing ? <SnoozeDialog path={tabs.snoozing} /> : null}
            <CommandPalette key={palette.key} drafts={drafts} mode={palette.mode} setMode={(mode) => setPalette((current) => ({ ...current, mode }))} newChat={newChat} keyCommands={keyCommands(tabs.step, sidebar.toggle, sidebar.medium)} />
         <div className="flex min-h-0 flex-1 flex-col overflow-hidden overscroll-y-none bg-background">
            <BottomNavigation>
              {sidebar.medium ? <SidebarToggle open={sidebar.open} toggle={sidebar.toggle} /> : null}
              <div className="min-w-0 max-w-full justify-self-center overflow-x-auto md:col-start-2 overflow-y-hidden scrollbar-none">
                <div className="flex w-max items-center gap-[var(--navigation-gap)]">
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="New session" onClick={newChat}>
                    <SquarePen className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">New session</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="Search sessions and commands" onClick={() => openPalette("sessions")}>
                    <Search className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">Search sessions and commands</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="Voice" onClick={() => navigate("/voice")}>
                    <Mic className="size-[var(--navigation-icon)]" />
                  </TooltipTrigger><TooltipContent side="top">Voice</TooltipContent></Tooltip>
                  <CopyPageLink />
                </div>
              </div>
            </BottomNavigation>
            <div className="fixed top-2 right-2 z-40 rounded-md bg-background shadow-sm"><ThemeToggle /></div>
          <TabDrawer {...sidebar}><TabStrip tabs={tabs} newChat={newChat} /></TabDrawer>
          <div className="flex min-h-0 min-w-0 flex-1">
            {sidebar.medium ? <ResizableAside side="tabs" id="tab-sidebar" className={cn("border-r", !sidebar.open && "hidden")}><TabStrip tabs={tabs} newChat={newChat} /></ResizableAside> : null}
            <main className={cn("flex min-h-0 min-w-0 flex-1 flex-col md:min-w-[26rem]", command?.target && "pt-[min(75dvh,30rem)]")}>
              <WarmTabs cron={route.cron} agents={route.agents} skills={route.skills} config={route.config} />
              {route.search ? <SearchPage /> : null}
              {route.voice ? <VoicePage /> : null}
              <PageIndex show={route.pages} />
              <ShortcutsPage show={route.shortcuts} />
              <HostedPage key={route.page} page={route.page} />
              <SkillPage name={route.skill} />
              <RemoteSession key={route.remote} id={route.remote} />
              <TabPane show={showChat}>
                <MessageScrollerProvider key={conversation.key} autoScroll scrollEdgeThreshold={48}>
                  <Transcript id={conversation.id} drafts={drafts} scopeError={scopeError} onDraftChange={onDraftChange} onCreated={(id) => { tabs.create(id); setConversation((current) => ({ ...current, created: id })); }} />
                </MessageScrollerProvider>
              </TabPane>
             </main>
             {showChat ? <><DelegationPanel id={route.id} /><SkillPanel /></> : null}
           </div>
          </div>
          </TabActions></SessionCommands></DraftScope>
  );
}

// The palette runs the shortcuts' own actions; the sidebar toggle is listed only while Cmd/Ctrl+B works.
function keyCommands(step: (back: boolean) => void, toggle: () => void, medium: boolean) {
  return [{ key: "tab-previous", label: "Tabs: Previous", run: () => step(true) }, { key: "tab-next", label: "Tabs: Next", run: () => step(false) }, ...medium ? [{ key: "tab-sidebar", label: "Tabs: Toggle sidebar", run: toggle }] : []];
}

// Pick date & time snoozes a tab's notifications until a local date and time, starting at tomorrow 9:00; a time
// already past is refused.
function SnoozeDialog({ path }: { path: string }) {
  const { setSnoozing, mute } = useContext(TabActions), id = useId();
  // The date field reads tomorrow as a local date.
  const [fields, setFields] = useState(() => ({ date: new Date(new Date().setHours(33, 0, 0, 0) - new Date().getTimezoneOffset() * 60_000).toISOString().slice(0, 10), time: "09:00" }));
  const until = new Date(`${fields.date}T${fields.time}`), late = !(until.getTime() > Date.now());
  return <Dialog open onOpenChange={(open) => { if (!open) setSnoozing(undefined); }}>
    <DialogContent>
      <DialogTitle>Snooze notifications</DialogTitle>
      <DialogDescription>This tab sends you no desktop notifications until then.</DialogDescription>
      <form className="mt-4 flex flex-col gap-3" action={() => { mute(path, until.toISOString()); setSnoozing(undefined); }}>
        <FieldGroup className="flex-row">
          {(["date", "time"] as const).map((key) => <Field key={key} data-invalid={late}><FieldLabel htmlFor={`${id}-${key}`}>{key === "date" ? "Date" : "Time"}</FieldLabel>
            <Input id={`${id}-${key}`} type={key} value={fields[key]} aria-invalid={late} onChange={(event) => setFields({ ...fields, [key]: event.target.value })} /></Field>)}
        </FieldGroup>
        {late ? <FieldError>Pick a time in the future.</FieldError> : null}
        <div className="flex justify-end gap-2">
          <DialogClose render={<Button variant="ghost" />}>Cancel</DialogClose>
          <Button type="submit" disabled={late}>Snooze</Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>;
}

function SessionCommandDialog({ command, drafts, onDraftChange }: { command: SessionCommand; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  if (command.mode === "name" || command.mode === "tag") return <NameSessionDialog id={command.source} tag={command.mode === "tag"} />;
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
  const space = useContext(TabActions).state.space;
  const action = useMutation({ mutationFn: async (kind: "copy" | "new" | "stash") => {
    const principal = identity.isSuccess && !identity.isFetching ? identity.data.principal : undefined;
    const { document } = await queryClient.fetchQuery(handoffRequest);
    if (kind === "copy") await copyText(document, popup.current!);
    if (kind === "new") {
      const id = await mutations.createSession({ agent: "main", spaceId: space });
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
  const targetSession = useMemo(() => target && (sidebar.rows.find((row) => row.id === target.conversationId) ?? { id: target.conversationId }), [sidebar.rows, target]);
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
          <div className="min-w-0 flex-1"><SessionRowContent session={targetSession!} /><p className="truncate text-xs text-muted-foreground"><InlineText text={target.message.text.replace(/\s+/g, " ")} /></p></div>
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
  typed: string,
  rows: Session[],
  jobs: { stem: string; status: string; schedule?: string; agent?: string; channel?: string }[] | undefined,
  newChat: () => void,
  tabs: ReturnType<typeof useTabs>,
  openCron: () => void,
  runStem: (stem: string) => void,
  actions: { key: string; label: string; detail?: string; keep?: boolean; disabled?: boolean; run: () => void }[],
  recent: string[],
): { key: string; label?: string; detail?: string; session?: Session; keep?: boolean; disabled?: boolean; run: () => void }[] {
  const needle = typed.toLowerCase(), pages = searchPages(tabs), fresh = () => `/search/${crypto.getRandomValues(new Uint32Array(2)).join("-")}`;
  if (mode === "cron") {
    return (jobs ?? []).filter((job) => job.status !== "ran" && (needle === "" || `${job.stem} ${job.schedule ?? ""} ${job.agent ?? ""} ${job.channel ?? ""}`.toLowerCase().includes(needle))).map((job) => ({
      key: job.stem, label: job.stem, detail: [job.schedule, job.agent, job.channel].filter(Boolean).join(" · "), keep: true, run: () => runStem(job.stem),
    }));
  }
  // Match quality: 0 the whole label, 1 one contiguous run, 2 three or more letters in order with gaps, 3 none; all case-insensitive.
  const tier = (label: string) => {
    const text = label.toLowerCase(), letters = [...needle];
    if (text === needle) return 0;
    if (text.includes(needle)) return 1;
    let at = 0;
    for (const char of text) if (char === letters[at]) at++;
    return letters.length >= 3 && at === letters.length ? 2 : 3;
  };
  // Session search lists commands, which match at once, above the session rows; an empty query or a filter lists sessions only.
  const commands = mode === "sessions" && (needle === "" || new RegExp(`(?:^|\\s)(?:${filterKeys}):\\S`).test(needle)) ? [] : [
    ...actions,
    { key: "new", label: "Sessions: New", run: newChat },
    { key: "search", label: "Sessions: Search", run: () => tabs.open(fresh()) },
    { key: "run-cron", label: "Cron: Run", keep: true, run: openCron },
    ...([["voice", "Voice"], ["cron", "Cron: Dashboard"], ["agents", "List Agents"], ["skills", "List Skills"], ["config", "Settings"], ["pages", "Pages: Index"], ["shortcuts", "Help: Keyboard Shortcuts"]] as const).map(([key, label]) => ({ key, label, run: () => navigate(`/${key}`) })),
    // VS Code: src/vs/platform/quickinput/browser/commandsQuickAccess.ts, _getPicks.
  ].filter((item) => tier(item.label) < 3).sort((a, b) => {
    // Better matches come first. Within a tier, recently run commands lead, except Timeline levels, which always keep the Config slider's order.
    const level = (key: string) => timelineLevels.findIndex(({ id }) => key === `timeline-${id}`);
    const rank = (key: string) => level(key) >= 0 ? Infinity : recent.indexOf(key) + 1 || Infinity;
    return tier(a.label) - tier(b.label) || rank(a.key) - rank(b.key) || (level(a.key) >= 0 && level(b.key) >= 0 ? level(a.key) - level(b.key) : a.label.localeCompare(b.label));
  });
  // Typed text lists every space's Search pages next, matched by title or any of their queries; choosing one enters its
  // space. An empty query lists sessions only, so Enter still opens the first session.
  const searches = pages.filter((tab) => needle && [searchTitle(tab.search), ...tab.search?.searches.map((item) => item.query) ?? []].some((text) => tier(text) < 3))
    .map((tab) => ({ key: `page-${tab.space} ${tab.path}`, label: `Search: ${searchTitle(tab.search)}`, detail: tabs.state.spaces.find((space) => space.id === tab.space)?.name, run: () => tabs.open(tab.path, "", tab.space) }));
  // Each session is one row, as in the session list; message and field hits belong to a Search page, which typed text can
  // open last: the one already searching for exactly that text, else a new one.
  const searchEverywhere = () => {
    const found = pages.find((tab) => tab.search?.searches.some((item) => item.query === typed));
    tabs.open(found?.path ?? fresh(), `?${new URLSearchParams({ q: typed })}`, found?.space);
  };
  return mode === "commands" ? commands : [...commands, ...searches, ...rows.map((session) => ({ key: session.id, session, run: () => tabs.open(rowPath(session)) })),
    ...typed ? [{ key: searchEverywhereKey, label: `Search everywhere for '${typed}'`, run: searchEverywhere }] : []];
}

const searchEverywhereKey = "search-everywhere";

// A person's own Search page whose location is shared in its space shows the Shared one there, so only that one lists.
const searchPages = ({ state, shared }: ReturnType<typeof useTabs>) => [...state.tabs.filter((tab) => isSearch(tab.path) && !isShared(shared, tab.space!, tab.path)), ...shared.filter((tab) => isSearch(tab.path))];

// The server reads and matches the query. While typing, an unfinished last word is left out; Enter sends everything.
// Requests start after a 250 ms pause (and by 1 s with messages), a newer one aborts the older, and they rerun when sidebar chats change.
function useSessionSearch(query: string, scope: "messages" | "filters", entered: boolean) {
  const { rows } = useContext(Sidebar);
  const ids = useMemo(() => rows.map(({ id }) => id).toSorted().join(" "), [rows]);
  const { word, unfinished } = typingWord(query);
  const sent = (entered || !unfinished ? query : query.slice(0, query.length - word.length)).trimEnd();
  const principalChoices = !sent && /^principal:/i.test(word);
  const [result, setResult] = useState<{ query: string; response?: SearchSessionsResponse; error?: string }>({ query: "" });
  const request = useRef<{ key: string; version: number; failed?: boolean; controller?: AbortController; pending?: Promise<SearchSessionsResponse | undefined> }>({ key: "", version: 0 });
  const pause = useRef<ReturnType<typeof setTimeout>>(undefined);
  const deadline = useRef<ReturnType<typeof setTimeout>>(undefined);
  const firstEdit = useRef(0);
  const run = useCallback((text: string) => {
    clearTimeout(pause.current);
    clearTimeout(deadline.current);
    firstEdit.current = 0;
    const key = `${text}\n${ids}\n${principalChoices}`;
    if (request.current.key === key && request.current.pending) return request.current.pending;
    request.current.controller?.abort();
    const controller = new AbortController(), version = request.current.version + 1;
    const current = () => request.current.version === version;
    const pending = text || principalChoices ? rpc<SearchSessionsResponse>("SearchSessions", { query: text, [scope]: true }, controller.signal).then((response) => {
      if (!current()) return undefined;
      setResult({ query: text, response });
      return response;
    }, (error: Error) => {
      if (current()) { request.current.failed = true; setResult({ query: text, error: error.message }); }
      return undefined;
    }).finally(() => { if (current()) request.current.pending = undefined; }) : undefined;
    request.current = { key, version, controller, pending };
    if (!text && !principalChoices) setResult((previous) => previous.query === "" ? previous : { query: "" });
    return pending ?? Promise.resolve(undefined);
  }, [ids, scope, principalChoices]);
  useEffect(() => {
    // A failed search retries on the next keystroke.
    if (request.current.key === `${sent}\n${ids}\n${principalChoices}` && !request.current.failed) return;
    if (!firstEdit.current) firstEdit.current = Date.now();
    pause.current = setTimeout(() => void run(sent), sent ? 250 : 0);
    if (scope === "messages") deadline.current = setTimeout(() => void run(sent), Math.max(0, 1000 - (Date.now() - firstEdit.current)));
    return () => { clearTimeout(pause.current); clearTimeout(deadline.current); };
  }, [query, sent, ids, scope, principalChoices, run]);
  useEffect(() => () => { request.current.controller?.abort(); request.current.version++; }, []);
  const pending = !!sent && result.query !== sent;
  const { found, missing } = useMemo(() => matchedRows(result.response, rows), [result.response, rows]);
  return {
    word, sent, result, found, missing, run, pending,
    error: sent && result.query === sent ? result.error : undefined,
    // Before any response for typed text the rows are the sidebar's, in its order.
    rows: sent && result.query ? found.map(({ session }) => session) : rows,
    // The response the rows come from, if any.
    response: sent && result.query ? result.response : undefined,
    // enter acts on the shown rows when they are the response for the whole query, never on a dimmed older list;
    // otherwise it sends the query at once, or waits for it in flight, and hands open that response's rows.
    enter: (shown: () => void, open: (sessions: Session[]) => void) => {
      const text = query.trimEnd();
      if (!text || text === sent && !pending) return shown();
      void run(text).then((response) => open(matchedRows(response, rows).found.map(({ session }) => session)));
    },
  };
}

// Server matches in server order with their sidebar rows; missing reports a match the sidebar has not loaded yet.
function matchedRows(response: SearchSessionsResponse | undefined, rows: Session[]) {
  const byId = new Map(rows.map((row) => [row.id, row]));
  const matches = response?.matches ?? [];
  const found = matches.flatMap((match) => byId.has(match.conversationId) ? [{ match, session: byId.get(match.conversationId)! }] : []);
  return { found, missing: found.length < matches.length };
}

// Text matches shown titles here, as rows show them: markdown and Slack tags read as their text. Plain text
// matches at once; a filter asks the server, whose passing rows, matched the same way, replace these once they arrive.
function usePaletteSessions(query: string, sessions: boolean, entered: boolean) {
  const { rows } = useContext(Sidebar);
  const free = query.replace(new RegExp(`(?:^|\\s)(?:${filterKeys}):(?:"(?:[^"\\\\]|\\\\.)*"?|\\S*)`, "gi"), " ").trim().toLowerCase();
  const filtered = sessions && free !== query.trim().toLowerCase();
  const search = useSessionSearch(filtered ? query : "", "filters", entered);
  const raw = useMemo(() => sessions ? rows.map((row) => sessionTitle(row)) : [], [rows, sessions]);
  const names = useSlackNames(useMemo(() => [...new Set(raw.flatMap(mentionIds))], [raw]));
  // Titles are cleaned and lowered once per list or name change, so a keystroke only scans them.
  const titles = useMemo(() => raw.map((title) => cleanText(title, names).toLowerCase()), [raw, names]);
  const byId = useMemo(() => new Map(rows.map((row, i) => [row.id, titles[i] ?? ""])), [rows, titles]);
  const matches = useCallback((row: Session) => !free || !!byId.get(row.id)?.includes(free), [byId, free]);
  const titled = useMemo(() => sessions ? rows.filter(matches) : rows, [rows, sessions, matches]);
  const passed = useMemo(() => search.rows.filter(matches), [search.rows, matches]);
  // A failed filtered search lists nothing rather than title matches that never passed its filters.
  return { search, filtered, free, matches, rows: filtered && (search.response || search.error) ? passed : titled };
}

// The keys of filter terms; the server reads the same keys.
const filterKeys = "tag|cron|agent|room|principal|server|model|is|sort";

const paletteCopy = { sessions: { title: "Go to session or command", desc: "Search and open a session, or run a command.", placeholder: "Search sessions and commands" }, commands: { title: "Run command", desc: "Search and run a command.", placeholder: "Type a command" },
  cron: { title: "Run cron", desc: "Search and run a cron job.", placeholder: "Search cron jobs" } };

// "Sessions: New on <server>" turns the palette into a folder picker for a remote OpenCode
// session: a typed folder, or one of that server's sessions' folders, those the person picked most recently first,
// since agents also start sessions there.
function useRemoteCreation(query: string, close: () => void, restart: () => void, copy: { title: string; desc: string; placeholder: string }, recent: string[]) {
  const sidebar = useContext(Sidebar);
  const tabs = useContext(TabActions);
  const [server, setServer] = useState<string>();
  const create = useMutation({ mutationFn: mutations.createSession, onSuccess: (created) => { sidebar.invalidateQueries(); close(); tabs.open(rowPath({ id: created, kind: "REMOTE_OPENCODE" })); } });
  const typed = query.trim();
  const rank = (directory: string) => recent.indexOf(`folder-${directory}`) + 1 || Infinity;
  const known = sidebar.rows.flatMap((row) => row.server === server && row.directory ? [row.directory] : []).toSorted((a, b) => rank(a) - rank(b) || 0);
  const items: ReturnType<typeof paletteRows> = !server ? [] : [...new Set([typed, ...known])]
    .filter((directory) => directory.startsWith("/") && directory.toLowerCase().includes(typed.toLowerCase()))
    .map((directory) => ({ key: `folder-${directory}`, label: directory, keep: true, disabled: create.isPending, run: () => create.mutate({ opencodeServer: server, directory, spaceId: tabs.state.space }) }));
  return {
    pick: (palette: () => typeof items) => server ? items : palette(),
    commands: Object.keys(sidebar.remote?.servers ?? {}).toSorted().map((name) => ({ key: `opencode-${name}`, label: `Sessions: New on ${name}`, keep: true, run: () => { restart(); setServer(name); } })),
    copy: server ? { title: `New session on ${server}`, desc: "Type or pick a folder for the new OpenCode session.", placeholder: `Folder on ${server}, like /srv/app` } : copy,
    empty: server && `No known folders on ${server} yet; type one`,
    alert: create.error ? <p role="alert" className="px-3 text-sm text-destructive">{create.error.message}</p> : null,
  };
}

// Paging bounds initial/edit work; deep browsing or wrapping to the last row still mounts it all. Window rows if that becomes costly.
function usePalettePage<T>(items: T[], selected: number, key: string) {
  const [budget, setBudget] = useState({ key, rows: 50 });
  if (budget.key !== key) setBudget({ key, rows: 50 });
  const drawn = Math.max(budget.key === key ? budget.rows : 50, selected + 1);
  return {
    shown: items.slice(0, drawn),
    next: drawn < items.length ? <li aria-hidden ref={(node) => {
      const observer = new IntersectionObserver(([entry]) => {
        if (entry.isIntersecting) setBudget({ key, rows: drawn + 50 });
      }, { root: node!.parentElement, rootMargin: "0px 0px 100% 0px" });
      observer.observe(node!);
      return () => observer.disconnect();
    }} /> : null,
  };
}

function CommandPalette({ drafts, mode, setMode, newChat, keyCommands }: { drafts: Map<string, ComposerDraft>; mode: "sessions" | "commands" | "cron" | undefined; setMode: (mode: "sessions" | "commands" | "cron" | undefined) => void; newChat: () => void; keyCommands: { key: string; label: string; run: () => void }[] }) {
  const sidebar = useContext(Sidebar);
  const { setCommand, composer } = useContext(SessionCommands);
  // Arrowing onto a session shows it in the preview tab. The palette remounts per opening and keeps acting on the
  // location it opened at, returning there when dismissed and before running a choice. Typing, or pressing a
  // command, returns there first, so commands render, and run, with that location's state.
  const at = () => location.pathname + location.search + location.hash;
  const [origin] = useState(() => ({ path: location.pathname, href: at() }));
  const id = pathSession(origin.path);
  const tabs = useContext(TabActions);
  const back = () => { if (at() !== origin.href) flushSync(() => navigate(origin.href, true)); };
  // Only a session that opens in this space is shown, so browsing never switches spaces.
  const peek = (session?: Session) => {
    const path = session ? rowPath(session) : "", tab = tabs.state.tabs.find((item) => item.path === path);
    const here = session && (tab ? tab.space === tabs.state.space : !session.spaceId || session.spaceId === tabs.state.space);
    const target = here ? path : origin.href;
    if (target !== at()) navigate(target, true);
  };
  const tabCommands = tabs.commands(origin.path, sidebar.rows).map((item) => ({ ...item, key: `tab-${item.key}`, label: `Tabs: ${item.label}` }));
  const actions = useSessionActions(sidebar.rows.find((row) => row.id === id), () => setMode(undefined));
  const choices = useQuery({ ...queries.agents({ conversationId: id }), enabled: id !== "" });
  const draft = drafts.get(id);
  const commands = id ? dollarCommands.filter(({ name }) => name !== "cron" && name !== "queue" && (name !== "undo" || draft?.revertEligible && (draft.canUndo || !draft.revertMessageId && !!unsavedRequest(draft.lines))) && (name !== "redo" || draft?.revertEligible && draft.revertMessageId) && (name !== "stop" || (draft?.busy ?? sidebar.rows.find((row) => row.id === id)?.running)) && (name !== "agent" || !!choices.data?.agents.length)).map(({ name, label }) => ({ key: name, label: ["fork", "handoff", "stop", "agent"].includes(name) ? `Sessions: ${label}` : `Command: ${label} ($${name})`, disabled: [!draft?.hydrated, draft?.sending, draft?.reverting].some(Boolean), run: () => {
    if (name === "fork" || name === "handoff") setCommand({ mode: name, source: id });
    else composer.current!(name);
  } })) : [];
  const level = timelineLevel(useTimelineDetail());
  // The same levels and descriptions as the Config slider; the active one is marked.
  const timeline = timelineLevels.map(({ id: key, label, description, rows }) => ({ key: `timeline-${key}`, label: `Timeline: ${label}`, detail: key === level?.id ? `Current · ${description}` : description, run: () => setTimelineRows(rows) }));
  const markdown = usePreference(OUTPUT_KEY) !== "raw";
  const outputs = ([["raw", "Raw", "Show messages with Slack formatting."], ["markdown", "Markdown", "Render messages as Markdown, including headings and tables."]] as const)
    .map(([key, label, description]) => ({ key: `output-${key}`, label: `Output: ${label}`, detail: (key === "markdown") === markdown ? `Current · ${description}` : description, run: () => setPreference(OUTPUT_KEY, key) }));
  const [query, setQuery] = useState("");
  const [recent, setRecent] = useState<string[]>(() => JSON.parse(localStorage.getItem("command-history") ?? "[]"));
  const [pick, setPick] = useState(0);
  const [entered, setEntered] = useState(false);
  // Whether the arrows moved the highlight since the last edit.
  const moved = useRef(false);
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
  // Leaving session search for cron or folders swaps the input, so each mode focuses its own.
  useLayoutEffect(() => { if (mode !== undefined) input.current?.focus(); }, [mode]);
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000, enabled: mode === "sessions" });
  const active = useRef<HTMLButtonElement>(null);
  const { search, filtered, free, matches, rows } = usePaletteSessions(query, mode === "sessions", entered);
  const jobs = useQuery({ ...queries.cronJobs(), staleTime: 10_000, enabled: mode === "commands" || mode === "cron" });
  const runCron = useMutation({
    mutationFn: mutations.runCron,
    onSuccess: (id, { stem }) => {
      void queryClient.invalidateQueries({ queryKey: ["cronJobs"] });
      sidebar.invalidateQueries();
      if (id === "") return;
      firstMessages.set(id, stem);
      navigate(sessionPath(id));
      setMode(undefined);
    },
  });
  // The folder picker leaves session search, so typed folders are never sent as a search.
  const remote = useRemoteCreation(query, () => setMode(undefined), () => { setQuery(""); setPick(0); setMode("commands"); }, paletteCopy[mode ?? "sessions"], recent);
  const inSpace = useInSpace();
  const items = mode === undefined ? [] : remote.pick(() => paletteRows(mode, query.trim(), rows, jobs.data?.filter(inSpace), newChat, tabs, () => { setQuery(""); setPick(0); setMode("cron"); }, (stem) => runCron.mutate({ stem }), [...actions.items.map((item) => ({ ...item, label: `Sessions: ${item.label}` })), ...remote.commands, ...commands, ...tabCommands, ...keyCommands, ...timeline, ...outputs], recent));
  const selected = items.length === 0 ? 0 : pick % items.length;
  // Draw more rows only as scrolling or the keyboard needs them; every choice stays reachable.
  const { shown, next } = usePalettePage(items, selected, `${mode}:${query}`);
  const choose = (item: (typeof items)[number]) => {
    if (item.disabled) return;
    if (mode !== "cron" && !item.session) {
      const next = [item.key, ...recent.filter((key) => key !== item.key)].slice(0, 50);
      localStorage.setItem("command-history", JSON.stringify(next));
      setRecent(next);
      setPick(0);
    }
    if (!item.keep) setMode(undefined);
    back();
    item.run();
  };
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [selected, mode]);
  const copy = remote.copy;
  const onKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    const next = paletteMove(event, selected, items.length, () => {
      const item = items[selected];
      // In session search, Enter runs a command containing the typed text or one picked with the arrows; a fuzzy one on top yields to the sessions, then to Search for plain text.
      if (item && !item.session && (mode !== "sessions" || moved.current || item.key !== searchEverywhereKey && item.label!.toLowerCase().includes(query.trim().toLowerCase()))) return choose(item);
      setEntered(true);
      search.enter(() => { const row = item?.session ? item : items.find(({ session }) => session) ?? items.find(({ key }) => !filtered && key === searchEverywhereKey); if (row) choose(row); }, (sessions) => { const session = sessions.find(matches); if (session) choose({ key: session.id, session, run: () => tabs.open(rowPath(session)) }); });
    });
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      moved.current = true;
      peek(items[next % items.length]?.session);
    }
    if (event.key !== "Enter") setPick(next);
  };
  return (
    <Dialog open={mode !== undefined} onOpenChange={(open) => { if (!open) { setMode(undefined); back(); } }}>
      <DialogContent initialFocus={input} showCloseButton={false} className="top-[20%] flex max-h-[75vh] translate-y-0 flex-col overflow-hidden sm:max-w-lg [@media(pointer:coarse)]:top-[var(--search-top,1rem)] [@media(pointer:coarse)]:max-h-[var(--search-height,75vh)]">
        <DialogTitle className="sr-only">{copy.title}</DialogTitle>
        <DialogDescription className="sr-only">{copy.desc}</DialogDescription>
        {mode === "sessions" ? <SessionSearch rows={sidebar.rows} catalog={agents.data?.agents ?? []} query={query} setQuery={(update) => { setPick(0); setEntered(false); moved.current = false; setQuery(update); peek(); }} search={search} inputRef={input} placeholder={copy.placeholder} onKeyDown={onKeyDown} /> : <Input
          ref={input}
          value={query}
          onChange={(event) => { setPick(0); setQuery(event.target.value); }}
          placeholder={copy.placeholder}
          variant="embedded"
          onKeyDown={onKeyDown}
        />}
        {runCron.error || actions.error ? <p role="alert" className="px-3 text-sm text-destructive">{(runCron.error ?? actions.error)?.message}</p> : null}
        {remote.alert}
        {mode === "sessions" ? <div className="px-3 text-sm empty:hidden"><SearchStatus search={search} /></div> : null}
        <ul key={`${mode}:${query}`} data-pending={search.pending} className="max-h-[min(24rem,50vh)] overflow-y-auto p-1 [scrollbar-width:thin] [scrollbar-color:var(--muted-foreground)_transparent]">
          {shown.every(({ key }) => key === searchEverywhereKey) ? <li className="px-3 py-2 text-sm text-muted-foreground">{paletteEmpty(mode, sidebar, search, jobs.isLoading, remote.empty)}</li> : null}
          {shown.map((item, index) => (
            <li key={item.key}>
              <button disabled={item.disabled} ref={index === selected ? active : null} type="button" className={cn("flex w-full flex-col items-start justify-center rounded-md px-3 text-left text-sm disabled:opacity-50", item.session ? "py-2" : "min-h-9 py-1.5 [@media(pointer:coarse)]:min-h-11", item.session && search.pending && "opacity-50", index === selected && "bg-accent")} onMouseDown={(event) => { event.preventDefault(); if (!item.session) peek(); }} onClick={() => choose(item)}>
                {item.session ? <SessionRowContent session={item.session} needle={free} /> : <PaletteCommandContent item={item} mode={mode} />}
              </button>
            </li>
          ))}
          {next}
        </ul>
      </DialogContent>
    </Dialog>
  );
}

function PaletteCommandContent({ item, mode }: { item: ReturnType<typeof paletteRows>[number]; mode: "sessions" | "commands" | "cron" | undefined }) {
  return <>
    <span className="flex w-full flex-wrap items-center justify-between gap-x-3 gap-y-1"><span className="font-medium">{item.label}</span>{mode !== "cron" ? <Keys id={item.key} hidden /> : null}</span>
    {item.detail ? <span className="text-xs text-muted-foreground">{item.detail}</span> : null}
  </>;
}

// Every keyboard shortcut. A palette command shows the one whose id is its key, so ids of keys with no command never match one; the Shortcuts page lists them all.
const shortcuts = [
  { id: "open-palette", label: "Search sessions and commands", keys: ["mod", "P"] },
  { id: "open-commands", label: "Run a command", keys: ["mod", "Shift", "P"] },
  { id: "new", label: "New session", keys: ["mod", "Alt", "N"] },
  { id: "tab-previous", label: "Previous tab", keys: ["mod", "["] },
  { id: "tab-next", label: "Next tab", keys: ["mod", "]"] },
  { id: "tab-sidebar", label: "Show or hide the tab sidebar", where: "Tabs in the left sidebar, on a wide window", keys: ["mod", "B"] },
  { id: "page-close", label: "Close the tab", where: "Pages other than sessions and Search pages", keys: ["Escape"] },
  { id: "message-steer", label: "Steer the running turn", where: "Message box, while a turn runs", keys: ["mod", "Enter"] },
  { id: "message-stash", label: "Stash the message", where: "Message box", keys: ["mod", "Alt", "Enter"] },
];

function Keys({ id, hidden }: { id: string; hidden?: boolean }) {
  const mac = /(Mac|iPod|iPhone|iPad)/.test(navigator.platform);
  const keys = shortcuts.find((shortcut) => shortcut.id === id)?.keys.map((key) => key === "mod" ? mac ? "⌘" : "Ctrl" : key === "Alt" && mac ? "Option" : key);
  return keys ? <span aria-hidden={hidden} className="inline-flex shrink-0 items-center gap-0.5">{keys.map((key) => <kbd key={key} className="inline-flex h-3.5 min-w-3.5 items-center justify-center rounded-[2px] bg-muted px-1 text-[10px] font-medium text-muted-foreground">{key}</kbd>)}</span> : null;
}

function ShortcutsPage({ show }: { show: boolean }) {
  if (!show) return null;
  return <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
    <PageTitle>Keyboard shortcuts</PageTitle>
    <ul aria-label="Keyboard shortcuts" className="flex flex-col divide-y text-sm">
      {shortcuts.map(({ id, label, where }) => <li key={id} className="flex items-center justify-between gap-4 py-2"><span>{label}{where ? <span className="block text-xs text-muted-foreground">{where}</span> : null}</span><Keys id={id} /></li>)}
    </ul>
  </div>;
}

function paletteEmpty(mode: "sessions" | "commands" | "cron" | undefined, sidebar: SidebarView, search: ReturnType<typeof useSessionSearch>, loadingJobs: boolean, folders?: string) {
  if (folders) return folders;
  if (mode === "sessions" && search.error) return "Search incomplete";
  if (mode === "sessions" && (!searchIsAuthoritative(sidebar) || search.pending || search.missing)) return "loading...";
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

// One formatter for every row: toLocaleString with options builds a new one per call. Same output.
const updatedFormat = new Intl.DateTimeFormat(undefined, { year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", second: "numeric", timeZoneName: "short" });

// Memoized so typing in the palette does not redraw thousands of unchanged rows.
const SessionRowContent = memo(function SessionRowContent({ session, age = relativeTime(session.updatedAt ?? ""), needle }: { session: Session; age?: string; needle?: string }) {
  const title = sessionTitle(session);
  const clean = useCleanText(title);
  // A match past the title's start shows from a little before it, so a long title's "…" never hides it.
  const at = needle ? clean.toLowerCase().indexOf(needle) : -1;
  const shown = at > 24 ? "…" + clean.slice(clean.lastIndexOf(" ", at - 16) + 1) : title;
  const channel = slackSession(session) ? (session.title ?? "") : "";
  const meta = [channel, session.server, session.offline ? "offline" : "", session.agent, ...(session.tags ?? [])].filter(Boolean).join(" · ");
  const updated = session.updatedAt ? `Updated ${updatedFormat.format(new Date(session.updatedAt))}` : "";
  return <span className={cn("flex min-w-0 w-full flex-1 flex-col gap-0.5", session.offline && "opacity-60")}>
    <span className="flex items-center gap-1.5 text-sm font-medium">{session.forkedFrom ? <GitFork role="img" aria-label="Forked session" className="size-3.5 shrink-0" /> : null}<span data-slot="session-title" className="truncate" title={clean}><InlineText text={shown} needle={needle || undefined} /></span></span>
    <span className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"><span className="min-w-0 flex-1 truncate" title={meta}>{meta}</span>{session.running ? <LoaderCircle role="img" aria-label="Turn running" className="size-3 shrink-0 animate-spin motion-reduce:animate-none" /> : null}{age ? <Tooltip><TooltipTrigger render={<time dateTime={session.updatedAt} />} aria-label={updated} className="shrink-0"><span className="tabular-nums">{age}</span></TooltipTrigger><TooltipContent>{updated}</TooltipContent></Tooltip> : null}</span>
  </span>;
});

const remoteView = { route: "", shown: "", openPalette: () => {} };

// A remote session's link on its own server, for the session the open view shows, and that view's palette.
// The link is OpenCode's own session route; the server asks whoever opens it to sign in.
function remoteActions(id: string, origin: string, remoteSessionId: string) {
  const open = remoteView.route === id;
  const shown = open ? remoteView.shown : remoteSessionId;
  return [
    { key: "server-link", label: "Copy server link", run: () => void copyText(`${origin}/server/${encodeSessionId(origin)}/session/${shown}`, document.body) },
    ...(open ? [{ key: "opencode-commands", label: "OpenCode commands", run: () => remoteView.openPalette() }] : []),
  ];
}

// The copied OpenCode view's entry (internal/rocketclaw/oc/host/entry.ts), loaded at run time so the Web never
// builds its Solid code.
const ocEntry = "/oc-ui/_assets/index.js";
type OcModule = { mount(element: HTMLElement, options: MountOptions): Mounted };

// A remote OpenCode session mounts the copied OpenCode view, which reaches its server through the Web's
// /oc/<server>/api/ proxy. Any session of a configured server opens, listed yet or not; a view already open
// stays while the server blips and shows that state itself.
function RemoteSession({ id }: { id: string }) {
  const sidebar = useContext(Sidebar);
  const tabs = useContext(TabActions);
  const container = useRef<HTMLDivElement>(null);
  const [, server = "", ...rest] = id.split(":");
  const sessionID = rest.join(":");
  const configured = !!sidebar.remote?.servers[server];
  const offline = sidebar.rows.find((row) => row.id === id)?.offline;
  const [reported, setReported] = useState(false);
  const [failed, setFailed] = useState("");
  // Each retry loads the entry from a fresh URL, as browsers may keep a failed module load.
  const [retries, setRetries] = useState(0);
  const create = useMutation({ mutationFn: mutations.createSession, onSuccess: (created) => { sidebar.invalidateQueries(); tabs.open(rowPath({ id: created, kind: "REMOTE_OPENCODE" })); } });
  const ready = configured && (!offline || reported);
  const openSession = useEffectEvent((other: string) => tabs.open(rowPath({ id: `opencode:${server}:${other}`, kind: "REMOTE_OPENCODE" })));
  const forkSession = useEffectEvent(async (other: string, before: string) => {
    const forked = await mutations.forkSession({ id: `opencode:${server}:${other}`, before });
    sidebar.invalidateQueries();
    return forked.id.slice(`opencode:${server}:`.length);
  });
  // A press while a create runs is the same request; once it settles, success or failure, a press starts another.
  const creating = useRef(false);
  const newSession = useEffectEvent((directory: string) => {
    if (creating.current) return;
    creating.current = true;
    create.mutate({ opencodeServer: server, directory, spaceId: tabs.state.space }, { onSettled: () => { creating.current = false; } });
  });
  const closeTab = useEffectEvent(() => tabs.close([rowPath({ id, kind: "REMOTE_OPENCODE" })]));
  const reportSession = useEffectEvent((other: string) => { remoteView.shown = other; setReported(true); });
  useEffect(() => {
    if (!ready) return;
    let view: Mounted | undefined, gone = false;
    void (import(/* @vite-ignore */ retries ? `${ocEntry}?retry=${retries}` : ocEntry) as Promise<OcModule>).then(({ mount }) => {
      if (gone) return;
      view = mount(container.current!, { server, url: `${location.origin}/oc/${server}`, sessionID, navigation: { openSession, newSession, forkSession, closeTab, reportSession } });
      Object.assign(remoteView, { route: id, shown: sessionID, openPalette: view.openPalette });
    }, (error: Error) => setFailed(error.message));
    return () => {
      gone = true;
      view?.dispose();
      Object.assign(remoteView, { route: "", shown: "", openPalette: () => {} });
    };
  }, [ready, id, server, sessionID, retries]);
  if (!id) return null;
  if (!configured) return <p role="status" className="m-auto p-4 text-sm text-muted-foreground">{sidebar.enumerationComplete ? "This OpenCode session is no longer available." : "Loading…"}</p>;
  if (!ready) return <p role="status" className="m-auto p-4 text-sm text-muted-foreground">{server} is offline; this session reopens when it is reachable.</p>;
  return <>
    {reported || failed ? null : <p role="status" className="p-4 text-sm text-muted-foreground">Loading…</p>}
    {failed || create.error ? <p role="alert" className="px-3 text-sm text-destructive">{failed || create.error?.message}{failed ? <> <button type="button" className="underline" onClick={() => { setFailed(""); setRetries((count) => count + 1); }}>Retry</button></> : null}</p> : null}
    <div ref={container} className="flex min-h-0 w-full flex-1 flex-col" />
  </>;
}

// A hosted page runs in an opaque origin and reads its agent's Page Data by asking this tab. The answer goes only
// to this frame and only with this load's token, which a document the frame navigates to never sees.
function HostedPage({ page }: { page: string }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [token] = useState(() => crypto.getRandomValues(new Uint32Array(4)).join("-"));
  const agent = decodeURIComponent(page.split("/")[0]);
  useEffect(() => {
    const read = async ({ source, data }: MessageEvent) => {
      if (source !== frame.current?.contentWindow || data?.type !== "rocketclaw.read" || data.token !== token) return;
      const reply = await rpc<{ body: string }>("GetPage", { agent, path: data.key, data: true }).then(
        ({ body }) => ({ value: JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(body), (char) => char.charCodeAt(0)))) }),
        (error: Error) => ({ error: error.message }));
      (source as Window).postMessage({ type: "rocketclaw.read", id: data.id, ...reply }, "*");
    };
    addEventListener("message", read);
    return () => removeEventListener("message", read);
  }, [agent, token]);
  if (!page) return null;
  return <iframe ref={frame} title={pageLabel(page)} sandbox="allow-scripts allow-popups allow-forms allow-modals allow-downloads" className="min-h-0 w-full flex-1 border-0" src={`/pages-raw/${page}#rc=${token}`} />;
}

// The current space's pages and spaceless agents' pages, which the server lists by agent and path. Each links
// under this space's name, which pageSpace gives a "-" link.
function PageIndex({ show }: { show: boolean }) {
  const pages = useQuery({ ...queries.pages(), staleTime: 10_000, enabled: show });
  const inSpace = useInSpace();
  const { state } = useContext(TabActions);
  const rows = (pages.data ?? []).filter(inSpace);
  if (!show) return null;
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Pages</PageTitle>
      {pages.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {pages.error ? <p className="text-sm text-destructive">{pages.error.message}</p> : null}
      {pages.isSuccess && rows.length === 0 ? <p className="text-sm text-muted-foreground">No pages in this space</p> : null}
      <ul className="flex flex-col gap-1">
        {rows.map((page) => <li key={`${page.agent}/${page.path}`}>
          <Link href={pageSpace(state, `/pages/-/${encodeURIComponent(page.agent)}/${page.path.split("/").map(encodeURIComponent).join("/")}`)[0]} className="block break-words py-1 text-sm hover:underline">{page.agent} · {page.path}</Link>
        </li>)}
      </ul>
    </div>
  );
}

function useSessionActions(session: Session | undefined, onSuccess?: () => void) {
  const invalidate = useContext(SidebarInvalidation);
  const remote = useContext(Sidebar).remote;
  const agentTags = remote?.agentTags ?? {};
  const { setCommand } = useContext(SessionCommands);
  const saved = () => { invalidate(); onSuccess?.(); };
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: saved });
  const id = session?.id ?? "";
  const tags = new Set(session?.tags);
  // People tag remote OpenCode sessions freely and RocketClaw sessions only as their agent allows.
  const choices = session?.server ? [...tags] : agentTags[session?.agent ?? ""] ?? [];
  const items = session ? [
    ...(session.forkedFrom ? [{ key: "origin", label: "Open original conversation", run: () => navigate(sessionPath(session.forkedFrom!)) }] : []),
    { key: "name", label: "Name session", run: () => setCommand({ mode: "name", source: id }) },
    { key: "pin", label: session.pinned ? "Unpin session" : "Pin session", keep: true, run: () => update.mutate({ id, pinned: !session.pinned }) },
    ...choices.map((tag) => ({ key: `tag-${tag}`, label: `${tags.has(tag) ? "Remove" : "Add"} tag ${tag}`, keep: true, run: () => update.mutate({ id, tag }) })),
    ...(session.server ? [{ key: "add-tag", label: "Add tag…", run: () => setCommand({ mode: "tag", source: id }) }, ...remoteActions(id, remote?.servers[session.server] ?? "", session.remoteSessionId ?? "")] : []),
  ].map((item) => ({ ...item, disabled: update.isPending })) : [];
  return { items, error: update.error };
}

// With tag, it adds a typed tag to a remote OpenCode session, suggesting tags already in use.
function NameSessionDialog({ id, tag = false }: { id: string; tag?: boolean }) {
  const sidebar = useContext(Sidebar);
  const session = sidebar.rows.find((row) => row.id === id);
  const { setCommand } = useContext(SessionCommands);
  const [value, setValue] = useState(tag ? "" : session?.name ?? "");
  const nameId = useId();
  const listId = useId();
  const used = [...new Set(sidebar.rows.flatMap((row) => row.tags ?? []))].toSorted();
  const update = useMutation({ mutationFn: mutations.updateSession, onSuccess: () => { sidebar.invalidateQueries(); setCommand(undefined); } });
  // The tag RPC toggles, so a tag the session already has is left alone.
  const save = () => tag ? (session?.tags?.includes(value.trim()) ? setCommand(undefined) : update.mutate({ id, tag: value })) : update.mutate({ id, name: value });
  return <Dialog open onOpenChange={(open) => { if (!open) setCommand(undefined); }}>
        <DialogContent>
          <DialogTitle>{tag ? "Add tag" : "Name session"}</DialogTitle>
          <DialogDescription>{tag ? "Pick a tag already in use or type a new one. Search tag:<name> to find tagged sessions." : session?.server ? "Shared with everyone who can see this session. Leave blank to clear the OpenCode title too." : "Shared with everyone who can see this session. Leave blank to show the last message."}</DialogDescription>
          <form className="mt-4 flex flex-col gap-3" action={save}>
            <FieldGroup>
              <Field data-invalid={!!update.error}>
                <FieldLabel htmlFor={nameId}>{tag ? "Tag" : "Session name"}</FieldLabel>
                <Input id={nameId} name="name" type="text" list={tag ? listId : undefined} value={value} aria-invalid={!!update.error} onChange={(event) => setValue(event.target.value)} />
                {tag ? <datalist id={listId}>{used.map((name) => <option key={name} value={name} />)}</datalist> : null}
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

type SavedSearch = SearchPageState["searches"][number];

type LegacySearch = SavedSearch & { agentFilter?: string; roomFilter?: string };

// mine, made from base, made again onto theirs: their searches with mine's changes, those mine added, and none mine
// closed; mine's name and shown search when it changed them.
function rebase(base: SearchPageState | undefined, mine: SearchPageState, theirs: SearchPageState): SearchPageState {
  const find = (page: SearchPageState | undefined, id: string) => page?.searches.find((item) => item.id === id);
  const searches = [
    ...theirs.searches.flatMap((item) => {
      const own = find(mine, item.id), was = find(base, item.id);
      return !own ? (was ? [] : [item]) : comparablePage([own]) === comparablePage(was && [was]) ? [item] : [own];
    }),
    ...mine.searches.filter((item) => !find(theirs, item.id) && !find(base, item.id)),
  ];
  const active = mine.active !== base?.active ? mine.active : theirs.active;
  return { name: mine.name !== base?.name ? mine.name : theirs.name, searches, active: searches.some((item) => item.id === active) ? active : searches[0]?.id ?? "" };
}

const comparablePage = (searches: SavedSearch[] = [], active = "") => JSON.stringify([searches.map(({ id, name, query }) => [id, name ?? "", query]), active]);

// Legacy agent and room filters fold into the query once, in one canonical form.
function foldFilters(query: string, agent?: string | null, room?: string | null) {
  return [agent ? `agent:${termValue(agent)}` : "", room ? `room:${termValue(room)}` : "", query].filter(Boolean).join(" ");
}
type SavedSearches = Pick<SearchPageState, "searches" | "active">;

// withQuery activates a saved search for query, the active one first, or adds one.
function withQuery({ searches, active }: SavedSearches, query: string): SavedSearches {
  const match = searches.toSorted((a, b) => Number(b.id === active) - Number(a.id === active)).find((item) => item.query === query);
  const id = match?.id ?? crypto.getRandomValues(new Uint32Array(4)).join("-");
  return { searches: match ? searches : [...searches, { id, query }], active: id };
}

// A Search page waits for this person's saved tabs, which hold its searches. A page shared in this space shows the one
// page everyone there edits. A link's query opens once, in the page it led to; a page opened again in place, as when it
// is shared, shows what it holds.
function SearchPage() {
  const { state, shared } = useContext(TabActions), path = usePathname(), inShared = sharedTab(shared, state.space, path);
  const [pendingLink, setPendingLink] = useState({ path, search: location.search });
  if (pendingLink.path !== path) setPendingLink({ path, search: location.search });
  if (state.owner === undefined) return null;
  return <SearchTabs key={`${state.owner} ${path} ${!!inShared}`} path={path} owner={state.owner} inShared={!!inShared} page={(inShared ?? state.tabs.find((tab) => tab.path === path))?.search} link={pendingLink.search}
    consumeLink={() => setPendingLink((current) => current.search ? { path, search: "" } : current)} />;
}

// A page's searches are saved with its tab: typing after a pause, as each tab change redraws the app, anything else and
// leaving the page at once. Searches this browser kept before Search pages were tabs move into the first page without any.
function SearchTabs({ path, owner, inShared, page, link, consumeLink }: { path: string; owner: string; inShared: boolean; page?: SearchPageState; link: string; consumeLink: () => void }) {
  // Only the person's own page takes this browser's old searches; a Shared one would hand them to everyone.
  const legacy = `search-tabs:${owner}`, tabs = useContext(TabActions), owned = !inShared && tabs.state.tabs.some((tab) => tab.path === path);
  // Only the page that took them in deletes them, once its tab holds them.
  const [imported] = useState(() => !page?.searches.length && !inShared && localStorage.getItem(legacy) !== null);
  const [rename, setRename] = useState<string | null>(null);
  const [saved, setSaved] = useState<SavedSearches>(() => {
    const stored = (page?.searches.length || inShared ? { tabs: page?.searches ?? [], active: page?.active ?? "" } : JSON.parse(localStorage.getItem(legacy) ?? '{"tabs":[]}')) as { tabs: LegacySearch[]; active: string };
    const searches = stored.tabs.map(({ agentFilter, roomFilter, ...tab }) => ({ ...tab, query: foldFilters(tab.query, agentFilter, roomFilter) }));
    const params = new URLSearchParams(link);
    const query = foldFilters(params.get("q") ?? "", params.get("agent"), params.get("room"));
    return searches.length && !query ? { searches, active: stored.active } : withQuery({ searches, active: stored.active }, query);
  });
  const savedRef = useRef(saved), typing = useRef<ReturnType<typeof setTimeout>>(undefined);
  const shown = useMemo(() => comparablePage(page?.searches, page?.active), [page]), lastPageSeen = useRef(shown);
  // Saving what the page already holds sends nothing.
  const persist = () => {
    clearTimeout(typing.current);
    typing.current = undefined;
    if (comparablePage(savedRef.current.searches, savedRef.current.active) === shown) return;
    tabs.editSearch(path, savedRef.current, inShared);
    if (owned && imported) localStorage.removeItem(legacy);
  };
  // Opening saves what it opened; leaving saves typing that waits, unless another person signed in meanwhile.
  const settle = useEffectEvent((leaving: boolean) => {
    if (!leaving || (typing.current && tabs.state.owner === owner)) persist();
    clearTimeout(typing.current);
    typing.current = undefined;
  });
  useEffect(() => {
    settle(false);
    return () => settle(true);
  }, []);
  const consume = useEffectEvent(consumeLink);
  useEffect(() => consume(), []);
  // A newer page saved elsewhere, by another window or person, replaces what this one shows unless typing waits to be
  // saved; the page as it opened, and this page's own saves coming back, change nothing.
  const adoptNewerPage = useEffectEvent(() => {
    if (lastPageSeen.current === shown) return;
    lastPageSeen.current = shown;
    if (typing.current || !page?.searches.length || shown === comparablePage(savedRef.current.searches, savedRef.current.active)) return;
    savedRef.current = { searches: page.searches, active: page.active };
    setSaved(savedRef.current);
  });
  useEffect(() => adoptNewerPage(), [shown]);
  // A link to the page with ?q= while it is open, like Cmd/Ctrl+P's hand-off, opens that query as at mount; replaceState sends no popstate.
  const opened = useEffectEvent(() => {
    const query = new URLSearchParams(location.search).get("q");
    if (location.pathname === path && query) update(withQuery(savedRef.current, query));
  });
  useEffect(() => {
    const listener = () => opened();
    addEventListener("popstate", listener);
    return () => removeEventListener("popstate", listener);
  }, []);
  const focusAfterClose = useRef(false);
  const activeTab = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => { input.current?.focus(); }, []);
  const sidebar = useContext(Sidebar);
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000 });
  const selected = saved.searches.findIndex((tab) => tab.id === saved.active);
  const tab = saved.searches[selected];
  const search = useSearch();
  useEffect(() => {
    const params = new URLSearchParams(tab.query ? { q: tab.query } : {});
    history.replaceState(history.state, "", `${path}${params.size ? `?${params}` : ""}`);
  }, [tab, search, path]);
  useLayoutEffect(() => {
    if (rename !== null) { activeTab.current!.focus(); window.getSelection()?.selectAllChildren(activeTab.current!); }
    if (focusAfterClose.current) {
      activeTab.current?.focus();
      focusAfterClose.current = false;
    }
  }, [saved, rename]);
  const update = (next: SavedSearches, typed = false) => {
    savedRef.current = next;
    setSaved(next);
    clearTimeout(typing.current);
    // A Shared page waits too, so clicking through its searches sends one change, not one per click.
    if (typed || inShared) typing.current = setTimeout(persist, 500); else persist();
  };
  const edit = (change: (item: SavedSearch) => Partial<SavedSearch>, typed = false) => update({ ...savedRef.current, searches: savedRef.current.searches.map((item) => item.id === tab.id ? { ...item, ...change(item) } : item) }, typed);
  const select = (active: string) => update({ ...savedRef.current, active });
  // Closing the last search closes the page and returns to the last message this person saw.
  const close = (index: number) => {
    const rest = saved.searches.filter((_, position) => position !== index);
    if (!rest.length) {
      const last = localStorage.getItem(`last-seen:${owner}`);
      return owned ? tabs.close([path], last ?? undefined) : navigate(last ?? "/");
    }
    focusAfterClose.current = true;
    update({ searches: rest, active: saved.active === saved.searches[index].id ? rest[Math.min(index, rest.length - 1)].id : saved.active });
  };
  return <section aria-label="Saved searches" className="flex min-h-0 min-w-0 flex-1 flex-col gap-4 p-4 sm:p-6">
    <div className="flex min-w-0 items-center gap-2">
      <div role="tablist" aria-label="Searches" className="flex min-w-0 gap-1 overflow-x-auto">
        {saved.searches.map((item, index) => <div key={item.id} className="flex shrink-0 items-center rounded-md border border-sidebar-border bg-background">
          <div role={item.id === saved.active && rename !== null ? "textbox" : "tab"} aria-label={item.id === saved.active && rename !== null ? "Search name" : undefined} contentEditable={item.id === saved.active && rename !== null ? "plaintext-only" : false} suppressContentEditableWarning ref={item.id === saved.active ? activeTab : null} tabIndex={item.id === saved.active ? 0 : -1} aria-selected={rename === null || item.id !== saved.active ? item.id === saved.active : undefined} aria-controls="search-tab-panel" className={cn("max-w-44 truncate rounded-l-md px-3 py-2 text-sm", item.id === saved.active && "bg-sidebar-row-active", item.id === saved.active && rename !== null ? "cursor-text" : "cursor-pointer")} onClick={() => { if (item.id !== saved.active) select(item.id); else if (rename === null) setRename(item.name || item.query || `Search ${index + 1}`); }} onBlur={(event) => {
            if (item.id !== saved.active || rename === null) return;
            const name = event.currentTarget.textContent!.trim();
            event.currentTarget.textContent = name || item.query || `Search ${index + 1}`;
            edit(() => ({ name })); setRename(null);
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
            const position = event.key === "ArrowRight" ? (index + 1) % saved.searches.length : event.key === "ArrowLeft" ? (index + saved.searches.length - 1) % saved.searches.length : event.key === "Home" ? 0 : event.key === "End" ? saved.searches.length - 1 : -1;
            if (position < 0) return;
            event.preventDefault();
            select(saved.searches[position].id);
            event.currentTarget.parentElement?.parentElement?.querySelectorAll<HTMLElement>('[role="tab"]')[position]?.focus();
          }}>{item.name || item.query || `Search ${index + 1}`}</div>
          <button type="button" aria-label={`Close search ${index + 1}`} className="rounded-r-md px-2 py-2 text-muted-foreground hover:text-foreground" onClick={() => close(index)}><X className="size-4" /></button>
        </div>)}
      </div>
      <Button variant="outline" size="icon" aria-label="New search" onClick={() => {
        const id = crypto.getRandomValues(new Uint32Array(4)).join("-");
        update({ searches: [...saved.searches, { id, query: "" }], active: id });
        requestAnimationFrame(() => input.current?.focus());
      }}><Plus className="size-4" /></Button>
    </div>
    <SearchResults key={tab.id} tab={tab} rows={sidebar.rows} catalog={agents.data?.agents ?? []} edit={edit} input={input} />
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

// drawn caps the rows shown so far; the count still covers every hit.
function SearchResultGroup({ session, hits, text, field, needle, tagIds, drawn }: { session: Session; hits: MessageMatch[]; text: string; field: string; needle: string; tagIds: string[]; drawn: number }) {
  const count = hits.length + Number(!!field);
  const label = sessionTitle(session);
  const clean = useCleanText(label);
  const row = (href: string, kind: string, body: string, key?: string) => <li key={key}><Link href={href} className="flex min-h-11 min-w-0 items-start gap-3 px-2 py-1 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring sm:min-h-7"><span className="w-16 shrink-0 text-xs text-muted-foreground">{kind}</span><div className="min-w-0"><TranscriptText text={windowed(body, needle, tagIds)} needle={needle} interactive={false} /></div></Link></li>;
  return <li role="group" aria-label={clean} className="min-w-0">
    <h2 className="flex min-w-0 items-center gap-1 text-sm font-medium"><Link href={rowPath(session)} title={clean} className="min-w-0 truncate py-1 hover:underline focus-visible:outline-2 focus-visible:outline-ring"><InlineText text={label} /></Link><span className="shrink-0 text-muted-foreground">({count})</span>{session.pinned ? <Pin aria-label="Pinned" className="size-3 shrink-0" /> : null}</h2>
    <ul className="mt-1 border-l pl-2 text-sm leading-5">
      {field ? row(rowPath(session), field, text) : null}
      {hits.slice(0, drawn - Number(!!field)).map((match, i) => row(sessionPath(session.id) + (match.message.messageId ? `?message=${encodeURIComponent(match.message.messageId)}` : ""), match.message.role === "user" ? "You" : "Assistant", match.message.text, match.message.messageId || String(i)))}
    </ul>
  </li>;
}

// The palette lists sessions itself when nothing matched, so only /search passes empty.
function SearchStatus({ search, empty = false, incomplete = false }: { search: ReturnType<typeof useSessionSearch>; empty?: boolean; incomplete?: boolean }) {
  const { pending, error } = search;
  return <>
    {pending ? <p role="status" className="text-muted-foreground">Searching…</p> : null}
    {error ? <p role="alert" className="text-destructive">Search failed: {error} <button type="button" className="underline" onClick={() => void search.run(search.sent)}>Retry</button></p> : null}
    {empty ? <p role="status" className="text-muted-foreground">{incomplete ? "Session search is still loading." : "No matches"}</p> : null}
  </>;
}

const searchPage = 200;

function SearchResults({ tab, rows, catalog, edit, input }: { tab: SavedSearch; rows: Session[]; catalog: Agent[]; edit: (change: (item: SavedSearch) => Partial<SavedSearch>, typed?: boolean) => void; input: RefObject<HTMLInputElement | null> }) {
  const sidebar = useContext(Sidebar);
  // Opening a saved search or a link sends its whole query.
  const [entered, setEntered] = useState(true);
  const search = useSessionSearch(tab.query, "messages", entered);
  const { response } = search.result;
  // Rows are drawn a page at a time as the end nears the screen; a new response starts again at one page.
  const [budget, setBudget] = useState({ response, rows: searchPage });
  const rowsDrawn = budget.response === response ? budget.rows : searchPage;
  const { groups, more } = useMemo(() => {
    const hits = Map.groupBy(response?.messages ?? [], (match) => match.conversationId);
    let left = rowsDrawn;
    const groups = [];
    for (const { match, session } of search.found) {
      if (left <= 0) break;
      const own = hits.get(session.id) ?? [];
      groups.push(<SearchResultGroup key={session.id} session={session} hits={own} text={match.text} field={match.field} needle={response!.needle} tagIds={response!.mentionIds} drawn={left} />);
      left -= own.length + Number(!!match.field);
    }
    return { groups, more: left < 0 || groups.length < search.found.length };
  }, [response, search.found, rowsDrawn]);
  return <>
    <div id="search-tab-panel" role="tabpanel" aria-label="Search" className="min-w-0">
      <SessionSearch rows={rows} catalog={catalog} query={tab.query} setQuery={(update) => { setEntered(false); edit((item) => ({ query: update(item.query) }), true); }} search={search} inputRef={input} onKeyDown={(event) => { if (event.key === "Enter") { setEntered(true); void search.run(tab.query.trimEnd()); } }} />
    </div>
    <div className="min-h-0 flex-1 overflow-y-auto text-sm" aria-label="Search results">
      <SearchStatus search={search} empty={!!search.sent && !search.pending && !search.error && search.found.length === 0} incomplete={!searchIsAuthoritative(sidebar) || search.missing} />
      {search.sent ? <ul data-pending={search.pending} className="flex min-w-0 flex-col gap-4 data-[pending=true]:opacity-50">
        {groups}
        {more ? <li aria-hidden ref={(node) => {
          const observer = new IntersectionObserver(([entry]) => {
            if (entry.isIntersecting) setBudget((last) => ({ response, rows: (last.response === response ? last.rows : searchPage) + searchPage }));
          }, { root: node!.closest('[aria-label="Search results"]'), rootMargin: "0px 0px 100% 0px" });
          observer.observe(node!);
          return () => observer.disconnect();
        }} /> : null}
      </ul> : <p className="text-muted-foreground">Type to search messages and conversations.</p>}
    </div>
  </>;
}

// Values offered for the operator word being typed; a completed value commits on space or Enter instead of offering itself again.
// Remote OpenCode sessions add their servers, agents and models; RocketClaw agents bring their configured models.
function searchChoices(word: string, rows: Session[], catalog: Agent[], principals: string[]) {
  const typed = new RegExp(`^(${filterKeys}):"?(.*?)"?$`, "is").exec(word);
  const key = typed?.[1].toLowerCase() ?? "", needle = typed?.[2].toLowerCase() ?? "";
  const remote = (field: "server" | "agent" | "model") => [...new Set(rows.flatMap((row) => row.server && row[field] ? [row[field]!] : []))];
  const names = key === "principal" ? principals : key === "room" ? slackRooms(rows) : key === "cron" ? [...new Set(rows.flatMap((row) => row.cronName ? [row.cronName] : []))] : key === "tag" ? [...new Set(rows.flatMap((row) => row.tags ?? []))] : key === "sort" ? ["newest", "oldest"]
    : key === "server" ? remote("server") : key === "model" ? [...new Set([...catalog.flatMap(({ model }) => model ? [model] : []), ...remote("model")])] : ["pinned", "forked", "cron", "unread"];
  const agents = [...catalog.map(({ name, model, reasoning }) => ({ key: name, detail: [model, reasoning].filter(Boolean).join(" · ") })), ...remote("agent").filter((name) => !catalog.some((agent) => agent.name === name)).map((name) => ({ key: name, detail: "OpenCode" }))];
  const offered = !key ? [] : (key === "agent" ? agents : names.map((name) => ({ key: name, detail: "" }))).filter((item) => item.key.toLowerCase().includes(needle));
  return { key, choices: offered.some((item) => item.key.toLowerCase() === needle) ? [] : offered };
}

// Pills are the filter terms the server read in a query this one still starts with, each followed by whitespace; the input edits the rest.
function usePills(query: string, setQuery: (update: (query: string) => string) => void, search: ReturnType<typeof useSessionSearch>, inputRef: RefObject<HTMLInputElement | null>) {
  const caret = useRef(query.length); // In query positions.
  const { query: read, response } = search.result;
  const terms = useMemo(() => (response?.terms ?? []).filter((term) => query.slice(0, term.end) === read.slice(0, term.end)), [query, read, response]);
  const pills = useMemo(() => terms.filter((term) => /\s/.test(query[term.end] ?? "")), [query, terms]);
  const input = cutTerms(query, pills);
  // Keep the caret in place when pills take text out of the input, but leave a selection alone: typing replaces it.
  useLayoutEffect(() => {
    const at = cutTerms(query.slice(0, caret.current), pills).length;
    const element = inputRef.current!;
    if (element === document.activeElement && element.selectionStart === element.selectionEnd) element.setSelectionRange(at, at);
  }, [inputRef, pills, query]);
  const toQuery = (at: number, after: boolean) => pills.reduce((position, term) => term.start < position || after && term.start === position ? position + term.end + 1 - term.start : position, at);
  const change = (next: string, at = next.length) => {
    caret.current = at;
    setQuery(() => next);
  };
  const head = query.slice(0, query.length - search.word.length);
  return {
    pills, input, change, head,
    select: (at: number) => { caret.current = toQuery(at, true); },
    edit: (value: string, at: number) => {
      let same = 0, tail = 0;
      while (same < at && value[same] === input[same]) same++;
      while (tail < value.length - at && tail < input.length - same && value.at(-1 - tail) === input.at(-1 - tail)) tail++;
      // Hidden pills in or right after the edited text are kept, moved in front of the free text after the previous pill, so an edit never deletes or glues a term.
      const start = toQuery(same, true), end = toQuery(input.length - tail, true), front = pills.reduce((position, term) => term.end < start ? term.end + 1 : position, 0);
      const kept = pills.filter((term) => term.start >= start && term.end < end).map((term) => query.slice(term.start, term.end + 1)).join("");
      change(query.slice(0, front) + kept + query.slice(front, start) + value.slice(same, value.length - tail) + query.slice(end), start + kept.length + at - same);
    },
    remove: (term: { start: number; end: number }) => change(cutTerms(query, [term]), cutTerms(query.slice(0, caret.current), [term]).length),
    pick: (key: string, name: string) => change(`${head}${key}:${key === "is" || key === "sort" ? name : termValue(name)} `),
  };
}

function SessionSearch({ rows, catalog, query, setQuery, search, inputRef, placeholder, onKeyDown }: {
  rows: Session[];
  catalog: Agent[];
  query: string;
  setQuery: (update: (query: string) => string) => void;
  search: ReturnType<typeof useSessionSearch>;
  inputRef: RefObject<HTMLInputElement | null>;
  placeholder?: string;
  onKeyDown?: React.KeyboardEventHandler<HTMLInputElement>;
}) {
  const sidebar = useContext(Sidebar);
  const stale = !sidebar.refreshing && rows.length > 0 && !searchIsAuthoritative(sidebar);
  const [overlayPick, setOverlayPick] = useState(0);
  const { key, choices } = searchChoices(search.word, rows, catalog, search.result.response?.principals ?? []);
  const { pills, input, change, head, select, edit, remove, pick: choose } = usePills(query, setQuery, search, inputRef);
  const pick = choices.length === 0 ? 0 : overlayPick % choices.length;
  const active = useRef<HTMLButtonElement>(null);
  useEffect(() => { active.current?.scrollIntoView({ block: "nearest" }); }, [pick, key]);
  return (
        <div className="min-w-0 flex-1 shrink-0">
          <div className="flex min-h-8 min-w-0 flex-wrap items-center gap-1 rounded-md border border-sidebar-border bg-background pr-2 pl-2">
            <span className="flex size-3.5 shrink-0 items-center text-muted-foreground">
              {stale ? <Tooltip>
                <TooltipTrigger render={<span />} tabIndex={0} role="img" aria-label="Conversation list may be out of date">
                  <CircleAlert className="size-3.5" />
                </TooltipTrigger>
                <TooltipContent side="bottom">Conversation list may be out of date</TooltipContent>
              </Tooltip> : search.pending ? <LoaderCircle role="img" aria-label="Searching" className="size-3.5 animate-spin motion-reduce:animate-none" /> : <Search className="size-3.5" />}
            </span>
            {pills.map((term) => <FilterPill key={term.start} label={term.text} onClear={() => remove(term)} />)}
            <Input
              ref={inputRef}
              value={input}
              onChange={(event) => { setOverlayPick(0); edit(event.target.value, event.target.selectionStart ?? event.target.value.length); }}
              onSelect={(event) => select(event.currentTarget.selectionStart ?? 0)}
              aria-label={placeholder ?? "Search sessions"}
              placeholder={placeholder ?? (pills.length ? "Search" : "Search or agent: or room:")}
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
                    setOverlayPick(0);
                    choose(key, choices[pick].key);
                    return;
                  }
                  if (event.key === "Escape") {
                    event.preventDefault();
                    event.stopPropagation();
                    change(head);
                    return;
                  }
                }
                onKeyDown?.(event);
              }}
            />
          </div>
          {choices.length > 0 ? <ul className="mt-1 max-h-[min(12rem,25vh)] overflow-y-auto rounded-lg border bg-popover text-popover-foreground shadow-md">
            {choices.map((item, index) => (
              <li key={item.key}>
                <button ref={index === pick ? active : null} type="button" className={cn("flex w-full flex-col items-start px-3 py-2 text-left text-sm", index === pick && "bg-accent")} onMouseDown={(event) => event.preventDefault()} onClick={() => { setOverlayPick(0); choose(key, item.key); }}>
                  <span className="font-medium">{item.key}</span>
                  {item.detail ? <span className="text-xs text-muted-foreground">{item.detail}</span> : null}
                </button>
              </li>
            ))}
          </ul> : null}
        </div>
  );
}

// A page only in Shared has no close button, as closing it does nothing.
function PageTitle({ children }: { children: ReactNode }) {
  const { close, shown, pathname } = useContext(TabActions);
  return <header className="flex w-full shrink-0 items-center gap-2">
    <h1 className="text-lg font-semibold">{children}</h1>
    {shown.some((tab) => tab.path === pathname) ? <Button type="button" variant="ghost" size="icon-sm" className="ml-auto hidden md:inline-flex" aria-label="Close" onClick={() => close([pathname])}><X /></Button> : null}
  </header>;
}

type Line = { id: string; text: string; role: "user" | "assistant" | "thinking" | "tool" | "developer"; complete?: boolean; entryKey?: string; inputId?: string; messageId?: string; turnId?: string; toolCallId?: string; toolName?: string; toolParts?: Line[]; attachments?: (AttachmentMeta & { file?: File })[] } & Pick<TranscriptEvent, "agent" | "model" | "reasoningEffort" | "origin" | "header" | "principal" | "state" | "parentId" | "completionNotes" | "delegation" | "review" | "codeModeCalls" | "scriptReason" | "scriptOutcome" | "reviewTurn">;
type OriginFilter = { sandboxed: boolean; canonical: boolean };

function lineId(role: Line["role"], text: string, seen: Map<string, number>) {
  const base = `${role}:${text}`;
  const n = (seen.get(base) ?? 0) + 1;
  seen.set(base, n);
  return `${base}:${n}`;
}

function transcriptTurns(lines: Line[], filter: OriginFilter = { sandboxed: true, canonical: true }, comparison = false) {
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
    const previous = current.lines.at(-1);
    if (comparison && line.turnId ? previous?.turnId !== line.turnId || previous?.entryKey !== line.entryKey : line.role === "user") flush();
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
      if (comparison) current.items.push(line);
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

function comparisonRows(parents: Turn[], children: Turn[], binding: ShadowBinding) {
  const childTurns = new Map(children.map((turn) => [turn.lines[0]?.turnId, turn]));
  const correlations = new Map(binding.turns.map((turn) => [`turn:${turn.parentSourceConversationId}:${turn.parentTurnId}`, turn]));
  const rows = parents.map((parent) => {
    const correlation = correlations.get(parent.lines[0]?.entryKey ?? "");
    correlations.delete(parent.lines[0]?.entryKey ?? "");
    return { key: turnKey(parent), parent, correlation, child: correlation?.childTurnId ? childTurns.get(correlation.childTurnId) : undefined };
  });
  const missing = [...correlations].map(([key, correlation]) => ({ key, parent: undefined, correlation, child: childTurns.get(correlation.childTurnId) }));
  let next = 0;
  return [...rows.flatMap((row) => {
    const start = next;
    if (row.correlation) while (next < missing.length && BigInt(missing[next].correlation.order) < BigInt(row.correlation.order)) next++;
    return [...missing.slice(start, next), row];
  }), ...missing.slice(next)];
}

function toolTitle(line: Line) {
  const name = line.toolName;
  if (!name) return "Tool result";
  let input: { name?: string; code?: string; command?: string; path?: string; description?: string };
  try {
    input = JSON.parse(line.text.slice(name.length + 1));
  } catch {
    return name;
  }
  if (!input || typeof input !== "object") return name;
  if (name === "skill" && typeof input.name === "string") return `Skill · ${input.name}`;
  if (name === "execute" && typeof input.description === "string" && input.description.trim() !== "") return input.description;
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
  return text ? <div data-slot="message-footer" className="flex max-w-full items-center gap-1 break-words px-3 text-[11px] text-muted-foreground/85 group-has-data-[variant=ghost]/message:px-0">
    <span className="min-w-0">{text}</span>
  </div> : null;
}

// A stored header shows after the pointer rests on the bubble, or after holding a blank part of it on
// touch, where holding text still selects it.
function MessageHeader({ header, children }: { header?: string; children: ReactElement }) {
  const [open, setOpen] = useState(false);
  const hold = useRef<ReturnType<typeof setTimeout>>(undefined);
  if (!header) return children;
  return <Tooltip open={open} onOpenChange={setOpen}>
    <TooltipTrigger data-slot="bubble" delay={1500} render={children} onPointerDown={(event) => {
      if (event.pointerType === "touch" && (event.target as Element).matches('[data-slot="bubble"], [data-slot="bubble-content"], [data-slot="message-author"]')) hold.current = setTimeout(() => setOpen(true), 500);
    }} onPointerUp={() => clearTimeout(hold.current)} onPointerCancel={() => clearTimeout(hold.current)} />
    <TooltipContent className="max-w-[min(24rem,calc(100vw-2rem))] whitespace-pre-wrap [overflow-wrap:anywhere]">{header}</TooltipContent>
  </Tooltip>;
}

// Undo keeps floor, the newest message from a cron run, External MCP, or Slack, and every message before it.
function keptByFloor(messageId?: string, floor?: string) {
  if (!messageId || !floor) return false;
  const [entry, index] = messageId.split(":").map(Number), [floorEntry, floorIndex] = floor.split(":").map(Number);
  return entry < floorEntry || entry === floorEntry && index <= floorIndex;
}

// Revert keeps its place on every user message; where it cannot apply it is disabled and says why.
// A disabled button takes no pointer events, so its reason sits on the wrapper.
function RevertButton({ line }: { line: Line }) {
  const revert = useContext(RevertActions);
  const kept = keptByFloor(line.messageId, revert?.floor);
  const revertable = !!revert?.available && !!line.entryKey && !kept && !!(line.messageId || line.inputId && line.entryKey === revert.latest);
  const reason = !revert?.available ? "Revert isn't available in this session" : kept ? "Revert can't go back past a message from a cron run, External MCP, or Slack" : revertable ? "Revert message" : "This message can't be reverted";
  return <span className="flex" title={reason}><Button type="button" size="icon-xs" variant="ghost" className="size-11 sm:size-6" aria-label="Revert message" disabled={!revertable || revert!.pending} onClick={() => void revert!.run(line.messageId || line.inputId)}><Undo2 /></Button></span>;
}

// Copy is always last, with Revert before it on user messages, so the row keeps its shape
// whether or not the session allows reverting.
function MessageActions({ line, hasSandboxed }: { line: Line; hasSandboxed: boolean }) {
  const [copied, setCopied] = useState<string>();
  const [error, setError] = useState(false);
  return <div data-slot="message-actions" className={cn("invisible flex max-w-full items-center gap-1 group-focus-visible/message:visible [@media(hover:hover)]:group-hover/message:visible [@media(hover:hover)]:group-has-[:focus-visible]/message:visible [@media(hover:none)]:group-focus-within/message:visible", line.role === "user" && "self-end")}>
    <MessageFooter line={line} hasSandboxed={hasSandboxed} />
    <span role="status" className={error ? "text-xs text-destructive" : "sr-only"}>{error ? "Could not copy. Select and copy the text." : copied === line.text ? "Copied" : ""}</span>
    {line.role === "user" ? <RevertButton line={line} /> : null}
    <Button type="button" size="icon-xs" variant="ghost" aria-label="Copy message" title="Copy message" onClick={async () => {
      setError(false);
      try {
        await copyText(line.text, document.body);
        setCopied(line.text);
      } catch {
        setError(true);
      }
    }}>{copied === line.text && !error ? <Check /> : <Copy />}</Button>
  </div>;
}

type TraceStatus = "running" | "error";
// Whether the turn a row belongs to is still working, so its unanswered calls are running.
const TurnRunning = createContext(false);
// A "background" call keeps that state after its job ends, so only the Background Jobs list shows the job running.
const toolStates: Partial<Record<NonNullable<Line["state"]>, TraceStatus>> = { working: "running", review: "running", failed: "error", stopped: "error" };

// A call runs until its result arrives while the turn works, and fails on a failed state or an in-band
// failure, as in https://ui.shadcn.com/docs/ai-elements/base/tool#states.
function toolStatus(line: Line, running: boolean): TraceStatus | undefined {
  if (line.role !== "tool") return undefined;
  if (line.state) return toolStates[line.state];
  if (timelineFailure.test((line.toolName ? line.toolParts?.[0]?.text : line.text) ?? "")) return "error";
  return running && line.toolName && !line.toolParts?.length ? "running" : undefined;
}

// A running line spins and a failed one warns, in place of its own icon.
function TraceStatusIcon({ status, icon }: { status?: TraceStatus; icon: ReactNode }) {
  return <TraceIcon>{status === "running" ? <LoaderCircle className="animate-spin" /> : status === "error" ? <CircleAlert /> : icon}</TraceIcon>;
}

// Activity is a Trace line that expands into its detail (https://ui.shadcn.com/docs/components/base/trace).
// `open` is the timeline setting; callers key rows by it, so a new setting opens or closes them.
function TraceRow({ open = true, icon, title, label, status, description, children, ...props }: { open?: boolean; icon: ReactNode; title: ReactNode; label?: string; status?: TraceStatus; description?: string; children: ReactNode; "data-tool-call-id"?: string }) {
  return <TraceCollapsible defaultOpen={open} {...props}>
    <TraceCollapsibleTrigger title={label}>
      <TraceLine status={status}>
        <TraceStatusIcon status={status} icon={icon} />
        <TraceTitle>{title}</TraceTitle>
        {description ? <TraceDescription aria-live="polite">{description}</TraceDescription> : null}
      </TraceLine>
    </TraceCollapsibleTrigger>
    <TraceCollapsibleContent hiddenUntilFound>{children}</TraceCollapsibleContent>
  </TraceCollapsible>;
}

function MessageAuthor({ principal, role = "user" }: { principal?: string; role?: Line["role"] }) {
  return role === "user" && principal ? <span data-slot="message-author" className="mb-1 block whitespace-pre-wrap wrap-anywhere text-xs text-muted-foreground">{principal}</span> : null;
}

// Each Completion Note is one row with its note, from its stored job, folded beneath.
function CompletionNotes({ notes }: { notes: BackgroundJob[] }) {
  return notes.map((note) => <TraceRow key={note.jobId} open={false} icon={<BellRing />} title={note.label} description={jobState(note)} status={jobStatus(note)}>
    <CodeBlock size="sm" label={note.label} text={note.note} />
  </TraceRow>);
}

const toolIcons: Partial<Record<string, ReactNode>> = { task: <Bot />, skill: <Sparkles />, execute: <SquareTerminal />, bash: <SquareTerminal /> };

// The panels a call opens, as Trace link lines: its subagent (https://ui.shadcn.com/docs/ai-elements/base/subagent)
// or skill. Its permission review opens from its reasons; see ReviewReason.
function ToolLinks({ line, title }: { line: Line; title: string }) {
  // toolTitle names a skill call's skill as "Skill · name".
  const skill = line.toolName === "skill" && title.startsWith("Skill · ") ? title.slice("Skill · ".length) : "";
  const delegation = line.delegation;
  const links = [
    delegation && { href: panelHref(delegation), text: "Open delegation", label: `Open delegation: ${title}`, icon: <Bot /> },
    skill && { href: panelHref(skill, "skill"), text: "View skill", label: `View skill: ${skill}`, icon: <Sparkles /> },
  ].filter((link) => !!link);
  return links.length ? <Trace className="mb-2">{links.map((link) => <Link key={link.text} href={link.href} aria-label={link.label}>
    <TraceLine><TraceIcon>{link.icon}</TraceIcon><TraceTitle>{link.text}</TraceTitle><TraceIcon><ChevronRight /></TraceIcon></TraceLine>
  </Link>)}</Trace> : null;
}

// How a call a script made reads in its row. Its command is as written: bash's script, the path or pattern
// read, glob and grep take, or its arguments with real line breaks.
function codeModeCallRow(call: CodeModeCall, running: boolean) {
  let command = call.arguments ?? "";
  try {
    const input = JSON.parse(command);
    const text = call.name === "bash" ? input.command : ["read", "glob", "grep"].includes(call.name) ? input.filePath ?? input.pattern : undefined;
    command = typeof text === "string" ? text : JSON.stringify(input, null, 2);
  } catch {
    // Clipped arguments are no longer JSON and show as sent.
  }
  const denied = call.status === "denied" || call.outcome === "deny";
  const status: TraceStatus | undefined = denied || call.status === "error" ? "error" : call.status === "running" && running ? "running" : undefined;
  return { command, status, title: `${call.name} · ${command.replace(/\s+/g, " ").slice(0, 120)}`,
    description: denied ? "denied" : call.status === "error" ? "failed" : undefined };
}

// An approver's reason, one line under the box it explains until clicked. Its icon opens the review at
// the answer that gave the reason.
function ReviewReason({ review, reason, outcome, turn }: { review?: string; reason: string; outcome?: string; turn?: string }) {
  const [open, setOpen] = useState(false);
  return <footer data-slot="permission-review-rationale" className="-mt-1 mb-2 flex min-w-0 items-start gap-1.5 text-xs text-muted-foreground">
    {review ? <Link href={panelHref(review, "delegation", reason, turn)} aria-label="Open permission review" title="Open permission review" className="shrink-0 py-0.5 hover:text-foreground"><ShieldCheck className="size-3.5" /></Link> : null}
    <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className={cn("min-w-0 text-left hover:text-foreground", open ? "whitespace-pre-wrap wrap-anywhere" : "truncate")}>
      {outcome ? `${outcome === "deny" ? "Denied" : "Allowed"}: ` : ""}{reason}
    </button>
  </footer>;
}

// An Execute block follows OpenCode's Execute and ExecuteCallView (packages/tui/src/routes/session/index.tsx):
// a closed row per call its script made, marked when it failed, with its command on expand; then its result.
function CodeModeBlock({ line, status, review, rationale }: { line: Line; status?: TraceStatus; review?: string; rationale?: string }) {
  const calls = line.codeModeCalls ?? [];
  // A review that is neither the script reason nor a row's reason, such as one whose action cannot be read
  // back or one a background script saved after its turn ended, shows with the script reason.
  const shown = [line.scriptReason, ...calls.map((call) => call.reason)];
  const unshown = rationale?.split("\n").filter((reason) => reason && !shown.some((text) => text?.includes(reason)));
  const seen = new Map<string, number>();
  let result = line.toolParts?.map((part) => part.text).join("\n\n");
  try {
    // A script's result is JSON: a returned string or list of strings shows as its text.
    const value = JSON.parse(result ?? "");
    result = typeof value === "string" ? value : [value].flat().every((item) => typeof item === "string") ? [value].flat().join("\n\n") : JSON.stringify(value, null, 2);
  } catch {
    // Plain text results show as sent.
  }
  return <>
    {calls.length ? <Trace className="my-2">{calls.map((call) => {
      const row = codeModeCallRow(call, status === "running");
      return <TraceRow key={lineId("tool", `${call.name} ${call.arguments}`, seen)} open={false} icon={toolIcons[call.name] ?? <Wrench />} label={row.title} status={row.status} description={row.description}
        title={<span className="block truncate font-mono">{row.title}</span>}>
        <CodeBlock size="sm" label={call.name} text={row.command} language={call.name === "bash" ? "bash" : ""} />
        {call.outcome ? <ReviewReason review={review} reason={call.reason ?? ""} outcome={call.outcome} turn={call.reviewTurn} /> : null}
      </TraceRow>;
    })}</Trace> : null}
    {calls.length === 200 ? <p className="text-muted-foreground">Showing the first 200 calls; the script may have made more.</p> : null}
    {line.state === "background" ? <p className="text-muted-foreground">Moved to the background. Its result arrives in a Completion Note.</p> : null}
    {result ? <CodeBlock size="sm" label="Result" text={result} /> : null}
    {/* The script reason and those reviews follow its result, or stand alone until a result arrives. */}
    {line.scriptReason ? <ReviewReason review={review} reason={line.scriptReason} outcome={line.scriptOutcome} turn={line.reviewTurn} /> : null}
    {unshown?.map((reason) => <ReviewReason key={lineId("tool", reason, seen)} review={review} reason={reason} />)}
    <CodeModeScript text={line.text.slice("execute ".length)} />
  </>;
}

// An Execute call's script shows at Everything, behind a toggle at other levels, and not at Compact.
function CodeModeScript({ text }: { text: string }) {
  const level = timelineLevel(useTimelineDetail())?.id;
  const [shown, setShown] = useState(false);
  let code = text;
  try {
    code = JSON.parse(text).code ?? text;
  } catch {
    // Unparsable arguments show as sent.
  }
  if (level === "compact") return null;
  return <>
    {level === "everything" ? null : <Button type="button" variant="ghost" size="xs" aria-expanded={shown} onClick={() => setShown(!shown)}>{shown ? "Hide script" : "Show script"}</Button>}
    {level === "everything" || shown ? <CodeBlock size="sm" label="Script" text={code} language="python" /> : null}
  </>;
}

function ToolLine({ line, conversationId, hasSandboxed, open }: { line: Line; conversationId: string; hasSandboxed: boolean; open?: boolean }) {
  const title = toolTitle(line);
  const { delegations, reviewRationales } = use(Delegations) ?? {};
  // A reviewed call opens its permission review once the delegations list it.
  const review = line.review && delegations?.includes(line.review) ? line.review : undefined;
  const rationale = reviewRationales?.[review ?? ""];
  const seen = new Map<string, number>();
  const status = toolStatus(line, use(TurnRunning));
  const parts = [line, ...(line.toolParts ?? [])];
  const text = [
    line.toolName ? `Arguments\n${line.text.slice(line.toolName.length + 1)}` : `Result\n${line.text}`,
    ...parts.slice(1).map((part) => `${part.role === "developer" ? "Skill instructions" : "Result"}\n${part.text}`),
  ].join("\n\n");
  return (
    <TraceRow open={open} data-tool-call-id={line.toolCallId} icon={toolIcons[line.toolName ?? ""] ?? <Wrench />} title={<span className="font-mono">{title}</span>} label={title} status={status} description={line.state}>
      <ToolLinks line={line} title={title} />
      {line.state ? <MessageFooter line={line} hasSandboxed={hasSandboxed} /> : null}
      {line.toolName === "execute" ? <CodeModeBlock line={line} status={status} review={review} rationale={rationale} /> : <>
        <CodeBlock size="sm" label={title} text={text} />
        {rationale?.split("\n").map((reason) => reason ? <ReviewReason key={lineId("tool", reason, seen)} review={review} reason={reason} turn={line.reviewTurn} /> : null)}
      </>}
      {parts.map((part) => (
        <div key={part.id}>
          <MessageAttachments attachments={part.attachments} conversationId={conversationId} />
        </div>
      ))}
      <Button type="button" variant="ghost" size="xs" onClick={(event) => {
        const trigger = event.currentTarget.closest('[data-slot="trace-collapsible"]')!.querySelector<HTMLElement>('[data-slot="trace-collapsible-trigger"]')!;
        trigger.click();
        trigger.scrollIntoView({ block: "nearest" });
        trigger.focus({ preventScroll: true });
      }}>Collapse tool ↑</Button>
    </TraceRow>
  );
}

function TranscriptLine({ line, conversationId, hasSandboxed, open }: { line: Line; conversationId: string; hasSandboxed: boolean; open?: boolean }) {
  const markdown = usePreference(OUTPUT_KEY) !== "raw";
  if (line.completionNotes?.length) return <CompletionNotes notes={line.completionNotes} />;
  // Reasoning follows https://ui.shadcn.com/docs/ai-elements/base/reasoning; saved thinking is always finished.
  if (line.role === "thinking") return <TraceRow key={String(open)} open={open} icon={<Brain />} title="Thought"><div className="whitespace-pre-wrap">{line.text}</div></TraceRow>;
  if (line.role === "developer") return <TraceRow key={String(open)} open={open} icon={<ScrollText />} title="Instructions">
    <CodeBlock size="sm" label="Instructions" text={line.text} />
  </TraceRow>;
  if (line.role === "tool") return <ToolLine key={String(open)} line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} open={open} />;
  const align = line.role === "user" ? "end" : undefined;
  return (
    <Message data-message-id={line.messageId || undefined} align={align} className="mb-4" tabIndex={0} onPointerDown={(event) => {
      if (event.pointerType === "touch" && !(event.target as Element).closest("button, a, input, textarea, summary")) event.currentTarget.focus({ preventScroll: true, focusVisible: false });
    }}>
      <MessageContent>
        <MessageHeader header={line.header}><Bubble variant={line.role === "user" ? "secondary" : "ghost"} align={align}>
          <BubbleContent>
            <MessageAuthor principal={line.principal} role={line.role} />
            {markdown ? <MarkdownText text={line.text} /> : <TranscriptText text={line.text} />}
          </BubbleContent>
          <MessageAttachments attachments={line.attachments} conversationId={conversationId} />
        </Bubble></MessageHeader>
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
      if (node.closest("[data-shadow-cell]")) return false;
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
const TranscriptTurn = memo(function TranscriptTurn({ turn, index, running, detail, conversationId, hasSandboxed }: { turn: Turn; index: number; live: boolean; running: boolean; detail: TimelineRows; conversationId: string; hasSandboxed: boolean }) {
  return (
    <MessageScrollerItem messageId={`turn-${turnKey(turn)}`} data-turn-key={turnKey(turn)} role="region" aria-label={`Turn ${index + 1}`} tabIndex={-1}>
      <TurnRunning value={running}><TurnContent turn={turn} detail={detail} conversationId={conversationId} hasSandboxed={hasSandboxed} /></TurnRunning>
    </MessageScrollerItem>
  );
}, (previous, next) => !next.live && previous.running === next.running && previous.index === next.index && previous.detail === next.detail && previous.conversationId === next.conversationId && previous.hasSandboxed === next.hasSandboxed
  && previous.turn.lines.length === next.turn.lines.length && previous.turn.lines.every((line, index) => line === next.turn.lines[index]));

function TurnContent({ turn, detail, conversationId, hasSandboxed, comparison = false }: { turn: Turn; detail: TimelineRows; conversationId: string; hasSandboxed: boolean; comparison?: boolean }) {
  return <>
      {comparison ? null : turn.user.map((line) => <TranscriptLine key={line.id} line={line} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
      <TranscriptLines rows={projectTimeline(turn.items, detail)} conversationId={conversationId} hasSandboxed={hasSandboxed} />
      {timelineLevel(detail)?.id === "messages" && turn.items.some((line) => line.role === "tool" && (line.state === "blocked" || toolStatus(line, false) === "error")) ? <p role="status" title="Increase timeline detail in the command palette to see tool details." className="mb-4 flex items-center gap-1 text-xs text-muted-foreground"><CircleAlert aria-hidden="true" className="size-3.5" />A tool failed</p> : null}
    </>;
}

type TimelineRow = ReturnType<typeof projectTimeline<Line>>[number];

// Consecutive activity rows share one Trace between the messages around them. A grouped row is a
// Chain of Thought (https://ui.shadcn.com/docs/ai-elements/base/chain-of-thought) whose status comes from its steps.
function TranscriptLines({ rows, conversationId, hasSandboxed }: { rows: TimelineRow[]; conversationId: string; hasSandboxed: boolean }) {
  const running = use(TurnRunning);
  const activity = (row: TimelineRow) => row.kind === "group" || !!row.line.completionNotes?.length || !["user", "assistant"].includes(row.line.role);
  const render = (row: TimelineRow) => {
    if (row.kind === "line") return <TranscriptLine key={row.line.id} line={row.line} open={row.open} conversationId={conversationId} hasSandboxed={hasSandboxed} />;
    const statuses = row.members.map(({ line }) => toolStatus(line, running));
    // A group of only thoughts shows their text directly instead of a Thought row inside a Thought row.
    const thoughts = row.members.every(({ line }) => line.role === "thinking");
    return <TraceRow key={row.key} open={false} icon={<ListChecks />} title={row.label} status={(["running", "error"] as const).find((status) => statuses.includes(status))}>
      <Trace>{row.members.map(({ line, open }) => thoughts ? <div key={line.id} className="whitespace-pre-wrap">{line.text}</div>
        : <TranscriptLine key={line.id} line={line} open={open} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}</Trace>
    </TraceRow>;
  };
  const runs: TimelineRow[][] = [];
  for (const row of rows) {
    const run = runs.at(-1);
    if (run && activity(run[0]) && activity(row)) run.push(row);
    else runs.push([row]);
  }
  return runs.map((run) => {
    const first = run[0];
    return activity(first) ? <Trace key={`trace:${first.kind === "group" ? first.key : first.line.id}`} className="mb-4">{run.map(render)}</Trace> : render(first);
  });
}

function shadowHistory(binding: ShadowBinding | undefined, drafts: Map<string, ComposerDraft>) {
  const id = binding?.childConversationId ?? "";
  return queryOptions<{ lines: Line[]; delegations?: Pick<HistoryView, "delegations" | "reviewRationales"> }>({ queryKey: ["shadow-history", id, binding?.turns], enabled: !!binding,
    placeholderData: (previous, query) => query?.queryKey[1] === id ? previous : undefined,
    queryFn: async ({ signal }) => {
      const draft: ComposerDraft = drafts.get(id) ?? { text: "", files: [], agent: "", sessionId: id, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0 };
      drafts.set(id, draft);
      await readHistoryDelta(id, draft, () => {}, signal);
      let missing = binding!.turns.map((turn) => turn.childTurnId).filter((turn) => turn && !draft.lines.some((line) => line.turnId === turn));
      // Each read is bounded, but a newly opened old parent page may need several
      // child pages. Seed entries never become comparison rows.
      while (draft.more && missing.length > 0) {
        const before = draft.start;
        await readEarlierHistory(id, draft, () => {}, undefined, signal);
        missing = missing.filter((turn) => !draft.lines.some((line) => line.turnId === turn));
        if (draft.start === before) break;
      }
      if (draft.historyError) throw new Error(draft.historyError);
      const history = queryClient.getQueryData<HistoryView>(queries.history({ id }).queryKey);
      return { lines: draft.lines, delegations: { delegations: [...new Set([...(draft.delegations ?? []), ...(history?.delegations ?? [])])], reviewRationales: { ...draft.reviewRationales, ...history?.reviewRationales } } };
  } });
}

function useShadowHistory(binding?: ShadowBinding) {
  const drafts = useRef(new Map<string, ComposerDraft>());
  return useQuery(shadowHistory(binding, drafts.current));
}

function ShadowCell({ row, index, binding, detail, delegations }: { row: ReturnType<typeof comparisonRows>[number]; index: number; binding: ShadowBinding; detail: TimelineRows; delegations?: Pick<HistoryView, "delegations" | "reviewRationales"> }) {
  let status = "Not attached for this turn or inactive agent segment.";
  if (row.correlation) {
    status = row.correlation.running ? "Working…" : row.correlation.terminal === "complete"
      ? row.child?.lines.some((line) => line.role === "assistant") ? "Completed." : "Completed silently."
      : row.correlation.terminal ? `Turn ${row.correlation.terminal}.` : "Waiting for parent input…";
  }
  return <RevertActions value={undefined}><Delegations value={delegations}><TurnRunning value={!!row.correlation?.running}>
    <div data-shadow-cell={row.key} role="region" aria-label={`Shadow turn ${index + 1}`} className="min-w-0">
      <p className="mb-2 text-xs text-muted-foreground">Turn {index + 1}</p>
      {row.child ? <TurnContent comparison turn={row.child} detail={detail} conversationId={binding.childConversationId} hasSandboxed={row.child.lines.some((line) => line.origin === "sandboxed")} /> : null}
      <p role="status" className="px-1 pb-3 text-xs text-muted-foreground">{status}</p>
    </div>
  </TurnRunning></Delegations></RevertActions>;
}

function WorkingStatus({ conversationId, movable, onChange }: { conversationId: string; movable: boolean; onChange: () => Promise<unknown> }) {
  const move = useMutation({ mutationFn: mutations.moveToBackground, onSuccess: onChange });
  return <MessageScrollerItem><div className="flex flex-wrap items-center gap-2 px-1 pb-4">
    <p role="status" className="text-sm text-muted-foreground">Working…</p>
    {movable ? <Button type="button" size="xs" variant="outline" disabled={move.isPending} onClick={() => move.mutate({ conversationId })}>Move to background</Button> : null}
    {move.error ? <span role="alert" className="text-xs text-destructive">{move.error.message}</span> : null}
  </div></MessageScrollerItem>;
}

function emptyTranscriptMessage(opening: boolean, empty: boolean) {
  if (opening) return "Loading conversation…";
  if (!empty) return "No messages for selected origins.";
  return "Send a message to start the conversation.";
}

function TranscriptBody({ conversationId, working, terminal, origin, hasSandboxed, movable, refreshHistory, turns, rows, binding, detail, wide, loading, emptyMessage, delegations }: Pick<ComponentProps<typeof TranscriptLog>, "conversationId" | "working" | "terminal" | "origin" | "hasSandboxed" | "movable" | "refreshHistory"> & { turns: Turn[]; rows: ReturnType<typeof comparisonRows>; binding?: ShadowBinding; detail: TimelineRows; wide: boolean; loading: boolean; emptyMessage: string; delegations?: Pick<HistoryView, "delegations" | "reviewRationales"> }) {
  return <MessageScrollerContent className={cn("mx-auto w-full min-w-0", binding && wide ? "max-w-7xl" : "max-w-3xl")}>
    {origin && ["cron", "external_mcp"].includes(origin.kind) ? <MessageScrollerItem messageId="origin"><OriginCard origin={origin} /></MessageScrollerItem> : null}
    {(binding ? rows : turns).length === 0 && !working ? (
      <MessageScrollerItem className="flex flex-1 items-center justify-center">
        <p role="status" className="text-sm text-muted-foreground">{emptyMessage}</p>
      </MessageScrollerItem>
    ) : (
      <>
        {loading ? <MessageScrollerItem><p role="status" className="px-1 text-sm text-muted-foreground">Loading earlier messages…</p></MessageScrollerItem> : null}
        {binding ? rows.map((row, index) => <MessageScrollerItem key={row.key} messageId={`turn-${row.key}`} data-turn-key={row.key} role="region" aria-label={`Turn ${index + 1}`} tabIndex={-1}>
          <div data-comparison-row className={cn("min-w-0", wide && "grid grid-cols-[minmax(0,1fr)_minmax(18rem,var(--shadow-width))] gap-5")}>
            <div className="min-w-0"><p className="mb-2 text-xs text-muted-foreground">Turn {index + 1} · {row.parent?.lines[0]?.agent ?? binding.parentAgent}</p>{row.parent ? <TurnContent comparison turn={row.parent} detail={detail} conversationId={conversationId} hasSandboxed={hasSandboxed} /> : <p role="status" className="pb-3 text-xs text-muted-foreground">Parent turn has no transcript output.</p>}</div>
            {wide ? <div className="min-w-0 border-l pl-5"><ShadowCell row={row} index={index} binding={binding} detail={detail} delegations={delegations} /></div> : null}
          </div>
        </MessageScrollerItem>) : turns.map((turn, index) => <TranscriptTurn key={turnKey(turn)} turn={turn} index={index} live={index === turns.length - 1} running={working && index === turns.length - 1} detail={detail} conversationId={conversationId} hasSandboxed={hasSandboxed} />)}
        {working ? <WorkingStatus conversationId={conversationId} movable={movable} onChange={refreshHistory} /> : null}
        {terminal ? <MessageScrollerItem><p role="status" className="px-1 pb-4 text-sm text-muted-foreground">Turn {terminal}.</p></MessageScrollerItem> : null}
      </>
    )}
  </MessageScrollerContent>;
}

function TranscriptControls({ children, comparison }: { children?: ReactNode; comparison?: ReactNode }) {
  if (!children && !comparison) return null;
  return <div role="group" aria-label="Conversation view" className={cn("mx-auto my-2.5 flex w-full max-w-3xl shrink-0 flex-wrap items-center gap-x-3 gap-y-5 px-3", comparison ? "justify-between" : "justify-center")}>
    {children}{comparison}
  </div>;
}

function TranscriptLog({
  conversationId,
  lines,
  opening,
  working,
  terminal,
  origin,
  filter,
  hasSandboxed,
  more,
  loadEarlier,
  movable,
  refreshHistory,
  shadows = [],
  footer,
  children,
}: {
  lines: Line[];
  conversationId: string;
  opening: boolean;
  working: boolean;
  movable: boolean;
  refreshHistory: () => Promise<unknown>;
  terminal?: string;
  origin?: ChatOrigin;
  filter: OriginFilter;
  hasSandboxed: boolean;
  more: boolean;
  loadEarlier: () => Promise<void>;
  shadows?: ShadowBinding[];
  footer: ReactNode;
  children?: ReactNode;
}) {
  const search = new URLSearchParams(useSearch());
  const selected = ["delegation", "skill"].some((panel) => search.has(panel)) ? undefined : shadows.find((binding) => binding.childConversationId === search.get("shadow")) ?? shadows[0];
  const wide = useMedia("(min-width: 64rem)");
  const mobileComparison = !!selected && !wide;
  const [mobileOpen, setMobileOpen] = useState(false);
  const shadow = useShadowHistory(selected);
  const shadowWidth = useAsideWidth("shadow");
  const turns = transcriptTurns(lines, filter, !!selected);
  const childTurns = transcriptTurns(shadow.data?.lines ?? [], { sandboxed: true, canonical: true }, true);
  const pairedRows = selected ? comparisonRows(turns, childTurns, selected) : [];
  const detail = useTimelineDetail();
  const { viewport, seen, scrollToMessage } = useTranscriptPosition(conversationId, lines, turns);
  // TODO(@shadcn/react > 0.3.1): delete `bottom`, this effect, and its onScroll update once MessageScroller ships
  // shadcn-ui/ui#11223 and #12306. A wheel at the bottom or a scrollbar drag after a jump stops following for good.
  const { scrollToEnd } = useMessageScroller();
  const bottom = useRef<number | undefined>(0);
  useLayoutEffect(() => { if (viewport.current!.scrollTop === bottom.current) scrollToEnd(); });
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
    if (!loading && !shadow.isFetching) anchor.current = undefined;
  }, [lines, loading, viewport, shadow.data, shadow.isFetching, selected?.childConversationId]);
  const nearTop = useCallback(() => {
    const element = viewport.current;
    if (more && !loading && element && element.scrollTop < element.clientHeight) earlier();
  }, [more, loading, earlier, viewport]);
  useEffect(() => {
    const frame = requestAnimationFrame(nearTop);
    return () => cancelAnimationFrame(frame);
  }, [lines, nearTop]);
  const emptyMessage = emptyTranscriptMessage(opening, lines.length === 0);
  const jumpToTurn = (turn: Turn) => {
    scrollToMessage(`turn-${turnKey(turn)}`, { align: "start", behavior: "instant" });
    viewport.current?.querySelector<HTMLElement>(`[data-turn-key="${CSS.escape(turnKey(turn))}"]`)?.focus({ preventScroll: true });
  };
  const selectShadow = (value: Tabs.Tab.Value) => {
    const element = viewport.current!;
    const first = [...element.querySelectorAll<HTMLElement>("[data-turn-key]")].find((node) => node.getBoundingClientRect().bottom > element.getBoundingClientRect().top);
    if (first) anchor.current = { key: first.dataset.turnKey!, top: first.getBoundingClientRect().top };
    navigate(panelHref(String(value), "shadow"));
  };
  const commonParent = shadows.every((binding) => binding.parentAgent === shadows[0].parentAgent);
  const tabs = <Tabs.List aria-label="Shadow agents" activateOnFocus className="flex min-w-0 gap-1 overflow-x-auto border-b p-2 lg:pr-12">
    {shadows.map((binding) => <Tabs.Tab key={binding.childConversationId} value={binding.childConversationId} aria-label={binding.shadowAgent} title={`${binding.shadowAgent} · parent ${binding.parentAgent}`} className="shrink-0 rounded-md px-3 py-2 text-xs text-muted-foreground data-active:bg-muted data-active:text-foreground focus-visible:outline-2 focus-visible:outline-ring">{commonParent ? binding.shadowAgent.slice(binding.parentAgent.length + 1) : binding.shadowAgent}</Tabs.Tab>)}
  </Tabs.List>;
  const body = (
    <MessageScroller className="flex-1">
    <MessageScrollerViewport ref={viewport} id="transcript-scroll" preserveScrollOnPrepend={false} onScroll={({ currentTarget: element }) => {
      bottom.current = element.scrollHeight - element.clientHeight - element.scrollTop <= 1 ? element.scrollTop : undefined;
      seen(); nearTop();
    }} className="overflow-x-hidden [overflow-anchor:none]">
      <div className="min-h-full pl-3 pr-8 pt-3 pb-4 sm:pl-5 sm:pr-10 sm:pt-4">
        <TranscriptBody conversationId={conversationId} working={working} terminal={terminal} origin={origin} hasSandboxed={hasSandboxed} movable={movable} refreshHistory={refreshHistory} turns={turns} rows={pairedRows} binding={selected} detail={detail} wide={wide} loading={loading} emptyMessage={emptyMessage} delegations={shadow.data?.delegations} />
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
  const controls = <TranscriptControls comparison={mobileComparison ? <Button type="button" variant="ghost" size="sm" className="min-h-11" onClick={() => setMobileOpen(true)}>Shadows</Button> : null}>{children}</TranscriptControls>;
  if (!selected) return <>{body}{footer}{controls}</>;
  return <><Tabs.Root value={selected.childConversationId} onValueChange={selectShadow} style={{ "--shadow-width": `${shadowWidth.width}px` } as CSSProperties} className="flex min-h-0 flex-1 flex-col">
    {wide ? <header className="grid grid-cols-[minmax(0,1fr)_minmax(18rem,var(--shadow-width))] border-b"><span className="p-3 text-xs text-muted-foreground">Conversation</span><div className="relative min-w-0">{tabs}<AsideResizeHandle {...shadowWidth} /></div></header> : null}
    {shadow.error ? <p role="alert" className="px-3 text-sm text-destructive">{shadow.error.message}</p> : null}
    <Tabs.Panel value={selected.childConversationId} className="flex min-h-0 flex-1 flex-col">{body}</Tabs.Panel>
    {!wide ? <Sheet open={mobileOpen} onOpenChange={setMobileOpen}><SheetContent side="right" className="data-[side=right]:w-full data-[side=right]:sm:max-w-none"><SheetTitle>Shadow comparison · {selected.parentAgent}</SheetTitle>{tabs}<ScrollArea className="min-h-0 flex-1">{pairedRows.map((row, index) => <div key={row.key} className="p-3"><Button size="xs" variant="ghost" onClick={() => { setMobileOpen(false); scrollToMessage(`turn-${row.key}`, { align: "start", behavior: "instant" }); }}>Go to parent turn {index + 1}</Button><ShadowCell row={row} index={index} binding={selected} detail={detail} delegations={shadow.data?.delegations} /></div>)}</ScrollArea></SheetContent></Sheet> : null}
  </Tabs.Root>{footer}{controls}
  </>;
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
    draft.reviewRationales = undefined;
    draft.earlierShadows = undefined;
  }
  draft.busy = view.running || draft.lines.some((line) => !line.entryKey && line.role === "user" && !line.complete);
  draft.terminal = view.terminal;
  draft.origin = view.origin;
  draft.revertEligible = view.revertEligible;
  draft.revertMessageId = view.revertMessageId;
  draft.revertFloor = view.revertFloor;
  draft.canUndo = view.canUndo;
  draft.movable = view.movable;
  draft.jobs = view.backgroundJobs;
  draft.questions = view.pendingQuestions;
  draft.shadows = view.shadows;
  draft.revision = view.revision;
  draft.historyError = "";
  draft.missing = false;
  return newlyConsumed;
}

function readHistoryDelta(id: string, draft: ComposerDraft, onDraftChange: () => void, signal?: AbortSignal): Promise<void> {
  // A query's replacement signal must not inherit a cancelled read's promise.
  if (signal && draft.historyRead) return draft.historyRead.catch(() => {}).then(() => {
    signal.throwIfAborted();
    return readHistoryDelta(id, draft, onDraftChange, signal);
  });
  draft.historyAgain = true;
  if (draft.historyRead) return draft.historyRead;
  draft.historyRead = (async () => {
    do {
      draft.historyAgain = false;
      const epoch = draft.historyEpoch ?? 0;
      try {
        const key = queries.history({ id }).queryKey;
        const cached = queryClient.getQueryData<HistoryView>(key);
        const view = await queries.history({ id, revision: cached ? draft.revision : undefined, limit: historyPage }).queryFn({ signal });
        signal?.throwIfAborted();
        if (epoch !== (draft.historyEpoch ?? 0)) { draft.historyAgain = true; continue; }
        // Earlier parent entries are settled, but their comparisons can still be live.
        let earlierRefreshed = false;
        for (const page of draft.earlierShadows ?? []) {
          if (!page.shadows.some((binding) => binding.turns.some((turn) => !turn.terminal))) continue;
          const known = new Set(page.shadows.flatMap((binding) => binding.turns.map((turn) => turn.order)));
          for (let before = page.before; BigInt(before) > BigInt(page.from);) {
            const earlier = await queries.history({ id, before, limit: historyPage }).queryFn({ signal });
            signal?.throwIfAborted();
            if (epoch !== (draft.historyEpoch ?? 0)) { draft.historyAgain = true; break; }
            page.shadows = mergeShadowBindings(page.shadows, (earlier.shadows ?? []).map((binding) => ({ ...binding, turns: binding.turns.filter((turn) => known.has(turn.order)) })).filter((binding) => binding.turns.length));
            earlierRefreshed = true;
            before = earlier.start;
          }
        }
        if (epoch !== (draft.historyEpoch ?? 0)) continue;
        if (!view.reset && view.revision === draft.revision
          && draft.busy === (view.running || draft.lines.some((line) => !line.entryKey && line.role === "user" && !line.complete))
          && draft.terminal === view.terminal && JSON.stringify(draft.origin) === JSON.stringify(view.origin)
          && JSON.stringify(cached?.delegations) === JSON.stringify(view.delegations)
          && JSON.stringify(cached?.reviewRationales) === JSON.stringify(view.reviewRationales)
          && draft.movable === view.movable && JSON.stringify(draft.jobs) === JSON.stringify(view.backgroundJobs)
          && JSON.stringify(draft.questions) === JSON.stringify(view.pendingQuestions)
          && !draft.historyError) { if (earlierRefreshed) onDraftChange(); continue; }
        if (applyHistoryDelta(draft, view)) void queryClient.invalidateQueries({ queryKey: ["queue"] });
        // Main's delegation panel reads a full durable parent view from this cache.
        const changed = new Set([...view.replacedKeys, ...view.removedKeys]);
        const groups = Map.groupBy([...(view.reset ? [] : cached?.messages ?? []).filter((message) => !changed.has(message.entryKey)), ...view.messages], (message) => message.entryKey);
        queryClient.setQueryData<HistoryView>(key, { ...view, reset: true, replacedKeys: [], removedKeys: [], messages: view.entryKeys.flatMap((entry) => groups.get(entry) ?? []) });
      } catch (err) {
        if (signal?.aborted) throw err;
        // A history failure must not turn an accepted Prompt into a failed send.
        draft.missing = err instanceof RPCError && err.code === 5;
        if (!draft.missing) captureException(err);
        draft.historyError = err instanceof Error ? err.message : "history failed";
      }
      onDraftChange();
    } while (draft.historyAgain);
  })().finally(() => { draft.historyRead = undefined; });
  return draft.historyRead;
}

const historyPage = 50;

// Prepends one settled page: the previous historyPage entries, or every entry from a linked message onward.
function readEarlierHistory(id: string, draft: ComposerDraft, onDraftChange: () => void, from?: string, signal?: AbortSignal): Promise<void> {
  if (signal && draft.earlier) return draft.earlier.catch(() => {}).then(() => {
    signal.throwIfAborted();
    return readEarlierHistory(id, draft, onDraftChange, from, signal);
  });
  const before = draft.start;
  const epoch = draft.historyEpoch ?? 0;
  if (draft.earlier || !draft.more || !before) return draft.earlier ?? Promise.resolve();
  draft.earlier = (async () => {
    try {
      const view = await queries.history({ id, before, ...(from ? { from } : { limit: historyPage }) }).queryFn({ signal });
      signal?.throwIfAborted();
      if (draft.start === before && epoch === (draft.historyEpoch ?? 0)) {
        const known = new Set(draft.lines.map((line) => line.entryKey));
        draft.lines = [...historyLines(view.messages).filter((line) => !known.has(line.entryKey)), ...draft.lines];
        draft.start = view.start;
        draft.more = view.more;
        draft.delegations = [...new Set([...(draft.delegations ?? []), ...view.delegations])];
        draft.reviewRationales = { ...draft.reviewRationales, ...view.reviewRationales };
        (draft.earlierShadows ??= []).push({ from: view.start, before, shadows: view.shadows ?? [] });
      }
    } catch (err) {
      if (signal?.aborted) throw err;
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
  for (let group = node.parentElement?.closest('[data-slot="trace-collapsible"]'); group; group = group.parentElement?.closest('[data-slot="trace-collapsible"]')) {
    const trigger = group.querySelector<HTMLElement>(':scope > [data-slot="trace-collapsible-trigger"]')!;
    if (trigger.getAttribute("aria-expanded") === "false") trigger.click();
  }
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
  const refreshHistory = useCallback(() => readHistoryDelta(draft.sessionId, draft, onDraftChange), [draft, onDraftChange]);
  const loadEarlier = useCallback((from?: string) => readEarlierHistory(draft.sessionId, draft, onDraftChange, from), [draft, onDraftChange]);
  const followed = history.data, earlier = draft.delegations, earlierReviews = draft.reviewRationales;
  const delegations = useMemo(() => ({ delegations: [...new Set([...(followed?.delegations ?? []), ...(earlier ?? [])])], reviewRationales: { ...earlierReviews, ...followed?.reviewRationales } }), [followed, earlier, earlierReviews]);
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
  return { busy: draft.busy, setBusy, lines: draft.lines, setLines, refreshHistory, opening: id !== "" && !draft.revision, historyError: draft.historyError, origin: draft.origin, terminal: draft.terminal, delegations, shadows: mergeShadowBindings((draft.earlierShadows ?? []).reduce<ShadowBinding[]>((bindings, page) => mergeShadowBindings(bindings, page.shadows), []), draft.shadows ?? []), more: draft.more ?? false, start: draft.start, loadEarlier, movable: draft.movable ?? false, hasSandboxed: draft.lines.some((line) => line.origin === "sandboxed") };
}

function mergeShadowBindings(earlier: ShadowBinding[], followed: ShadowBinding[]) {
  const bindings = new Map(earlier.map((binding) => [binding.childConversationId, binding]));
  for (const binding of followed) {
    const turns = new Map((bindings.get(binding.childConversationId)?.turns ?? []).map((turn) => [turn.order, turn]));
    for (const turn of binding.turns) turns.set(turn.order, turn);
    bindings.set(binding.childConversationId, { ...binding, turns: [...turns.values()].sort((a, b) => a.order.length - b.order.length || a.order.localeCompare(b.order)) });
  }
  return [...bindings.values()];
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
  // This Transcript stays mounted, hidden, on other pages; a created chat opens once the new-chat page shows again.
  useLayoutEffect(() => {
    if (location.pathname === "/" && id === "" && draft.sessionId !== "" && drafts.get("") === draft) {
      drafts.delete("");
      onCreated(draft.sessionId);
      route.goSession(draft.sessionId);
    }
  });
  const { busy, setBusy, lines, setLines, refreshHistory, opening, historyError, origin, terminal, delegations, shadows, more, start, loadEarlier, movable, hasSandboxed } = useSessionStream(id, draft, onDraftChange);
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
  const latest = lines.findLast((line) => line.entryKey)?.entryKey;
  const floor = draft.revertFloor;
  const revert = useMemo(() => ({ available, pending, latest, floor, run: runRevert }), [available, pending, latest, floor, runRevert]);
  const error = [scopeError, historyError].find(Boolean);
  const followed = previewing ? { lines: previewLines ?? lines, delegations: preview.data, busy: false, more: false, terminal: undefined, shadows: undefined } : { lines, delegations, busy, more, terminal, shadows };
  const queue = useQuery({ ...queries.queue({ id }), enabled: id !== "", refetchInterval: 2000 });
  const { queued, parked } = pendingInputs(draft, queue.data ?? []);
  // Pending steers and errors end the transcript so the origin switcher stays next to the composer.
  const footer = <>
    {parked.length > 0 && !previewing ? <section aria-label="Pending steers" className="mx-auto w-full max-w-3xl px-3"><p className="text-xs text-muted-foreground">Waiting to steer</p>{parked.map((line) => <TranscriptLine key={line.id} line={line} conversationId={id} hasSandboxed={false} />)}</section> : null}
    {error ? <p role="alert" className="px-3 text-sm text-destructive">{error}</p> : null}
  </>;
  // A chat with no record has nothing to show or send to.
  if (draft.missing) return <p role="status" className="m-auto p-6 text-sm text-muted-foreground">This chat doesn't exist. <Link href="/" className="underline">Start a new chat</Link></p>;
  return (
    <RevertActions value={revert}>
      <Delegations value={followed.delegations}><TranscriptLog conversationId={id} lines={followed.lines} opening={opening} working={followed.busy} terminal={followed.terminal} origin={origin} filter={visibleFilter} hasSandboxed={hasSandboxed} more={followed.more} loadEarlier={loadEarlier} movable={movable} refreshHistory={refreshHistory} shadows={followed.shadows} footer={footer}>
      {hasSandboxed ? <ButtonGroup aria-label="Show messages from">
        {(["sandboxed", "canonical"] as const).map((choice) => (
          <Button key={choice} type="button" size="xs" className="relative before:absolute before:-inset-y-2.5 before:inset-x-0" variant={visibleFilter[choice] ? "default" : "outline"} aria-pressed={visibleFilter[choice]} onClick={() => {
            if (matchedOrigin === choice) navigate(sessionPath(id));
            setFilter((current) => ({ ...current, [choice]: !visibleFilter[choice] }));
          }}>
            {choice}
          </Button>
        ))}
      </ButtonGroup> : null}
      </TranscriptLog></Delegations>
      <fieldset disabled={[opening, previewing, !draft.hydrated].some(Boolean)} className={previewing ? "hidden" : "contents"}>
        <SessionComposer id={id} draft={draft} drafts={drafts} onDraftChange={onDraftChange} busy={busy} setBusy={setBusy} setLines={setLines} refreshHistory={refreshHistory} queued={queued} />
      </fieldset>
    </RevertActions>
  );
}

// Each delegation the panel shows scrolls on its own, as the conversation does. One opened at an
// approver's answer stays there rather than following its end.
function DelegationPanel({ id }: { id: string }) {
  const search = new URLSearchParams(useSearch());
  const child = search.get("delegation") ?? "";
  return child ? <MessageScrollerProvider key={child} autoScroll={!search.has("reason")} scrollEdgeThreshold={48}><DelegationTranscript id={id} child={child} /></MessageScrollerProvider> : null;
}

// A delegation opens scrolled all the way down, or, for a review opened from a reason, at the
// approver's answer that gave it: the latest saved under its review turn, else the first giving it.
function DelegationTranscript({ id, child }: { id: string; child: string }) {
  const search = new URLSearchParams(useSearch());
  const reason = search.get("reason"), turn = search.get("answer");
  const main = useQuery({ ...queries.history({ id }), enabled: false });
  const history = useQuery(queries.history({ id: child }));
  // Lineage runs from the chat down to child; it is child alone until child's history loads.
  const lineage = useMemo(() => history.data?.lineage ?? [{ id: child }], [history.data, child]);
  useEffect(() => {
    if (history.data && main.data?.revertMessageId && !main.data.delegations.some((level) => lineage.some((at) => at.id === level))) navigate(panelHref());
  }, [history.data, lineage, main.data]);
  const lines = historyLines(history.data?.messages ?? []);
  const turns = transcriptTurns(lines);
  const answers = reason === null ? [] : lines.flatMap((line) => {
    try {
      const rationale: unknown = line.role === "assistant" && JSON.parse(line.text).rationale;
      return typeof rationale === "string" ? [{ line, rationale }] : [];
    } catch {
      return []; // Only an approver's answer is JSON.
    }
  });
  const target = (answers.findLast((answer) => answer.line.turnId === turn) ?? answers.find((answer) => answer.rationale === reason))?.line;
  const targetTurn = target && turns.find((turn) => turn.items.includes(target));
  const targetKey = targetTurn && turnKey(targetTurn);
  const { scrollToMessage } = useMessageScroller();
  // An answer ends its review's turn, so the turn's end shows it, clear of the viewport's bottom fade.
  useEffect(() => {
    if (targetKey) scrollToMessage(`turn-${targetKey}`, { align: "end", behavior: "instant", scrollMargin: 64 });
  }, [targetKey, scrollToMessage]);
  const opened = new Set(historyLines(main.data?.messages ?? []).map((line) => line.delegation));
  // The breadcrumbs start at the level a row of the chat opens, else at child.
  const first = lineage.findIndex((level) => opened.has(level.id));
  const levels = lineage.slice(first < 0 ? -1 : first).map(({ id: level, kind, agent }, index, all) => {
    const parent = queryClient.getQueryData<HistoryView>(queries.history({ id: index ? all[index - 1].id : id }).queryKey);
    const row = historyLines(parent?.messages ?? []).find((line) => line.toolName && [line.delegation, line.review].includes(level));
    return { level, label: row ? toolTitle(row) : kind === "REVIEW" ? "Review" : agent || level };
  });
  const parent = levels.at(-2);
  const sandboxed = lines.some((line) => line.origin === "sandboxed");
  const close = () => navigate(panelHref());
  const body = <>
    <header className="flex shrink-0 items-center gap-1 border-b p-2 text-xs lg:pr-12">
      <nav aria-label="Delegation breadcrumbs" className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden whitespace-nowrap">
        <Link href={panelHref()} className="shrink-0 hover:underline">Conversation</Link>
        {/* Only the current level shortens, down to a few characters; the levels before it keep up to 10rem each. */}
        {levels.map(({ level, label }, index) => <span key={level} className={cn("flex min-w-0 items-center gap-1", index === levels.length - 1 ? "min-w-16" : "max-w-40 shrink-0")}><span aria-hidden="true">/</span>{index === levels.length - 1 ? <span aria-current="page" title={label} className="min-w-0 truncate font-medium">{label}</span> : <Link href={panelHref(level)} title={label} className="min-w-0 truncate hover:underline">{label}</Link>}</span>)}
      </nav>
      <Button variant="ghost" size="icon-sm" aria-label="Close delegation" onClick={close}><X /></Button>
    </header>
    <MessageScroller className="min-h-0 flex-1">
    <MessageScrollerViewport className="overflow-x-hidden [overflow-anchor:none]"><div className="min-h-full p-3" data-delegation-transcript>
      {/* A subagent is a session, so its own Background Jobs list and stop here as a chat's do. */}
      {history.data?.backgroundJobs.length ? <BackgroundJobs id={child} jobs={history.data.backgroundJobs} onChange={history.refetch} openCall={(callId) => document.querySelector(`[data-delegation-transcript] [data-tool-call-id="${CSS.escape(callId)}"]`)?.scrollIntoView({ block: "center" })} /> : null}
      {history.isPending ? <p role="status" className="text-sm text-muted-foreground">Loading delegation…</p> : null}
      {history.error ? <p role="alert" className="text-sm text-destructive">{history.error.message}</p> : null}
      {history.data?.messages.length === 0 ? <p role="status" className="text-sm text-muted-foreground">No saved transcript</p> : null}
      <Delegations value={history.data}><MessageScrollerContent>
        {turns.map((turn) => <MessageScrollerItem key={turnKey(turn)} messageId={`turn-${turnKey(turn)}`}>
          <TranscriptLines rows={[...turn.user, ...turn.items].map((line) => ({ kind: "line", line }))} conversationId={child} hasSandboxed={sandboxed} />
        </MessageScrollerItem>)}
      </MessageScrollerContent></Delegations>
      {history.isSuccess ? <Link href={panelHref(parent?.level)} className="block w-fit text-xs text-muted-foreground underline hover:text-foreground">{parent ? `Back to ${parent.label}` : "Back to conversation"}</Link> : null}
    </div></MessageScrollerViewport>
    <MessageScrollerButton aria-label="Scroll to latest" title="Scroll to latest" behavior="instant" />
    </MessageScroller>
  </>;
  return <SidePanel label="Delegation" close={close}>{body}</SidePanel>;
}

function SidePanel({ label, close, children }: { label: string; close: () => void; children: ReactNode }) {
  const wide = useMedia("(min-width: 64rem)");
  return wide ? <ResizableAside side="delegation" aria-label={label} className="flex flex-col border-l bg-background">{children}</ResizableAside> : <Sheet open onOpenChange={(open) => { if (!open) close(); }}>
    <SheetContent side="right" showCloseButton={false} className="data-[side=right]:w-full data-[side=right]:sm:max-w-none"><SheetTitle className="sr-only">{label}</SheetTitle>{children}</SheetContent>
  </Sheet>;
}

function SkillPanel() {
  const name = new URLSearchParams(useSearch()).get("skill") ?? "";
  if (!name) return null;
  const close = () => navigate(panelHref());
  return <SidePanel label="Skill" close={close}>
    <header className="flex shrink-0 items-center gap-2 border-b p-2 text-xs lg:pr-12">
      <span className="min-w-0 flex-1 truncate font-medium">{name}</span>
      <Link href={`/skills/${name}`} className="shrink-0 underline hover:text-foreground">Open page</Link>
      <Button variant="ghost" size="icon-sm" aria-label="Close skill" onClick={close}><X /></Button>
    </header>
    <ScrollArea className="min-h-0 flex-1"><div className="p-3"><SkillView name={name} /></div></ScrollArea>
  </SidePanel>;
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
      firstMessages.set(sessionId, optimistic.text.trim().split("\n", 1)[0]);
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
    draft.focus = (draft.focus ?? 0) + 1;
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

// The newest turn's last request when that turn has not saved it, named by its input ID.
function unsavedRequest(lines: Line[]) {
  const latest = lines.findLast((line) => line.entryKey)?.entryKey;
  return lines.findLast((line) => line.role === "user" && line.entryKey === latest && !line.messageId)?.inputId || undefined;
}

// OpenCode V2 packages/app/src/session/revert.ts: only the dispatched session
// owns restored content. Redo changes history, never the composer. Undo starts
// from the newest request, even one its running turn has not saved yet.
async function revertComposer(draft: ComposerDraft, messageId: string | undefined, redo: boolean, refresh: () => Promise<unknown>, changed: () => void) {
  if (draft.reverting || draft.sending) return;
  if (!redo && !draft.revertMessageId) messageId ??= unsavedRequest(draft.lines);
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
  queued,
}: {
  id: string;
  draft: ComposerDraft;
  drafts: Map<string, ComposerDraft>;
  onDraftChange: () => void;
  busy: boolean;
  setBusy: (value: boolean) => void;
  setLines: (update: (current: Line[]) => Line[]) => void;
  refreshHistory: () => Promise<unknown>;
  queued: QueueItem[];
}) {
  const identity = useQuery(queries.identity());
  const { setCommand } = useContext(SessionCommands);
  const { promote } = useContext(TabActions);
  const scope = useContext(DraftScope)!;
  const revert = useContext(RevertActions)!;
  const { scrollToEnd, scrollToMessage } = useMessageScroller();
  const prompt = useMutation({ mutationFn: mutations.prompt, onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["queue"] }); } });
  const agents = useQuery({ ...queries.agents({ conversationId: id }), refetchInterval: 2000 });
  const removeQueueItem = useMutation({ mutationFn: mutations.removeQueueItem, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const steerQueueItem = useMutation({ mutationFn: mutations.steerQueueItem, onSuccess: () => queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const popQueueItem = useMutation({ mutationFn: mutations.popQueueItem, onSuccess: () => queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const reorderQueue = useMutation({ mutationFn: mutations.reorderQueue, onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["queue"] }) });
  const invalidateSidebar = useContext(SidebarInvalidation);
  // A session whose agent names no space is born in the space the person is in.
  const space = useContext(TabActions).state.space;
  const create = useMutation({ mutationFn: (input: { agent?: string }) => mutations.createSession({ ...input, spaceId: space }), onSuccess: invalidateSidebar });
  const { text, files, sending, agent } = draft;
  const messageInput = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const input = messageInput.current!;
    if (!draft.focus || id !== draft.sessionId || input.matches(":disabled")) return;
    draft.focus = undefined;
    if (document.activeElement === document.body || document.activeElement === input) { input.focus(); input.setSelectionRange(input.value.length, input.value.length); }
  });
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
  const voice = useOwnVoice(id);
  const sendError = draft.error || voice.error;
  // Clearing the send error also dismisses this composer's failed call.
  const setSendError = (value: string) => { draft.error = value; onDraftChange(); if (!value && voice.state === "error") stopVoice(); };
  const currentAgent = id === "" ? "main" : agents.data?.currentAgent ?? "";
  // A new session offers every agent; the server narrows an existing one to its agent's space.
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
  const question = useQuestion(id, draft, refreshHistory, setSendError);
  const { placeholder, sendLabel } = composerLabels(question.pending, busy, id);
  const goSession = (sessionId: string) => {
    void draftContent(JSON.stringify([scope, ""]), { text: "", files: [], agent: "" }).catch(() => { draft.persistenceError = "Draft could not be saved locally. Keep this page open."; onDraftChange(); });
    draft.sessionId = sessionId;
    draft.persistenceKey = JSON.stringify([scope, sessionId]);
    draft.persistedEdit = undefined;
    drafts.set(sessionId, draft);
    onDraftChange();
  };
  // A call from a new session creates it with the shown agent, as Send does.
  const call = async (speaker: string) => {
    let sessionId = draft.sessionId;
    try {
      if (sessionId === "") {
        promote("/");
        sessionId = await create.mutateAsync({ agent: selected });
        goSession(sessionId);
      }
    } catch (err) {
      return setSendError(err instanceof Error ? err.message : "create failed");
    }
    void startVoice(sessionId, speaker);
  };
  const send = (delivery?: PromptDelivery) => {
    const command = /^\p{White_Space}*\$(fork|handoff|undo|redo)\p{White_Space}*$/u.exec(draft.text);
    if (delivery !== "STASH" && command) {
      if (["undo", "redo"].includes(command[1])) { setText(""); return revert.run(undefined, command[1] === "redo"); }
      if (!id) { setSendError("Open a session first."); return Promise.resolve(); }
      setCommand({ mode: command[1] as "fork" | "handoff", source: id });
      setText("");
      return Promise.resolve();
    }
    // A pending question takes Send's text as its custom answer instead of queueing it behind the waiting turn.
    const answering = question.take(delivery, setText);
    if (answering) return answering;
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
      goSession,
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
      <Composer
        files={files}
        setFiles={setFiles}
        sending={[sending, draft.reverting, question.answering, create.isPending].some(Boolean)}
        messageInput={messageInput}
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
        sendLabel={sendLabel}
        queued={queued}
        send={send}
        stop={stop}
        call={call}
        steerQueued={promoteQueued}
        popQueued={(itemId) => popComposer({ draft, onDraftChange, id, itemId, selected, currentAgent, prompt, popQueueItem, setSendError })}
        removeQueued={(itemId) => void removeQueueItem.mutateAsync({ id, itemId }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "remove failed"))}
        reorderQueued={(itemIds) => void reorderQueue.mutateAsync({ id, itemIds }).catch((err: unknown) => setSendError(err instanceof Error ? err.message : "reorder failed"))}
        jobs={jobsSlot(id, draft.jobs, refreshHistory, (callId) => void openCall(draft, onDraftChange, scrollToMessage, callId), question.card)}
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
  messageInput,
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
  sendLabel,
  queued,
  send,
  stop,
  call,
  steerQueued,
  popQueued,
  removeQueued,
  reorderQueued,
  jobs,
}: {
  files: PendingFile[];
  setFiles: (files: PendingFile[]) => void;
  sending: boolean;
  messageInput: React.RefObject<HTMLTextAreaElement | null>;
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
  sendLabel: string;
  queued: QueueItem[];
  send: (delivery?: PromptDelivery) => Promise<void>;
  stop: () => Promise<void>;
  call: (speaker: string) => Promise<void>;
  steerQueued: (id: string) => Promise<void>;
  popQueued: (id: string) => Promise<unknown>;
  removeQueued: (id: string) => void;
  reorderQueued: (itemIds: string[]) => void;
  jobs: ReactNode;
}) {
  const mac = typeof navigator === "object" && /(Mac|iPod|iPhone|iPad)/.test(navigator.platform);
  const selectedButton = useRef<HTMLButtonElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const { composer } = useContext(SessionCommands);
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
        <QueuePanel key={sessionId} conversationId={sessionId} items={queued} busy={busy} sending={sending} onSteer={steerQueued} onPop={popQueued} onRemove={removeQueued} onReorder={reorderQueued} jobs={jobs} />
        <PromptInput files={files} setFiles={setFiles} sending={sending} fileInput={fileInput}>
          <InputGroupTextarea
            ref={messageInput}
            disabled={sending}
            value={text}
            onChange={(event) => {
              setDollarOff(false);
              setAgentOpen(false);
              setText(event.target.value);
            }}
            placeholder={placeholder}
            className="min-h-16 max-h-[40dvh] overflow-y-auto sm:mt-2 sm:min-h-[52px] sm:w-[calc(100%-0.75rem)]"
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
          <InputGroupAddon align="block-end" className="sm:mt-0.5">
            <Tooltip><TooltipTrigger render={<InputGroupButton size="icon-sm" className="size-11 sm:-ml-0.5 sm:size-7" disabled={sending} />} aria-label="Add files" onClick={() => fileInput.current?.click()}><Plus /></TooltipTrigger><TooltipContent>Add files</TooltipContent></Tooltip>
            <AgentPicker catalog={catalog} selected={selected} setAgent={setAgent} open={agentOpen} setOpen={setAgentOpen} disabled={sending || catalog.length === 0} />
            <InputGroupButton size="sm" variant={steerOff ? "quiet" : "ghost"} className="ml-auto h-11 sm:h-7" disabled={sending || empty} onClick={() => void send("STASH")}>Stash</InputGroupButton>
            <Tooltip>
              <TooltipTrigger render={<InputGroupButton size="sm" className="h-11 sm:h-7" disabled={steerOff} />} aria-label="Steer" onClick={() => void send("STEER")}>
                Steer
              </TooltipTrigger>
              <TooltipContent>Guide the current response <ModEnterKeys mac={mac} /></TooltipContent>
            </Tooltip>
            <VoiceToggle sessionId={sessionId} agent={selected} sending={sending} call={call} />
            <Tooltip><TooltipTrigger render={<InputGroupButton size="icon-sm" variant="default" className="size-11 sm:-mr-0.5 sm:size-7" disabled={sending} />} aria-label={stopping ? "Stop" : "Send"} onClick={() => void (stopping ? stop() : send())}>
              {stopping ? <Square /> : <Send />}
            </TooltipTrigger><TooltipContent>{stopping ? "Stop response" : sendLabel}</TooltipContent></Tooltip>
          </InputGroupAddon>
        </PromptInput>
      </div>
    </div>
  );
}

// The agent menu follows Model Selector (https://ui.shadcn.com/docs/ai-elements/base/model-selector):
// one radio menu, each agent with its model and effort. Typing a name's first letters moves to it.
function AgentPicker({ catalog, selected, setAgent, open, setOpen, disabled }: { catalog: Agent[]; selected: string; setAgent: (name: string) => void; open: boolean; setOpen: (open: boolean) => void; disabled: boolean }) {
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger disabled={disabled} aria-label="Choose agent" title={selected} render={<InputGroupButton size="sm" className="min-h-11 min-w-0 max-w-[220px] shrink sm:-ml-1 sm:h-7 sm:min-h-0" />}>
        <Bot /><span className={cn("min-w-0 truncate", !selected && "text-muted-foreground")}>{selected || "Choose agent"}</span><ChevronDown />
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" className="w-[min(28rem,calc(100vw-2rem))]">
        <DropdownMenuRadioGroup value={selected} onValueChange={(name: string) => setAgent(name)}>
          {catalog.map((item) => <DropdownMenuRadioItem key={item.name} value={item.name} label={item.name} closeOnClick className="min-h-11 sm:min-h-8">
            <span className="flex min-w-0 flex-1 flex-col"><span className="break-all">{item.name}</span><span className="break-all text-xs text-muted-foreground">{[item.model, item.reasoning].filter(Boolean).join(" · ")}</span></span>
          </DropdownMenuRadioItem>)}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// The composer follows Prompt Input (https://ui.shadcn.com/docs/ai-elements/base/prompt-input): an Input Group
// whose files show as Attachments above the text, added with + or by dropping or pasting them.
function PromptInput({ files, setFiles, sending, fileInput, children }: {
  files: PendingFile[];
  setFiles: (files: PendingFile[]) => void;
  sending: boolean;
  fileInput: React.RefObject<HTMLInputElement | null>;
  children: ReactNode;
}) {
  const addFiles = (added: File[]) => setFiles([...files, ...added.map((file) => ({ id: crypto.getRandomValues(new Uint32Array(4)).join("-"), file }))]);
  return <><input ref={fileInput} type="file" multiple hidden aria-label="Attach files" disabled={sending} onChange={(event) => { addFiles(Array.from(event.target.files ?? [])); event.target.value = ""; }} />
  <InputGroup onDragOver={(event) => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); }} onDrop={(event) => {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    if (!sending) addFiles(Array.from(event.dataTransfer.files));
  }} onPaste={(event) => {
    if (!event.clipboardData.files.length) return;
    event.preventDefault();
    if (!sending) addFiles(Array.from(event.clipboardData.files));
  }}>
    {files.length ? <InputGroupAddon align="block-start"><AttachmentGroup aria-label="Pending attachments">{files.map(({ id, file }) => <Attachment key={id} size="sm" state={sending ? "uploading" : "idle"}>
      <AttachmentMedia><FileIcon /></AttachmentMedia>
      <AttachmentContent><AttachmentTitle>{file.name}</AttachmentTitle><AttachmentDescription>{sending ? "Sending…" : `${file.size} bytes`}</AttachmentDescription></AttachmentContent>
      <AttachmentActions><AttachmentAction disabled={sending} aria-label={`Remove ${file.name}`} onClick={() => setFiles(files.filter((pending) => pending.id !== id))}><X /></AttachmentAction></AttachmentActions>
    </Attachment>)}</AttachmentGroup></InputGroupAddon> : null}
    {children}
  </InputGroup></>;
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
  const [position, setPosition] = useState<{ left: number; top: number } | null>(null);
  const id = useId();
  function showTooltip(event: SyntheticEvent<HTMLElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    setPosition({ left: Math.max(8, Math.min(bounds.left, window.innerWidth - 232)), top: bounds.bottom });
  }
  return (
    <span className={cn("absolute top-0 h-4 w-3", position && "z-50")} style={{ left: `calc(${pct}% - 0.375rem)` }} onPointerEnter={(event) => { if (event.pointerType === "mouse") showTooltip(event); }} onPointerLeave={() => setPosition(null)}>
      <button type="button" aria-label={label} aria-describedby={position ? id : undefined} className="flex h-4 w-3 items-center justify-center rounded-sm focus-visible:outline-2"
        onFocus={showTooltip} onBlur={() => setPosition(null)} onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); setPosition(null); } }} onClick={(event) => { showTooltip(event); onClick?.(); }}>
        <span className={ran ? "h-3 w-2 rounded-sm bg-cyan-600 dark:bg-cyan-400" : "h-3 w-2 rounded-sm border-2 border-amber-600 dark:border-amber-400"} />
      </button>
      {position ? <span id={id} role="tooltip" style={position} className="fixed z-50 w-max max-w-56 break-words rounded-md border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md">{tooltip}</span> : null}
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
        <TranscriptLines rows={previewLines.map((line) => ({ kind: "line", line }))} conversationId={conversationId} hasSandboxed={hasSandboxed} />
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
  const inSpace = useInSpace();
  // Runs carry no space, so a job outside this space hides its runs too.
  const away = new Set((jobs.data ?? []).filter((job) => !inSpace(job)).map((job) => job.stem));
  const rows = (jobs.data ?? []).filter((job) => !away.has(job.stem));
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
                run.mutate({ stem: confirmStem }, { onSuccess: (id, { stem }) => { if (id === "") return; firstMessages.set(id, stem); route.goSession(id); } });
                setConfirmStem("");
              }}>Run</Button>
            </div>
          </DialogContent>
      </Dialog>
    </div>
  );
}

function VoicePage() {
  const identity = useQuery(queries.identity());
  if (!identity.data) return null;
  return <VoiceCall key={identity.data.username} username={identity.data.username} />;
}

// useOwnVoice is the tab's one call as seen from conversation id: idle unless the call is on id.
function useOwnVoice(id: string): VoiceView {
  const voice = useVoice();
  return voice.conversationId === id ? voice : { state: "idle", conversationId: id, error: "", notice: "", captions: [], sticky: voice.sticky };
}

function VoiceClock({ startedAt }: { startedAt: number }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const seconds = Math.max(0, Math.floor((now - startedAt) / 1000));
  return ` · ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

// VoiceOrb is the Voice page's call button. It ebbs and flows while a call is active,
// and swells with the voice model's speech through --level, set every frame.
function VoiceOrb({ state, ...props }: { state: VoiceState } & ComponentProps<"button">) {
  const orb = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const element = orb.current;
    if (state !== "live" || !element) return;
    let frame = 0, level = 0;
    const tick = () => {
      level = Math.max(voiceLevel(), level * 0.9); // Rises at once, falls slowly.
      element.style.setProperty("--level", level.toFixed(3));
      frame = requestAnimationFrame(tick);
    };
    tick();
    return () => {
      cancelAnimationFrame(frame);
      element.style.setProperty("--level", "0");
    };
  }, [state]);
  return <button ref={orb} type="button" data-state={state} className="voice-orb" {...props}><span aria-hidden className="voice-orb-core" /><span aria-hidden className="voice-orb-rings" /></button>;
}

// VoicePicker chooses among the voices an agent's provider offers.
function VoicePicker({ voices, value, disabled, onPick }: { voices: string[]; value: string; disabled: boolean; onPick: (voice: string) => void }) {
  const items = voices.map((name) => ({ value: name, label: name.charAt(0).toUpperCase() + name.slice(1) }));
  return <Select items={items} value={value} disabled={disabled} onValueChange={(name) => { if (typeof name === "string") onPick(name); }}>
    <SelectTrigger aria-label="Choose voice" className="data-[size=default]:h-11 sm:data-[size=default]:h-8"><SelectValue /></SelectTrigger>
    <SelectContent><SelectGroup>{items.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectGroup></SelectContent>
  </Select>;
}

// VoiceTranscript shows the call's captions as a chat, following the newest line.
function VoiceTranscript({ captions }: { captions: VoiceCaption[] }) {
  const scroller = useRef<HTMLElement>(null);
  useEffect(() => { scroller.current?.scrollTo({ top: scroller.current.scrollHeight }); }, [captions]);
  return <section ref={scroller} aria-label="Transcript" className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto text-sm">
    {captions.length === 0 ? <p className="m-auto text-muted-foreground">Nothing said yet.</p> : captions.map(({ id, role, text, written }) => written
      // A reply too long to speak, formatted as in the conversation.
      ? <div key={id} className="max-w-[85%] self-start rounded-2xl bg-muted px-3 py-2 whitespace-pre-wrap"><span className="sr-only">Agent: </span><TranscriptText text={text} /></div>
      : <p key={id} className={cn("max-w-[85%] rounded-2xl px-3 py-2 whitespace-pre-wrap", role === "user" ? "self-end bg-primary text-primary-foreground" : "self-start bg-muted")}><span className="sr-only">{role === "user" ? "You: " : "Voice: "}</span>{text}</p>
    )}
  </section>;
}

const voiceStatus: Record<VoiceState, string> = { idle: "Tap to talk", connecting: "Connecting…", reconnecting: "Reconnecting…", live: "Listening", ending: "Ending…", error: "Tap to try again" };

// VoiceStage is the orb with the call's status under it: big and centered, or small
// at the bottom beside the transcript. It is one orb either way, so it shrinks rather than reappearing.
// A call starts only once the agents have loaded, since they decide its voice; hanging up never waits.
function VoiceStage({ voice, compact, busy, loading, error, onPress }: { voice: VoiceView; compact: boolean; busy: boolean; loading: boolean; error?: string; onPress: () => void }) {
  const active = voice.state !== "idle" && voice.state !== "error";
  return <div className={cn("flex flex-col items-center", compact ? "shrink-0 gap-2" : "flex-1 justify-center gap-10 py-10")}>
    <VoiceOrb state={voice.state === "reconnecting" ? "connecting" : voice.state} data-size={compact ? "small" : "large"} aria-label={active ? "End voice call" : "Start voice call"} aria-pressed={active} disabled={busy || voice.state === "ending" || (!active && loading)} onClick={onPress} />
    <span role="status" aria-live="polite" className="text-sm tracking-wide text-muted-foreground">{voiceStatus[voice.state]}{voice.state === "live" ? <VoiceClock startedAt={voice.startedAt!} /> : null}</span>
    <span aria-live="polite" className="text-center text-sm">{voice.notice}</span>
    {error ? <p role="alert" className="text-center text-sm text-destructive">{error}</p> : null}
  </div>;
}

// useVoiceSettings is the person's voice conversation and settings, kept by the server and
// the same on every device; update shows a change at once, keeps the server's answer, and
// puts the old value back if the server refuses it. The page shows its own call's
// conversation while that call runs, though another device moves the current one: changing
// the shown id hangs the call up. A call that ended with an error keeps its message only.
function useVoiceSettings(username: string) {
  const settings = useQuery(queries.voiceSettings(username));
  const saved = settings.data ?? { conversationId: "", agent: "", voice: "", transcript: false };
  const save = (next: VoiceSettings) => queryClient.setQueryData(queries.voiceSettings(username).queryKey, next);
  const version = useRef({ agent: 0, voice: 0, transcript: 0 });
  const update = useMutation({
    mutationFn: (change: Partial<Omit<VoiceSettings, "conversationId">>) => mutations.voiceSettings({ action: "UPDATE", ...change }),
    onMutate: (change) => {
      for (const key of ["agent", "voice", "transcript"] as const) if (key in change) version.current[key]++;
      save({ ...saved, ...change });
      return { saved, version: { ...version.current } };
    },
    onError: (_error, _change, before) => save(before!.saved),
    // Keep newer local changes, while accepting replies to independent settings.
    onSuccess: (next, _change, before) => {
      const current = queryClient.getQueryData<VoiceSettings>(queries.voiceSettings(username).queryKey)!;
      save({ ...next,
        agent: before.version.agent === version.current.agent ? next.agent : current.agent,
        voice: before.version.voice === version.current.voice ? next.voice : current.voice,
        transcript: before.version.transcript === version.current.transcript ? next.transcript : current.transcript,
      });
    },
  });
  const [callId, setCallId] = useState("");
  const call = useOwnVoice(callId);
  const id = ["connecting", "reconnecting", "live", "ending"].includes(call.state) ? callId : saved.conversationId;
  // The page owns its call: Reset and leaving the page hang it up.
  useEffect(() => () => { if (voiceView().conversationId === id && voiceView().state !== "error") stopVoice(); }, [id]);
  // Until the person's settings load, Start would create a conversation with defaults that overwrite them.
  return { saved, save, update, id, setCallId, agent: saved.agent || "main", loading: settings.isPending, problem: call.error || settings.error?.message };
}

function VoiceCall({ username }: { username: string }) {
  const sidebar = useContext(Sidebar);
  const { saved, save, update, id, setCallId, agent, loading, problem } = useVoiceSettings(username);
  const voice = useOwnVoice(id);
  const [picking, setPicking] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const { mutate: saveSetting } = update;
  const transcript = saved.transcript;
  // A reply too long to speak turns the transcript on, as the button does, so it is on screen.
  useEffect(() => {
    const newest = () => voiceView().conversationId === id ? voiceView().captions.findLast((caption) => caption.written) : undefined;
    let shown = newest();
    return subscribeVoice(() => {
      const written = newest();
      if (written === shown) return;
      shown = written;
      if (written) saveSetting({ transcript: true }); // A new call's transcript starts empty.
    });
  }, [id, saveSetting]);
  const agents = useQuery(queries.agents(id ? { conversationId: id } : undefined));
  const catalog = agents.data?.agents ?? [];
  const selected = [id ? agents.data?.currentAgent : agent].find((name) => catalog.some((item) => item.name === name)) ?? catalog[0]?.name ?? "";
  const active = voice.state !== "idle" && voice.state !== "error";
  const agentInfo = catalog.find((item) => item.name === selected);
  const voices = agentInfo?.voices ?? [];
  const speaker = voiceChoice(agentInfo, saved.voice);
  // Leaving cancels a start still creating its conversation.
  const left = useRef(false);
  useEffect(() => () => { left.current = true; setVoiceSticky(false); }, []);
  // Start always asks the server, so a conversation another device reset is never called.
  const start = useMutation({ mutationKey: ["voiceCall", username], mutationFn: async () => {
    const next = await mutations.voiceSettings({ action: "START", agent: selected });
    save(next);
    sidebar.invalidateQueries();
    if (left.current) return;
    setCallId(next.conversationId);
    // Another device's conversation may run an agent whose provider lacks this voice; its default is safe.
    void startVoice(next.conversationId, next.conversationId === id || next.agent === selected ? speaker : "");
  } });
  const pick = useMutation({ mutationFn: async (name: string) => {
    await update.mutateAsync({ agent: name });
    if (!id) return;
    await mutations.prompt({ id, text: `$agent ${name}` });
    await queryClient.invalidateQueries({ queryKey: ["agents"] });
  } });
  // Only this page's call can be live here: the composer ends its call on leaving its session.
  const reset = useMutation({ mutationKey: ["voiceCall", username], mutationFn: async () => {
    stopVoice();
    setCallId("");
    setConfirm(false);
    await mutations.voiceSettings({ action: "RESET", conversationId: id });
    await queryClient.invalidateQueries({ queryKey: ["voiceSettings"] });
  } });
  // Start and Reset never overlap, so a Start cannot land on the conversation a Reset is forgetting.
  const settling = useIsMutating({ mutationKey: ["voiceCall", username] }) > 0;
  const error = [voice.error, problem, ...[start, pick, update, reset].map((mutation) => mutation.error?.message)].find(Boolean);
  return (
    <div className="mx-auto flex h-full min-h-0 w-full max-w-3xl flex-col gap-4 p-4">
      <PageTitle>Voice</PageTitle>
      <div className="flex items-center gap-1.5 sm:gap-2 max-sm:[&>:first-child>svg:first-child]:hidden">
        <AgentPicker catalog={catalog} selected={selected} setAgent={(name) => pick.mutate(name)} open={picking} setOpen={setPicking} disabled={[active, start.isPending, pick.isPending, catalog.length === 0].some(Boolean)} />
        {agentInfo && voices.length > 0 ? <VoicePicker voices={voices} value={speaker} disabled={active || start.isPending} onPick={(name) => update.mutate({ voice: name })} /> : null}
        <StickyToggle />
        <div className="ml-auto flex items-center gap-1">
          <Button variant={transcript ? "secondary" : "ghost"} size="icon" className="size-11 sm:size-8" aria-label="Show transcript" title="Show transcript" aria-pressed={transcript} onClick={() => update.mutate({ transcript: !transcript })}><Captions /></Button>
          {id ? <>
            <Button variant="ghost" size="icon" className="size-11 sm:size-8" role="link" nativeButton={false} render={<Link href={sessionPath(id)} />} aria-label="Open conversation" title="Open conversation"><MessageSquare /></Button>
            <Button variant="ghost" size="icon" className="size-11 sm:size-8" aria-label="Reset" title="Start a new voice conversation" disabled={settling} onClick={() => sidebar.rows.find((row) => row.id === id)?.running ? setConfirm(true) : reset.mutate()}><RotateCcw /></Button>
          </> : null}
        </div>
      </div>
      {transcript ? <VoiceTranscript captions={voice.captions} /> : null}
      <VoiceStage voice={voice} compact={transcript} busy={settling} loading={agents.isPending || loading} error={error} onPress={() => active ? stopVoice() : start.mutate()} />
      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogContent>
          <DialogTitle>Reset voice conversation?</DialogTitle>
          <DialogDescription>The agent is still working. Its turn keeps running in the old conversation.</DialogDescription>
          <div className="mt-6 flex justify-end gap-2">
            <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
            <Button disabled={settling} onClick={() => reset.mutate()}>Reset</Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// StickyToggle makes the tab's call reconnect after network losses until it is hung up.
function StickyToggle() {
  const { sticky } = useVoice();
  return <Button variant={sticky ? "default" : "outline"} size="icon" className="size-11 sm:size-8" aria-label="Sticky" aria-pressed={sticky} title="Sticky: reconnect after a lost connection until you hang up" onClick={() => setVoiceSticky(!sticky)}><Magnet /></Button>;
}

// VoiceToggle calls with the session's agent, or with agent when the session is new.
function VoiceToggle({ sessionId, agent, sending, call }: { sessionId: string; agent: string; sending: boolean; call: (speaker: string) => Promise<void> }) {
  const route = useRoute();
  const { state, notice } = useOwnVoice(sessionId);
  const calling = state !== "idle" && state !== "error";
  const choices = useQuery(queries.agents({ conversationId: sessionId }));
  const identity = useQuery(queries.identity());
  const settings = useQuery({ ...queries.voiceSettings(identity.data?.username ?? ""), enabled: identity.isSuccess });
  const speaker = voiceChoice(choices.data?.agents.find((item) => item.name === (sessionId ? choices.data?.currentAgent : agent)), settings.data?.voice);
  // The session pane stays mounted on page routes, so leaving the session ends its call as unmounting does.
  useEffect(() => () => { if (voiceView().conversationId === sessionId) stopVoice(); }, [route.id, sessionId]);
  return <>
    <span aria-live="polite" title={notice} className="hidden max-w-48 truncate text-xs text-muted-foreground empty:absolute sm:inline">{notice}</span>
    <Tooltip><TooltipTrigger render={<Button type="button" variant={calling ? "secondary" : "ghost"} size="icon" className="size-11 sm:size-7" disabled={!calling && sending} />} aria-label="Voice call" aria-pressed={calling} onClick={() => void (calling ? stopVoice() : call(speaker))}>
      {state === "connecting" ? <LoaderCircle className="animate-spin" /> : calling ? <MicOff /> : <Mic />}
    </TooltipTrigger><TooltipContent>{calling ? `End voice call (${state})` : "Start voice call"}</TooltipContent></Tooltip>
  </>;
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
              {agent.customShell ? (
                <div>
                  <h3 className="text-xs font-medium text-muted-foreground">Shell</h3>
                  <pre className="mt-1 whitespace-pre-wrap text-xs">{agent.customShell}</pre>
                </div>
              ) : null}
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
  const rows = skills.data ?? [];
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Skills</PageTitle>
      {skills.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {skills.error ? <p className="text-sm text-destructive">{skills.error.message}</p> : null}
      {rows.length === 0 && !skills.isLoading && !skills.error ? <p className="text-sm text-muted-foreground">No skills configured.</p> : null}
      {rows.map((skill) => (
        <section key={skill.name} className="border-b pb-4">
          <Link href={`/skills/${skill.name}`} className="block w-full text-left hover:underline">
            <h2 className="text-sm font-medium">{skill.name}</h2>
          </Link>
          <p className="text-xs text-muted-foreground">{[skill.license, skill.compatibility, skill.origin].filter(Boolean).join(" · ")}</p>
          {skill.description ? <p className="mt-1 text-sm leading-relaxed">{skill.description}</p> : null}
        </section>
      ))}
    </div>
  );
}

function SkillPage({ name }: { name: string }) {
  if (!name) return null;
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <Link href="/skills" className="w-fit text-xs text-muted-foreground underline hover:text-foreground">All skills</Link>
      <PageTitle>{name}</PageTitle>
      <SkillView name={name} />
    </div>
  );
}

// The skill's current definition, shown on its page and in the right panel.
function SkillView({ name }: { name: string }) {
  const skills = useQuery({ ...queries.skills(), staleTime: 60_000 });
  const skill = skills.data?.find((item) => item.name === name);
  if (skills.isLoading) return <p role="status" className="text-sm text-muted-foreground">Loading…</p>;
  if (skills.error) return <p role="alert" className="text-sm text-destructive">{skills.error.message}</p>;
  if (!skill) return <p className="text-sm text-muted-foreground">No skill named {name}.</p>;
  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">{[skill.license, skill.compatibility, skill.origin].filter(Boolean).join(" · ")}</p>
      {skill.description ? <p className="text-sm leading-relaxed">{skill.description}</p> : null}
      {skill.content ? <pre className="whitespace-pre-wrap text-xs leading-relaxed">{skill.content}</pre> : null}
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
      <ConfigSection title="Timeline">
        <TimelineDetailCard />
      </ConfigSection>
      {config.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {config.error ? <p className="text-sm text-destructive">{config.error.message}</p> : null}
      {config.data ? <ConfigLoaded view={config.data} /> : null}
    </div>
  );
}
