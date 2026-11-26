"use client";

import { QueryClient, QueryClientProvider, queryOptions, type QueryStatus, useQueries, useQuery, useMutation, useIsMutating, useMutationState } from "@tanstack/react-query";
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
import { queries, queryKeyHash, mutations, listSessions, rpc, searchAll, RPCError } from "./api";
import { draftContent } from "./drafts";
import { holdWakeLock, startVoice, stopVoice, subscribeVoice, useVoice, voiceChoice, voiceInputsPending, voiceLevel, voiceView, type VoiceCaption, type VoiceState, type VoiceView } from "./voice";
import type { ChatOrigin, HistoryView, MessageMatch, PageEntry, PendingQuestion, PromptDelivery, SearchSessionsResponse, ShadowBinding, TreeNode } from "./types";
import { ArrowUp, BellRing, Bot, Brain, Captions, Check, ChevronDown, ChevronRight, CircleAlert, CirclePause, Clock, Copy, Download, Ellipsis, FileIcon, Files, FileText, Folder, FolderOpen, GitFork, GripVertical, History, Hourglass, Inbox, Keyboard, Link2, ListChecks, LoaderCircle, MessageSquare, Mic, MicOff, PanelLeftClose, PanelLeftOpen, PanelRightClose, PanelRightOpen, Play, Plus, ScrollText, Search, Settings, ShieldCheck, Sparkles, Square, SquarePen, SquareTerminal, Timer, Undo2, Users, Wrench, X } from "lucide-react";
import Link, { usePathname, useSearch, useServer, navigate, pagePath, panelHref, rocketclaws, server, serverURL, at as serverPath } from "./navigation";
import { createContext, memo, use, useCallback, useContext, useEffect, useEffectEvent, useId, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type Dispatch, type SetStateAction, type ReactNode, type ReactElement, type SyntheticEvent, type RefObject, type ComponentProps, type CSSProperties } from "react";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Switch } from "@/components/ui/switch";
import { flushSync } from "react-dom";
import { PaletteChooser, setThemeMode, THEME_MODES, ThemeModeChooser, useTheme } from "@/components/theme";
import { CodeBlock, InlineText, MarkdownText, OUTPUT_KEY, TranscriptText, cleanText, copyText, mentionIds, setPreference, useCleanText, usePreference, useSlackNames } from "./transcript-text";
import { navigations, projectTimeline, setTimelineRows, timelineFailure, timelineLevel, timelineLevels, useTimelineDetail, type TimelineRows } from "./timeline-detail";
import { TimelineDetailCard } from "./timeline-detail-card";
import { BehaviorSettings } from "./behavior-settings";
import { ThreadPRContent, ThreadPRWatch, threadGitHubLabel } from "./thread-pr-panel";
import { floorReason, MessageTree } from "./tree-panel";
import { SelectionQuote, type DiffNote, type PRComposer } from "./github-pr-preview";
import type { GitHubPR } from "./github-pr";
import { reportTyping, TypingPill } from "./typing";
import { SlackWarning } from "./slack-warning";
import { Button, buttonVariants } from "@/components/ui/button";
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
import type { Agent, AgentChoices, Attachment as AttachmentMeta, AttentionAction, BackgroundJob, CodeModeCall, ConfigView, ConversationKind, CronJob, QueueItem, ListTab, TabListName, Remote, Session, SavedTabs, SearchPage as SearchPageState, StripLayout, TabAttention, TranscriptEvent, VoiceSettings } from "@/types";
import { decodeSessionId, encodeSessionId } from "@/session-id";
import { AgentEditorPage, BuiltInNote, editAgent } from "@/agent-editor";
import type { MountOptions, Mounted } from "../../oc/host/api";
import { matchesComposerMention, useComposerMentions, type ComposerMention } from "./composer-mentions";
import {
  invalidatePendingSaves,
  loadSavedSessions,
  loadSnapshotGeneration,
  listedRows,
  listedTabs,
  readSessionEnumeration,
  type SavedListing,
  saveCompleteSessions,
  searchIsAuthoritative,
} from "@/session-list";

const queryClient = new QueryClient({
  defaultOptions: { queries: { queryKeyHashFn: queryKeyHash } },
});
// A New space command names in source the server the space is made on.
type SessionCommand = { mode: "fork" | "handoff" | "name" | "tag" | "delete" | "space"; source: string; target?: MessageMatch };
const SessionCommands = createContext<{ command?: SessionCommand; setCommand: Dispatch<SetStateAction<SessionCommand | undefined>>; composer: RefObject<((command: string) => void) | null> }>(null!);

function sessionPath(id: string) {
  return `/s/${encodeSessionId(id)}`;
}

// A remote OpenCode session opens at /oc/, so its path says what it is before the session list arrives.
function rowPath(session: { id: string; kind?: ConversationKind }) {
  return session.kind === "REMOTE_OPENCODE" ? `/oc/${encodeSessionId(session.id)}` : sessionPath(session.id);
}

const messageURL = (path: string, messageID: string) => `${location.origin}${serverPath(`${path}?message=${encodeURIComponent(messageID)}`)}`;

// A row is its chat on its server; a local row keeps its bare ID.
function rowKey(id: string, server?: string) {
  return server ? `${server}/${id}` : id;
}

// The local server has no configured name, so it goes by the page's host: its first label, or a whole IP address.
const serverLabel = (name: string) => name || (/^[\d.]+$|:/.test(location.hostname) ? location.hostname : location.hostname.split(".")[0]);

// A row of this server opens in a tab here; another server's opens on that server, whose tab rules pick its space.
function openRow(tabs: { open: (path: string) => void }, row: Session) {
  if ((row.rocketclaw ?? "") === server()) tabs.open(rowPath(row));
  else navigate(rowPath(row), false, row.rocketclaw ?? "");
}

// pathSession is the session a /s/ or /oc/ path opens, else "".
function pathSession(path: string) {
  return path.startsWith("/s/") ? decodeSessionId(path.slice(3)) : path.startsWith("/oc/") ? decodeSessionId(path.slice(4)) : "";
}

// The tab location this browser tab marked unread while showing it, by rowKey. The session list hears its view as held,
// which keeps the mark, until the page shows another tab; a reload keeps the hold.
const heldUnread = "held-unread";
addEventListener("popstate", () => { if (sessionStorage.getItem(heldUnread) !== rowKey(pagePath(), server())) sessionStorage.removeItem(heldUnread); });

const Delegations = createContext<Pick<HistoryView, "delegations" | "reviewRationales"> | undefined>(undefined);

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
  const word = /(?:^|\s)((?:tag|routine|agent|room|principal|server|model|space):"(?:[^"\\]|\\.)*\\?|\S*)$/i.exec(query)![1];
  const lower = word.toLowerCase();
  return { word, unfinished: /^(?:tag|routine|agent|room|principal|server|model|space):(?:"(?:[^"\\]|\\.)*\\?)?$/.test(lower) || /^(?:is|sort):/.test(lower) && ["is:forked", "is:routine", "is:unread", "is:running", "is:queued", "is:waiting", "is:external", "sort:newest", "sort:oldest"].some((keyword) => keyword !== lower && keyword.startsWith(lower)) };
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

const slashCommands = [
  { name: "fork", label: "Fork session", hint: "", desc: "Fork from a chosen message" },
  { name: "handoff", label: "Handoff session", hint: "", desc: "Copy a handoff or send it to another session" },
  { name: "undo", label: "Undo message", hint: "", desc: "Restore the previous request for editing" },
  { name: "tree", label: "Open message tree", hint: "", desc: "Show and move between every branch of this chat" },
  { name: "goal", label: "Start goal", hint: "<objective>", desc: "Start a goal loop" },
  { name: "stop", label: "Stop turn", hint: "", desc: "End the active turn" },
  { name: "agent", label: "Choose agent", hint: "[name]", desc: "List or switch agent" },
  { name: "enqueue", label: "Enqueue work", hint: "<text>", desc: "Queue work to run automatically" },
  { name: "stash", label: "Stash work", hint: "<text>", desc: "Hold work until explicitly sent" },
  { name: "steer", label: "Steer turn", hint: "<text>", desc: "Send now or guide the active turn" },
  { name: "skill", label: "Invoke skill", hint: "<name> [args]", desc: "Invoke a skill by name" },
];

function slashMatches(text: string, skills: { name: string; description?: string }[]) {
  const named = /^\/skill(?:[\t ]+([^\s]*))?$/i.exec(text);
  if (named) {
    return skills.flatMap((item) => item.name.toLowerCase().startsWith((named[1] ?? "").toLowerCase()) ? [{
      name: item.name,
      hint: "[args]",
      desc: item.description ?? "",
      invocation: `/skill ${item.name} `,
    }] : []);
  }
  if (!text.startsWith("/") || text.includes("\n") || text.includes(" ")) {
    return [];
  }
  const query = text.slice(1).toLowerCase();
  if (slashCommands.some((cmd) => cmd.name === query && cmd.hint === "")) {
    return [];
  }
  return [
    ...slashCommands.flatMap((cmd) => cmd.name.startsWith(query) ? [{ ...cmd, invocation: `/${cmd.name} ` }] : []),
    ...skills.flatMap((skill) => skill.name.toLowerCase().startsWith(query) ? [{
      name: skill.name,
      hint: "[args]",
      desc: skill.description ?? "",
      invocation: slashCommands.some((cmd) => cmd.name === skill.name.toLowerCase()) ? `/skill ${skill.name} ` : `/${skill.name} `,
    }] : []),
  ];
}

function isStopCommand(text: string) {
  const trimmed = text.trim().toLowerCase();
  return trimmed === "/stop" || trimmed.startsWith("/stop ");
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
  // A paused agent runs queued work only after it is resumed; stashed items still pop into the queue.
  const held = usePausedChats()(conversationId) ? "Runs after the agent is resumed" : undefined;
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
    <div className="relative -mb-3 rounded-t-xl border border-b-0 bg-background px-1.5 pt-1.5 pb-4">
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
              disabled={sending || settling || edit?.id === item.id || (item.delivery === "STASH" ? poppingId !== "" : !!held) || item.id === steeringId}
              title={item.delivery === "STASH" ? undefined : held}
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

const jobState = (job: BackgroundJob) => ({ completed: "finished", stopped: job.stoppedBy ? `stopped by ${job.stoppedBy}` : "stopped", killed: "killed (server restarted)" } as Record<string, string | undefined>)[job.state] ?? job.state;
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
// While the agent is paused, the message box goes to the people here, so only the card answers.
function QuestionCard({ question, sending, paused, answer }: { question: PendingQuestion; sending: boolean; paused: boolean; answer: (selected: string[]) => void }) {
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
      {paused ? null : <p className="text-xs text-muted-foreground">Or send your own answer from the message box.</p>}
      <QuestionnaireSubmit size="sm" />
    </QuestionnaireActions>
  </Questionnaire>;
}

// useQuestion answers the conversation's first pending Web question, from its card or with Send's
// text, unless that stops the turn or the agent is paused, when Send's text goes to the people here.
// A question no longer pending leaves its card when History refreshes.
function useQuestion(id: string, draft: ComposerDraft, paused: boolean, onChange: () => Promise<unknown>, setSendError: (value: string) => void) {
  const mutation = useMutation({ mutationFn: mutations.answerQuestion, onSettled: onChange });
  const limited = useQuery(queries.identity()).data?.limited, agents = useQuery(queries.agents({ conversationId: id })).data;
  const answerable = !limited || agents?.agents.some((agent) => agent.name === agents.currentAgent);
  const question = answerable ? draft.questions?.[0] : undefined;
  const answer = (input: { selected?: string[]; custom?: string }) => mutation.mutateAsync({ conversationId: id, askId: question!.id, ...input })
    .then(() => { setSendError(""); return true; }, (err: unknown) => { setSendError(err instanceof RPCError && err.code === 5 ? "This question was already answered or ended" : err instanceof Error ? err.message : "answer failed"); return false; });
  const card = question ? <QuestionCard key={question.id} question={question} sending={mutation.isPending} paused={paused} answer={(selected) => void answer({ selected })} /> : null;
  const take = (delivery: PromptDelivery | undefined, setText: (value: string) => void) => question && !paused && delivery === undefined && draft.text.trim() && !isStopCommand(draft.text)
    ? answer({ custom: draft.text }).then((saved) => { if (saved) setText(""); }) : undefined;
  return { answering: mutation.isPending, pending: !!question, take, card };
}

// composerLabels says what Send does: post to the people here while the agent is paused, answer the pending question,
// queue behind the running turn, or send.
function composerLabels(paused: boolean, asking: boolean, busy: boolean, id: string) {
  if (paused) return { placeholder: "Message the people here", sendLabel: "Send to the people here" };
  if (asking) return { placeholder: "Answer the question", sendLabel: "Send answer" };
  if (busy) return { placeholder: "Queue a follow-up", sendLabel: "Send to queue" };
  return { placeholder: id ? "Message or /command" : "Message a new session", sendLabel: "Send message" };
}

// Whether someone paused the agent of a chat, by its id: its messages then go to the people there, starting no turn.
function usePausedChats() {
  const rows = useContext(Sidebar).rows;
  return (id: string) => !!rows.find((row) => row.id === id)?.paused;
}

// The toggle button of a session a person started in the Web, next to Send, pauses or resumes its agent for everyone
// in it, and says why beside it when the server refuses, as the voice call button does.
function AgentPauseToggle({ id }: { id: string }) {
  const session = useContext(Sidebar).rows.find((row) => row.id === id), invalidate = useContext(SidebarInvalidation);
  const change = useMutation({ mutationFn: mutations.updateSession, onSuccess: invalidate });
  if (!session?.pausable) return null;
  const paused = !!session.paused;
  return <>
    {change.error ? <span role="alert" title={change.error.message} className="max-w-48 truncate text-xs text-destructive">{change.error.message}</span> : null}
    <Tooltip><TooltipTrigger render={<Button type="button" variant={paused ? "default" : "ghost"} size="icon" className="size-11 sm:size-7" disabled={change.isPending} />} aria-label="Pause agent" aria-pressed={paused} onMouseDown={(event) => event.preventDefault()} onClick={() => change.mutate({ id, paused: !paused })}>
      <CirclePause />
    </TooltipTrigger><TooltipContent>{paused ? "Resume agent" : "Pause agent"}</TooltipContent></Tooltip>
  </>;
}

// offeredAgent is the first of names the catalog offers, else the catalog's first agent.
function offeredAgent(catalog: Agent[], ...names: string[]) {
  return names.find((name) => catalog.some((item) => item.name === name)) ?? catalog[0]?.name;
}

function alertResumed(agent = "This agent") {
  window.alert(`${agent} keeps a single session, so its existing session opens instead of a new one.`);
}


// A session created here is titled by its first message, or a cron run by its cron's name, until the session list has it.
const firstMessages = new Map<string, string>();

function sessionTitle(session: Session) {
  return session.name || (session.preview ?? "").split("\n", 1)[0] || firstMessages.get(session.id) || session.label || session.id;
}

function useRoute() {
  const pathname = usePathname() || "/";
  return {
    cron: pathname === "/routines",
    timeline: pathname.startsWith("/routines/") ? decodeSessionId(pathname.slice("/routines/".length)) : "",
    agents: pathname === "/agents",
    editor: /^\/agents\/(new|drafts\/[^/]+)$/.test(pathname),
    skills: pathname === "/skills",
    // Skill names are lowercase letters, digits and hyphens (internal/rocketclaw/harness skills.go), so paths hold them as-is.
    skill: pathname.startsWith("/skills/") ? pathname.slice("/skills/".length) : "",
    config: pathname === "/config",
    search: isSearch(pathname),
    voice: pathname === "/voice",
    pages: pathname === "/pages",
    shortcuts: pathname === "/shortcuts",
    closed: pathname === "/closed",
    page: pathname.startsWith("/pages/") ? pathname.slice("/pages/".length) : "",
    id: pathname.startsWith("/s/") ? decodeSessionId(pathname.slice(3)) : "",
    remote: pathname.startsWith("/oc/") ? decodeSessionId(pathname.slice(4)) : "",
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
  const shown = { cron, agents, skills, config };
  return (["cron", "agents", "skills", "config"] as const).map((name) => {
    const Page = { cron: CronPage, agents: AgentsPage, skills: SkillsPage, config: ConfigPage }[name];
    return warm[name] ? <TabPane key={name} show={shown[name]}><Page /></TabPane> : null;
  });
});

// A remote that never answered since load is unavailable, one that stopped answering is offline, and one on another
// protocol keeps working with a notice. The local server's own protocol change reloads the page instead.
function useRemoteStatus(name: string) {
  const local = useQuery(queries.protocol("")).data;
  const proto = useQuery({ ...queries.protocol(name), refetchInterval: 2000, retry: false, enabled: !!name });
  if (!name || !proto.isError && proto.data === undefined) return { text: "", block: false };
  if (proto.data === undefined) return { text: `${name} is unavailable: it needs an HTTPS address this browser trusts, such as its tsnet name, and a RocketClaw version with remote RocketClaws. Address: ${serverURL(name)}`, block: true };
  if (proto.isError) return { text: `${name} is offline; this page reopens when it is reachable.`, block: true };
  return { text: proto.data === local ? "" : `${name} runs a different RocketClaw version; some actions may fail.`, block: false };
}

function ServerGate({ children }: { children: ReactNode }) {
  const status = useRemoteStatus(useServer());
  return <>{status.text ? <p role="status" className="m-auto p-4 text-sm text-muted-foreground">{status.text}</p> : null}{status.block ? null : children}</>;
}

function ProtocolGuard() {
  const proto = useQuery({ ...queries.protocol(""), refetchInterval: 2000 });
  const seen = useRef("");
  useEffect(() => {
    const hash = proto.data ?? "";
    if (seen.current && hash && hash !== seen.current) {
      window.location.reload();
    } else {
      seen.current ||= hash;
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
  const { sandboxError = "", slackWarning = "" } = useQuery({ ...queries.identity(), refetchInterval: 2000, retry: false }).data ?? {};
  return <div className={cn("flex shrink-0 items-center gap-3 pr-12 pl-3 text-sm", (updated || sandboxError !== "" || slackWarning !== "") && "border-b bg-muted py-1.5")}>
    <p role="status">{updated ? "A new version of RocketClaw is available." : ""}</p>
    {updated ? <Button size="sm" onClick={() => window.location.reload()}>Refresh</Button> : null}
    {sandboxError !== "" ? <p role="alert">{sandboxError}</p> : null}
    <SlackWarning text={slackWarning} />
  </div>;
}

// marks are the attention rows of this person's tab locations that are not sessions. offline is a server that stopped
// answering, foreign one on another protocol than this page's.
type SidebarView = {
  rows: Session[];
  refreshing: boolean;
  enumerationComplete: boolean;
  remote?: Remote;
  marks?: TabAttention[];
  // The listing names a newer tab state than this window holds, which it is reloading.
  stale?: boolean;
  offline?: boolean;
  foreign?: boolean;
};

// One server's listing, with its saved tabs while the page is on another server.
type ServerView = SidebarView & { invalidateQueries: () => void; tabs?: SavedTabs };

// rows, refreshing and invalidation are the page's server's; all holds every server's rows, newest first.
type SidebarState = ServerView & {
  all: Session[];
  servers: Record<string, ServerView>;
};

const Sidebar = createContext<SidebarState>({
  rows: [],
  all: [],
  servers: {},
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

// Each server's view, gathered by nesting one ServerSessions per server around the merged list.
const ServerViews = createContext<Record<string, ServerView>>({});
const ServerName = createContext("");

function SidebarOwner({ children }: { children: ReactNode }) {
  return ["", ...rocketclaws().map((remote) => remote.name)].reduceRight<ReactNode>((inner, name) => <ServerName.Provider key={name} value={name}><ServerSessions>{inner}</ServerSessions></ServerName.Provider>, <SidebarMerge>{children}</SidebarMerge>);
}

function SidebarMerge({ children }: { children: ReactNode }) {
  const name = useServer(), servers = useContext(ServerViews);
  const value = useMemo(() => {
    const here = servers[name] ?? { rows: [], refreshing: false, enumerationComplete: false, invalidateQueries: () => {} };
    const all = Object.keys(servers).length > 1 ? Object.values(servers).flatMap((view) => view.rows).sort((a, b) => (Date.parse(b.updatedAt ?? "") || 0) - (Date.parse(a.updatedAt ?? "") || 0)) : here.rows;
    return { ...here, all, servers };
  }, [servers, name]);
  return (
    <SidebarInvalidation.Provider value={value.invalidateQueries}>
      <Sidebar.Provider value={value}>{children}</Sidebar.Provider>
    </SidebarInvalidation.Provider>
  );
}

function useListingRings(name: string, enabled: boolean, onRing: () => void) {
  const rang = useEffectEvent(onRing);
  useEffect(() => {
    if (!enabled) return;
    const stream = new EventSource(`${serverURL(name)}/listing`);
    stream.onmessage = () => rang();
    return () => stream.close();
  }, [name, enabled]);
}

// A server's session list. A remote that stops answering keeps its last rows, offline; only a refusal clears them.
function ServerSessions({ children }: { children: ReactNode }) {
  const name = useContext(ServerName);
  const here = useServer() === name;
  const identity = useQuery({ ...queries.identity(name), refetchInterval: 2000, retry: false });
  // A remote that has not answered yet is asked again until it does.
  const protocol = useQuery({ ...queries.protocol(name), retry: false, refetchInterval: (query) => query.state.data === undefined && 2000 });
  const local = useQuery({ ...queries.protocol(""), enabled: false }).data, foreign = protocol.data !== undefined && protocol.data !== local;
  // A remote's saved rows keep apart from the local server's, whose cache key stays as it was.
  const scope = protocol.data === undefined || !name ? protocol.data : JSON.stringify([name, protocol.data]);
  const identityRejected = identity.isError && (!name || identity.error instanceof RPCError);
  const unreachable = identity.isError && !identityRejected;
  const owner = identity.isSuccess || unreachable ? identity.data?.username : undefined;
  const tabs = useQuery({ ...queries.tabs(owner ?? "", name), refetchInterval: 2000, enabled: owner !== undefined && !here });
  const [identityGeneration, setIdentityGeneration] = useState(0);
  const [generation, setGeneration] = useState(0);
  const [view, setView] = useState<SidebarView>({ rows: [], refreshing: false, enumerationComplete: false });
  const gen = useRef(0);
  const rowsRef = useRef<Session[]>([]);
  // The last complete listing's version and rows; the next poll asks for only what changed since.
  const listing = useRef<SavedListing>({ version: "", rows: [] });
  const ownerRef = useRef(owner);
  const protocolRef = useRef(scope);
  const ring = useRef(() => {});
  useListingRings(name, owner !== undefined && !identityRejected, () => {
    ring.current();
    if (!here) void tabs.refetch();
  });
  const bump = useCallback(() => {
    gen.current += 1;
    setGeneration((value) => value + 1);
  }, []);
  const reset = useCallback(() => {
    invalidatePendingSaves();
    gen.current += 1;
    rowsRef.current = [];
    listing.current = { version: "", rows: [] };
    setView({ rows: [], refreshing: false, enumerationComplete: false });
  }, []);
  useEffect(() => {
    const prev = ownerRef.current;
    const prevProtocol = protocolRef.current;
    ownerRef.current = owner;
    protocolRef.current = scope;
    if (!identityRejected && prev === owner && prevProtocol === scope) {
      return;
    }
    reset();
  }, [owner, scope, identityRejected, identityGeneration, reset]);
  useEffect(() => {
    if (owner === undefined || scope === undefined || identityRejected) {
      return;
    }
    const captured = gen.current;
    const currentOwner = owner;
    const currentProtocol = scope;
    const ac = new AbortController();
    let rung = new AbortController();
    ring.current = () => rung.abort();
    const next = () => {
      if (ac.signal.aborted || captured !== gen.current) return;
      const { signal } = rung, fetched = Date.now();
      void delay(2000, AbortSignal.any([ac.signal, signal])).then(async () => {
        const floor = fetched + 500 - Date.now();
        if (floor > 0) await delay(floor, ac.signal);
        await tick();
      });
    };
    const tick = async () => {
      if (ac.signal.aborted || captured !== gen.current) return;
      rung = new AbortController();
      setView((current) => ({ ...current, refreshing: true, enumerationComplete: false }));
      // With nothing shown yet, the saved listing shows at once and its version makes the poll send only changes.
      if (!listing.current.version && !rowsRef.current.length) {
        await loadSavedSessions(currentOwner, currentProtocol).then((saved) => {
          if (saved === undefined || ac.signal.aborted || captured !== gen.current) return;
          listing.current = saved;
          // Saved activity is stale until the poll confirms it.
          rowsRef.current = saved.rows.map((row) => row.running || row.background ? { ...row, running: false, background: false } : row);
          setView((current) => ({ ...current, rows: rowsRef.current, marks: saved.tabs, remote: saved.remote }));
        }, () => {});
      }
      const snapshotGeneration = await loadSnapshotGeneration(currentOwner, currentProtocol).catch(() => undefined);
      if (ac.signal.aborted || captured !== gen.current) return;
      const viewing = document.hasFocus() && server() === name ? pagePath() : "";
      const held = listing.current, keepMark = viewing && sessionStorage.getItem(heldUnread) === rowKey(viewing, name) ? [["hold", ""]] : [];
      readSessionEnumeration(currentOwner, Promise.resolve(listSessions(ac.signal, `${serverURL(name)}/api/ListSessions?${new URLSearchParams([["viewing", viewing], ["since", held.version], ...keepMark])}`)), () => rowsRef.current, (merged) => {
        // Only changed rows arrive since a held version; they show in place once the listing completes.
        if (ac.signal.aborted || captured !== gen.current || held.version) {
          return;
        }
        rowsRef.current = merged;
        setView((current) => ({ ...current, rows: merged }));
      }, ac.signal, (remote) => {
        // OpenCode tabs mount once they know their server, without waiting for the rows.
        if (!ac.signal.aborted && captured === gen.current) setView((current) => ({ ...current, remote }));
      }).then((result) => {
        if (result.navigate) navigate(result.navigate, false, name);
        if (ac.signal.aborted || captured !== gen.current) {
          return;
        }
        const rows = result.complete ? listedRows(held, result) : undefined;
        if (result.mismatch) {
          reset();
          ownerRef.current = undefined;
          // A fast same-owner refetch can hide the intermediate pending state.
          void queryClient.resetQueries({ queryKey: [{ server: name }, "identity"] }).then(() => setIdentityGeneration((value) => value + 1));
        } else if (rows) {
          const marks = listedTabs(held, result), { queryKey } = queries.tabs(currentOwner, name);
          listing.current = { version: result.version ?? "", rows, remote: result.remote, tabs: marks };
          rowsRef.current = rows;
          // A tab-state revision newer than this window's tabs, from a tab closed or opened elsewhere or a timer bringing
          // one back, reloads them; until then this window decides no notifications from this listing.
          const stale = Number(result.tabRevision) > Math.max(Number(queryClient.getQueryData<SavedTabs>(queryKey)?.revision), savedRevisions.get(name) ?? 0);
          setView({ rows, marks, stale, enumerationComplete: true, refreshing: false, remote: result.remote });
          void saveCompleteSessions(currentOwner, currentProtocol, listing.current, snapshotGeneration).catch(() => {});
          if (stale) void queryClient.invalidateQueries({ queryKey });
        } else {
          setView((current) => ({ ...current, enumerationComplete: false, refreshing: false, remote: result.remote ?? current.remote }));
        }
      }).then(next);
    };
    void tick();
    return () => {
      ac.abort();
      invalidatePendingSaves(currentOwner, currentProtocol);
    };
  }, [name, owner, scope, identityRejected, identityGeneration, generation, reset]);
  const visible = owner !== undefined && ownerRef.current === owner && protocolRef.current === scope;
  const parent = useContext(ServerViews);
  const views = useMemo(() => ({ ...parent, [name]: {
    ...view, invalidateQueries: bump, tabs: here ? undefined : tabs.data, enumerationComplete: visible && view.enumerationComplete, marks: visible && !unreachable ? view.marks : undefined, offline: unreachable, foreign,
    rows: !visible ? [] : name ? view.rows.map((row) => ({ ...row, rocketclaw: name, offline: row.offline || unreachable })) : view.rows,
  } }), [parent, name, view, visible, unreachable, foreign, here, tabs.data, bump]);
  return <ServerViews.Provider value={views}>{children}</ServerViews.Provider>;
}

type PendingFile = { id: string; file: File };
type ComposerDraft = { text: string; mentions?: { text: string; tags: ComposerMention[] }; files: PendingFile[]; agent: string; sessionId: string; sending: boolean; busy: boolean; lines: Line[]; parked?: Line[]; consumed?: Set<string>; revision?: string; origin?: ChatOrigin; terminal?: string; historyError?: string; missing?: boolean; historyRead?: Promise<void>; historyAgain?: boolean; start?: string; more?: boolean; earlier?: Promise<void>; delegations?: string[]; reviewRationales?: Record<string, string>; shadows?: ShadowBinding[]; earlierShadows?: { from: string; before: string; shadows: ShadowBinding[] }[]; movable?: boolean; jobs?: BackgroundJob[]; questions?: PendingQuestion[]; error: string; edit: number; submission: number; historyEpoch?: number; revertEligible?: boolean; revertMessageId?: string; revertFloor?: string; canUndo?: boolean; reverting?: boolean; restored?: { text: string; files: PendingFile[] }; focus?: number; hydrated?: boolean; persistenceKey?: string; persistedEdit?: number; persistenceError?: string };
// latest is the newest turn's entry key: its unsaved messages revert by input ID.
const RevertActions = createContext<{ available: boolean; pending: boolean; latest?: string; floor?: string; run: (messageId?: string) => Promise<void> } | undefined>(undefined);
const DraftScope = createContext<string | undefined>(undefined);

function BottomNavigation({ children }: { children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(false);
  const swipe = useRef({ pointerId: -1, y: 0, completed: false });
  return (
    <footer className="order-last flex shrink-0 flex-col md:border-t px-2 pb-[env(safe-area-inset-bottom)] [--navigation-button:32px] [--navigation-icon:24px] [--navigation-gap:4px] [--navigation-handle:24px] max-lg:[--navigation-button:48px] max-lg:[--navigation-gap:8px] [@media(any-pointer:coarse)]:[--navigation-button:48px] [@media(any-pointer:coarse)]:[--navigation-gap:8px] max-[22rem]:px-1 max-[22rem]:[--navigation-gap:4px]!">
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
      <div id="bottom-navigation" className={cn("min-h-0 grid-cols-[1fr_auto_1fr] items-center gap-[var(--navigation-gap)] py-1", collapsed ? "hidden" : "grid")}>{children}</div>
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

// The drawer opens from the footer's sidebar button or a rightward swipe that starts anywhere except text fields, sliders,
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
  return <Drawer.Root open={open} onOpenChange={toggle} swipeDirection="left">
    <Drawer.Portal>
      <Drawer.Backdrop className="fixed inset-0 z-50 bg-black/10 opacity-[calc(1-var(--drawer-swipe-progress))] transition-opacity duration-200 data-swiping:duration-0 data-starting-style:opacity-0 data-ending-style:opacity-0 supports-backdrop-filter:backdrop-blur-xs" />
      <Drawer.Viewport className="fixed inset-0 z-50 flex">
        {/* Picking the tab already shown leaves the address alone, so the pick itself closes the drawer. A mouse
            click lands on the popup, which holds the pointer for its swipe, so it is passed to the tab under the pointer. */}
        <Drawer.Popup id="tab-sidebar" onClick={(event) => {
          if ((event.target as Element).closest('[role="tab"]')) toggle();
          else document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('[role="tab"]')?.click();
        }} className="flex h-full w-3/4 max-w-sm flex-col border-r bg-popover text-sm text-popover-foreground shadow-lg outline-none touch-auto [transform:translateX(var(--drawer-swipe-movement-x))] transition-transform duration-200 ease-in-out data-swiping:select-none data-starting-style:[transform:translateX(-100%)] data-ending-style:[transform:translateX(-100%)]">
          <Drawer.Title className="sr-only">Tabs</Drawer.Title>
          {children}
        </Drawer.Popup>
      </Drawer.Viewport>
    </Drawer.Portal>
  </Drawer.Root>;
}

function TabTitle() {
  const tabs = useContext(TabActions), { state, shared, pathname } = tabs, id = pathSession(pathname), view = useContext(Sidebar), { setCommand } = useContext(SessionCommands);
  const session = view.rows.find((row) => row.id === id) ?? { id };
  const own = state.tabs.find((tab) => tab.path === pathname), tab = own ?? sharedTab(shared, state.space, pathname);
  const [label, Icon] = tabLabel({ path: pathname, search: tab?.search }, session, MessageSquare);
  const title = useCleanText(label);
  const [anchor, setAnchor] = useState<MenuAnchor>(), [editing, setEditing] = useState("");
  const menu = anchor && { path: pathname, anchor, space: state.space, shared: !own };
  const items = menu ? [...threadPanelItems(menu, true, tabs.name), ...menuItems(tabs, menu, view, new Set([pathname]), () => setEditing(pathname), (doomed) => setCommand({ mode: "delete", source: doomed }))] : [];
  return <header className="flex shrink-0 items-center gap-2 border-b px-4 py-1.5 text-sm">
    <Icon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
    {editing === pathname ? <TabRename session={session} tab={{ path: pathname, search: tab?.search }} shared={!own} space={state.space} placeholder={title} endEdit={() => setEditing("")} /> : <span className="truncate font-semibold">{title}</span>}
    {tab ? <button type="button" aria-label="Page actions" className="-my-0.5 flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring" onClick={(event) => setAnchor(event.currentTarget)}><Ellipsis className="size-3.5" /></button> : null}
    <TabMenu items={items} anchor={anchor} close={() => setAnchor(undefined)} refocus={() => true} />
  </header>;
}

function SidebarToggle({ open, toggle }: { open: boolean; toggle: () => void }) {
  const label = open ? "Hide sidebar" : "Show sidebar";
  const Icon = open ? PanelLeftClose : PanelLeftOpen;
  return <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)]" />} aria-label={label} aria-expanded={open} aria-controls="tab-sidebar" onClick={toggle}>
    <Icon className="size-[var(--navigation-icon)]" />
  </TooltipTrigger><TooltipContent side="top">{label}</TooltipContent></Tooltip>;
}

// Closes whichever right pane is open, or opens the thread's pull requests; so does Cmd/Ctrl+Shift+B. The key is
// RocketClaw's even while the button is disabled, so the browser's bookmarks bar never takes it.
function PanelToggle() {
  const session = pathSession(usePathname());
  const search = new URLSearchParams(useSearch());
  const open = ["delegation", "skill", "pullRequests", "pagePreview", "tree"].some((panel) => search.has(panel));
  const label = open ? "Hide right pane" : "Show pull requests and issues";
  const Icon = open ? PanelRightClose : PanelRightOpen;
  const toggle = () => navigate(panelHref(open ? undefined : "1", "pullRequests"));
  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (event.defaultPrevented || !(event.metaKey || event.ctrlKey) || event.altKey || !event.shiftKey || event.code !== "KeyB") return;
    event.preventDefault();
    event.stopPropagation();
    if (!event.repeat && (open || session)) toggle();
  });
  useEffect(() => {
    const key = (event: KeyboardEvent) => onKey(event);
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, []);
  return <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)]" disabled={!open && !session} />} aria-label={label} aria-expanded={open} onClick={toggle}>
    <Icon className="size-[var(--navigation-icon)]" />
  </TooltipTrigger><TooltipContent side="top">{label}</TooltipContent></Tooltip>;
}

type OpenCodeServer = { rocketclaw: string; server: string };

// New session starts one on this page's server. With other RocketClaws or OpenCode servers it opens a menu of where to
// start one, with this server highlighted so Enter starts it there: another RocketClaw opens its New session page, and
// an OpenCode server the palette's folder picker.
function NewSessionButton({ newChat, openCode }: { newChat: () => void; openCode: (target: OpenCodeServer) => void }) {
  const here = useServer();
  const claws = ["", ...rocketclaws().map((remote) => remote.name)].filter((name) => name !== here);
  const codes = Object.entries(useContext(Sidebar).servers).map(([rocketclaw, view]) => [rocketclaws().length ? `OpenCode on ${serverLabel(rocketclaw)}` : "OpenCode",
    Object.keys(view.remote?.servers ?? {}).toSorted(), (server: string) => openCode({ rocketclaw, server })] as const);
  // Base UI focuses the popup itself on a mouse open, highlighting no item; this server's item takes that focus
  // instead, which highlights it, so Enter starts it and arrows move from it.
  const native = useRef<HTMLDivElement>(null);
  const menu = claws.length > 0 || codes.some(([, names]) => names.length > 0);
  const button = <Button variant="ghost" size="icon" aria-label="New session" className="size-[var(--navigation-button)] shrink-0" />;
  const pen = <Tooltip><TooltipTrigger render={menu ? <Menu.Trigger render={button} /> : button} onClick={menu ? undefined : newChat}>
    <SquarePen className="size-[var(--navigation-icon)]" />
  </TooltipTrigger><TooltipContent side="top">New session</TooltipContent></Tooltip>;
  if (!menu) return pen;
  const groups = [["RocketClaw", [here, ...claws], (name: string) => name === here ? newChat() : navigate("/", false, name)] as const, ...codes];
  return <DropdownMenu>
    {pen}
    <DropdownMenuContent side="top" className="w-auto min-w-44" onFocus={(event) => { if (event.target === event.currentTarget) native.current!.focus(); }}>
      {groups.map(([group, names, open]) => names.length ? <Menu.Group key={group}>
        <Menu.GroupLabel className="px-1.5 py-1 text-xs text-muted-foreground">{group}</Menu.GroupLabel>
        {names.map((name) => <Menu.Item key={name} ref={group === "RocketClaw" && name === here ? native : undefined} className="flex cursor-default items-center rounded-md px-1.5 py-1 text-sm outline-none focus:bg-accent focus:text-accent-foreground" onClick={() => open(name)}>{serverLabel(name)}</Menu.Item>)}
      </Menu.Group> : null)}
    </DropdownMenuContent>
  </DropdownMenu>;
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
// last names each space's last active tab; sharedOrder is this person's order of Shared tabs by path.
type TabState = { owner?: string; tabs: Tab[]; active: string; groups: TabGroup[]; spaces: Space[]; space: string; last: Record<string, string>; sharedOrder: string[] };
const TabActions = createContext<ReturnType<typeof useTabs>>(null!);
const pageTabs: Record<string, [string, typeof Search]> = { "/": ["New session", SquarePen], "/voice": ["Voice", Mic], "/routines": ["Routines", Clock], "/agents": ["Agents", Bot], "/agents/new": ["Create New Agent", Bot], "/skills": ["Skills", Sparkles], "/config": ["Settings", Settings], "/pages": ["Pages", Files], "/shortcuts": ["Shortcuts", Keyboard], "/closed": ["Recently closed", History] };

// A Search page is /search or a location under it, each its own tab; it is titled with its name, else the query it shows.
const isSearch = (path: string) => /^\/search(\/|$)/.test(path);
const searchTitle = (search?: SearchPageState) => search?.name || search?.searches.find((item) => item.id === search.active)?.query || "Search";
const freshSearch = () => `/search/${crypto.getRandomValues(new Uint32Array(2)).join("-")}`;

function pageLabel(page: string) {
  const [space, ...path] = page.split("/").map(decodeURIComponent);
  return `${space} · ${path.join("/")}`;
}

// A skill's or cron's page tab is titled with its name, and a hosted page's tab with its label.
function pageTab(path: string): [string, typeof Search] | undefined {
  if (path.startsWith("/pages/")) return [pageLabel(path.slice("/pages/".length)), FileText];
  if (path.startsWith("/routines/")) return [decodeSessionId(path.slice("/routines/".length)), Clock];
  if (path.startsWith("/agents/drafts/")) return ["Agent draft", Bot];
  return pageTabs[path] ?? (path.startsWith("/skills/") ? [path.slice("/skills/".length), Sparkles] : undefined);
}

// A hosted page opens in the space named by its link's space segment, ignoring case, else in the current space.
function pageSpace(state: Pick<TabState, "spaces" | "space">, path: string): [string, string] {
  const [, root, segment, ...rest] = path.split("/");
  const space = root === "pages" && rest.length ? state.spaces.find((item) => item.name.toLowerCase() === decodeURIComponent(segment).toLowerCase()) : undefined;
  return space ? [`/pages/${encodeURIComponent(space.name)}/${rest.join("/")}`, space.id] : [path, state.space];
}

// Group colors light enough for black text in both themes.
const groupColors: Record<string, string> = { Grey: "bg-gray-400", Blue: "bg-blue-400", Red: "bg-red-400", Yellow: "bg-yellow-400", Green: "bg-green-400", Pink: "bg-pink-400", Purple: "bg-purple-400", Cyan: "bg-cyan-400", Orange: "bg-orange-400" };
const groupName = (group: TabGroup) => group.name || `${group.color} group`;
// Every group holds a placeholder among its tabs, so it stays where it was after its last tab goes, until deleted. Only
// the strip draws it, as New session in the emptied group; tab commands, closing, and stepping never see it.
const keeper = (group: string) => `group:${group}`, isKeeper = (tab: Tab) => tab.path.startsWith("group:");

// Each group sits where its first member is, so its tabs stay adjacent.
function arrange(tabs: Tab[]) {
  const rank = (tab: Tab) => tabs.findIndex((other) => other === tab || (!!tab.group && other.group === tab.group));
  return tabs.toSorted((a, b) => rank(a) - rank(b));
}

function join(tabs: Tab[], path: string, group: string, space: string) {
  const rest = tabs.filter((tab) => tab.path !== path), tab = tabs.find((item) => item.path === path);
  return rest.toSpliced(rest.findLastIndex((item) => item.group === group) + 1, 0, { ...tab, path, group, space });
}

// New session is one location, so the space that opens it takes its tab, out of any group.
function home(tabs: Tab[], space: string) {
  return tabs.some((tab) => tab.path === "/") ? tabs.map((tab) => tab.path === "/" && tab.space !== space ? { path: "/", space } : tab) : [...tabs, { path: "/", space }];
}

function openTab(state: TabState, link: string, shared: SavedTabs["shared"], preview = false, search?: SearchPageState): TabState {
  const [path, target] = pageSpace(state, link);
  const inShared = isShared(shared, state.space, path), open = state.tabs.find((tab) => tab.path === path);
  const slot = preview && !open && !inShared ? state.tabs.findIndex((tab) => tab.preview) : -1;
  if (slot < 0) state = { ...state, tabs: state.tabs.filter((tab) => !tab.preview || tab.path === path) };
  const current = inShared ? state.space : open && path !== "/" ? open.space! : target, last = preview ? state.last : { ...state.last, [current]: path };
  if (inShared) return { ...state, active: path, last };
  const group = state.tabs.find((tab) => tab.path === state.active && tab.space === current)?.group;
  const at = slot >= 0 ? slot : state.tabs.findIndex((tab) => tab.path === state.active) + 1 || state.tabs.length;
  const added = state.tabs.toSpliced(at, Number(slot >= 0), { path, preview, group, space: current, unplaced: pathSession(path) !== "" || undefined, search });
  return { ...state, active: path, space: current, last,
    groups: state.groups.map((item) => item.id === open?.group && path !== state.active ? { ...item, collapsed: false } : item),
    tabs: !open ? arrange(group && state.tabs[at]?.group !== group ? join(added, path, group, current) : added) : path === "/" ? home(state.tabs, current) : state.tabs };
}

// Entering a space returns to its last active tab, which may be a Shared one, else its first tab, else New session.
function entry(state: Pick<TabState, "tabs" | "last">, space: string, shared: SavedTabs["shared"]) {
  const there = state.tabs.filter((tab) => tab.space === space && !isKeeper(tab)), last = state.last[space];
  return there.some((tab) => tab.path === last) || isShared(shared, space, last) ? last : there[0]?.path ?? "/";
}

function leave(state: TabState, doomed: string, spaces: Space[]): TabState {
  return { ...state, spaces, space: state.space === doomed ? spaces[0].id : state.space, tabs: state.tabs.map((tab) => tab.space === doomed ? relocate(tab, spaces[0].id) : tab) };
}

const relocate = ({ preview: _preview, unplaced: _unplaced, ...tab }: Tab, space: string): Tab => ({ ...tab, space });

// The server's saved tabs; tabs left in a lost space or group belong to the first space and no group, and a lost group's
// placeholder goes. A group saved before every group held one gets its placeholder after its last tab.
function savedTabs(owner?: string, saved?: Pick<SavedTabs, "spaces" | "state">): TabState {
  const spaces = saved?.spaces ?? [{ id: "main", name: "Main" }], known = (id?: string) => spaces.some((space) => space.id === id);
  const groups = saved?.state.groups ?? [];
  const tabs = (saved?.state.tabs ?? []).map(({ group, space, ...tab }) => ({ ...tab, group: groups.some((item) => item.id === group) ? group : undefined, space: known(space) ? space : spaces[0].id })).filter((tab) => tab.group || !isKeeper(tab));
  const kept = groups.reduce<Tab[]>((list, { id }) => {
    const member = list.find((tab) => tab.group === id);
    return member && !list.some((tab) => tab.path === keeper(id)) ? join(list, keeper(id), id, member.space!) : list;
  }, tabs);
  return { owner, tabs: arrange(kept), active: "", groups, spaces, space: known(saved?.state.space) ? saved!.state.space : spaces[0].id, last: saved?.state.last ?? {}, sharedOrder: saved?.state.sharedOrder ?? [] };
}

// A space chosen on another server's card, which that server's first load enters.
let arriving = "";
// The revision of this window's last tab save, by server: the session list names it as the newest, which holds nothing
// this window has not got.
const savedRevisions = new Map<string, number>();

function loadTabs(path: string, owner?: string, saved?: SavedTabs, shared: SavedTabs["shared"] = []): TabState {
  const state = savedTabs(owner, saved);
  // Only the load with the server's saved tabs knows the spaces, so it alone enters, and clears, the arriving space.
  if (saved && state.spaces.some((space) => space.id === arriving)) state.space = arriving;
  if (saved) arriving = "";
  const [link, target] = pageSpace(state, path);
  const tabs = state.tabs.some((tab) => tab.path === link) || isShared(shared, state.space, link) ? state.tabs : arrange([...state.tabs, { path: link, space: target, unplaced: pathSession(link) !== "" || undefined }]);
  return openTab({ ...state, tabs, active: link }, link, shared);
}

const savedState = (state: TabState): SavedTabs["state"] => {
  const tabs = state.tabs.flatMap(({ path, preview, group, space, search }) => preview ? [] : [{ path, group, space, search }]);
  return { tabs, groups: state.groups.filter((group) => tabs.some((tab) => tab.group === group.id)), space: state.space, last: state.last, sharedOrder: state.sharedOrder };
};

type ClosedTab = NonNullable<SavedTabs["closed"]>[number];
const noShared: SavedTabs["shared"] = [], noClosed: ClosedTab[] = [], noLayout: StripLayout = {};

// This person's strip layout, kept by the local server, from the saved tabs its view or its session list fetches.
function useLayout(): [StripLayout, (change: (layout: StripLayout) => StripLayout) => void] {
  return [useLocalTabs()?.layout ?? noLayout, saveLayout];
}

function useLocalTabs() {
  const owner = useQuery(queries.identity("")).data?.username;
  return useQuery({ ...queries.tabs(owner ?? "", ""), enabled: false }).data;
}

function localTabsKey() {
  const owner = queryClient.getQueryData<{ username: string }>(queries.identity("").queryKey)?.username;
  return { owner, queryKey: queries.tabs(owner ?? "", "").queryKey };
}

// A layout change saves the whole layout to the local server, one save at a time in order, and the cache takes it at
// once. Until the last save answers, changes build on the newest layout, not on one a poll brought back meanwhile, and its
// answer then replaces the cache. Before the layout arrives nothing is saved, which would replace it.
let laying: { latest?: StripLayout; saves: Promise<unknown> } = { saves: Promise.resolve() };
function saveLayout(change: (layout: StripLayout) => StripLayout) {
  const { owner, queryKey } = localTabsKey();
  const saved = queryClient.getQueryData<SavedTabs>(queryKey);
  if (owner === undefined || !saved) return;
  const layout = laying.latest = change(laying.latest ?? saved.layout ?? noLayout);
  void queryClient.cancelQueries({ queryKey });
  queryClient.setQueryData<SavedTabs>(queryKey, (old) => old && { ...old, layout });
  laying.saves = laying.saves.then(() => mutations.tabs({ layout }, "")).then((reply) => {
    if (laying.latest !== layout) return;
    laying = { saves: laying.saves };
    queryClient.setQueryData<SavedTabs>(queryKey, (old) => old && { ...old, layout: reply.layout });
  }, () => {
    laying = { saves: Promise.resolve() };
    void queryClient.invalidateQueries({ queryKey });
  });
}
const foldCard = (key: string, folded: boolean) => saveLayout((layout) => ({ ...layout, folded: { ...layout.folded, [key]: folded } }));
const sharedTab = (shared: SavedTabs["shared"], space: string, path: string) => shared.find((tab) => tab.space === space && tab.path === path);
const isShared = (shared: SavedTabs["shared"], space: string, path: string) => !!sharedTab(shared, space, path);

// received sees the reply before the cache takes it. name is the server the tabs belong to, which a reply arriving
// after the page moved to another server still updates.
async function tabsCall(name: string, input: Parameters<typeof mutations.tabs>[0], received?: (reply: SavedTabs) => void) {
  const cached = queryClient.getQueriesData<SavedTabs>({ queryKey: [{ server: name }, "tabs"] });
  const reply = await mutations.tabs(input, name);
  received?.(reply);
  for (const [key, before] of cached) queryClient.setQueryData<SavedTabs>(key, (old) => {
    if (!old) return;
    // A personal save's incidental snapshots must not undo changes received while it was in flight.
    const shared = input.state && old.shared !== before?.shared ? old.shared : reply.shared;
    if (reply.stale) return input.state ? { ...reply, shared } : undefined;
    const { closed } = reply;
    return JSON.stringify([old.shared, old.closed]) !== JSON.stringify([shared, closed]) ? { ...old, shared, closed } : undefined;
  });
  return reply;
}

// A three-way merge of one list: what this window opened, closed, or changed since base wins; the rest is the server's.
function mergeList<T>(base: T[], local: T[], server: T[], key: (item: T) => string, sig: (item: T) => string, localOrder: boolean) {
  const find = (list: T[], item: T) => list.find((other) => key(other) === key(item));
  const all = localOrder ? [...local, ...server] : [...server, ...local];
  return all.reduce<T[]>((merged, item, i) => {
    if (all.findIndex((other) => key(other) === key(item)) !== i) return merged;
    const mine = find(local, item), was = find(base, item), theirs = find(server, item);
    if (theirs ? mine || !was : mine && !was) merged.push(mine && (!was || sig(mine) !== sig(was)) ? mine : theirs ?? item);
    return merged;
  }, []);
}

// Tabs saved elsewhere since base, with this window's own changes since base on top. This window keeps its order, tabs
// opened elsewhere going last, unless only the other window reordered tabs.
function mergeTabs(base: SavedTabs["state"], local: SavedTabs["state"], server: SavedTabs["state"]): SavedTabs["state"] {
  const shared = (list: SavedTabs["state"]["tabs"], other: SavedTabs["state"]["tabs"]) => list.flatMap((tab) => other.some((item) => item.path === tab.path) ? [tab.path] : []).join("\n");
  return {
    tabs: mergeList(base.tabs, local.tabs, server.tabs, (tab) => tab.path, (tab) => [tab.path, tab.group ?? "", tab.space ?? "", JSON.stringify(tab.search ?? null)].join("\n"), shared(local.tabs, base.tabs) !== shared(base.tabs, local.tabs) || shared(server.tabs, base.tabs) === shared(base.tabs, server.tabs)),
    groups: mergeList(base.groups, local.groups, server.groups, (group) => group.id, (group) => [group.id, group.name, group.color, !!group.collapsed].join("\n"), false),
    space: local.space !== base.space ? local.space : server.space,
    sharedOrder: JSON.stringify(local.sharedOrder) !== JSON.stringify(base.sharedOrder) ? local.sharedOrder : server.sharedOrder,
    last: { ...server.last, ...Object.fromEntries(Object.entries(local.last).filter(([space, path]) => base.last[space] !== path)) },
  };
}

// The server whose tabs a section edits when it is not the page's, and how to ask its session list again.
const RemoteTabs = createContext<{ name: string; invalidate: () => void } | null>(null);

// A server's tabs for this person. The page's server's follow the page: its address opens a tab there, and closing or
// entering moves the page. Another server's, named by RemoteTabs, are its saved tabs, edited and saved the same way,
// where opening a tab moves the page to that server.
function useTabs() {
  const remote = useContext(RemoteTabs), here = useServer(), name = remote?.name ?? here, viewed = !remote;
  const identity = useQuery(queries.identity(name));
  const owner = identity.data?.username;
  const saved = useQuery({ ...queries.tabs(owner ?? "", name), enabled: owner !== undefined });
  const shared = saved.data?.shared ?? noShared;
  const visited = usePathname() || "/", pathname = viewed ? visited : "";
  const [state, setState] = useState(() => viewed ? loadTabs(pathname) : savedTabs());
  // A tab this window reopened leaves the list before the save that reopened it answers.
  const closed = useMemo(() => (saved.data?.closed ?? noClosed).filter((tab) => !state.tabs.some((item) => item.path === tab.path)), [saved.data?.closed, state.tabs]);
  // This person's order puts a space's Shared tabs it names first; the rest follow, oldest added first.
  const sharedIn = (space: string, order = state.sharedOrder) => {
    const rank = (path: string) => order.indexOf(path) + 1 || Infinity;
    return shared.flatMap((tab) => tab.space === space ? [tab.path] : []).toSorted((a, b) => rank(a) - rank(b) || 0);
  };
  const [editor, setEditor] = useState<{ id: string; open: boolean }>();
  const [picking, setPicking] = useState<Picking>();
  // This server's parked tab locations, one per line: as its session list says, and as this window's own parks and
  // unparks say until the list does.
  const [parkedList, setParked] = useState(""), parked = new Set(parkedList.split("\n")), pageInvalidate = useContext(SidebarInvalidation), invalidate = remote?.invalidate ?? pageInvalidate;
  // base is the saved state this window last saved or took in, at revision; at stamps the read last taken in.
  const sync = useRef<{ saving: boolean; dirty: boolean; next?: TabState; applied?: TabState; base?: SavedTabs["state"]; revision: number; at?: number; server: string }>({ saving: false, dirty: false, revision: 0, server: name });
  const spaceChange = useMutation({ mutationFn: (input: Parameters<typeof tabsCall>[1]) => tabsCall(name, input), onMutate: () => queryClient.cancelQueries({ queryKey: queries.tabs(owner ?? "", name).queryKey }), onSuccess: (view) => {
    setState((current) => ({ ...current, spaces: view.spaces }));
    queryClient.setQueryData<SavedTabs>(queries.tabs(owner ?? "", name).queryKey, (old) => old && { ...old, spaces: view.spaces });
  }, onError: () => queryClient.invalidateQueries({ queryKey: [{ server: name }, "tabs"] }) });
  const sharing = useMutation({ mutationFn: (input: Parameters<typeof tabsCall>[1]) => tabsCall(name, input), onError: () => queryClient.invalidateQueries({ queryKey: [{ server: name }, "tabs"] }) });
  if (viewed && state.active !== pageSpace(state, pathname)[0]) setState(openTab(state, pathname, shared));
  // Another server's tabs have no tab in view, so a change that moves tabs only moves them.
  const settled = (next: TabState) => viewed ? openTab(next, next.active, shared) : next;
  // A page link whose space segment was rewritten shows the rewritten path, once the owner's spaces are known.
  useEffect(() => {
    if (!viewed || state.owner === undefined || state.active === pathname) return;
    history.replaceState(history.state, "", serverPath(state.active) + window.location.search);
    dispatchEvent(new PopStateEvent("popstate"));
  }, [viewed, state.owner, state.active, pathname]);
  // The query refetches when the page becomes visible; a window that stayed visible reloads when it regains focus.
  useEffect(() => {
    const reload = () => void saved.refetch({ cancelRefetch: false });
    addEventListener("focus", reload);
    return () => removeEventListener("focus", reload);
  }, [saved.refetch]);
  // A newer saved state merges tabs, groups, and spaces changed elsewhere with this window's own unsaved changes, which
  // are then saved. The page's window stays on its tab; when that tab closed elsewhere, or left the space's Shared tabs,
  // it enters its space as if switching to it. A tab shared in the space stays open there. A space deleted elsewhere is
  // left as if this window deleted it.
  useEffect(() => {
    const queue = sync.current;
    if (!saved.data || (state.owner === owner && queue.at === saved.dataUpdatedAt)) return;
    queue.at = saved.dataUpdatedAt;
    const alive = saved.data.spaces.some((item) => item.id === state.space);
    const base = queue.base, revision = Number(saved.data.revision), inShared = viewed && alive && isShared(shared, state.space, state.active);
    // A first load saves the tab its URL added.
    if (state.owner !== owner) {
      [queue.base, queue.revision] = [saved.data.state, revision];
      return setState(viewed ? loadTabs(pathname, owner, saved.data, shared) : savedTabs(owner, saved.data));
    }
    if (revision <= queue.revision) {
      if (!alive) return setState(viewed ? openTab(leave(state, state.space, saved.data.spaces), state.active, shared) : leave(state, state.space, saved.data.spaces));
      // A space made or renamed elsewhere changes no tabs, so it arrives without a newer revision.
      if (JSON.stringify(saved.data.spaces) !== JSON.stringify(state.spaces)) setState((current) => ({ ...current, spaces: saved.data!.spaces }));
      if (viewed && !inShared && !state.tabs.some((tab) => tab.path === state.active && tab.space === state.space)) navigate(entry(state, state.space, shared));
      return;
    }
    [queue.base, queue.revision] = [saved.data.state, revision];
    const next = savedTabs(owner, { spaces: saved.data.spaces, state: mergeTabs(base!, savedState(state), saved.data.state) }), preview = state.tabs.find((tab) => tab.preview);
    const placed: Tab[] = next.tabs.map((tab) => ({ ...tab, unplaced: state.tabs.find((item) => item.path === tab.path)?.unplaced }));
    const tabs = preview && !placed.some((tab) => tab.path === preview.path) && next.spaces.some((item) => item.id === preview.space) ? arrange(placed.toSpliced(state.tabs.indexOf(preview), 0, preview)) : placed;
    // Another server's tabs take the space it saved; the page's stay in the space of the tab in view.
    const open = !viewed ? { space: next.space } : inShared ? { space: state.space } : tabs.find((tab) => tab.path === state.active);
    const merged = { ...next, tabs, active: state.active, space: open?.space && next.spaces.some((item) => item.id === open.space) ? open.space : next.spaces.some((item) => item.id === state.space) ? state.space : next.spaces[0].id };
    queue.applied = queue.dirty ? undefined : merged;
    setState(merged);
    if (viewed && !open) navigate(entry(merged, merged.space, shared));
  }, [saved.data, saved.dataUpdatedAt, owner, state, pathname, shared, viewed]);
  // Each change to the saved form saves the whole state, made from the saved revision, one save at a time, the newest
  // waiting change next. A failed save is not undone: the next change or reload saves it again. A save refused because
  // another window saved first answers with that newer state, which the reload above merges in and saves again.
  const flush = () => {
    const queue = sync.current, sent = savedState(queue.next!);
    queue.next = undefined;
    if (JSON.stringify(sent) === JSON.stringify(queue.base)) return void (queue.dirty = false);
    queue.saving = true;
    void tabsCall(queue.server, { state: sent, revision: queue.revision }).then((reply) => {
      if (reply.stale) return;
      [queue.base, queue.revision] = [sent, Number(reply.revision)];
      savedRevisions.set(queue.server, queue.revision);
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
  const moveTo = (paths: Set<string>, space: string) => setState((current) => settled({ ...current, tabs: current.tabs.map((tab) => paths.has(tab.path) ? { ...relocate(tab, space), group: undefined } : tab) }));
  // An unplaced session moves to the space it was born in once its row names it; an unknown new space is fetched first.
  // A background window holds stale tabs, so it waits until it is focused and has reloaded.
  const settle = (rows: Session[]) => {
    if (!document.hasFocus() || saved.isFetching) return;
    for (const tab of state.tabs.filter((item) => item.unplaced)) {
      const birth = rows.find((row) => rowPath(row) === tab.path)?.spaceId;
      if (birth === undefined) continue;
      if (birth && !state.spaces.some((space) => space.id === birth)) {
        void tabsCall(name, {}).then((view) => setState((current) => view.spaces.some((space) => space.id === birth) ? { ...current, spaces: view.spaces } : current));
        return;
      }
      if (birth && birth !== tab.space) moveTo(new Set([tab.path]), birth);
      else setState((current) => ({ ...current, tabs: current.tabs.map((item) => item.path === tab.path ? { ...item, unplaced: undefined } : item) }));
    }
  };
  const update = (change: (tabs: Tab[]) => Tab[]) => setState((current) => ({ ...current, tabs: arrange(change(current.tabs)) }));
  const regroup = (match: (tab: Tab) => boolean, group?: string) => update((tabs) => tabs.map((tab) => match(tab) ? { ...tab, group } : tab));
  const edit = (id: string, change: Partial<TabGroup>) => setState((current) => ({ ...current, groups: current.groups.map((group) => group.id === id ? { ...group, ...change } : group) }));
  const stripOf = (space: string) => state.tabs.filter((tab) => tab.space === space && (!parked.has(tab.path) || tab.path === pathname));
  const shownOf = (space: string) => stripOf(space).filter((tab) => !isKeeper(tab));
  const tabsOf = (space: string) => state.tabs.filter((tab) => tab.space === space && !isKeeper(tab));
  const groupsOf = (space: string) => state.groups.filter((group) => stripOf(space).some((tab) => tab.group === group.id));
  const spaceOfGroup = (id: string) => state.tabs.find((tab) => tab.group === id)?.space ?? state.space;
  const shown = shownOf(state.space);
  // Closing the active tab activates the target, else its right neighbour, else its left one, in this space; an emptied space gets New session.
  // Only this person's tabs close: here, in Parked, or in another space unless it is the page in view, so a tab only in
  // Shared stays open.
  const close = (paths: string[], target?: string) => {
    const doomed = new Set(paths.filter((path) => shown.some((tab) => tab.path === path) || ((parked.has(path) || path !== pathname) && state.tabs.some((tab) => tab.path === path)))), kept = (tab: Tab) => !doomed.has(tab.path);
    const rest = state.tabs.filter(kept), empty = viewed && !shown.some(kept);
    setState({ ...state, tabs: empty ? home(rest, state.space) : rest });
    if (!doomed.has(pathname) || (empty && pathname === "/")) return;
    const at = shown.findIndex((tab) => tab.path === pathname);
    navigate(target ?? (shown.slice(at + 1).find(kept) ?? shown.slice(0, at).findLast(kept))?.path ?? "/");
  };
  // Entering a space of another server moves the page there, into that space.
  const enter = (id: string, spaces = state.spaces) => {
    const path = entry(state, id, shared);
    if (!viewed) {
      arriving = id;
      return navigate(path, false, name);
    }
    setState(openTab({ ...state, spaces, space: id }, path, shared));
    if (path !== pathname) navigate(path);
  };
  // An attention change acts on this person's attention on the tab locations paths; the session list, asked again, shows
  // it. A park or unpark moves the tabs at once, and marking the tab in view unread holds the mark while it stays in view.
  const attend: Attend = (paths, action, until) => {
    if (action === "MARK_UNREAD" && paths.includes(pathname)) sessionStorage.setItem(heldUnread, rowKey(pathname, name));
    // Each call moves and, when refused, moves back only its own tabs, as other changes may be in flight.
    const parks = action === "PARK" || action === "PARK_UNTIL_DONE";
    const move = (park: boolean) => (parks || action === "UNPARK") && setParked((list) => [...new Set(list.split("\n"))[park ? "union" : "difference"](new Set(paths))].filter(Boolean).sort().join("\n"));
    move(parks);
    sharing.mutateAsync({ attention: { paths, action, until } }).then(() => invalidate(), () => move(!parks));
  };
  // New session is one location, so a new tab in a group moves it into the group and its space, which it enters.
  const newIn = (space: string, change: (tabs: Tab[]) => Tab[] = (tabs) => tabs) => {
    if (!viewed) {
      update(change);
      arriving = space;
      return navigate("/", false, name);
    }
    setState((current) => ({ ...current, space, tabs: arrange(change(current.tabs)) }));
    navigate("/");
  };
  const newInGroup = (id: string) => newIn(spaceOfGroup(id), (tabs) => join(tabs, "/", id, spaceOfGroup(id)));
  // A Shared Search page's edit saves only onto the page it was made from. Refused because someone changed the page
  // meanwhile, it is made again onto theirs, keeping their searches and this person's change.
  const saveShared = (space: string, path: string, base: SearchPageState | undefined, page: SearchPageState) => sharing.mutate({ editShared: { space, path, base, search: page } }, { onSuccess: (reply) => {
    const theirs = sharedTab(reply.shared, space, path)?.search;
    if (!reply.stale) return;
    queryClient.setQueriesData<SavedTabs>({ queryKey: [{ server: name }, "tabs"] }, (old) => old && { ...old, shared: reply.shared });
    if (theirs) saveShared(space, path, theirs, rebase(base, page, theirs));
  } });
  const open = (path: string, query = "", space?: string, preview = false, list = shared, search?: SearchPageState) => flushSync(() => { setState((current) => openTab(space ? { ...current, space } : current, path, list, preview, search)); navigate(path + query, preview); });
  const addSearch = (page: SearchPageState, inShared: boolean) => {
    const path = freshSearch(), id = (i: number) => `${path.slice("/search/".length)}-${i}`, search = { ...page, searches: page.searches.map((item, i) => ({ ...item, id: id(i) })), active: id(page.searches.findIndex((item) => item.id === page.active)) };
    return inShared ? sharing.mutate({ share: { space: state.space, path, search } }, { onSuccess: (reply) => open(path, "", undefined, false, reply.shared) }) : open(path, "", undefined, false, shared, search);
  };
  return {
    name, viewed, state, sharedIn, stripOf, shownOf, shared, closed, pathname, close, enter, edit, editor, setEditor, settle, picking, setPicking, parked, setParked, attend, open, newIn,
    deleted: (path: string) => close([path]),
    // Opening a tab, or a Shared row without a tab of its own, enters its space; another server's moves the page there.
    go: (path: string, space: string, inShared = false) => {
      if (!viewed) {
        arriving = space;
        return navigate(path, false, name);
      }
      if (inShared) return open(path, "", space);
      if (path !== pathname) navigate(path);
    },
    reopen: ({ path, space, search }: ClosedTab = closed[0]) => {
      const known = state.spaces.find((item) => item.id === space)?.id;
      flushSync(() => {
        setState((current) => {
          const next = openTab(known ? { ...current, space: known } : current, path, shared, false, search);
          return known ? { ...next, tabs: next.tabs.map((tab) => tab.path === path ? { ...tab, unplaced: undefined } : tab) } : next;
        });
        navigate(path);
      });
    },
    // A moved Shared row moves, within its space, for this person alone; their order keeps only paths still shared.
    moveShared: (from: string, to: string) => setState((current) => {
      const space = shared.find((tab) => tab.path === from)?.space ?? current.space;
      return { ...current, sharedOrder: [...new Set([...moveQueueId(sharedIn(space, current.sharedOrder), from, to), ...current.sharedOrder])].filter((path) => shared.some((tab) => tab.path === path)) };
    }),
    // A rejected name keeps the old one.
    renameSpace: (id: string, name: string) => spaceChange.mutate({ renameSpace: { id, name } }),
    deleteSpace: async (doomed: string) => {
      const reply = await spaceChange.mutateAsync({ deleteSpace: doomed });
      setState((current) => settled(leave(current, doomed, reply.spaces)));
    },
    // Cmd/Ctrl+[ and ], and their palette commands, step through this space's Shared rows, then its tabs, each tab once,
    // wrapping and skipping parked ones.
    step: (back: boolean) => {
      const order = [...new Set([...sharedIn(state.space).filter((path) => !parked.has(path)), ...shown.map((tab) => tab.path)])], at = order.indexOf(pathname);
      const target = order[at < 0 ? (back ? order.length - 1 : 0) : (at + (back ? order.length - 1 : 1)) % order.length];
      if (target !== pathname) navigate(target);
    },
    // A Search page's change merges into the person's own page or, inShared, into the one page everyone in space shares,
    // which the cache takes at once so the next change merges into it.
    editSearch: (path: string, change: Partial<SearchPageState>, inShared = false, space = state.space) => {
      const merge = <T extends { path: string; search?: SearchPageState }>(tab: T) => tab.path === path ? { ...tab, search: { searches: [], active: "", ...tab.search, ...change } } : tab;
      if (!inShared) return setState((current) => ({ ...current, tabs: current.tabs.map(merge) }));
      const next = shared.map((tab) => tab.space === space ? merge(tab) : tab);
      queryClient.setQueriesData<SavedTabs>({ queryKey: [{ server: name }, "tabs"] }, (old) => old && { ...old, shared: next });
      saveShared(space, path, sharedTab(shared, space, path)?.search, sharedTab(next, space, path)!.search!);
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
    // A new session's tab waits for its row to learn the space it was born in. A sticky agent's resumed session
    // keeps the tab it already has.
    create: (id: string) => update((tabs) => tabs.some((tab) => tab.path === sessionPath(id)) ? tabs.filter((tab) => tab.path !== "/")
      : tabs.map((tab) => tab.path === "/" ? { path: sessionPath(id), group: tab.group, space: tab.space, unplaced: true } : tab)),
    // Close, the attention items, the group commands, and the moves act on paths, the selected tabs of space; view is
    // this server's.
    commands: (path: string, view: SidebarView, paths = new Set([path]), space = state.tabs.find((item) => item.path === path)?.space ?? state.space): TabCommand[] => {
      const tab = state.tabs.find((item) => item.path === path), there = tabsOf(space), sharedRow = sharedTab(shared, space, path);
      const id = pathSession(path);
      const birthSpace = paths.size === 1 && id ? view.rows.find((row) => row.id === id && rowPath(row) === path)?.spaceId : undefined;
      const birth = state.spaces.find((item) => item.id === birthSpace);
      // Shared changes act on this one tab, for everyone.
      // Taking a Search page out of Shared keeps the page it held, as the server returns it, as this person's own, with
      // any searches of their own page there, so nobody's searches are lost.
      const keep = ({ unshared: kept }: SavedTabs) => kept && setState((current) => ({ ...current, tabs: current.tabs.some((item) => item.path === path)
        ? current.tabs.map((item) => item.path === path ? { ...item, search: { ...kept, searches: [...kept.searches, ...item.search?.searches.filter((own) => !kept.searches.some((other) => other.id === own.id)) ?? []] } } : item)
        : [...current.tabs, { path, space, search: kept }] }));
      const share = sharedRow ? [{ key: "unshare", label: "Remove from Shared", run: () => void tabsCall(name, { unshare: { space, path } }, keep).catch(() => queryClient.invalidateQueries({ queryKey: [{ server: name }, "tabs"] })) }]
        : path === "/" ? [] : [{ key: "share", label: "Add to Shared", run: () => sharing.mutate({ share: { space, path, search: tab?.search } }) }];
      const source = sharedRow?.search ?? tab?.search;
      const selected = tab || !sharedRow ? state.tabs.filter((item) => paths.has(item.path)) : [{ path, space, search: source }];
      const copy = viewed && isSearch(path) ? [{ key: "duplicate", label: "Duplicate", run: () => addSearch({ ...source!, name: `${searchTitle(source)} copy` }, !!sharedRow) }] : [];
      const attention = attentionItems(view, path, paths, attend, setPicking), closing = { key: "close", label: "Close", run: () => close([...paths]) };
      // The tab in view copies its whole address, with the view state in its query and fragment.
      const link = { key: "link", label: "Copy link", run: () => void copyText(viewed && path === pagePath() ? window.location.href : window.location.origin + serverPath(path, name), document.body) };
      const adopt = (tabs: Tab[]) => [...tabs, ...selected.filter((item) => !tabs.some((other) => other.path === item.path))];
      const placing: TabCommand[] = [
        { key: "group-new", label: "Add to new group", sub: { menu: "Add to group", label: "New group" }, run: () => {
          const id = crypto.getRandomValues(new Uint32Array(2)).join("-");
          setState((current) => ({ ...current, groups: [...current.groups, { id, name: "", color: Object.keys(groupColors)[groupsOf(space).length % Object.keys(groupColors).length] }] }));
          update((tabs) => join(adopt(tabs).map((item) => paths.has(item.path) ? { ...item, group: id } : item), keeper(id), id, space));
          setPreference(SIDEBAR_VIEW_KEY, "spaces");
          setEditor({ id, open: true });
        } },
        ...groupsOf(space).filter((group) => selected.some((item) => item.group !== group.id)).map((group) => ({ key: `group-${group.id}`, label: `Add to group "${groupName(group)}"`, sub: { menu: "Add to group", label: groupName(group) }, run: () => update((tabs) => selected.reduce((joined, item) => join(joined, item.path, group.id, space), adopt(tabs))) })),
        ...selected.some((item) => item.group) ? [{ key: "group-remove", label: "Remove from group", run: () => regroup((item) => paths.has(item.path)) }] : [],
        // A session moved to another space can go back to the space it was born in.
        ...birth && birth.id !== (tab?.space ?? space) ? [{ key: "restore", label: `Restore to "${birth.name}"`, run: () => {
          update(adopt);
          moveTo(paths, birth.id);
        } }] : [],
        ...state.spaces.filter((item) => item.id !== space).map((item) => ({ key: `space-${item.id}`, label: `Move to "${item.name}"`, sub: { menu: "Move to space", label: item.name }, run: () => {
          update(adopt);
          moveTo(paths, item.id);
        } })),
      ];
      const closings = !shownOf(space).some((item) => item.path === path) ? tab ? [closing] : [] : [
        closing,
        { key: "close-others", label: "Close others", run: () => close(there.flatMap((item) => item.path === path ? [] : [item.path]), path) },
        { key: "close-right", label: "Close to the right", run: () => close(there.slice(there.findIndex((item) => item.path === path) + 1).map((item) => item.path), path) },
        { key: "close-all", label: "Close all", run: () => close(there.map((item) => item.path)) },
      ];
      return [...closings, link, ...share, ...copy, ...attention, ...placing];
    },
    newInGroup,
    // Ungrouping or closing a group's tabs leaves it empty; only Delete group, which closes them too, removes it.
    groupCommands: (id: string) => {
      const space = spaceOfGroup(id), members = tabsOf(space).flatMap((tab) => tab.group === id ? [tab.path] : []);
      return [
        { key: "new", label: "New tab in group", run: () => newInGroup(id) },
        { key: "ungroup", label: "Ungroup", run: () => regroup((tab) => tab.group === id && !isKeeper(tab)) },
        { key: "close", label: "Close group", run: () => close(members) },
        { key: "delete", label: "Delete group", run: () => {
          close(members);
          update((tabs) => tabs.filter((tab) => tab.path !== keeper(id)));
        } },
        ...state.spaces.filter((item) => item.id !== space).map((item) => ({ key: `space-${item.id}`, label: `Move group to "${item.name}"`, run: () => setState(settled({ ...state, tabs: state.tabs.map((tab) => tab.group === id ? relocate(tab, item.id) : tab) })) })),
      ];
    },
  };
}

// Sessions follow their sidebar rows to the spaces they were born in, and the tabs a server lists as parked leave its
// cards for Parked. Only this, TabNotifications and AttentionButton read the page's rows at the top, so their updates do
// not redraw the whole app; another server's section reads its own.
function TabSettling() {
  const { rows, marks } = useContext(Sidebar), page = useContext(TabActions), { settle, setParked } = useContext(TabStore) ?? page;
  const parked = [...parkedPaths({ rows, marks })].sort().join("\n");
  useEffect(() => settle(rows), [rows, settle]);
  // A server that holds no listing, as while it does not answer, says nothing about what is parked.
  useEffect(() => { if (marks !== undefined) setParked(parked); }, [parked, marks, setParked]);
  return null;
}

// A server's tabs this person sees: their own tabs of any space, then its Shared rows, each in its space; the live ones
// here, the saved ones elsewhere.
function useTabsOf() {
  const { servers } = useContext(Sidebar), { state, shared } = useContext(TabActions);
  const name = useServer();
  return (server: string) => {
    const saved = servers[server]?.tabs, own = server === name ? state.tabs : saved?.state.tabs ?? [];
    return { own, open: [...own, ...server === name ? shared : saved?.shared ?? []] };
  };
}

// The marks on a server's tab locations, from its session rows and its rows of tabs that are not sessions.
type Mark = TabAttention & { server: string; row?: Session };
const marksOf = (server: string, view: SidebarView): Mark[] => [...view.rows.flatMap((row) => row.unreadAt ? [{ ...rowAttention(row), path: rowPath(row), server, row }] : []), ...(view.marks ?? []).flatMap((mark) => mark.unreadAt ? [{ ...mark, server }] : [])];
// A session row's attention, which carries its quiet flag as markedUnread.
const rowAttention = (row: Session): Omit<TabAttention, "path"> => ({ unreadAt: row.unreadAt, quiet: row.markedUnread, parked: row.parked, until: row.until, untilDone: row.untilDone, note: row.note, autoPark: row.autoPark, unfollowed: row.unfollowed });
// A tab location's attention on its server: its session row's, else its own row's.
function attentionOf(view: Pick<SidebarView, "rows" | "marks"> | undefined, path: string): Omit<TabAttention, "path"> | undefined {
  const id = pathSession(path), row = id ? view?.rows.find((item) => item.id === id) : undefined;
  if (!id) return view?.marks?.find((mark) => mark.path === path);
  return row && rowAttention(row);
}

// sub names an item's submenu and its label there.
type TabCommand = { key: string; label: string; sub?: { menu: string; label: string }; run: () => void };
// A tab menu draws a line between its sections, as a macOS menu does: renaming, closing, sharing, attention, grouping and
// moving, then deleting. Its items come in that order.
const menuSections = [/^rename/, /^close/, /^(link|share|unshare|duplicate)/, /^(read|unread|park|unpark|follow|unfollow)/, /^(group|restore|space)/, /^delete/];
const menuSection = (key: string) => menuSections.findIndex((pattern) => pattern.test(key));
// A submenu draws its lines the same way: Park's are Until it is done, its times, then Until I unpark.
const submenuSections = [/^park-done$/, /^park-/];
type Attend = (paths: string[], action: AttentionAction, until?: string) => void;
// A Park waiting for the person to pick its time.
type Picking = { send: (until: string) => void };
// Park offers these times, reckoned when chosen, in local time; each names itself in the submenu, and after Park.
const nine = (days: number) => new Date().setHours(24 * days + 9, 0, 0, 0);
const laters = [["1h", "1 hour", "for 1 hour", () => Date.now() + 3_600_000], ["4h", "4 hours", "for 4 hours", () => Date.now() + 14_400_000],
  ["tomorrow", "Tomorrow 9:00", "until tomorrow at 9:00", () => nine(1)], ["monday", "Next Monday 9:00", "until next Monday at 9:00", () => nine((8 - new Date().getDay()) % 7 || 7)],
  ["pick", "Pick a time…", "until a picked time…", undefined]] as const;

// The attention items for the tab locations paths, from the menu of path, read from their server's view: send makes the
// change on that server, and pick asks for a time. New session has none, and nor does a server offline or on another
// protocol. Mark as read and Mark as unread each act on the tabs it fits, Park offers Until it is done and Auto-park
// only when every tab is a session's, and a parked tab offers Unpark in its place.
function attentionItems(view: SidebarView | undefined, path: string, targets: Iterable<string>, send: Attend, pick: (picking: Picking) => void): TabCommand[] {
  const paths = [...targets].filter((item) => item !== "/"), of = (item: string) => attentionOf(view, item) ?? {};
  if (!view || view.offline || view.foreign || !paths.length) return [];
  const bold = paths.filter((item) => of(item).unreadAt && !of(item).parked), read = paths.filter((item) => !of(item).unreadAt && !of(item).parked);
  const followed = paths.filter((item) => !of(item).unfollowed);
  const sessions = paths.every((item) => pathSession(item));
  return [
    ...bold.length ? [{ key: "read", label: "Mark as read", run: () => send(bold, "MARK_READ") }] : [],
    ...read.length ? [{ key: "unread", label: "Mark as unread", run: () => send(read, "MARK_UNREAD") }] : [],
    ...of(path).parked ? [{ key: "unpark", label: "Unpark", run: () => send(paths, "UNPARK") }] : [
      // A session's tab can wait for its next turn to end.
      ...sessions ? [{ key: "park-done", label: "Park until it is done", sub: { menu: "Park", label: "Until it is done" }, run: () => send(paths, "PARK_UNTIL_DONE") }] : [],
      ...laters.map(([key, label, park, at]) => ({ key: `park-${key}`, label: `Park ${park}`, sub: { menu: "Park", label },
        run: () => at ? send(paths, "PARK", new Date(at()).toISOString()) : pick({ send: (until) => send(paths, "PARK", until) }) })),
      { key: "park", label: "Park until I unpark", sub: { menu: "Park", label: "Until I unpark" }, run: () => send(paths, "PARK") },
    ],
    // A session's tab can park until it is done whenever this person sends it a prompt.
    ...sessions ? [of(path).autoPark ? { key: "park-auto", label: "Stop auto-parking", run: () => send(paths, "STOP_AUTO_PARK") } : { key: "park-auto", label: "Auto-park when I send", run: () => send(paths, "AUTO_PARK") }] : [],
    followed.length ? { key: "unfollow", label: "Unfollow", run: () => send(followed, "UNFOLLOW") } : { key: "follow", label: "Follow", run: () => send(paths, "FOLLOW") },
  ];
}

// The tabs waiting for this person among those they see on every server: marked and not parked, leaving out sessions
// that are offline.
function useWaiting() {
  const { servers } = useContext(Sidebar), tabsOf = useTabsOf();
  return Object.entries(servers).flatMap(([name, view]) => {
    const open = new Set(tabsOf(name).open.map((tab) => tab.path));
    return marksOf(name, view).filter((mark) => !mark.parked && !mark.row?.offline && open.has(mark.path));
  });
}

// Opens a tab this person has on its server: one that is only a Shared row there opens in that row's space without a tab
// of its own.
function useVisit() {
  const tabs = useContext(TabActions), tabsOf = useTabsOf();
  return (name: string, path: string) => {
    const { own, open } = tabsOf(name), space = own.some((tab) => tab.path === path) ? undefined : open.find((tab) => tab.path === path)?.space;
    if (name === server()) return tabs.open(path, "", space);
    arriving = space ?? "";
    navigate(path, false, name);
  };
}

// Opens the next tab waiting for this person after the one at path on this server, wrapping: sessions asking a question,
// then the rest, each longest waiting first. Undefined when no other tab waits.
function useNextAttention(path: string) {
  const waiting = useWaiting(), visit = useVisit();
  const since = (mark: Mark) => Date.parse((mark.row?.asking ? mark.row.stateSince : mark.unreadAt)!);
  const order = waiting.toSorted((a, b) => Number(!!b.row?.asking) - Number(!!a.row?.asking) || since(a) - since(b));
  const at = order.findIndex((mark) => mark.server === server() && mark.path === path);
  const next = order[at + 1] ?? (at > 0 ? order[0] : undefined);
  return next && (() => visit(next.server, next.path));
}

// Cmd/Ctrl+Shift+A, or this bottom bar button, opens the next tab waiting for this person, and Cmd/Ctrl+Shift+S parks
// the tab in view until it is done, as its palette command does. Each key is RocketClaw's even while it has nothing to
// do, so neither OpenCode nor the browser takes it.
function AttentionButton() {
  const path = usePathname(), next = useNextAttention(path), tabs = useContext(TabActions), sidebar = useContext(Sidebar);
  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (event.defaultPrevented || !(event.metaKey || event.ctrlKey) || event.altKey || !event.shiftKey || (event.code !== "KeyA" && event.code !== "KeyS")) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.repeat) return;
    if (event.code === "KeyA") next?.();
    else tabs.commands(path, sidebar).find((item) => item.key === "park-done")?.run();
  });
  useEffect(() => {
    const key = (event: KeyboardEvent) => onKey(event);
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, []);
  return <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label="Next session needing you" disabled={!next} onClick={next}>
    <Inbox className="size-[var(--navigation-icon)]" />
  </TooltipTrigger><TooltipContent side="top">Next session needing you</TooltipContent></Tooltip>;
}

// The server decides who is marked, on a session's row or a tab location's row: a mark that moves past the one this window
// last saw raises a desktop notification, unless it is quiet, its tab is parked, or it is the tab in view in a focused
// window, or this person has no tab for it: in their own tabs or Shared. Its click opens the tab where
// they have it. A window whose tabs are older than its listing names notifies nothing from it until it reloads them, so it
// never takes a tab closed or opened elsewhere for one it holds. The first complete live list of each server after a page load or owner change only records the marks; restored rows
// never count. A session is titled by its name, else its agent, or a remote OpenCode session by its tab title, and shows
// the note a timer wrote, else the start of the reply: a RocketClaw row's preview is its last message, an OpenCode row's
// reply its last answer. Any other tab is titled as its tab is and shows the note the server wrote.
// ponytail: rows refresh on the sidebar poll, so a hidden window's throttled timers can delay the notification;
// server push would fix it. Every window that saw the mark notifies under the location's tag, so the system shows
// one notification, even while another window has the tab in view.
function TabNotifications() {
  const { servers } = useContext(Sidebar), tabsOf = useTabsOf(), visit = useVisit(), waiting = useWaiting().length;
  const owner = useQuery(queries.identity("")).data?.username;
  const seen = useRef<{ owner?: string; unread: Map<string, string>; servers: Set<string> }>({ unread: new Map(), servers: new Set() });
  useEffect(() => {
    // A server's rows count once it listed them all, and its first full listing raises nothing.
    const complete = Object.keys(servers).filter((name) => servers[name].enumerationComplete && !servers[name].stale);
    if (!complete.length) return;
    const marks = complete.flatMap((name) => marksOf(name, servers[name])), key = (mark: Mark) => rowKey(mark.path, mark.server);
    const last = seen.current.owner === owner ? seen.current : undefined;
    seen.current = { owner, unread: new Map([...last?.unread ?? [], ...marks.map((mark) => [key(mark), mark.unreadAt!] as const)]), servers: new Set([...last?.servers ?? [], ...complete]) };
    if (!last || !("Notification" in window) || Notification.permission !== "granted") return;
    for (const mark of marks) {
      if (!last.servers.has(mark.server) || mark.quiet || mark.parked || mark.unreadAt === last.unread.get(key(mark)) || (document.hasFocus() && mark.server === server() && mark.path === pagePath())) continue;
      const tab = tabsOf(mark.server).open.find((item) => item.path === mark.path); if (!tab) continue;
      const { row } = mark, reply = row ? cleanText((row.server ? row.reply : row.preview) ?? "", {}).replace(/\s+/g, " ").trim() : "";
      const title = !row ? tabLabel({ path: mark.path, search: tab.search }, { id: mark.path }, Search)[0] : row.server || !row.agent ? sessionTitle(row) : row.name || row.agent;
      // A notification replacing one with its tag alerts again in Chromium only with renotify, which TypeScript's DOM types omit.
      const options = { body: mark.note || (reply.length > 200 ? `${reply.slice(0, 199)}…` : reply || (row ? "Finished answering" : "")), tag: key(mark), renotify: true };
      let notification: Notification;
      // Android Chrome shows notifications only from a service worker, which this app lacks: its constructor throws.
      try { notification = new Notification(cleanText(title, {}), options); } catch { return; }
      notification.onclick = () => { focus(); visit(mark.server, mark.path); notification.close(); };
    }
  });
  // The window title counts the tabs waiting for this person, as "(2) lab - RocketClaw", keeping the server's name from
  // index.html. An OpenCode view's own titles come after index.html's, so they never show.
  useEffect(() => { document.title = (waiting ? `(${waiting}) ` : "") + document.title.replace(/^\(\d+\) /, ""); }, [waiting]);
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

const arrows: Record<string, number> = { ArrowLeft: -1, ArrowUp: -1, ArrowRight: 1, ArrowDown: 1 }, sharedDrag = "application/x-rocketclaw-shared", listDrag = "application/x-rocketclaw-tab-list";
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

function menuItems(tabs: TabStoreValue, menu: CardMenu, view: ServerView, targets: Set<string>, rename: () => void, remove: (id: string) => void) {
  const search = isSearch(menu.path), id = pathSession(menu.path);
  const regenerate = { key: "rename-title", label: "Regenerate title", run: () => void queryClient.getMutationCache().build(queryClient, { mutationKey: ["regenerateTitle"], mutationFn: mutations.updateSession }).execute({ id, name: "", server: tabs.name }).then(view.invalidateQueries, () => undefined) };
  const renaming = (search || id) && targets.size === 1 ? [{ key: "rename", label: search ? "Rename search" : "Rename session", run: rename }, ...search ? [] : [regenerate]] : [];
  const session = tabs.viewed && targets.size === 1 ? view.rows.find((row) => row.id === id) : undefined;
  return [...renaming, ...tabs.commands(menu.path, view, targets, menu.space), ...session ? [{ key: "delete", label: "Delete session", run: () => remove(session.id) }] : []];
}

const threadPanelItems = (menu: { path: string }, single: boolean, name: string): TabCommand[] =>
  pathSession(menu.path) && single ? [
    { key: "pull-requests", label: threadGitHubLabel, run: () => navigate(`${menu.path}?pullRequests=1`, false, name) },
    { key: "hosted-pages", label: "Hosted Pages", run: () => navigate(menu.path === pagePath() && name === server() ? panelHref("1", "pagePreview") : `${menu.path}?pagePreview=1`, false, name) },
  ] : [];

type TabStoreValue = ReturnType<typeof useTabs>;
// A card's tabs, which its rows rename and edit through; outside a card, the page's.
const TabStore = createContext<TabStoreValue | null>(null);
type CardMenu = { path: string; anchor: MenuAnchor; space: string; shared?: boolean };
// The tabs picked besides the one in view, in one card, while that tab stays in view.
type Picked = { at: string; card: string; paths: Set<string> };
type Card = { tabs: TabStoreValue; label: string; folds: Record<string, boolean>; waiting: Set<string>; newChat: () => void; picked: Picked; setPicked: (picked: Picked) => void; editing?: string; setEditing: (key?: string) => void; renaming?: string; setRenaming: (space?: string) => void; menu?: CardMenu; setMenu: (menu: CardMenu) => void; refocus: (space: string) => void; move: (key: string, step: number) => void };
const selectionIn = (tabs: TabStoreValue, picked: Picked, space: string) =>
  new Set(tabs.shownOf(space).flatMap((tab) => tab.path === tabs.pathname || (picked.at === tabs.pathname && picked.card === `${tabs.name}/${space}` && picked.paths.has(tab.path)) ? [tab.path] : []));
// The keyboard stop of a card, else of the first list that has one.
const selected = (node: RefObject<HTMLElement | null>, card?: string) => (card && node.current?.querySelector<HTMLElement>(`[data-card="${CSS.escape(card)}"] [role="tab"][tabindex="0"]`)) || node.current?.querySelector<HTMLElement>('[role="tab"][tabindex="0"]');
// What a drag started on: a card's tabs and groups move only within it, and a card, as "card:" and its key, among cards.
let dragging = "";

// A card's work: its tabs waiting for this person, whether any of its tabs asks them something, and whether a turn runs
// in any of its tabs.
function cardWork(name: string, paths: string[], rows: Session[], waiting: Set<string>) {
  const all = new Set(paths);
  return { unread: new Set(paths.filter((path) => waiting.has(rowKey(path, name)))), asking: rows.some((row) => row.asking && all.has(rowPath(row))), running: rows.some((row) => row.running && !row.asking && all.has(rowPath(row))) };
}

const stripViews = [["recent", "Recent"], ["spaces", "Spaces"], ["parked", "Parked"]] as const, SIDEBAR_VIEW_KEY = "sidebar-view";

function TabStrip({ tabs, newChat }: { tabs: TabStoreValue; newChat: () => void }) {
  const here = useServer(), names = ["", ...rocketclaws().map((remote) => remote.name)];
  return <StoreChain names={names.filter((name) => name !== here)} stores={[tabs]} render={(stores) => <SidebarViews stores={stores.toSorted((a, b) => names.indexOf(a.name) - names.indexOf(b.name))} newChat={newChat} />} />;
}

function SidebarViews({ stores, newChat }: { stores: TabStoreValue[]; newChat: () => void }) {
  const view = usePreference(SIDEBAR_VIEW_KEY) ?? "recent", { servers } = useContext(Sidebar), lists = useLists();
  const unread = (server: string, path: string, parked = false) => { const mark = attentionOf(servers[server], path); return !!mark?.unreadAt && (parked || !mark.parked) && !servers[server]?.offline; };
  const spaces = stores.flatMap((tabs) => [...new Set(tabs.state.spaces.flatMap((space) => [...tabs.sharedIn(space.id), ...tabs.shownOf(space.id).map((tab) => tab.path)]))].map((path) => ({ server: tabs.name, path })));
  const counts = { recent: recentOf(stores, lists, servers).filter((tab) => unread(tab.server, tab.path)).length, spaces: spaces.filter((tab) => unread(tab.server, tab.path)).length, parked: parkedOf(stores).filter((tab) => unread(tab.server, tab.path, true)).length };
  return <div data-tab-lists className="flex h-full shrink-0 flex-col">
    <div role="group" aria-label="Sidebar view" className="flex shrink-0 gap-0.5 border-b p-1">
      {stripViews.map(([key, label]) => <button key={key} type="button" aria-pressed={view === key} onClick={() => setPreference(SIDEBAR_VIEW_KEY, key)}
        className="flex min-w-0 flex-auto items-center justify-center gap-1 rounded-md px-1 py-1 text-xs font-medium text-muted-foreground hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring aria-pressed:bg-accent aria-pressed:text-foreground">
        <span className="truncate">{label}</span>{counts[key] ? <span aria-hidden="true" className="min-w-4 rounded-full bg-muted px-1 text-[11px] text-muted-foreground">{counts[key]}</span> : null}
      </button>)}
    </div>
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto [scrollbar-width:thin]">
      {view === "spaces" ? <Cards stores={stores} newChat={newChat} /> : view === "parked" ? <ParkedTabs stores={stores} /> : <RecentTabs stores={stores} />}
      <ParkedSync stores={stores} />
      <div className="min-h-4 flex-1" onDoubleClick={newChat} />
    </div>
  </div>;
}

type ListChange = { listAdd?: ListTab; listForget?: ListTab; listOrder?: ListTab[] };
const sameListed = (a: ListTab) => (b: ListTab) => a.list === b.list && a.server === b.server && a.path === b.path;
const listOf = (lists: ListTab[] | undefined, list: TabListName) => (lists ?? []).filter((tab) => tab.list === list);
const addListed = (tab: ListTab) => (old: ListTab[]) => old.some(sameListed(tab)) ? old : [tab, ...old];
const forgetListed = (tab: ListTab) => (old: ListTab[]) => old.filter((item) => !sameListed(tab)(item));
let listSaves: { latest?: ListChange; pending?: ListTab[]; saves: Promise<unknown> } = { saves: Promise.resolve() };
const listListeners = new Set<() => void>();
const showLists = (pending?: ListTab[]) => { listSaves.pending = pending; for (const listener of listListeners) listener(); };
const savedLists = () => listSaves.pending ?? queryClient.getQueryData<SavedTabs>(localTabsKey().queryKey)?.lists;
function saveList(change: ListChange, apply: (old: ListTab[]) => ListTab[]) {
  const { owner, queryKey } = localTabsKey();
  if (owner === undefined || !queryClient.getQueryData<SavedTabs>(queryKey)) return;
  listSaves.latest = change;
  showLists(apply(savedLists() ?? []));
  listSaves.saves = listSaves.saves.then(() => mutations.tabs(change, "")).then((reply) => {
    let done = false;
    const settled = () => {
      if (done || listSaves.latest !== change) return true;
      if (queryClient.getQueryState(queryKey)?.fetchStatus === "fetching") return false;
      done = true;
      queryClient.setQueryData<SavedTabs>(queryKey, (old) => old && { ...old, lists: reply.lists }, { updatedAt: queryClient.getQueryState(queryKey)?.dataUpdatedAt });
      showLists();
      return true;
    };
    if (settled()) return;
    const stop = queryClient.getQueryCache().subscribe(() => { if (settled()) stop(); });
  }, () => {
    listSaves = { saves: Promise.resolve() };
    showLists();
    void queryClient.invalidateQueries({ queryKey });
  });
}

function useLists() {
  const saved = useLocalTabs()?.lists;
  const pending = useSyncExternalStore((listener) => { listListeners.add(listener); return () => void listListeners.delete(listener); }, () => listSaves.pending);
  return pending ?? saved;
}

function useRecordVisit({ name, state }: TabStoreValue, browsing: boolean) {
  const loaded = useLocalTabs() !== undefined;
  const record = useEffectEvent(() => {
    const path = state.active, tab = state.tabs.find((item) => item.path === path), visit: ListTab = { list: "recent", server: name, path, space: tab?.space ?? state.space };
    if (state.owner === undefined || browsing || !path || path === "/" || tab?.preview || !document.hasFocus()) return;
    if (!savedLists()?.some(sameListed(visit))) saveList({ listAdd: visit }, addListed(visit));
  });
  const returned = useEffectEvent(async () => {
    await queryClient.refetchQueries({ queryKey: localTabsKey().queryKey });
    record();
  });
  useEffect(() => record(), [state.active, state.owner, browsing, loaded]);
  useEffect(() => {
    const focused = () => void returned();
    addEventListener("focus", focused);
    return () => removeEventListener("focus", focused);
  }, []);
}

const placeOf = (tabs: TabStoreValue, item: ListTab) => {
  const own = tabs.state.tabs.find((tab) => tab.path === item.path);
  return { own, space: own?.space ?? (isShared(tabs.shared, item.space, item.path) ? item.space : tabs.shared.find((tab) => tab.path === item.path)?.space ?? item.space) };
};

const recentOf = (stores: TabStoreValue[], lists: ListTab[] | undefined, servers: Record<string, ServerView>) => listOf(lists, "recent").filter((item) => {
  const tabs = stores.find((store) => store.name === item.server), mark = attentionOf(servers[item.server], item.path);
  const open = !!tabs && (tabs.state.tabs.some((tab) => tab.path === item.path && !tab.preview) || tabs.shared.some((tab) => tab.path === item.path));
  return open && !(mark?.parked && !mark.untilDone);
});

function RecentTabs({ stores }: { stores: TabStoreValue[] }) {
  const { servers } = useContext(Sidebar), lists = useLists(), recent = listOf(lists, "recent");
  return <TabList stores={stores} list="recent" label="Recent tabs" items={recentOf(stores, lists, servers)} remove={(item) => saveList({ listForget: item }, forgetListed(item))}
    removeLabel={(title) => `Remove ${title} from Recent`} empty={lists && !recent.length ? "Tabs you open show up here." : undefined} />;
}

const parkedOf = (stores: TabStoreValue[]) => stores.flatMap((tabs) => parkedRows(tabs.state.tabs, tabs.shared, tabs.parked).map((tab): ListTab => ({ list: "parked", server: tabs.name, path: tab.path, space: tab.space ?? "" })));

function ParkedTabs({ stores }: { stores: TabStoreValue[] }) {
  const parked = parkedOf(stores), lists = useLists(), listed = listOf(lists, "parked");
  const items = [...parked.filter((tab) => !listed.some(sameListed(tab))), ...listed.flatMap((tab) => parked.find(sameListed(tab)) ?? [])];
  if (!lists) return null;
  if (!items.length) return <p className="px-3 py-2 text-sm text-muted-foreground">No parked tabs.</p>;
  return <TabList stores={stores} list="parked" label="Parked tabs" items={items} remove={(item) => stores.find((store) => store.name === item.server)?.attend([item.path], "UNPARK")}
    removeLabel={(title) => `Unpark ${title}`} />;
}

function ParkedSync({ stores }: { stores: TabStoreValue[] }) {
  const { servers } = useContext(Sidebar), lists = useLists(), parked = parkedOf(stores), listed = listOf(lists, "parked"), [focus, setFocus] = useState(0);
  const live = (server: string) => servers[server]?.enumerationComplete && !servers[server]?.stale && servers[server]?.marks !== undefined && stores.some((store) => store.name === server);
  const listing = (tab: ListTab) => live(tab.server) && parkedPaths(servers[tab.server]).has(tab.path), loaded = lists !== undefined;
  const signature = JSON.stringify([parked, listed, stores.map((store) => live(store.name) ? [...parkedPaths(servers[store.name])].sort() : null)]);
  const sync = useEffectEvent(() => {
    if (!lists || !document.hasFocus()) return;
    for (const tab of parked.toReversed()) if (listing(tab) && !listed.some(sameListed(tab))) saveList({ listAdd: tab }, addListed(tab));
    for (const tab of listed) if (live(tab.server) && !listing(tab) && !parked.some(sameListed(tab))) saveList({ listForget: tab }, forgetListed(tab));
  });
  useEffect(() => sync(), [signature, loaded, focus]);
  useEffect(() => {
    const focused = () => setFocus((count) => count + 1);
    addEventListener("focus", focused);
    return () => removeEventListener("focus", focused);
  }, []);
  return null;
}

function TabList({ stores, list, label, items, empty, remove, removeLabel }: { stores: TabStoreValue[]; list: TabListName; label: string; items: ListTab[]; empty?: string; remove: (item: ListTab) => void; removeLabel: (title: string) => string }) {
  const page = stores.find((tabs) => tabs.viewed)!, node = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<ListMenu>(), [editing, setEditing] = useState<string>();
  const storeOf = (item: ListTab) => stores.find((store) => store.name === item.server)!, key = (item: ListTab) => `${item.server}\n${item.path}`, all = listOf(useLists(), list);
  const move = (from: ListTab, to: ListTab) => {
    const keys = all.map(key), moved = moveQueueId(keys, key(from), key(to)), order = moved.map((id) => all[keys.indexOf(id)]);
    if (moved !== keys) saveList({ listOrder: order }, (old) => [...old.filter((tab) => tab.list !== list), ...order]);
  };
  const at = (node: Element) => items.find((item) => key(item) === node.closest<HTMLElement>("[data-recent]")?.dataset.recent), stop = items.find((item) => item.server === page.name && item.path === page.pathname) ?? items[0];
  const menuAt = (item: ListTab, anchor: MenuAnchor) => {
    const { own, space } = placeOf(storeOf(item), item);
    setMenu({ path: item.path, anchor, server: item.server, space, shared: !own });
  };
  return <TabStore.Provider value={page}>
    <div ref={node} role="tablist" aria-label={label} aria-orientation="vertical" className="flex shrink-0 flex-col gap-0.5 px-1 pt-1"
      onDragStart={(event) => { dragging = "list"; event.dataTransfer.setData(listDrag, key(at(event.target as Element)!)); }}
      onDragOver={(event) => { if (dragging === "list") event.preventDefault(); }}
      onDragEnd={() => { dragging = ""; }}
      onDrop={(event) => {
        const from = items.find((item) => key(item) === event.dataTransfer.getData(listDrag)), to = at(event.target as Element);
        if (dragging === "list" && from && to) move(from, to);
      }}
      onKeyDown={(event) => {
        const target = event.target as HTMLElement;
        const moveBy = () => { const from = at(target), to = from && items[items.indexOf(from) + arrows[event.key]]; if (from && to) move(from, to); };
        if (target.role !== "tab" || (event.key !== "Enter" && event.key !== " ")) return tabKeys(event, page.pathname, (_, anchor) => { const item = at(anchor as Element); if (item) menuAt(item, anchor); }, moveBy);
        event.preventDefault();
        target.click();
      }}>
      {items.map((item) => <div key={key(item)} data-recent={key(item)}><ListRow item={item} tabs={storeOf(item)} page={page} stop={item === stop} editing={item.server === page.name && item.path === editing} endEdit={() => setEditing(undefined)}
        openMenu={(anchor) => menuAt(item, anchor)} remove={() => { flushSync(() => remove(item)); selected(node)?.focus(); }} removeLabel={removeLabel} /></div>)}
      {empty ? <p className="px-2 pt-2 text-xs text-muted-foreground">{empty}</p> : null}
    </div>
    <ListMenuPopup tabs={page} menu={menu} setMenu={setMenu} list={node} setEditing={setEditing} />
  </TabStore.Provider>;
}

function ListRow({ item, tabs, page, stop, editing, endEdit, openMenu, remove, removeLabel }: { item: ListTab; tabs: TabStoreValue; page: TabStoreValue; stop: boolean; editing: boolean; endEdit: () => void; openMenu: (anchor: MenuAnchor) => void; remove: () => void; removeLabel: (title: string) => string }) {
  const view = useContext(Sidebar).servers[item.server], here = item.server === page.name, { own, space } = placeOf(tabs, item);
  const spaceName = tabs.state.spaces.find((entry) => entry.id === space)?.name ?? "", active = here && item.path === page.pathname, search = own?.search ?? sharedTab(tabs.shared, space, item.path)?.search;
  const props = { description: `${own ? "" : "Shared, "}Space: ${spaceName}${here ? "" : `, ${serverLabel(item.server)}`}`, detail: [spaceName, !here && serverLabel(item.server)].filter(Boolean).join(" · "), chosen: false,
    rule: tabs.state.groups.find((group) => group.id === own?.group)?.color, active, stop, shared: !own, space, editing, endEdit, open: () => tabs.go(item.path, space, !own), openMenu, close: remove, closeLabel: removeLabel };
  return here ? <TabItem tab={own ?? { path: item.path, search }} {...props} /> : <RemoteRow tab={{ path: item.path, search }} view={view} offline={view?.offline} {...props} />;
}

// Each other server's tabs come from a store of their own, so the strip gathers them one server at a time.
function StoreChain({ names, stores, render }: { names: string[]; stores: TabStoreValue[]; render: (stores: TabStoreValue[]) => ReactNode }) {
  return names.length ? <RemoteSection name={names[0]} rest={names.slice(1)} stores={stores} render={render} /> : render(stores);
}

function RemoteSection({ name, ...chain }: { name: string; rest: string[]; stores: TabStoreValue[]; render: (stores: TabStoreValue[]) => ReactNode }) {
  const invalidate = useContext(Sidebar).servers[name]?.invalidateQueries, remote = useMemo(() => ({ name, invalidate: () => invalidate?.() }), [name, invalidate]);
  return <RemoteTabs value={remote}><RemoteStore {...chain} /></RemoteTabs>;
}

function RemoteStore({ rest, stores, render }: { rest: string[]; stores: TabStoreValue[]; render: (stores: TabStoreValue[]) => ReactNode }) {
  const tabs = useTabs();
  return <>
    <ServerScope tabs={tabs}><TabSettling /></ServerScope>
    <StoreChain names={rest} stores={[...stores, tabs]} render={render} />
  </>;
}

// Inside a server's scope, rows read that server's sessions and marks, and edit through its tabs.
function ServerScope({ tabs, children }: { tabs: TabStoreValue; children: ReactNode }) {
  const sidebar = useContext(Sidebar), view = sidebar.servers[tabs.name], value = useMemo(() => view ? { ...sidebar, ...view } : sidebar, [sidebar, view]);
  return <Sidebar.Provider value={value}><TabStore.Provider value={tabs}>{children}</TabStore.Provider></Sidebar.Provider>;
}

// Every server's space cards, in this person's order: dragging a card's title onto another card, or
// Shift+Arrow on it, moves it there, and a card not yet placed comes last, in its server's order. A server whose tabs
// have not arrived says why instead, and its cards keep their places.
function Cards({ stores, newChat }: { stores: TabStoreValue[]; newChat: () => void }) {
  const waiting = new Set(useWaiting().map((mark) => rowKey(mark.path, mark.server))), [layout] = useLayout(), titled = stores.length > 1, node = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<CardMenu & { server: string }>(), [editing, setEditing] = useState<{ server: string; key: string }>(), [renaming, setRenaming] = useState<{ server: string; space: string }>();
  const [picked, setPicked] = useState<Picked>({ at: "", card: "", paths: new Set() }), focusing = useRef("");
  const loaded = stores.filter((tabs) => tabs.viewed || tabs.state.owner !== undefined), order = layout.order ?? [];
  const rank = (key: string) => order.indexOf(key) + 1 || Infinity;
  const sorted = loaded.flatMap((tabs) => tabs.state.spaces.map((space) => ({ key: `${tabs.name}/${space.id}`, tabs, space }))).sort((a, b) => rank(a.key) - rank(b.key));
  const place = (from: string, to: string, after: boolean) => {
    const keys = [...new Set([...order, ...sorted.map((entry) => entry.key)])].filter((key) => key !== from);
    saveLayout((current) => ({ ...current, order: keys.toSpliced(keys.indexOf(to) + Number(after), 0, from) }));
  };
  // A card moved by key keeps its title focused once it lands.
  useLayoutEffect(() => {
    if (!focusing.current) return;
    node.current?.querySelector<HTMLElement>(`[data-card-key="${CSS.escape(focusing.current)}"] [aria-expanded]`)?.focus();
    focusing.current = "";
  });
  const cardFor = (tabs: TabStoreValue): Card => ({
    tabs, label: titled ? serverLabel(tabs.name) : "", folds: layout.folded ?? noFolds, waiting, newChat, picked, setPicked,
    editing: editing?.server === tabs.name ? editing.key : undefined, setEditing: (key) => setEditing(key === undefined ? undefined : { server: tabs.name, key }),
    renaming: renaming?.server === tabs.name ? renaming.space : undefined, setRenaming: (space) => setRenaming(space === undefined ? undefined : { server: tabs.name, space }),
    menu, setMenu: (item) => setMenu({ ...item, server: tabs.name }), refocus: (space) => selected(node, `${tabs.name}/${space}`)?.focus(),
    move: (key, step) => {
      const to = sorted[sorted.findIndex((entry) => entry.key === key) + step]?.key;
      if (!to) return;
      focusing.current = key;
      place(key, to, step > 0);
    },
  });
  return <div ref={node} className="flex shrink-0 flex-col"
    onDragOver={(event) => { if (dragging.startsWith("card:")) event.preventDefault(); }}
    onDrop={(event) => {
      const target = (event.target as Element).closest<HTMLElement>("[data-card-key]"), from = dragging.slice("card:".length);
      if (!dragging.startsWith("card:") || !target || target.dataset.cardKey === from) return;
      const box = target.getBoundingClientRect();
      place(from, target.dataset.cardKey!, event.clientY > box.top + box.height / 2);
    }}>
    {sorted.map((entry) => <ServerScope key={entry.key} tabs={entry.tabs}>
      <SpaceCard space={entry.space} {...cardFor(entry.tabs)} />
    </ServerScope>)}
    {stores.filter((tabs) => !tabs.viewed && tabs.state.owner === undefined).map((tabs) => <ServerStatus key={tabs.name} name={tabs.name} />)}
    {loaded.map((tabs) => <ServerScope key={tabs.name} tabs={tabs}>
      <CardMenuPopup tabs={tabs} menu={menu?.server === tabs.name ? menu : undefined} setMenu={setMenu} picked={picked} setEditing={(key) => setEditing(key === undefined ? undefined : { server: tabs.name, key })} node={node} />
      <GroupEditor tabs={tabs} node={node} />
    </ServerScope>)}
  </div>;
}
const noFolds: Record<string, boolean> = {};

// A server whose saved tabs have not arrived says why, while it does not answer.
function ServerStatus({ name }: { name: string }) {
  const status = useRemoteStatus(name);
  return <p role="status" className="mx-2 my-1 text-xs text-muted-foreground">{status.text || `${serverLabel(name)}: Loading…`}</p>;
}

const cardClass = (current: boolean) => cn("mx-1 mt-1 flex shrink-0 flex-col rounded-lg border p-0.5", current ? "border-primary/20 bg-accent/50 shadow-sm" : "bg-muted/40");

// Every card is headed alike: its title, which folds it, with a folder open while it is unfolded, its name and, with
// more than one server, its server, which narrows first, "offline" while that server does not answer, a spinner while a
// turn runs in it, and a count of its tabs waiting for this person; then Needs you while one asks, and its actions.
// Dragging the heading, or Shift+Arrow on the title, moves the card.
function CardHeading({ cardKey, icon, name, server, current, title, count, asking, running, folded, onFold, move, children }: { cardKey: string; icon: [typeof Folder, typeof Folder]; name: string; server: string; current?: boolean; title?: ReactNode; count: number; asking: boolean; running: boolean; folded: boolean; onFold: () => void; move: (step: number) => void; children?: ReactNode }) {
  const { offline } = useContext(Sidebar), Icon = icon[folded ? 1 : 0];
  const description = [count && `${count} waiting`, running && "Turn running", offline && "Server offline"].filter(Boolean).join(", ");
  return <div draggable={!title} className="flex shrink-0 items-center gap-0.5" onDragStart={(event) => { dragging = `card:${cardKey}`; event.dataTransfer.setData("application/x-rocketclaw-card", cardKey); }} onDragEnd={() => { dragging = ""; }}>
    {title ?? <button type="button" aria-expanded={!folded} aria-label={server ? `${name} · ${server}` : name} aria-current={current || undefined} aria-description={description} className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md px-2 py-1 text-[13px] hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" onClick={onFold}
      onKeyDown={(event) => {
        const step = event.shiftKey && (event.key === "ArrowUp" || event.key === "ArrowDown") ? arrows[event.key] : 0;
        if (!step) return;
        event.preventDefault();
        move(step);
      }}>
      <Icon aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" /><span className="truncate"><span className={count ? "font-bold" : "font-medium"}>{name}</span>{server ? <span className="text-muted-foreground"> · {server}</span> : null}</span>
      {offline ? <span className="shrink-0 text-xs text-muted-foreground">· offline</span> : null}
      {running ? <LoaderCircle aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground motion-safe:animate-spin" /> : null}
      {count ? <span aria-hidden="true" className="ml-auto min-w-5 rounded-full bg-blue-500 px-1.5 text-center text-[11px] font-semibold text-white">{count}</span> : null}
    </button>}
    {asking ? needsYou : null}
    {children}
  </div>;
}

// A space's card, the same whichever space is current and on whichever server's page: the current space's is tinted, and
// its title is marked current. With more than one server its title names the server too. Every card starts unfolded;
// folded, it lists only the tab in view.
function SpaceCard({ space, ...card }: Card & { space: Space }) {
  const { tabs, label, waiting, folds, renaming, setRenaming, move } = card, { rows } = useContext(Sidebar), key = `${tabs.name}/${space.id}`;
  const strip = tabs.stripOf(space.id), shared = tabs.sharedIn(space.id), { unread, asking, running } = cardWork(tabs.name, [...shared, ...strip.flatMap((tab) => isKeeper(tab) ? [] : [tab.path])], rows, waiting);
  const folded = folds[key] ?? false, listed = folded ? strip.filter((tab) => tab.path === tabs.pathname) : strip;
  const current = tabs.viewed && space.id === tabs.state.space;
  return <div data-card-key={key} className={cardClass(current)}>
    <CardHeading cardKey={key} icon={[FolderOpen, Folder]} name={space.name} server={label} current={current} count={unread.size} asking={asking} running={running} folded={folded} onFold={() => foldCard(key, !folded)} move={(step) => move(key, step)}
      title={renaming === space.id ? <SpaceName name={space.name} rename={(value) => tabs.renameSpace(space.id, value)} done={() => setRenaming(undefined)} /> : undefined}>
      <SpaceActions tabs={tabs} space={space} label={`${space.name} space actions${label && ` on ${label}`}`} renaming={renaming === space.id} rename={setRenaming} />
    </CardHeading>
    <SharedGroup {...card} space={space} paths={shared} folded={folded} />
    <CardTabs {...card} space={space} strip={strip} listed={listed} />
  </div>;
}

// Renaming edits the name in place of the title, as Base UI's Menu takes printable keys. Escape empties the field, and
// leaving it blank keeps the old name.
function SpaceName({ name, rename, done }: { name: string; rename: (name: string) => void; done: () => void }) {
  return <input ref={(node) => node?.focus()} aria-label="Space name" defaultValue={name} className="m-0.5 min-w-0 flex-1 rounded-md bg-background px-2 py-1 text-[13px] outline-1 -outline-offset-1 outline-ring"
    onFocus={(event) => event.currentTarget.select()}
    onKeyDown={(event) => {
      if (event.key === "Escape") { event.preventDefault(); event.currentTarget.value = ""; }
      if (event.key === "Enter" || event.key === "Escape") event.currentTarget.blur();
    }}
    onBlur={(event) => { const value = event.currentTarget.value.trim(); if (value) rename(value); done(); }} />;
}

// A space's actions: Go to space enters it, at its last tab, else New session, or on another server opens that server's
// page there. Renaming it, and deleting it while it is not the server's last, change it for everyone, and someone limited
// to their own sessions cannot.
function SpaceActions({ tabs, space, label, renaming, rename }: { tabs: TabStoreValue; space: Space; label: string; renaming: boolean; rename: (space?: string) => void }) {
  const limited = useQuery(queries.identity(tabs.name)).data?.limited, item = "flex cursor-default items-center rounded-md px-1.5 py-1 text-sm outline-none focus:bg-accent focus:text-accent-foreground";
  return <DropdownMenu>
    <Menu.Trigger aria-label={label} className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring"><Ellipsis aria-hidden="true" className="size-3.5" /></Menu.Trigger>
    <DropdownMenuContent finalFocus={!renaming} onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className="w-auto min-w-40">
      <Menu.Item className={item} onClick={() => tabs.enter(space.id)}>Go to space</Menu.Item>
      {limited ? null : <>
        <Menu.Item className={item} onClick={() => rename(space.id)}>Rename space</Menu.Item>
        {tabs.state.spaces.length > 1 ? <Menu.Item className={item} onClick={() => void tabs.deleteSpace(space.id)}>Delete space</Menu.Item> : null}
      </>}
    </DropdownMenuContent>
  </DropdownMenu>;
}

// A card's tabs: its groups, each under its name, and its tabs, which open, close, select, reorder by dragging within the
// card or by Shift+Arrow, and take their menu. Double-clicking its empty space starts a session in its space.
function CardTabs({ tabs, space, strip, listed, label, picked, setPicked, menu, setMenu, newChat, waiting, editing, setEditing, refocus }: Card & { space: Space; strip: Tab[]; listed: Tab[] }) {
  const { pathname } = tabs, key = `${tabs.name}/${space.id}`, selection = selectionIn(tabs, picked, space.id);
  const pathOf = (event: SyntheticEvent) => (event.target as Element).closest<HTMLElement>("[data-tab]")?.dataset.tab;
  const groupOf = (tab: Tab) => tabs.state.groups.find((item) => item.id === tab.group);
  const stop = listed.some((tab) => tab.path === pathname) ? pathname : listed.find((tab) => !isKeeper(tab) && !groupOf(tab)?.collapsed)?.path;
  const pick = (paths: string[]) => setPicked({ at: pathname, card: key, paths: new Set(paths) });
  return <div role="tablist" data-card={key} aria-label={`${space.name} tabs${label && ` on ${label}`}`} aria-orientation="vertical" className="flex min-h-1 flex-col gap-0.5 rounded-md pb-1 [&_[role=tab]]:py-1"
    // A selecting click keeps the tab in view; Shift+click selects the listed tabs from it, or the first, to the one
    // clicked. Any other click drops the selection and opens the tab.
    onClickCapture={(event) => {
      const path = pathOf(event);
      if (!path || !(event.target as Element).closest('[role="tab"]')) return;
      const rendered = [...event.currentTarget.querySelectorAll<HTMLElement>("[data-tab]")].map((node) => node.dataset.tab!);
      const [from, to] = [Math.max(rendered.indexOf(pathname), 0), rendered.indexOf(path)].toSorted((a, b) => a - b);
      if (event.shiftKey) pick(rendered.slice(from, to + 1));
      else if (event.metaKey || event.ctrlKey) pick([...selection.symmetricDifference(new Set([path]))]);
      else return pick([]);
      event.stopPropagation();
    }}
    onMouseDown={(event) => { if (event.button === 1) event.preventDefault(); }}
    onDoubleClick={(event) => { if (event.target === event.currentTarget) return tabs.viewed && space.id === tabs.state.space ? newChat() : tabs.newIn(space.id); }}
    onAuxClick={(event) => {
      const path = pathOf(event);
      if (event.button !== 1 || !path) return;
      event.preventDefault();
      tabs.close([path]);
    }}
    onDragStart={(event) => {
      const group = (event.target as Element).closest<HTMLElement>("[data-group]")?.dataset.group;
      dragging = key;
      event.dataTransfer.setData(group ? "application/x-rocketclaw-group" : "application/x-rocketclaw-tab", group ?? pathOf(event)!);
    }}
    onDragOver={(event) => { if (dragging === key) event.preventDefault(); }}
    onDragEnd={() => { dragging = ""; }}
    // A group header stands for its first tab, the only drop target a collapsed group shows, or an empty group's
    // placeholder, so a tab dropped there joins it.
    onDrop={(event) => {
      if (dragging !== key) return;
      const header = (event.target as Element).closest<HTMLElement>("[data-group]")?.dataset.group, group = event.dataTransfer.getData("application/x-rocketclaw-group");
      const path = pathOf(event) ?? listed.find((tab) => !!header && tab.group === header)?.path;
      if (path && group) tabs.moveGroup(group, path);
      else if (path) tabs.move(event.dataTransfer.getData("application/x-rocketclaw-tab"), path);
    }}
    onKeyDown={(event) => {
      const target = event.target as HTMLElement;
      if (target.role === "tab" && (event.key === "Enter" || event.key === " ")) {
        event.preventDefault();
        return target.click();
      }
      if (target.role === "tab" && event.key === "Escape" && selection.size > 1 && !menu) {
        event.preventDefault();
        return pick([]);
      }
      tabKeys(event, pathname, (path, anchor) => setMenu({ path, anchor, space: space.id }), tabs.move);
    }}>
    {listed.flatMap((tab, index) => {
      const group = groupOf(tab), head = group && listed[index - 1]?.group !== group.id;
      return [
        head ? <GroupHeader key={group.id} tabs={tabs} group={group} strip={strip} listed={listed} waiting={waiting} /> : null,
        <CardEntry key={tab.path} tabs={tabs} tab={tab} group={group} listed={listed} space={space} label={label} chosen={tab.path !== pathname && selection.has(tab.path)} stop={tab.path === stop} editing={editing} setEditing={setEditing} setMenu={setMenu} refocus={refocus} />,
      ];
    })}
  </div>;
}

// An expanded empty group offers New session, which starts one in the group; a collapsed group shows only its tab in view.
function CardEntry({ tab, group, listed, space, label, chosen, stop, tabs, editing, setEditing, setMenu, refocus }: Pick<Card, "tabs" | "editing" | "setEditing" | "setMenu" | "refocus"> & { tab: Tab; group?: TabGroup; listed: Tab[]; space: Space; label: string; chosen: boolean; stop: boolean }) {
  if (isKeeper(tab)) return group!.collapsed || listed.some((item) => item.group === group!.id && !isKeeper(item)) ? null
    : <button type="button" data-tab={tab.path} aria-label={`New session in ${groupName(group!)}`} className="flex shrink-0 items-center rounded-md text-sm text-muted-foreground hover:bg-accent focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring" onClick={() => tabs.newInGroup(group!.id)}>
      <span className={cn("pointer-events-none my-1.5 ml-1 w-0.5 shrink-0 self-stretch", groupColors[group!.color])} />
      <span className="flex items-center gap-1.5 py-2 pr-3 pl-3"><SquarePen aria-hidden="true" className="size-4 shrink-0" />New session</span>
    </button>;
  if (group?.collapsed && tab.path !== tabs.pathname) return null;
  const description = [chosen && "In selection", `${group ? `Group ${groupName(group)}, ` : ""}Space: ${space.name}${label && `, ${label}`}`].filter(Boolean).join(", ");
  return <TabItem tab={tab} description={description} chosen={chosen} rule={group?.color} active={tab.path === tabs.pathname} stop={stop} editing={tab.path === editing} endEdit={() => setEditing(undefined)}
    open={() => tabs.go(tab.path, space.id)} close={() => { flushSync(() => tabs.close([tab.path])); refocus(space.id); }} openMenu={(anchor) => setMenu({ path: tab.path, anchor, space: space.id })} />;
}

// A collapsed group's name, or a group's in a folded card, goes bold, like an unread tab's title, while any of its hidden
// tabs waits for this person, and it says it needs you while any of their sessions waits on this person's answer.
// Shift+Arrow moves the group past the tab or group next to it, and its menu key or right-click opens its editor.
function GroupHeader({ tabs, group, strip, listed, waiting }: { tabs: TabStoreValue; group: TabGroup; strip: Tab[]; listed: Tab[]; waiting: Set<string> }) {
  const { rows } = useContext(Sidebar), shown = new Set(listed.map((item) => item.path));
  const { unread, asking } = cardWork(tabs.name, strip.flatMap((item) => item.group === group.id && !isKeeper(item) && (group.collapsed || !shown.has(item.path)) ? [item.path] : []), rows, waiting);
  const openEditor = () => tabs.setEditor({ id: group.id, open: true });
  return <div data-group={group.id} draggable className="flex shrink-0 items-center pt-1" onContextMenu={(event) => { event.preventDefault(); openEditor(); }}>
    <button type="button" aria-expanded={!group.collapsed} aria-label={groupName(group)} className="flex h-7 min-w-0 items-center rounded-md px-1.5 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" onClick={() => tabs.edit(group.id, { collapsed: !group.collapsed })}
      onKeyDown={(event) => {
        if ((event.shiftKey && event.key === "F10") || event.key === "ContextMenu") { event.preventDefault(); openEditor(); }
        const button = event.currentTarget, step = event.shiftKey ? arrows[event.key] : 0;
        const neighbor = step && listed[step < 0 ? listed.findIndex((item) => item.group === group.id) - 1 : listed.findLastIndex((item) => item.group === group.id) + 1];
        if (!neighbor) return;
        event.preventDefault();
        flushSync(() => tabs.moveGroup(group.id, neighbor.path));
        button.focus();
      }}>
      <span className={cn("rounded-full", group.name ? cn("max-w-48 truncate px-2 py-0.5 text-xs text-black", unread.size ? "font-bold" : "font-medium") : "size-3", groupColors[group.color])}>{group.name}</span>
    </button>
    {asking ? needsYou : null}
    <button type="button" aria-label={`Edit ${groupName(group)}`} className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-foreground" onClick={openEditor}><Ellipsis className="size-3.5" /></button>
  </div>;
}

function SharedGroup({ tabs, space, paths, folded, label, waiting, folds, editing, setEditing, setMenu }: Pick<Card, "tabs" | "label" | "waiting" | "folds" | "editing" | "setEditing" | "setMenu"> & { space: Space; paths: string[]; folded: boolean }) {
  const { rows } = useContext(Sidebar), { state, pathname } = tabs, key = `${tabs.name}#shared/${space.id}`, closed = folds[key] ?? false;
  const active = (path: string) => tabs.viewed && space.id === state.space && path === pathname, listed = folded || closed ? paths.filter(active) : paths;
  if (!paths.length || (folded && !listed.length)) return null;
  const { unread, asking } = cardWork(tabs.name, folded || closed ? paths.filter((path) => !active(path)) : [], rows, waiting);
  const rowOf = (node: Element) => node.closest<HTMLElement>("[data-shared]")?.dataset.shared, stop = listed.find(active) ?? listed[0];
  return <div role="tablist" aria-label={`${space.name} Shared tabs${label && ` on ${label}`}`} aria-orientation="vertical" className="flex flex-col gap-0.5 rounded-md [&_[role=tab]]:py-1"
    onKeyDown={(event) => {
      const target = event.target as HTMLElement;
      if (target.role !== "tab" || (event.key !== "Enter" && event.key !== " ")) return tabKeys(event, pathname, (path, anchor) => setMenu({ path, anchor, space: space.id, shared: true }), tabs.moveShared);
      event.preventDefault();
      target.click();
    }}
    onDragStart={(event) => { dragging = key; event.dataTransfer.setData(sharedDrag, rowOf(event.target as Element)!); }}
    onDragOver={(event) => { if (dragging === key) event.preventDefault(); }}
    onDragEnd={() => { dragging = ""; }}
    onDrop={(event) => {
      const to = rowOf(event.target as Element);
      if (dragging === key && to) tabs.moveShared(event.dataTransfer.getData(sharedDrag), to);
    }}>
    <div className="flex shrink-0 items-center pt-1">
      <button type="button" aria-expanded={!closed} aria-label={`Shared in ${space.name}`} className="flex h-7 w-fit min-w-0 items-center rounded-md px-1.5 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" onClick={() => foldCard(key, !closed)}>
        <span className={cn("rounded-full px-2 py-0.5 text-xs text-black", unread.size ? "font-bold" : "font-medium", groupColors.Grey)}>Shared</span>
      </button>
      {asking ? needsYou : null}
    </div>
    {listed.map((path) => <div key={path} data-shared={path}>
      <TabItem tab={{ path, search: sharedTab(tabs.shared, space.id, path)?.search }} shared space={space.id} open={() => tabs.go(path, space.id, true)} description={`Shared, Space: ${space.name}${label && `, ${label}`}`} chosen={false} rule="Grey" active={active(path)} stop={path === stop} editing={editing === `shared:${space.id}:${path}`} endEdit={() => setEditing(undefined)} openMenu={(anchor) => setMenu({ path, anchor, space: space.id, shared: true })} />
    </div>)}
  </div>;
}

// A server's tab menu acts on the selected tabs of the menu's card when it opened on one of them, else on its own tab.
function CardMenuPopup({ tabs, menu, setMenu, picked, setEditing, node }: { tabs: TabStoreValue; menu?: CardMenu; setMenu: (menu?: CardMenu & { server: string }) => void; picked: Picked; setEditing: (key?: string) => void; node: RefObject<HTMLDivElement | null> }) {
  const view = useContext(Sidebar), { setCommand } = useContext(SessionCommands);
  const selection = menu ? selectionIn(tabs, picked, menu.space) : new Set<string>(), targets = menu && !selection.has(menu.path) ? new Set([menu.path]) : selection;
  const items = !menu ? [] : [...threadPanelItems(menu, targets.size === 1, tabs.name), ...menuItems(tabs, menu, view, targets, () => setEditing(menu.shared ? `shared:${menu.space}:${menu.path}` : menu.path), (id) => setCommand({ mode: "delete", source: id }))];
  // Only this person's tabs leave their card; Shared rows stay where they are.
  return <TabMenu items={items} anchor={menu?.anchor} close={() => setMenu(undefined)} refocus={(key) => !menu?.shared && /^(close|park|unpark)/.test(key) ? selected(node, menu && `${tabs.name}/${menu.space}`) ?? true : true} />;
}

// A tab menu draws a line between its sections and gathers each submenu's items under it.
function TabMenu({ items, anchor, close, refocus }: { items: TabCommand[]; anchor?: MenuAnchor; close: () => void; refocus: (key: string) => HTMLElement | boolean }) {
  const chosen = useRef("");
  const option = ({ key, label, run }: TabCommand) => <Menu.Item key={key} onClick={() => { chosen.current = key; run(); }} className={itemClass}>{label}</Menu.Item>;
  return <Menu.Root open={!!anchor} onOpenChange={(open) => { if (!open) close(); }}>
    <Menu.Portal><Menu.Positioner anchor={anchor} sideOffset={4} align="start" className="z-50 outline-none"><Menu.Popup finalFocus={() => { const key = chosen.current; chosen.current = ""; return /^(rename|group-new)$|pick$/.test(key) ? false : refocus(key); }} onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className={popupClass}>
      {items.map((item, i) => {
        const entry = !item.sub ? option(item) : item !== items.find((other) => other.sub?.menu === item.sub!.menu) ? null : <Menu.SubmenuRoot key={item.sub.menu}>
          <Menu.SubmenuTrigger className={cn(itemClass, "justify-between gap-4 data-popup-open:bg-accent")}>{item.sub.menu}<ChevronRight className="size-4" /></Menu.SubmenuTrigger>
          <Menu.Portal><Menu.Positioner className="z-50 outline-none"><Menu.Popup className={popupClass}>{items.filter((other) => other.sub?.menu === item.sub!.menu).map((other, j, subs) => {
            const section = (key: string) => submenuSections.findIndex((pattern) => pattern.test(key)), entry = option({ ...other, label: other.sub!.label });
            return j > 0 && section(other.key) !== section(subs[j - 1].key) ? [<Menu.Separator key={`line-${other.key}`} className="-mx-1 my-1 h-px bg-border" />, entry] : entry;
          })}</Menu.Popup></Menu.Positioner></Menu.Portal>
        </Menu.SubmenuRoot>;
        return entry && i > 0 && menuSection(item.key) !== menuSection(items[i - 1].key) ? [<Menu.Separator key={`line-${item.key}`} className="-mx-1 my-1 h-px bg-border" />, entry] : entry;
      })}
    </Menu.Popup></Menu.Positioner></Menu.Portal>
  </Menu.Root>;
}

// A group's name field lives in a popover because menus take typed letters for type-ahead.
function GroupEditor({ tabs, node }: { tabs: TabStoreValue; node: RefObject<HTMLDivElement | null> }) {
  const { editor, setEditor, edit, groupCommands } = tabs, group = tabs.state.groups.find((item) => item.id === editor?.id);
  const header = () => node.current?.querySelector<HTMLElement>(`[data-group="${editor?.id}"]`) ?? null;
  return <Popover.Root open={!!editor?.open && !!group} onOpenChange={(open) => setEditor((current) => current && { ...current, open })}>
    <Popover.Portal><Popover.Positioner anchor={header} sideOffset={4} align="start" className="z-50 outline-none"><Popover.Popup aria-label="Edit group" finalFocus={() => header()?.querySelector("button") ?? selected(node) ?? true} onKeyDown={(event) => { if (event.key === "Escape") event.stopPropagation(); }} className="flex w-56 flex-col gap-2 rounded-md border bg-popover p-2 text-popover-foreground shadow-md outline-none">
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
  </Popover.Root>;
}

type ParkedRow = Tab & { shared?: boolean };
type ListMenu = CardMenu & { server: string };
const parkedPaths = (view: Pick<SidebarView, "rows" | "marks">) => new Set([...view.rows.flatMap((row) => row.parked ? [rowPath(row)] : []), ...(view.marks ?? []).flatMap((mark) => mark.parked ? [mark.path] : [])]);
const parkedRows = (tabs: Tab[], shared: SavedTabs["shared"], parked: Set<string>): ParkedRow[] =>
  [...tabs.filter((tab) => parked.has(tab.path)), ...shared.flatMap((tab) => parked.has(tab.path) && !tabs.some((own) => own.path === tab.path) ? [{ ...tab, shared: true }] : [])];

function ListMenuPopup({ tabs, menu, setMenu, list, setEditing }: { tabs: TabStoreValue; menu?: ListMenu; setMenu: (menu?: ListMenu) => void; list: RefObject<HTMLDivElement | null>; setEditing: (path: string) => void }) {
  const sidebar = useContext(Sidebar), { setCommand } = useContext(SessionCommands);
  const send = (server: string): Attend => (paths, action, until) => void tabsCall(server, { attention: { paths, action, until } }).then(sidebar.servers[server].invalidateQueries, () => undefined);
  const items = !menu ? [] : menu.server === tabs.name ? [...threadPanelItems(menu, true, menu.server), ...menuItems(tabs, menu, sidebar, new Set([menu.path]), () => setEditing(menu.path), (id) => setCommand({ mode: "delete", source: id }))]
    : [...threadPanelItems(menu, true, menu.server), ...attentionItems(sidebar.servers[menu.server], menu.path, [menu.path], send(menu.server), tabs.setPicking)];
  return <TabMenu items={items} anchor={menu?.anchor} close={() => setMenu(undefined)} refocus={(key) => /^(close|park|unpark)/.test(key) ? selected(list) ?? true : true} />;
}

function RemoteRow({ tab, view, ...props }: { tab: Tab; view?: ServerView } & Omit<ComponentProps<typeof TabItem>, "tab">) {
  const sidebar = useContext(Sidebar), value = useMemo(() => view ? { ...sidebar, ...view } : sidebar, [sidebar, view]);
  return <Sidebar.Provider value={value}><TabItem tab={{ path: tab.path, search: tab.search }} {...props} /></Sidebar.Provider>;
}

// Marks a session waiting on this person's answer, in its tab, its collapsed group, and its session row.
const needsYou = <span className="ml-auto shrink-0 rounded-full bg-amber-200 px-1.5 text-[10.5px]/4 font-semibold whitespace-nowrap text-amber-900 dark:bg-amber-800 dark:text-amber-50">Needs you</span>;

// Waiting on this person's answer wins over the running turn that asks and over background work, such as a subagent
// or a shell command. A park until a time or until unparked shows a clock, else background work an hourglass, also
// while a turn runs, else an automation timer that will wake the session a stopwatch. Only a turn not waiting on this
// person is working, so its title shimmers. A chat whose agent is paused shows a pause glyph in place of the chat icon,
// else a forked session a fork glyph. A title being regenerated spins in place of every other glyph and shimmers.
function tabActivity(session: Session, unread: boolean, clocked: boolean, regenerating: boolean) {
  const Chat = session.paused ? CirclePause : session.forkedFrom ? GitFork : MessageSquare;
  const activity = session.asking ? ["Needs your answer"] : [session.running && "Turn running", session.background && "Background work running", session.automation && "Waiting for a timer"];
  return [[session.forkedFrom && "Forked session", session.paused && "Agent paused", regenerating && "Regenerating title", ...activity, unread && "Unread"].filter(Boolean).join(", "), regenerating ? LoaderCircle : session.asking ? Chat : clocked ? Clock : session.background ? Hourglass : session.automation ? Timer : Chat, regenerating || (session.running && !session.asking)] as const;
}

const tabLabel = (tab: Pick<Tab, "path" | "search">, session: Session, SessionIcon: typeof Search): [string, typeof Search] =>
  isSearch(tab.path) ? [searchTitle(tab.search), Search] : pageTab(tab.path) ?? [sessionTitle(session), SessionIcon];

function useTabTitle(path: string, label: string) {
  const id = path.startsWith("/agents/drafts/") ? path.slice("/agents/drafts/".length) : "";
  const draft = useQuery({ ...queries.agentDrafts(), enabled: !!id }).data?.drafts.find((item) => item.id === id);
  return useCleanText((draft && (draft.existing ? `Edit ${draft.name}` : draft.name)) || label);
}

// A tab's park, with the time it ends or until the session is done, in words.
function timerText(mark?: Omit<TabAttention, "path">) {
  const when = mark?.until && new Date(mark.until).toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
  return mark?.parked ? mark.untilDone ? "Parked until it is done" : when ? `Parked until ${when}` : "Parked" : undefined;
}

// A detail line, such as a parked tab's park, sits under the title inside the tab, so the row's highlight covers both. A
// row of a server that stopped answering is greyed and says so. A Shared row names its space, which renaming it edits in.
// Clicking a tab not in view opens it, by default on the page's server.
function TabItem({ tab, description, detail, chosen, rule, active, stop, shared, space, offline, editing, endEdit, open = () => navigate(tab.path), close, closeLabel, openMenu }: { tab: Tab; description: string; detail?: string; chosen: boolean; rule?: string; active: boolean; stop: boolean; shared?: boolean; space?: string; offline?: boolean; editing: boolean; endEdit: () => void; open?: () => void; close?: () => void; closeLabel?: (title: string) => string; openMenu: (anchor: MenuAnchor) => void }) {
  const sidebar = useContext(Sidebar);
  const id = pathSession(tab.path);
  const session = sidebar.rows.find((row) => row.id === id) ?? { id };
  // A tab of any kind waits while marked and not parked, by its session row or its location's row. A parked one is
  // dimmed; its park shows under its title.
  const mark = id ? rowAttention(session) : attentionOf(sidebar, tab.path), unread = !!mark?.unreadAt && !mark.parked, aside = timerText(mark);
  const regenerating = useMutationState({ filters: { mutationKey: ["regenerateTitle"], status: "pending", predicate: (mutation) => (mutation.state.variables as { id: string }).id === id } }).length > 0;
  const [activity, SessionIcon, working] = tabActivity(session, !!mark?.unreadAt, !!mark?.parked && !mark.untilDone, regenerating);
  const [label, Icon] = tabLabel(tab, session, SessionIcon);
  const title = useTabTitle(tab.path, label);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (active) ref.current!.scrollIntoView({ block: "nearest", inline: "nearest" }); }, [active, title]);
  return <div data-tab={tab.path} data-running={working} data-regenerating={regenerating} data-asking={!!session.asking} data-unread={unread} draggable={!editing} className={cn("group flex shrink-0 items-center rounded-md", active ? "bg-sidebar-row-active" : "hover:bg-accent", { "bg-accent": chosen, "opacity-60": mark?.parked || offline })}
    onContextMenu={(event) => { event.preventDefault(); openMenu({ getBoundingClientRect: () => DOMRect.fromRect({ x: event.clientX, y: event.clientY }) }); }}>
    {rule && <span className={cn("pointer-events-none my-1.5 ml-1 w-0.5 shrink-0 self-stretch", groupColors[rule])} />}
    <Tooltip disabled={editing}><TooltipTrigger delay={500} render={<div className="flex min-w-0 flex-1" />}>
      <div ref={ref} role="tab" aria-selected={active} aria-label={title} aria-description={[description, activity, aside, offline && "Server offline"].filter(Boolean).join(", ")} tabIndex={stop ? 0 : -1} className={cn("flex min-w-0 flex-1 cursor-pointer items-center gap-1.5 py-2 pr-1 pl-3 text-sm [:last-child>&]:pr-3 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring aria-[selected=false]:text-muted-foreground", tab.preview && "italic")}
        onClick={() => { if (!active) open(); }}>
        <Icon aria-hidden="true" className="mt-0.5 size-4 shrink-0 self-start motion-safe:group-data-[regenerating=true]:animate-spin" />
        <span className="flex min-w-0 flex-1 flex-col"><span className="flex min-w-0 items-center gap-1.5">
          {editing ? <TabRename session={session} tab={tab} shared={shared} space={space} placeholder={title} endEdit={endEdit} /> : <><span className="truncate group-data-[unread=true]:font-bold group-data-[asking=true]:font-semibold group-data-[asking=true]:text-amber-600 dark:group-data-[asking=true]:text-amber-300 motion-safe:group-data-[running=true]:shimmer motion-reduce:group-data-[running=true]:shimmer-still dark:in-aria-selected:shimmer-color-foreground/40">{title}</span><ParkedUnreadDot mark={mark} />{session.asking ? needsYou : null}</>}
        </span><span aria-hidden="true" className="truncate text-xs text-muted-foreground empty:hidden">{[detail, aside].filter(Boolean).join(" · ")}</span></span>
      </div>
    </TooltipTrigger><TooltipContent side="bottom">{offline ? `${title} · Server offline` : title}</TooltipContent></Tooltip>
    {active ? <button type="button" aria-label="Tab actions" className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground last:mr-1 hover:bg-accent hover:text-foreground" onClick={(event) => openMenu(event.currentTarget)}><Ellipsis className="size-3.5" /></button> : null}
    <TabEnd shared={shared} close={close} label={closeLabel} title={title} />
  </div>;
}

function ParkedUnreadDot({ mark }: { mark?: Omit<TabAttention, "path"> }) {
  return mark?.unreadAt && mark.parked ? <span data-parked-unread aria-hidden="true" className="size-1.5 shrink-0 rounded-full bg-primary" /> : null;
}

function TabEnd({ shared, close, label, title }: { shared?: boolean; close?: () => void; label?: (title: string) => string; title: string }) {
  if (close) return <button type="button" aria-label={label?.(title) ?? `Close ${title}`} className="mr-1 flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring" onClick={close}><X className="size-3.5" /></button>;
  return shared ? <span aria-hidden="true" title="In Shared: everyone in this space sees it" className="mr-1 flex size-6 shrink-0 items-center justify-center text-muted-foreground"><Users className="size-3.5" /></span> : null;
}

// Enter and leaving the field save, on the server of the tab's section; Escape restores the old name first, so its blur
// saves nothing.
function TabRename({ session, tab, shared, space, placeholder, endEdit }: { session: Session; tab: Tab; shared?: boolean; space?: string; placeholder: string; endEdit: () => void }) {
  const sidebar = useContext(Sidebar), page = useContext(TabActions), { editSearch, name: remote } = useContext(TabStore) ?? page, search = isSearch(tab.path);
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
      else if (search) { editSearch(tab.path, { name: value }, shared, space); endEdit(); }
      else update.mutate({ id: session.id, name: value, server: remote });
    }} />;
}

function persistComposerDraft(draft: ComposerDraft) {
  if (!draft.hydrated || !draft.persistenceKey || draft.persistedEdit === draft.edit) return;
  draft.persistedEdit = draft.edit;
  return draftContent(draft.persistenceKey, () => ({ text: draft.text, files: draft.files, agent: draft.agent })).then(
    () => { draft.persistenceError = ""; },
    () => { draft.persistenceError = "Draft could not be saved locally. Keep this page open."; },
  );
}

export function App() {
  const name = useServer();
  const identity = useQuery(queries.identity(), queryClient), config = useQuery(queries.config(), queryClient);
  const scope = identity.isSuccess && config.isSuccess ? JSON.stringify(["authenticated-username", location.origin + serverPath(""), identity.data.username, config.data.workspace ?? ""]) : undefined;
  const shell = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const viewport = window.visualViewport;
    if (!viewport) return;
    const resize = () => {
      // Keyboards shrink/pan the visual viewport without changing dvh. Pinch zoom keeps its existing layout.
      if (viewport.scale !== 1) return;
      shell.current!.style.height = `${viewport.height}px`;
      shell.current!.style.top = `${viewport.offsetTop}px`;
    };
    resize();
    viewport.addEventListener("resize", resize);
    viewport.addEventListener("scroll", resize);
    return () => {
      viewport.removeEventListener("resize", resize);
      viewport.removeEventListener("scroll", resize);
    };
  }, []);
  return <QueryClientProvider client={queryClient}><TooltipProvider><ProtocolGuard /><div ref={shell} className="fixed inset-x-0 top-0 flex h-dvh flex-col"><WebAppUpdate /><SidebarOwner>
    <SessionApp key={name} scope={scope} scopeError={identity.error?.message ?? config.error?.message} />
  </SidebarOwner></div></TooltipProvider></QueryClientProvider>;
}

function SessionApp({ scope, scopeError }: { scope?: string; scopeError?: string }) {
  useTheme();
  const [command, setCommand] = useState<SessionCommand>();
  const composer = useRef<((command: string) => void) | null>(null);
  const commands = useMemo(() => ({ command, setCommand, composer }), [command]);
  const route = useRoute();
  const showChat = ![route.editor, route.cron, route.timeline, route.agents, route.skills, route.skill, route.config, route.search, route.voice, route.pages, route.shortcuts, route.closed, route.page, route.remote].some(Boolean);
  // The screen stays on while the Voice page is open or a call runs.
  const awake = useSyncExternalStore(subscribeVoice, () => route.voice || !["idle", "error"].includes(voiceView().state));
  useEffect(() => awake ? holdWakeLock() : undefined, [awake]);
  const [palette, setPalette] = useState<{ mode: PaletteMode | undefined; key: number; opencode?: OpenCodeServer }>({ mode: undefined, key: 0 });
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
  useRecordVisit(tabs, palette.mode !== undefined);
  const newChat = useCallback(() => {
    const draft = drafts.get("") ?? drafts.get(conversation.id)!;
    if ([!scope, draft.reverting, !draft.hydrated].some(Boolean)) return;
    const owner = newChatOwner.current;
    draft.reverting = true;
    draft.persistenceError = "";
    onDraftChange();
    return draftContent(JSON.stringify([scope, ""]), () => ({ text: "", files: [], agent: "" })).then(() => {
      if (drafts.get("") === draft) { draft.text = ""; draft.mentions = undefined; draft.files = []; draft.agent = ""; draft.edit++; }
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
    if ((event.metaKey || event.ctrlKey) && !event.altKey && event.shiftKey && event.key.toLowerCase() === "r") {
      event.stopPropagation(); // Keep hard refresh native, ahead of embedded shortcuts and terminal input.
      return;
    }
    const mod = event.metaKey || event.ctrlKey, plain = mod && !event.altKey && !event.shiftKey;
    const action = mod && !event.altKey && event.code === "KeyP" ? () => openPalette(event.shiftKey ? "commands" : "sessions")
      : sidebar.medium && plain && event.code === "KeyB" ? sidebar.toggle
      : mod && event.altKey && !event.shiftKey && event.code === "KeyN" ? newChat
      : mod && event.altKey && !event.shiftKey && event.code === "KeyT" && tabs.closed.length ? tabs.reopen
      : plain && (event.code === "BracketLeft" || event.code === "BracketRight") ? () => tabs.step(event.code === "BracketLeft")
      : plain && /^Digit[123]$/.test(event.code) ? () => setPreference(SIDEBAR_VIEW_KEY, stripViews[Number(event.code.at(-1)) - 1][0])
      : undefined;
    if (!action) return;
    event.preventDefault();
    event.stopPropagation();
    if (!event.repeat) action();
  });

  const onEscape = useEffectEvent((event: KeyboardEvent) => {
    // Escape closes pages that hold nothing, so never a session or a Search page, whose searches would go with it.
    if (event.defaultPrevented || event.repeat || showChat || event.key !== "Escape" || route.remote || route.search) return;
    if (document.querySelector('[role="dialog"], [role="listbox"], [role="menu"], [role="tooltip"]')) return;
    event.preventDefault();
    closeTab([pagePath()]);
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
            {tabs.picking ? <PickTimeDialog picking={tabs.picking} /> : null}
            <CommandPalette key={palette.key} drafts={drafts} mode={palette.mode} setMode={(mode) => setPalette((current) => ({ ...current, mode }))} newChat={newChat} keyCommands={keyCommands(tabs, sidebar.toggle, sidebar.medium)} opencode={palette.opencode} />
         <div className="flex min-h-0 flex-1 flex-col overflow-hidden overscroll-y-none bg-background">
            <BottomNavigation>
              <SidebarToggle open={sidebar.open} toggle={sidebar.toggle} />
              <div className="min-w-0 max-w-full justify-self-center overflow-x-auto overflow-y-hidden scrollbar-none">
                <div className="flex w-max items-center gap-[var(--navigation-gap)]">
                  <NewSessionButton newChat={newChat} openCode={(opencode) => setPalette((current) => ({ mode: "commands", key: current.key + 1, opencode }))} />
                  {([["Search sessions and commands", Search, () => openPalette("sessions")], ["Voice", Mic, () => navigate("/voice")]] as const).map(([label, Icon, onClick]) => (
                    <Tooltip key={label}><TooltipTrigger render={<Button variant="ghost" size="icon" className="size-[var(--navigation-button)] shrink-0" />} aria-label={label} onClick={onClick}>
                      <Icon className="size-[var(--navigation-icon)]" />
                    </TooltipTrigger><TooltipContent side="top">{label}</TooltipContent></Tooltip>
                  ))}
                  <AttentionButton />
                </div>
              </div>
              <div className="justify-self-end"><PanelToggle /></div>
            </BottomNavigation>
          <TabDrawer {...sidebar}><TabStrip tabs={tabs} newChat={newChat} /></TabDrawer>
          <div className="flex min-h-0 min-w-0 flex-1">
            {sidebar.medium ? <ResizableAside side="tabs" id="tab-sidebar" className={cn("border-r", !sidebar.open && "hidden")}><TabStrip tabs={tabs} newChat={newChat} /></ResizableAside> : null}
            <div className="flex min-h-0 min-w-0 flex-1 flex-col md:min-w-[26rem]"><TabTitle />
            <main className={cn("relative flex min-h-0 min-w-0 flex-1 flex-col", command?.target && "pt-[min(75dvh,30rem)]")}><ServerGate>
              <WarmTabs cron={route.cron} agents={route.agents} skills={route.skills} config={route.config} />
              {route.search ? <SearchPage /> : null}
              {route.timeline ? <CronTimeline key={route.timeline} stem={route.timeline} /> : null}
              {route.voice ? <VoicePage /> : null}
              <PageIndex show={route.pages} />
              <ShortcutsPage show={route.shortcuts} />
              <AgentEditorPage show={route.editor} />
              <ClosedTabsPage show={route.closed} />
              <HostedPage key={route.page} page={route.page} />
              <SkillPage name={route.skill} />
              <RemoteSession id={route.remote} />
              <TabPane show={showChat}>
                <MessageScrollerProvider key={conversation.key} autoScroll scrollEdgeThreshold={48}>
                  <Transcript id={conversation.id} drafts={drafts} scopeError={scopeError} onDraftChange={onDraftChange} onCreated={(id) => { tabs.create(id); setConversation((current) => ({ ...current, created: id })); }} />
                </MessageScrollerProvider>
              </TabPane>
             </ServerGate></main></div>
              {showChat ? <><DelegationPanel id={route.id} /><SkillPanel /><TreePanel id={route.id} drafts={drafts} onDraftChange={onDraftChange} /></> : null}
               <ThreadPRPanel drafts={drafts} onDraftChange={onDraftChange} />
               <HostedPagePanel />
           </div>
          </div>
          </TabActions></SessionCommands></DraftScope>
  );
}

function keyCommands({ step, closed, reopen }: ReturnType<typeof useTabs>, toggle: () => void, medium: boolean) {
  return [{ key: "tab-previous", label: "Tabs: Previous", run: () => step(true) }, { key: "tab-next", label: "Tabs: Next", run: () => step(false) },
    ...closed.length ? [{ key: "tab-reopen", label: "Tabs: Reopen closed tab", run: reopen }] : [], ...medium ? [{ key: "tab-sidebar", label: "Tabs: Toggle sidebar", run: toggle }] : [],
    ...stripViews.map(([key, label]) => ({ key: `sidebar-${key}`, label: `Tabs: Show ${label}`, run: () => setPreference(SIDEBAR_VIEW_KEY, key) })),
    ...THEME_MODES.map(({ id, label }) => ({ key: `theme-${id}`, label: `Theme: ${label}`, run: () => setThemeMode(id) }))];
}

// Pick a time parks tabs until a local date and time, starting at the next whole hour, today unless that is past
// midnight; a time already past is refused.
function PickTimeDialog({ picking: { send } }: { picking: Picking }) {
  const { setPicking } = useContext(TabActions), id = useId();
  const [fields, setFields] = useState(() => {
    const next = new Date(new Date().setMinutes(60, 0, 0)), local = new Date(next.getTime() - next.getTimezoneOffset() * 60_000).toISOString();
    return { date: local.slice(0, 10), time: local.slice(11, 16) };
  });
  const until = new Date(`${fields.date}T${fields.time}`), late = !(until.getTime() > Date.now());
  return <Dialog open onOpenChange={(open) => { if (!open) setPicking(undefined); }}>
    <DialogContent>
      <DialogTitle>Park until</DialogTitle>
      <DialogDescription>Parked tabs wait in Parked and notify you of nothing until then but an agent's question.</DialogDescription>
      <form className="mt-4 flex flex-col gap-3" action={() => { send(until.toISOString()); setPicking(undefined); }}>
        <FieldGroup className="flex-row">
          {(["date", "time"] as const).map((key) => <Field key={key} data-invalid={late}><FieldLabel htmlFor={`${id}-${key}`}>{key === "date" ? "Date" : "Time"}</FieldLabel>
            <Input id={`${id}-${key}`} type={key} value={fields[key]} aria-invalid={late} onChange={(event) => setFields({ ...fields, [key]: event.target.value })} /></Field>)}
        </FieldGroup>
        {late ? <FieldError>Pick a time in the future.</FieldError> : null}
        <div className="flex justify-end gap-2">
          <DialogClose render={<Button variant="ghost" />}>Cancel</DialogClose>
          <Button type="submit" disabled={late}>Park</Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>;
}

function SessionCommandDialog({ command, drafts, onDraftChange }: { command: SessionCommand; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  if (command.mode === "name" || command.mode === "tag") return <NameSessionDialog id={command.source} tag={command.mode === "tag"} />;
  if (command.mode === "delete") return <DeleteSessionDialog id={command.source} />;
  if (command.mode === "space") return <NewSpaceDialog server={command.source} />;
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
  const name = useServer();
  const identity = useQuery(queries.identity());
  const scope = useContext(DraftScope)!;
  const { setCommand } = useContext(SessionCommands);
  const popup = useRef<HTMLDivElement>(null);
  const route = useRoute();
  const sidebar = useContext(Sidebar);
  const [query, setQuery] = useState("");
  const search = useQuery({ ...queries.searchMessages(query.trim()), enabled: query.trim() !== "" });
  const handoffRequest = { queryKey: [{ server: name }, "handoff", command.source], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<{ document: string }>("Handoff", { id: command.source }, signal, name), retry: false, staleTime: Infinity, gcTime: 0 };
  const handoff = useQuery(handoffRequest);
  const space = useContext(TabActions).state.space;
  const action = useMutation({ mutationFn: async (kind: "copy" | "new" | "stash") => {
    const principal = identity.isSuccess && !identity.isFetching ? identity.data.principal : undefined;
    const { document } = await queryClient.fetchQuery(handoffRequest);
    if (kind === "copy") await copyText(document, popup.current!);
    if (kind === "new") {
      const { agents } = await rpc<AgentChoices>("ListAgents", {}, undefined, name);
      const agent = offeredAgent(agents.filter((item) => item.sticky !== "slack"), "main");
      const { id, resumed } = await rpc<{ id: string; resumed?: boolean }>("CreateSession", { agent, spaceId: space }, undefined, name);
      if (resumed) alertResumed(agent);
      const optimistic: Line = { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), role: "user", text: document, principal };
      const draft: ComposerDraft = resumed ? await prepareResumedDraft(id, scope, drafts, onDraftChange, name) : { text: "", files: [], agent: "", sessionId: id, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0, hydrated: true, persistenceKey: JSON.stringify([scope, id]) };
      draft.busy = true;
      draft.lines = [...draft.lines, optimistic];
      drafts.set(id, draft);
      onDraftChange();
      sidebar.invalidateQueries();
      navigate(sessionPath(id), false, name);
      void mutations.prompt({ id, text: document, messageId: optimistic.id }, name).catch((err: Error) => {
        draft.text = [document, draft.text].filter(Boolean).join("\n\n");
        draft.edit++;
        draft.lines = draft.lines.filter((line) => line.id !== optimistic.id);
        draft.error = err.message;
        if (draft.submission === 0) draft.busy = false;
        onDraftChange();
      });
    }
    if (kind === "stash") {
      await mutations.prompt({ id: command.target!.conversationId, text: document, delivery: "STASH" }, name);
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
    const response = await fetch(`${serverURL()}/api/DownloadAttachment?${new URLSearchParams({ conversationId: file.conversationId, id: file.id })}`);
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
  mode: PaletteMode,
  typed: string,
  rows: Session[],
  jobs: CronJob[],
  newChat: () => void,
  tabs: ReturnType<typeof useTabs>,
  openCron: (mode: "cron" | "timeline") => void,
  runStem: (stem: string) => void,
  actions: { key: string; label: string; detail?: string; keep?: boolean; disabled?: boolean; run: () => void }[],
  recent: string[],
  origin: Space,
): { key: string; label?: string; detail?: string; session?: Session; keep?: boolean; disabled?: boolean; run: () => void }[] {
  const needle = typed.toLowerCase(), pages = searchPages(tabs);
  if (mode === "cron") {
    return jobs.filter((job) => job.status !== "ran" && (needle === "" || `${job.stem} ${job.schedule ?? ""} ${job.agent ?? ""}`.toLowerCase().includes(needle))).map((job) => ({
      key: job.stem, label: job.stem, detail: [job.schedule, job.agent].filter(Boolean).join(" · "), keep: true, run: () => runStem(job.stem),
    }));
  }
  // Every cron with a definition or runs, retired ones too; runs come newest first.
  if (mode === "timeline") {
    return [...Map.groupBy(jobs.filter((job) => job.stem.toLowerCase().includes(needle)), (job) => job.stem)].map(([stem, rows]) => {
      const runs = rows.filter((job) => job.status === "ran");
      return { key: stem, label: stem, detail: `${runs.length} runs${runs.length ? ` · last ${cronWhen(runs[0].lastRun)} UTC` : ""}`, run: () => navigate(cronPath(stem)) };
    });
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
    { key: "search", label: "Sessions: Search", run: () => tabs.open(freshSearch()) },
    { key: "run-cron", label: "Routines: Run", keep: true, run: () => openCron("cron") },
    { key: "cron-timeline", label: "Routines: Timeline", keep: true, run: () => openCron("timeline") },
    ...([["voice", "Voice"], ["routines", "Routines: Dashboard"], ["agents", "List Agents"], ["skills", "List Skills"], ["config", "Settings"], ["pages", "Pages: Index"], ["closed", "Tabs: Recently closed"], ["shortcuts", "Help: Keyboard Shortcuts"]] as const).map(([key, label]) => ({ key, label, run: () => navigate(`/${key}`) })),
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
  const search = (query: string) => {
    const found = pages.find((tab) => tab.space === origin.id && tab.search?.searches.some((item) => item.query === query));
    tabs.open(found?.path ?? freshSearch(), `?${new URLSearchParams({ q: query })}`, origin.id);
  };
  return mode === "commands" ? commands : [...commands, ...searches, ...rows.map((session) => ({ key: rowKey(session.id, session.rocketclaw), session, run: () => openRow(tabs, session) })),
    ...typed ? [{ key: searchInSpaceKey, label: `Search in this space for '${typed}'`, run: () => search(`space:${termValue(origin.name)} ${typed}`) },
      { key: searchEverywhereKey, label: `Search everywhere for '${typed}'`, run: () => search(typed) }] : []];
}

const searchEverywhereKey = "search-everywhere";
const searchInSpaceKey = "search-in-space";

// A person's own Search page whose location is shared in its space shows the Shared one there, so only that one lists.
const searchPages = ({ state, shared }: ReturnType<typeof useTabs>) => [...state.tabs.filter((tab) => isSearch(tab.path) && !isShared(shared, tab.space!, tab.path)), ...shared.filter((tab) => isSearch(tab.path))];

// The server reads and matches the query. While typing, an unfinished last word is left out; Enter sends everything.
// Requests start after a 250 ms pause (and by 1 s with messages), a newer one aborts the older, and they rerun when sidebar chats change.
// A Search page's search runs on its server past the request, so leaving only stops waiting. A request says the person
// entered its query when it follows typing, Enter or a link, not the page reopening on a query it held or new chats.
function useSessionSearch(query: string, scope: "messages" | "filters", entered: boolean, page?: { tab: string; searchId: string; held: boolean }) {
  const { all: rows } = useContext(Sidebar);
  const ids = useMemo(() => rows.map((row) => rowKey(row.id, row.rocketclaw)).toSorted().join(" "), [rows]);
  const { word, unfinished } = typingWord(query);
  const sent = (entered || !unfinished ? query : query.slice(0, query.length - word.length)).trimEnd();
  const principalChoices = !sent && /^principal:/i.test(word);
  const [result, setResult] = useState<{ query: string; response?: SearchSessionsResponse; error?: string }>({ query: "" });
  const request = useRef<{ key: string; text?: string; asked?: boolean; failed?: boolean; controller?: AbortController; pending?: Promise<SearchSessionsResponse | undefined> }>({ key: "", text: page?.held ? sent : undefined });
  const pause = useRef<ReturnType<typeof setTimeout>>(undefined);
  const deadline = useRef<ReturnType<typeof setTimeout>>(undefined);
  const firstEdit = useRef(0);
  const tab = page?.tab, searchId = page?.searchId;
  const run = useCallback((text: string, typed = true) => {
    clearTimeout(pause.current);
    clearTimeout(deadline.current);
    firstEdit.current = 0;
    const key = `${text}\n${ids}\n${principalChoices}`, asked = !!tab && typed;
    // The same text asked again because the session list changed starts the server's run over.
    const fresh = !!tab && !typed && request.current.text === text && !!request.current.key && request.current.key !== key;
    // Entering the query in flight sends it again, so its run on the server is entered too.
    if (request.current.key === key && request.current.pending && (request.current.asked || !asked)) return request.current.pending;
    request.current.controller?.abort();
    const controller = new AbortController();
    const pending = text || principalChoices ? searchAll(["", ...rocketclaws().map((remote) => remote.name)], { query: text, [scope]: true }, controller.signal, tab ? { tab, searchId, entered: asked, fresh: fresh || undefined } : undefined).then((response) => {
      if (controller.signal.aborted) return undefined;
      setResult({ query: text, response });
      return response;
    }, (error: Error) => {
      if (!controller.signal.aborted) { request.current.failed = true; setResult({ query: text, error: error.message }); }
      return undefined;
    }).finally(() => { if (!controller.signal.aborted) request.current.pending = undefined; }) : undefined;
    request.current = { key, text, asked, controller, pending };
    if (!text && !principalChoices) setResult((previous) => previous.query === "" ? previous : { query: "" });
    return pending ?? Promise.resolve(undefined);
  }, [ids, scope, principalChoices, tab, searchId]);
  useEffect(() => {
    // A failed search retries on the next keystroke.
    if (request.current.key === `${sent}\n${ids}\n${principalChoices}` && !request.current.failed) return;
    if (!firstEdit.current) firstEdit.current = Date.now();
    const rerun = () => void run(sent, sent !== request.current.text);
    pause.current = setTimeout(rerun, sent ? 250 : 0);
    if (scope === "messages") deadline.current = setTimeout(rerun, Math.max(0, 1000 - (Date.now() - firstEdit.current)));
    return () => { clearTimeout(pause.current); clearTimeout(deadline.current); };
  }, [query, sent, ids, scope, principalChoices, run]);
  useEffect(() => () => { request.current.controller?.abort(); }, []);
  const pending = !!sent && result.query !== sent;
  const { found, missing } = useMemo(() => matchedRows(result.response, rows), [result.response, rows]);
  return {
    word, sent, result, found, missing, run, pending,
    error: sent && result.query === sent ? result.error : undefined,
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
  const byId = new Map(rows.map((row) => [rowKey(row.id, row.rocketclaw), row]));
  const matches = response?.matches ?? [];
  const found = matches.flatMap((match) => byId.has(rowKey(match.conversationId, match.rocketclaw)) ? [{ match, session: byId.get(rowKey(match.conversationId, match.rocketclaw))! }] : []);
  return { found, missing: found.length < matches.length };
}

// Text matches shown titles here, as rows show them: markdown and Slack tags read as their text. Plain text
// matches at once; a filter asks the server, whose passing rows, matched the same way, replace these once they arrive.
function usePaletteSessions(query: string, sessions: boolean, entered: boolean) {
  const { all: rows } = useContext(Sidebar);
  const free = query.replace(new RegExp(`(?:^|\\s)(?:${filterKeys}):(?:"(?:[^"\\\\]|\\\\.)*"?|\\S*)`, "gi"), " ").trim().toLowerCase();
  const filtered = sessions && free !== query.trim().toLowerCase();
  const search = useSessionSearch(filtered ? query : "", "filters", entered);
  const raw = useMemo(() => sessions ? rows.map((row) => sessionTitle(row)) : [], [rows, sessions]);
  const names = useSlackNames(useMemo(() => [...new Set(raw.flatMap(mentionIds))], [raw]));
  const byId = useMemo(() => new Map(rows.map((row, i) => [rowKey(row.id, row.rocketclaw), raw[i] === undefined ? "" : cleanText(raw[i], names).toLowerCase()])), [rows, raw, names]);
  const matches = useCallback((row: Session) => !free || !!byId.get(rowKey(row.id, row.rocketclaw))?.includes(free), [byId, free]);
  const { found, result, sent, error } = search;
  const showMatches = !!(filtered && sent && result.query && (result.response || error));
  const listed = useMemo(() => showMatches ? found.flatMap(({ session }) => matches(session) ? [session] : []) : sessions ? rows.filter(matches) : rows, [showMatches, found, sessions, rows, matches]);
  return { search, filtered, free, matches, rows: listed };
}

// The keys of filter terms; the server reads the same keys.
const filterKeys = "tag|routine|agent|room|principal|server|model|space|is|sort";

const paletteCopy = { sessions: { title: "Go to session or command", desc: "Search and open a session, or run a command.", placeholder: "Search sessions and commands" }, commands: { title: "Run command", desc: "Search and run a command.", placeholder: "Type a command" },
  cron: { title: "Run routine", desc: "Search and run a routine.", placeholder: "Search routines" }, timeline: { title: "Routine timeline", desc: "Pick a routine to see all of its runs.", placeholder: "Search routines" } };
type PaletteMode = keyof typeof paletteCopy;

// "Sessions: New on <server>" turns the palette into a folder picker for a remote OpenCode
// session: a typed folder, or one of that server's sessions' folders, those the person picked most recently first,
// since agents also start sessions there.
function useRemoteCreation(query: string, close: () => void, restart: () => void, copy: { title: string; desc: string; placeholder: string }, recent: string[], initial?: OpenCodeServer) {
  const sidebar = useContext(Sidebar);
  const tabs = useContext(TabActions);
  const visit = useVisit(), here = useServer();
  const [target, setTarget] = useState(initial);
  const server = target?.server, rocketclaw = target?.rocketclaw ?? here, view = target ? sidebar.servers[rocketclaw] : sidebar;
  const create = useMutation({ mutationFn: mutations.createSession, onSuccess: ({ id }) => { view.invalidateQueries(); close(); visit(rocketclaw, rowPath({ id, kind: "REMOTE_OPENCODE" })); } });
  const typed = query.trim();
  const rank = (directory: string) => recent.indexOf(`folder-${directory}`) + 1 || Infinity;
  const known = view.rows.flatMap((row) => row.server === server && row.directory ? [row.directory] : []).toSorted((a, b) => rank(a) - rank(b) || 0);
  const items: ReturnType<typeof paletteRows> = !server ? [] : [...new Set([typed, ...known])]
    .filter((directory) => directory.startsWith("/") && directory.toLowerCase().includes(typed.toLowerCase()))
    .map((directory) => ({ key: `folder-${directory}`, label: directory, keep: true, disabled: create.isPending, run: () => create.mutate({ server: rocketclaw, opencodeServer: server, directory, spaceId: rocketclaw === here ? tabs.state.space : view.tabs?.state.space }) }));
  return {
    pick: (palette: () => typeof items) => server ? items : palette(),
    commands: Object.entries(sidebar.servers).flatMap(([claw, { remote }]) => Object.keys(remote?.servers ?? {}).toSorted().map((name) => ({ key: `opencode-${rowKey(name, claw)}`, label: `Sessions: New on ${name}${rocketclaws().length ? ` (${serverLabel(claw)})` : ""}`, keep: true, run: () => { restart(); setTarget({ rocketclaw: claw, server: name }); } }))),
    copy: server ? { title: `New session on ${server}`, desc: "Type or pick a folder for the new OpenCode session.", placeholder: `Folder on ${server}, like /srv/app` } : copy,
    empty: server && `No known folders on ${server} yet; type one`,
    alert: create.error ? <p role="alert" className="px-3 text-sm text-destructive">{create.error.message}</p> : null,
  };
}

// A ref for the last list row that grows the list once the row nears its scroll root.
function loadMore(root: (node: HTMLElement) => Element | null, grow: () => void) {
  return (node: HTMLElement | null) => {
    const observer = new IntersectionObserver(([entry]) => { if (entry.isIntersecting) grow(); }, { root: root(node!), rootMargin: "0px 0px 100% 0px" });
    observer.observe(node!);
    return () => observer.disconnect();
  };
}

// Paging bounds initial/edit work; deep browsing or wrapping to the last row still mounts it all. Window rows if that becomes costly.
function usePalettePage<T extends { key: string }>(items: T[], pick: string, key: string) {
  const [budget, setBudget] = useState({ key, rows: 50 });
  if (budget.key !== key) setBudget({ key, rows: 50 });
  const handoffsOnly = items.every(({ key }) => key === searchEverywhereKey || key === searchInSpaceKey);
  const chosen = items.findIndex((item) => item.key === pick);
  const selected = handoffsOnly && items.length > 0 && chosen < 0 ? 1 : Math.max(0, chosen);
  const drawn = Math.max(budget.key === key ? budget.rows : 50, selected + 1);
  return {
    selected, handoffsOnly,
    shown: items.slice(0, drawn),
    next: drawn < items.length ? <li aria-hidden ref={loadMore((node) => node.parentElement, () => setBudget({ key, rows: drawn + 50 }))} /> : null,
  };
}

function CommandPalette({ drafts, mode, setMode, newChat, keyCommands, opencode }: { drafts: Map<string, ComposerDraft>; mode: PaletteMode | undefined; setMode: (mode: PaletteMode | undefined) => void; newChat: () => void; keyCommands: { key: string; label: string; run: () => void }[]; opencode?: OpenCodeServer }) {
  const sidebar = useContext(Sidebar);
  const { setCommand, composer } = useContext(SessionCommands);
  const at = () => pagePath() + location.search + location.hash;
  const tabs = useContext(TabActions);
  const [origin] = useState(() => ({ path: pagePath(), href: at(), space: tabs.state.spaces.find((space) => space.id === tabs.state.space)! }));
  const id = pathSession(origin.path);
  const back = () => { if (at() !== origin.href) flushSync(() => navigate(origin.href, true)); };
  // Only a session that opens in this space is shown, so browsing never switches spaces.
  const peek = (session?: Session) => {
    const path = session ? rowPath(session) : "", tab = tabs.state.tabs.find((item) => item.path === path);
    const here = session && (session.rocketclaw ?? "") === server() && (tab ? tab.space === tabs.state.space : !session.spaceId || session.spaceId === tabs.state.space);
    if (!here) back();
    else if (path !== at()) tabs.open(path, "", undefined, true);
  };
  const tabCommands = tabs.commands(origin.path, sidebar).map((item) => ({ ...item, key: `tab-${item.key}`, label: `Tabs: ${item.label}` }));
  // New space asks for its name, on each server where this person is not limited to their own sessions.
  const spaceCommands = ["", ...rocketclaws().map((remote) => remote.name)].filter((name) => !queryClient.getQueryData<{ limited?: boolean }>(queries.identity(name).queryKey)?.limited).map((name) => ({ key: `space-new-${name}`, label: rocketclaws().length ? `Spaces: New space on ${serverLabel(name)}` : "Spaces: New space", run: () => setCommand({ mode: "space", source: name }) }));
  const attention = useNextAttention(origin.path);
  const actions = useSessionActions(sidebar.rows.find((row) => row.id === id), () => setMode(undefined));
  const choices = useQuery({ ...queries.agents({ conversationId: id }), enabled: id !== "" });
  const draft = drafts.get(id);
  const treeOpen = new URL(origin.href, location.origin).searchParams.has("tree");
  const tree = origin.path.startsWith("/s/") ? [{ key: "tree", label: `Sessions: ${treeOpen ? "Hide" : "Show"} message tree`, run: () => navigate(panelHref(treeOpen ? undefined : "1", "tree")) }] : [];
  const commands = id ? slashCommands.filter(({ name }) => name !== "tree" && (name !== "undo" || draft?.revertEligible && (draft.canUndo || !draft.revertMessageId && !!unsavedRequest(draft.lines))) && (name !== "stop" || (draft?.busy ?? sidebar.rows.find((row) => row.id === id)?.running)) && (name !== "agent" || !!choices.data?.agents.length)).map(({ name, label }) => ({ key: name, label: ["fork", "handoff", "stop", "agent"].includes(name) ? `Sessions: ${label}` : `Command: ${label} (/${name})`, disabled: [!draft?.hydrated, draft?.sending, draft?.reverting].some(Boolean), run: () => {
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
  const [pick, setPick] = useState("");
  const [entered, setEntered] = useState(false);
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
  const active = useRef<HTMLButtonElement>(null);
  const { search, filtered, free, matches, rows } = usePaletteSessions(query, mode === "sessions", entered);
  const jobs = useQuery({ ...queries.cronJobs(), staleTime: 10_000, enabled: mode === "commands" || mode === "cron" || mode === "timeline" });
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
  const remote = useRemoteCreation(query, () => setMode(undefined), () => { setQuery(""); setPick(""); setMode("commands"); }, paletteCopy[mode ?? "sessions"], recent, opencode);
  const cronJobs = cronInSpace(jobs.data, useInSpace());
  const items = mode === undefined ? [] : remote.pick(() => paletteRows(mode, query.trim(), rows, cronJobs, newChat, tabs, (cron) => { setQuery(""); setPick(""); setMode(cron); }, (stem) => runCron.mutate({ stem }), [...actions.items.map((item) => ({ ...item, label: `Sessions: ${item.label}` })), ...remote.commands, ...tree, ...commands, ...tabCommands, ...spaceCommands, ...keyCommands, ...attention ? [{ key: "attention-next", label: "Sessions: Next needing you", run: attention }] : [], ...timeline, ...outputs], recent, origin.space));
  // Draw more rows only as scrolling or the keyboard needs them; every choice stays reachable.
  const { shown, next, selected, handoffsOnly } = usePalettePage(items, pick, `${mode}:${query}`);
  const choose = (item: (typeof items)[number]) => {
    if (item.disabled) return;
    if ((mode === "sessions" || mode === "commands") && !item.session) {
      const next = [item.key, ...recent.filter((key) => key !== item.key)].slice(0, 50);
      localStorage.setItem("command-history", JSON.stringify(next));
      setRecent(next);
      setPick("");
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
      if (item && !item.session && (mode !== "sessions" || item.key === pick || item.key !== searchEverywhereKey && item.key !== searchInSpaceKey && item.label!.toLowerCase().includes(query.trim().toLowerCase()))) return choose(item);
      setEntered(true);
      search.enter(() => { const row = item?.session ? item : items.find(({ session }) => session) ?? items.find(({ key }) => !filtered && key === searchEverywhereKey); if (row) choose(row); }, (sessions) => { const session = sessions.find(matches); if (session) choose({ key: rowKey(session.id, session.rocketclaw), session, run: () => openRow(tabs, session) }); });
    });
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      setPick(items[next % items.length]?.key ?? "");
      peek(items[next % items.length]?.session);
    }
  };
  return (
    <Dialog open={mode !== undefined} onOpenChange={(open) => { if (!open) { setMode(undefined); back(); } }}>
      <DialogContent initialFocus={input} finalFocus={() => at() === origin.href || ([...document.querySelectorAll<HTMLElement>("main h1[tabindex]")].find((title) => title.checkVisibility()) ?? true)} showCloseButton={false} className="top-[20%] flex max-h-[75vh] translate-y-0 flex-col overflow-hidden sm:max-w-lg [@media(pointer:coarse)]:top-[var(--search-top,1rem)] [@media(pointer:coarse)]:max-h-[var(--search-height,75vh)]">
        <DialogTitle className="sr-only">{copy.title}</DialogTitle>
        <DialogDescription className="sr-only">{copy.desc}</DialogDescription>
        {mode === "sessions" ? <SessionSearch rows={sidebar.all} query={query} setQuery={(update) => { setPick(""); setEntered(false); setQuery(update); peek(); }} search={search} inputRef={input} placeholder={copy.placeholder} onKeyDown={onKeyDown} /> : <Input
          ref={input}
          value={query}
          onChange={(event) => { setPick(""); setQuery(event.target.value); }}
          placeholder={copy.placeholder}
          variant="embedded"
          onKeyDown={onKeyDown}
        />}
        {runCron.error || actions.error ? <p role="alert" className="px-3 text-sm text-destructive">{(runCron.error ?? actions.error)?.message}</p> : null}
        {remote.alert}
        {mode === "sessions" ? <div className="px-3 text-sm empty:hidden"><SearchStatus search={search} /></div> : null}
        <ul key={`${mode}:${query}`} data-pending={search.pending} className="max-h-[min(24rem,50vh)] overflow-y-auto p-1 [scrollbar-width:thin] [scrollbar-color:var(--muted-foreground)_transparent]">
          {handoffsOnly ? <li className="px-3 py-2 text-sm text-muted-foreground">{paletteEmpty(mode, sidebar, search, jobs.isLoading, remote.empty)}</li> : null}
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

function PaletteCommandContent({ item, mode }: { item: ReturnType<typeof paletteRows>[number]; mode: PaletteMode | undefined }) {
  return <>
    <span className="flex w-full flex-wrap items-center justify-between gap-x-3 gap-y-1"><span className="font-medium">{item.label}</span>{mode === "sessions" || mode === "commands" ? <Keys id={item.key} hidden /> : null}</span>
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
  { id: "tab-reopen", label: "Reopen the last closed tab", keys: ["mod", "Alt", "T"] },
  { id: "attention-next", label: "Next session needing you", where: "Waiting tabs: sessions that ask a question, then the rest, longest waiting first", keys: ["mod", "Shift", "A"] },
  { id: "tab-park-done", label: "Park the tab until it is done", where: "A session's tab", keys: ["mod", "Shift", "S"] },
  { id: "tab-sidebar", label: "Show or hide the tab sidebar", where: "Tabs in the left sidebar, on a wide window", keys: ["mod", "B"] },
  ...stripViews.map(([key, label], index) => ({ id: `sidebar-${key}`, label: `Show ${label} in the tab sidebar`, keys: ["mod", String(index + 1)] })),
  { id: "right-pane", label: "Show or hide the right pane", where: "Opens a session's pull requests, or closes the open pane", keys: ["mod", "Shift", "B"] },
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

function ClosedTabsPage({ show }: { show: boolean }) {
  const { closed, state } = useContext(TabActions);
  if (!show) return null;
  return <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
    <PageTitle>Recently closed</PageTitle>
    {closed.length ? null : <p className="text-sm text-muted-foreground">No closed tabs</p>}
    <ul aria-label="Recently closed tabs" className="flex flex-col gap-1">
      {closed.map((tab) => <li key={tab.path}><ClosedTabRow tab={tab} space={state.spaces.find((item) => item.id === tab.space)?.name} /></li>)}
    </ul>
  </div>;
}

function ClosedTabRow({ tab, space }: { tab: ClosedTab; space?: string }) {
  const { reopen } = useContext(TabActions), id = pathSession(tab.path);
  const [label, Icon] = tabLabel(tab, useContext(Sidebar).rows.find((row) => row.id === id) ?? { id }, MessageSquare);
  const title = useTabTitle(tab.path, label);
  return <button type="button" className="flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring" onClick={() => reopen(tab)}>
    <Icon aria-hidden="true" className="size-4 shrink-0" /><span className="min-w-0 flex-1 truncate">{title}</span>
    {space ? <span className="shrink-0 text-xs text-muted-foreground">{space}</span> : null}
  </button>;
}

function paletteEmpty(mode: PaletteMode | undefined, sidebar: SidebarView, search: ReturnType<typeof useSessionSearch>, loadingJobs: boolean, folders?: string) {
  if (folders) return folders;
  if (mode === "sessions" && search.error) return "Search incomplete";
  if (mode === "sessions" && (!searchIsAuthoritative(sidebar) || search.pending || search.missing)) return "loading...";
  return (mode === "cron" || mode === "timeline") && loadingJobs ? "Loading…" : "No matches";
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
  const meta = [session.rocketclaw, channel, session.server, session.offline ? "offline" : "", session.agent, ...(session.tags ?? [])].filter(Boolean).join(" · ");
  const updated = session.updatedAt ? `Updated ${updatedFormat.format(new Date(session.updatedAt))}` : "";
  return <span className={cn("flex min-w-0 w-full flex-1 flex-col gap-0.5", session.offline && "opacity-60")}>
    <span className="flex items-center gap-1.5 text-sm font-medium">{session.forkedFrom ? <GitFork role="img" aria-label="Forked session" className="size-3.5 shrink-0" /> : null}<span data-slot="session-title" className={cn("truncate", session.asking && "font-semibold text-amber-600 dark:text-amber-300")} title={clean}><InlineText text={shown} needle={needle || undefined} /></span></span>
    <span className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"><span className="min-w-0 flex-1 truncate" title={meta}>{meta}</span>{session.asking ? needsYou : session.running ? <LoaderCircle role="img" aria-label="Turn running" className="size-3 shrink-0 animate-spin motion-reduce:animate-none" /> : null}{age ? <Tooltip><TooltipTrigger render={<time dateTime={session.updatedAt} />} aria-label={updated} className="shrink-0"><span className="tabular-nums">{age}</span></TooltipTrigger><TooltipContent>{updated}</TooltipContent></Tooltip> : null}</span>
  </span>;
});

const inertView: Pick<Mounted, "openPalette" | "quotable" | "quote"> = { openPalette() {}, quotable: () => false, quote() {} };
const remoteView = { route: "", shown: "", ...inertView, listeners: new Set<() => void>() };
const announceRemoteView = () => remoteView.listeners.forEach((listener) => listener());
const subscribeRemoteView = (listener: () => void) => {
  remoteView.listeners.add(listener);
  return () => void remoteView.listeners.delete(listener);
};

// OpenCode's composer is empty when its text holds only line breaks and zero-width spaces, as its own isEditorDomBlank
// (oc/packages/app/src/composer/editor/dom.ts) decides; a mention pill's text counts.
const blankDraft = (element: Element) => /^[\n\u200b]*$/.test(element.textContent!);

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

function useLinkedMessage(path: string) {
  const search = useSearch();
  return usePathname() === path ? new URLSearchParams(search).get("message") ?? undefined : undefined;
}

function useOpenCodeTabs() {
  const sidebar = useContext(Sidebar);
  const tabs = useContext(TabActions);
  const servers = Object.keys(sidebar.remote?.servers ?? {}).sort();
  const configured = new Set(servers);
  const open = [...new Set(tabs.state.tabs.flatMap((tab) => tab.path.startsWith("/oc/") ? [decodeSessionId(tab.path.slice(4))] : []))]
    .map((item) => item.split(":")).filter(([, other]) => configured.has(other));
  const kept = Object.fromEntries(servers.map((other) => [other, open.flatMap(([, name, ...session]) => name === other ? [session.join(":")] : [])]));
  return { servers, kept, first: open[0]?.join(":") ?? "" };
}

const remoteRow = (server: string, session: string) => rowPath({ id: `opencode:${server}:${session}`, kind: "REMOTE_OPENCODE" });

function RemoteStatus({ server, configured, visible, reported, failed, created, retry }: { server: string; configured: boolean; visible: boolean; reported: boolean; failed: string; created: string; retry: () => void }) {
  const sidebar = useContext(Sidebar);
  const status = !configured ? sidebar.enumerationComplete ? "This OpenCode session is no longer available." : "Loading…" : visible ? "" : `${server} is offline; this session reopens when it is reachable.`;
  return <>
    {status ? <p role="status" className="m-auto p-4 text-sm text-muted-foreground">{status}</p> : null}
    {!visible || reported || failed ? null : <p role="status" className="p-4 text-sm text-muted-foreground">Loading…</p>}
    {failed || created ? <p role="alert" className="px-3 text-sm text-destructive">{failed || created}{failed ? <> <button type="button" className="underline" onClick={retry}>Retry</button></> : null}</p> : null}
  </>;
}

type RemoteViewProps = { id: string; start: string; servers: string[]; kept: Record<string, string[]>; visible: boolean; shown: string; setReport: (report: { route: string; shown: string }) => void; container: RefObject<HTMLDivElement | null> };

function useRemoteView({ id, start, servers, kept, visible, shown, setReport, container }: RemoteViewProps) {
  const sidebar = useContext(Sidebar);
  const tabs = useContext(TabActions);
  const path = id ? rowPath({ id, kind: "REMOTE_OPENCODE" }) : "";
  const [, server = "", ...rest] = id.split(":");
  const sessionID = rest.join(":");
  const [failed, setFailed] = useState("");
  // Each retry loads the entry from a fresh URL, as browsers may keep a failed module load.
  const [retries, setRetries] = useState(0);
  const create = useMutation({ mutationFn: mutations.createSession, onSuccess: ({ id }) => { sidebar.invalidateQueries(); tabs.open(rowPath({ id, kind: "REMOTE_OPENCODE" })); } });
  const openSession = useEffectEvent((other: string, session: string) => tabs.open(remoteRow(other, session)));
  const forkSession = useEffectEvent(async (other: string, session: string, before: string) => {
    const forked = await mutations.forkSession({ id: `opencode:${other}:${session}`, before });
    sidebar.invalidateQueries();
    return forked.id.slice(`opencode:${other}:`.length);
  });
  // A press while a create runs is the same request; once it settles, success or failure, a press starts another.
  const creating = useRef(false);
  const newSession = useEffectEvent((other: string, directory: string) => {
    if (creating.current) return;
    creating.current = true;
    create.mutate({ opencodeServer: other, directory, spaceId: tabs.state.space }, { onSettled: () => { creating.current = false; } });
  });
  const closeTab = useEffectEvent((other: string, session: string) => tabs.deleted(remoteRow(other, session)));
  const reportSession = useEffectEvent((other: string, session: string) => {
    if (visible) remoteView.shown = session;
    setReport({ route: id, shown: `opencode:${other}:${session}` });
  });
  const messageLink = useEffectEvent((other: string, session: string, message: string) => `opencode:${other}:${session}` === id ? messageURL(path, message) : "");
  const view = useRef<{ mounted: Mounted; id: string; messageID?: string }>(undefined);
  const messageID = useLinkedMessage(path);
  const keep = JSON.stringify(kept);
  const show = useEffectEvent(() => {
    if (!view.current) return;
    if (visible) {
      if (view.current.id !== id || (messageID && view.current.messageID !== messageID)) view.current.mounted.show(server, sessionID, messageID);
      view.current.id = id;
      view.current.messageID = messageID;
    }
    view.current.mounted.keep(kept);
    view.current.mounted.setVisible(visible);
    const { openPalette, quotable, quote } = view.current.mounted;
    Object.assign(remoteView, visible ? { route: id, shown: shown.slice(`opencode:${server}:`.length) || sessionID, openPalette, quotable, quote } : { route: "", shown: "", ...inertView });
    announceRemoteView();
  });
  // The view opens on the session shown when it mounts; later sessions move it.
  const attach = useEffectEvent(({ mount }: OcModule) => {
    const base = serverURL() || location.origin;
    const [, first = "", ...session] = start.split(":");
    const linked = start === id ? messageID : undefined;
    const navigation = { openSession, newSession, forkSession, closeTab, reportSession, messageLink };
    view.current = { mounted: mount(container.current!, { servers: Object.fromEntries(servers.map((other) => [other, `${base}/oc/${other}`])), server: first, sessionID: session.join(":"), messageID: linked, navigation }), id: start, messageID: linked };
    view.current.mounted.subscribeQuotable(announceRemoteView);
    show();
  });
  const wanted = !!start;
  const configured = servers.join("\n");
  useEffect(() => {
    if (!wanted) return;
    let gone = false;
    void (import(/* @vite-ignore */ retries ? `${ocEntry}?retry=${retries}` : ocEntry) as Promise<OcModule>).then((module) => {
      if (!gone) attach(module);
    }, (error: Error) => {
      if (!gone) setFailed(error.message);
    });
    return () => {
      gone = true;
      view.current?.mounted.dispose();
      view.current = undefined;
      Object.assign(remoteView, { route: "", shown: "", ...inertView });
      announceRemoteView();
    };
  }, [wanted, configured, retries]);
  useEffect(() => show(), [id, visible, keep, messageID]);
  return { failed, created: create.error?.message ?? "", retry: () => { setFailed(""); setRetries((count) => count + 1); } };
}

// The page's one OpenCode view holds every configured server and reaches each through the Web's /oc/<server>/api/
// proxy. It mounts once any OpenCode tab of a configured server is open, on any space, and stays mounted, hidden but
// keeping its size, while another tab shows, so what OpenCode holds in memory carries over (docs/verification/oc-view-keep).
// Any session of a configured server opens, listed yet or not; a view already showing a session stays while its
// server blips and shows that state itself.
function RemoteSession({ id }: { id: string }) {
  const sidebar = useContext(Sidebar);
  const container = useRef<HTMLDivElement>(null);
  const server = id.split(":")[1] ?? "";
  const { servers, kept, first } = useOpenCodeTabs();
  const configured = servers.includes(server);
  const offline = sidebar.rows.find((row) => row.id === id)?.offline;
  // The session the view last reported for its route, which can be a subagent's; the route's own until it reports.
  const [report, setReport] = useState({ route: "", shown: "" });
  const reported = !!id && report.route === id;
  const visible = configured && (!offline || reported);
  const start = configured ? id : first;
  const { failed, created, retry } = useRemoteView({ id, start, servers, kept, visible, shown: reported ? report.shown : "", setReport, container });
  const typingId = reported ? report.shown : id;
  const name = useServer();
  // The composer that last took input, whose emptying by OpenCode's own code (a send of any kind) fires no input event.
  const [composer, setComposer] = useState<Element>();
  const emptied = useEffectEvent(() => { if (blankDraft(composer!)) reportTyping(name, typingId, false); });
  useEffect(() => {
    if (!composer) return;
    const observer = new MutationObserver(emptied);
    observer.observe(composer, { subtree: true, characterData: true, childList: true });
    return () => observer.disconnect();
  }, [composer]);
  // Only OpenCode's composer is typing, not its terminal or other fields.
  const typed = (event: SyntheticEvent) => {
    const target = event.target as Element;
    if (!target.matches("[role=textbox][contenteditable]")) return;
    reportTyping(name, typingId, !blankDraft(target));
    setComposer(target);
  };
  if (!start && !id) return null;
  return <>
    {id ? <RemoteStatus server={server} configured={configured} visible={visible} reported={reported} failed={failed} created={created} retry={retry} /> : null}
    {/* The pill sits beside the view, over the view's own layers, below OpenCode's header: 48 px, or about 81 px of
        tabs and location bar once OpenCode's 768 px breakpoint (oc/host/media.ts) finds the view narrow. OpenCode
        measures the view's content box (oc/host/mount.tsx), inside the 1 px padding of oc/host/scope.css, so the
        wrapper switches at 770 px. A subagent's session takes no prompts, so the pill names who types in the tab's own
        session, its main one. */}
    <div inert={!visible} className={cn("@container flex min-h-0 w-full flex-col", visible ? "relative flex-1" : "absolute inset-0 opacity-0")}>
      <div ref={container} onInput={typed} className="isolate flex min-h-0 w-full flex-1 flex-col" />
      <TypingPill server={name} id={id} shown={visible} top="top-22 @min-[770px]:top-16" main={typingId !== id} />
    </div>
  </>;
}

function HostedPage({ page }: { page: string }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [token] = useState(() => crypto.getRandomValues(new Uint32Array(4)).join("-"));
  const name = useServer(), origin = serverURL(name);
  const space = decodeURIComponent(page.split("/")[0]);
  useEffect(() => {
    const read = async ({ source, data }: MessageEvent) => {
      if (source !== frame.current?.contentWindow || data?.type !== "rocketclaw.read" || data.token !== token) return;
      const reply = await rpc<{ body: string }>("GetPage", { space, path: data.key, data: true }, undefined, name).then(
        ({ body }) => ({ value: JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(body), (char) => char.charCodeAt(0)))) }),
        (error: Error) => ({ error: error.message }));
      if (source === frame.current?.contentWindow) (source as Window).postMessage({ type: "rocketclaw.read", id: data.id, ...reply }, "*");
    };
    addEventListener("message", read);
    return () => removeEventListener("message", read);
  }, [space, name, token]);
  if (!page) return null;
  if (/\.pdf$/i.test(page)) return <p className="m-auto p-4 text-sm"><a href={`${origin}/pages-raw/${page}`} target="_blank" rel="noopener noreferrer" className="underline">Open PDF</a></p>;
  return <iframe ref={frame} title={pageLabel(page)} sandbox="allow-scripts allow-popups allow-forms allow-modals allow-downloads" className="min-h-0 w-full flex-1 border-0" src={`${origin}/pages-raw/${page}#rc=${token}`} />;
}

const hostedPagePath = ({ space, path }: PageEntry) => [space, ...path.split("/")].map(encodeURIComponent).join("/");

function PageIndex({ show }: { show: boolean }) {
  const servers = ["", ...rocketclaws().map((remote) => remote.name)];
  const lists = useQueries({ queries: servers.map((name) => ({ ...queries.pages(name), staleTime: 10_000, enabled: show })) });
  const { state } = useContext(TabActions);
  if (!show) return null;
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Pages</PageTitle>
      {lists.some((list) => list.isLoading) ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {lists.map((list, i) => list.error ? <p key={servers[i]} className="text-sm text-destructive">{servers.length > 1 ? `${serverLabel(servers[i])}: ` : ""}{list.error.message}</p> : null)}
      {lists.every((list) => list.data?.length === 0) ? <p className="text-sm text-muted-foreground">No pages</p> : null}
      {lists.map((list, i) => <HostedPageLinks key={servers[i]} pages={list.data ?? []} state={state} server={servers[i]} />)}
    </div>
  );
}

function HostedPageLinks({ pages, state, server: name }: { pages: PageEntry[]; state?: TabState; server?: string }) {
  const here = useServer();
  return [...Map.groupBy(pages, (page) => page.space)].toSorted(([a], [b]) => a.localeCompare(b)).map(([space, rows]) => <section key={space} className="flex flex-col gap-1">
    <h2 className="text-xs font-medium text-muted-foreground">{name !== undefined && rocketclaws().length ? `${space} · ${serverLabel(name)}` : space}</h2>
    <ul className="flex flex-col gap-1">
      {rows.map((page) => {
        const path = hostedPagePath(page);
        const href = !state ? panelHref(path, "pagePreview") : name === here ? pageSpace(state, `/pages/${path}`)[0] : `/pages/${path}`;
        return <li key={path}><Link href={href} server={name} className="block break-words py-1 text-sm hover:underline">{page.path}</Link></li>;
      })}
    </ul>
  </section>);
}

function HostedPagePanel() {
  const pathname = usePathname(), name = useServer();
  const selection = new URLSearchParams(useSearch()).get("pagePreview");
  if (!pathSession(pathname) || !selection) return null;
  const close = () => navigate(panelHref());
  return <SidePanel label="Hosted Pages" close={close}>
    <header className="flex shrink-0 items-center gap-2 border-b p-2 text-xs lg:pr-12">
      <span className="min-w-0 flex-1 truncate font-medium">Hosted Pages</span>
      {selection === "1" ? null : <Link href={panelHref("1", "pagePreview")} className="shrink-0 underline hover:text-foreground">Choose another page</Link>}
      <Button variant="ghost" size="icon-sm" aria-label="Close hosted pages" onClick={close}><X /></Button>
    </header>
    {selection === "1" ? <HostedPagePicker /> : <LiveHostedPage key={JSON.stringify([name, pathname, selection])} name={name} pathname={pathname} selection={selection} />}
  </SidePanel>;
}

function HostedPagePicker() {
  const pages = useQuery(queries.pages());
  return <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-3">
    {pages.isPending ? <p role="status" className="text-sm">Loading pages…</p> : null}
    {pages.isError ? <p role="alert" className="text-sm text-destructive">Could not load pages</p> : null}
    {pages.data?.length === 0 ? <p className="text-sm">No pages</p> : null}
    <HostedPageLinks pages={pages.data ?? []} />
  </div>;
}

function pageSelection(selection: string) {
  const [space, ...path] = selection.split("/");
  try { return { space: decodeURIComponent(space), path: path.map(decodeURIComponent).join("/") }; }
  catch { return { space: "", path: "" }; }
}

function LiveHostedPage({ name, pathname, selection }: { name: string; pathname: string; selection: string }) {
  const view = useId();
  const input = pageSelection(selection), valid = !!(input.space && input.path);
  const page = useQuery({
    queryKey: [{ server: name }, "hostedPage", pathname, input, view],
    queryFn: ({ signal }) => rpc<{ pages?: PageEntry[] }>("ListPages", input, signal, name).then(
      ({ pages }) => pages?.find((page) => page.path === input.path) ?? null,
      (error: Error) => { if (error instanceof RPCError && [7, 16].includes(error.code)) return null; throw error; },
    ),
    enabled: valid, retry: false, gcTime: 0, refetchInterval: 2000,
  });
  const current = page.data;
  if (!valid || current === null) return <p className="p-3 text-sm">Page unavailable</p>;
  return <>
    {page.isError ? <p role="alert" className="p-3 text-sm text-destructive">Could not refresh page</p> : current ? null : <p role="status" className="p-3 text-sm">Loading page…</p>}
    {current ? <HostedPage key={current.updatedAt} page={hostedPagePath(current)} /> : null}
  </>;
}

// The palette of a session whose agent can pause offers Pause agent, or Resume agent while it is paused.
function pauseActions(session: Session, pause: (paused: boolean) => void) {
  return session.pausable ? [{ key: "pause", label: session.paused ? "Resume agent" : "Pause agent", keep: true, run: () => pause(!session.paused) }] : [];
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
    ...(session.forkedFrom ? [{ key: "origin", label: "Open original conversation", run: () => navigate(rowPath({ id: session.forkedFrom!, kind: session.kind })) }] : []),
    { key: "name", label: "Name session", run: () => setCommand({ mode: "name", source: id }) },
    { key: "title", label: "Regenerate title", keep: true, run: () => update.mutate({ id, name: "" }) },
    ...pauseActions(session, (paused) => update.mutate({ id, paused })),
    ...choices.map((tag) => ({ key: `tag-${tag}`, label: `${tags.has(tag) ? "Remove" : "Add"} tag ${tag}`, keep: true, run: () => update.mutate({ id, tag }) })),
    { key: "delete", label: "Delete session", run: () => setCommand({ mode: "delete", source: id }) },
    ...(session.server ? [{ key: "add-tag", label: "Add tag…", run: () => setCommand({ mode: "tag", source: id }) }, ...remoteActions(id, remote?.servers[session.server] ?? "", session.remoteSessionId ?? "")] : []),
  ].map((item) => ({ ...item, disabled: update.isPending })) : [];
  return { items, error: update.error };
}

// A new space is named before it is made, on the server its command named, and joins the end of the strip; the server
// refuses a name another space has, ignoring case.
function NewSpaceDialog({ server: name }: { server: string }) {
  const { setCommand } = useContext(SessionCommands), [value, setValue] = useState(""), nameId = useId();
  const create = useMutation({ mutationFn: (space: string) => tabsCall(name, { createSpace: space }), onSuccess: () => { void queryClient.invalidateQueries({ queryKey: [{ server: name }, "tabs"] }); setCommand(undefined); } });
  return <Dialog open onOpenChange={(open) => { if (!open) setCommand(undefined); }}>
    <DialogContent>
      <DialogTitle>New space</DialogTitle>
      <DialogDescription>{rocketclaws().length ? `On ${serverLabel(name)}. ` : ""}Everyone who can see spaces there sees it.</DialogDescription>
      <form className="mt-4 flex flex-col gap-3" action={() => create.mutate(value.trim())}>
        <FieldGroup>
          <Field data-invalid={!!create.error}>
            <FieldLabel htmlFor={nameId}>Space name</FieldLabel>
            <Input id={nameId} name="name" type="text" value={value} aria-invalid={!!create.error} onChange={(event) => setValue(event.target.value)} />
            {create.error ? <FieldError>{create.error.message}</FieldError> : null}
          </Field>
        </FieldGroup>
        <div className="flex justify-end gap-2">
          <DialogClose render={<Button variant="ghost" />}>Cancel</DialogClose>
          <Button type="submit" disabled={create.isPending || !value.trim()}>{create.isPending ? "Creating…" : "Create"}</Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>;
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
          <DialogDescription>{tag ? "Pick a tag already in use or type a new one. Search tag:<name> to find tagged sessions." : "Shared with everyone who can see this session. Leave blank to generate a title from the conversation."}</DialogDescription>
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

// Deletes a remote OpenCode session on its server, through the Web's /oc/<server>/api/ proxy, or a RocketClaw session
// through DeleteSession, and closes its tab.
function DeleteSessionDialog({ id }: { id: string }) {
  const sidebar = useContext(Sidebar);
  const tabs = useContext(TabActions);
  const { setCommand } = useContext(SessionCommands);
  const [session] = useState(() => sidebar.rows.find((row) => row.id === id)!);
  const remove = useMutation({
    mutationFn: async () => {
      if (!session.server) return rpc("DeleteSession", { id });
      const response = await fetch(`${serverURL()}/oc/${session.server}/api/session/${session.remoteSessionId}`, { method: "DELETE" });
      if (!response.ok) throw new Error(`${session.server} did not delete the session (HTTP ${response.status}).`);
    },
    onSuccess: () => { tabs.deleted(rowPath(session)); sidebar.invalidateQueries(); setCommand(undefined); },
  });
  return <Dialog open onOpenChange={(open) => { if (!open && !remove.isPending) setCommand(undefined); }}>
    <DialogContent>
      <DialogTitle>Delete session</DialogTitle>
      <DialogDescription>Delete session "{sessionTitle(session)}"?</DialogDescription>
      {remove.error ? <p role="alert" className="text-sm text-destructive">{remove.error.message}</p> : null}
      <div className="mt-6 flex justify-end gap-2">
        <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
        <Button disabled={remove.isPending} onClick={() => remove.mutate()}>{remove.isPending ? "Deleting…" : "Delete session"}</Button>
      </div>
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

function withQuery({ searches, active }: SavedSearches, query: string): SavedSearches {
  const match = searches.find((item) => item.id === active && item.query === query) ?? searches.find((item) => item.query === query);
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
  // The searches the page held as it opened: running one of them again is no query the person entered.
  const [held] = useState(() => page?.searches ?? []);
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
    if (pagePath() === path && query) update(withQuery(savedRef.current, query));
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
  const selected = saved.searches.findIndex((tab) => tab.id === saved.active);
  const tab = saved.searches[selected];
  const search = useSearch();
  // Finish this page's URL write with its commit, before a later navigation can leave it.
  useLayoutEffect(() => {
    const params = new URLSearchParams(tab.query ? { q: tab.query } : {});
    history.replaceState(history.state, "", `${serverPath(path)}${params.size ? `?${params}` : ""}`);
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
    <SearchResults key={tab.id} path={path} held={held.some((item) => item.id === tab.id && item.query === tab.query)} tab={tab} rows={sidebar.all} edit={edit} input={input} />
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
  const row = (href: string, kind: string, body: string, key?: string) => <li key={key}><Link href={href} server={session.rocketclaw ?? ""} className="flex min-h-11 min-w-0 items-start gap-3 px-2 py-1 hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring sm:min-h-7"><span className="w-16 shrink-0 text-xs text-muted-foreground">{kind}</span><div className="min-w-0"><TranscriptText text={windowed(body, needle, tagIds)} needle={needle} interactive={false} /></div></Link></li>;
  return <li role="group" aria-label={clean} className="min-w-0">
    <h2 className="flex min-w-0 items-center gap-1 text-sm font-medium"><Link href={rowPath(session)} server={session.rocketclaw ?? ""} title={clean} className="min-w-0 truncate py-1 hover:underline focus-visible:outline-2 focus-visible:outline-ring"><InlineText text={label} /></Link><span className="shrink-0 text-muted-foreground">({count})</span></h2>
    <ul className="mt-1 border-l pl-2 text-sm leading-5">
      {field ? row(rowPath(session), field, text) : null}
      {hits.slice(0, drawn - Number(!!field)).map((match, i) => row(sessionPath(session.id) + (match.message.messageId ? `?message=${encodeURIComponent(match.message.messageId)}` : ""), match.message.role === "user" ? "You" : "Assistant", match.message.text, match.message.messageId || String(i)))}
    </ul>
  </li>;
}

// The palette lists sessions itself when nothing matched, so only /search passes empty.
function SearchStatus({ search, empty = false, incomplete = false }: { search: { pending: boolean; error?: string; sent: string; run: (text: string) => unknown }; empty?: boolean; incomplete?: boolean }) {
  const { pending, error } = search;
  return <>
    {pending ? <p role="status" className="text-muted-foreground">Searching…</p> : null}
    {error ? <p role="alert" className="text-destructive">Search failed: {error} <button type="button" className="underline" onClick={() => void search.run(search.sent)}>Retry</button></p> : null}
    {empty ? <p role="status" className="text-muted-foreground">{incomplete ? "Session search is still loading." : "No matches"}</p> : null}
  </>;
}

const searchPage = 200;

function SearchResults({ path, held, tab, rows, edit, input }: { path: string; held: boolean; tab: SavedSearch; rows: Session[]; edit: (change: (item: SavedSearch) => Partial<SavedSearch>, typed?: boolean) => void; input: RefObject<HTMLInputElement | null> }) {
  const sidebar = useContext(Sidebar);
  // Opening a saved search or a link sends its whole query.
  const [entered, setEntered] = useState(true);
  const search = useSessionSearch(tab.query, "messages", entered, { tab: path, searchId: tab.id, held });
  const { response } = search.result;
  // Rows are drawn a page at a time as the end nears the screen; a new response starts again at one page.
  const [budget, setBudget] = useState({ response, rows: searchPage });
  const rowsDrawn = budget.response === response ? budget.rows : searchPage;
  const { groups, more } = useMemo(() => {
    const hits = Map.groupBy(response?.messages ?? [], (match) => rowKey(match.conversationId, match.rocketclaw));
    let left = rowsDrawn;
    const groups = [];
    for (const { match, session } of search.found) {
      if (left <= 0) break;
      const own = hits.get(rowKey(session.id, session.rocketclaw)) ?? [];
      groups.push(<SearchResultGroup key={rowKey(session.id, session.rocketclaw)} session={session} hits={own} text={match.text} field={match.field} needle={response!.needle} tagIds={response!.mentionIds} drawn={left} />);
      left -= own.length + Number(!!match.field);
    }
    return { groups, more: left < 0 || groups.length < search.found.length };
  }, [response, search.found, rowsDrawn]);
  return <>
    <div id="search-tab-panel" role="tabpanel" aria-label="Search" className="min-w-0">
      <SessionSearch rows={rows} query={tab.query} setQuery={(update) => { setEntered(false); edit((item) => ({ query: update(item.query) }), true); }} search={search} inputRef={input} onKeyDown={(event) => { if (event.key === "Enter") { setEntered(true); void search.run(tab.query.trimEnd()); } }} />
    </div>
    <div className="min-h-0 flex-1 overflow-y-auto text-sm" aria-label="Search results">
      <SearchStatus search={search} empty={!!search.sent && !search.pending && !search.error && search.found.length === 0} incomplete={!searchIsAuthoritative(sidebar) || search.missing} />
      {search.sent ? <ul data-pending={search.pending} className="flex min-w-0 flex-col gap-4 data-[pending=true]:opacity-50">
        {groups}
        {more ? <li aria-hidden ref={loadMore((node) => node.closest('[aria-label="Search results"]'), () => setBudget((last) => ({ response, rows: (last.response === response ? last.rows : searchPage) + searchPage })))} /> : null}
      </ul> : <p className="text-muted-foreground">Type to search messages and conversations.</p>}
    </div>
  </>;
}

// Values offered for the operator word being typed; a completed value commits on space or Enter instead of offering itself again.
// Remote OpenCode sessions add their servers, agents and models; every RocketClaw's agents bring their configured models,
// a name several share offered once.
function searchChoices(word: string, rows: Session[], catalog: Agent[], principals: string[], spaces: Space[]) {
  const typed = new RegExp(`^(${filterKeys}):"?(.*?)"?$`, "is").exec(word);
  const key = typed?.[1].toLowerCase() ?? "", needle = typed?.[2].toLowerCase() ?? "";
  const remote = (field: "server" | "agent" | "model") => [...new Set(rows.flatMap((row) => row.server && row[field] ? [row[field]!] : []))];
  const names = key === "space" ? spaces.map(({ name }) => name) : key === "principal" ? principals : key === "room" ? slackRooms(rows) : key === "routine" ? [...new Set(rows.flatMap((row) => row.cronName ? [row.cronName] : []))] : key === "tag" ? [...new Set(rows.flatMap((row) => row.tags ?? []))] : key === "sort" ? ["newest", "oldest"]
    : key === "server" ? remote("server") : key === "model" ? [...new Set([...catalog.flatMap(({ model }) => model ? [model] : []), ...remote("model")])] : ["forked", "routine", "unread", "running", "queued", "waiting", "external"];
  const agents = key === "agent" ? [...catalog.filter((agent, i) => catalog.findIndex(({ name }) => name === agent.name) === i).map(({ name, model, reasoning, rocketclaw }) => ({ key: name, detail: [rocketclaw, model, reasoning].filter(Boolean).join(" · ") })), ...remote("agent").filter((name) => !catalog.some((agent) => agent.name === name)).map((name) => ({ key: name, detail: "OpenCode" }))] : [];
  const quoted = word.includes(':"');
  const spelling = (name: string) => quoted ? JSON.stringify(name).slice(1, -1) : name;
  const offered = !key ? [] : (key === "agent" ? agents : names.map((name) => ({ key: name, detail: "" }))).filter((item) => spelling(item.key).toLowerCase().includes(needle));
  return { key, choices: offered.some((item) => spelling(item.key).toLowerCase() === needle) ? [] : offered };
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

function SessionSearch({ rows, query, setQuery, search, inputRef, placeholder, onKeyDown }: {
  rows: Session[];
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
  const servers = ["", ...rocketclaws().map((remote) => remote.name)];
  // Agent and model choices cover every server's agents, a remote's marked with its name.
  const catalog = useQueries({ queries: servers.map((name) => ({ queryKey: [{ server: name }, "agents"], queryFn: ({ signal }: { signal: AbortSignal }) => rpc<AgentChoices>("ListAgents", {}, signal, name), staleTime: 60_000 })) })
    .flatMap(({ data }, i) => (data?.agents ?? []).map((agent) => ({ ...agent, rocketclaw: servers[i] })));
  const { key, choices } = searchChoices(search.word, rows, catalog, search.result.response?.principals ?? [], useContext(TabActions).state.spaces);
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
  const { close, state, pathname } = useContext(TabActions);
  return <header className="flex w-full shrink-0 items-center gap-2">
    <h1 tabIndex={-1} className="text-lg font-semibold outline-none">{children}</h1>
    {state.tabs.some((tab) => tab.path === pathname) ? <Button type="button" variant="ghost" size="icon-sm" className="ml-auto hidden md:inline-flex" aria-label="Close" onClick={() => close([pathname])}><X /></Button> : null}
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

export function transcriptTurns(lines: Line[], filter: OriginFilter = { sandboxed: true, canonical: true }, comparison = false) {
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

export function comparisonRows(parents: Turn[], children: Turn[], binding: ShadowBinding) {
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
  let input: { id?: string; code?: string; command?: string; path?: string; description?: string };
  try {
    input = JSON.parse(line.text.slice(name.length + 1));
  } catch {
    return name;
  }
  if (!input || typeof input !== "object") return name;
  if (name === "skill" && typeof input.id === "string") return `Skill · ${input.id}`;
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
  const url = file.file ? preview : `${serverURL()}/api/DownloadAttachment?${new URLSearchParams({ conversationId, id: file.id })}`;
  const image = /^image\/(png|jpeg|gif|webp|avif)$/.test(file.mimeType);
  return <Attachment orientation={image ? "vertical" : "horizontal"}>
    <AttachmentMedia variant={image ? "image" : "icon"}>{image ? <img src={url} alt={file.name} crossOrigin="anonymous" loading="lazy" /> : <FileIcon />}</AttachmentMedia>
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
  const reason = !revert?.available ? "Revert isn't available in this session" : kept ? floorReason : revertable ? "Revert message" : "This message can't be reverted";
  return <span className="flex" title={reason}><Button type="button" size="icon-xs" variant="ghost" className="size-11 sm:size-6" aria-label="Revert message" disabled={!revertable || revert!.pending} onClick={() => void revert!.run(line.messageId || line.inputId)}><Undo2 /></Button></span>;
}

// Copy is always last, with Revert before it on user messages, so the row keeps its shape
// whether or not the session allows reverting.
function MessageActions({ line, hasSandboxed, conversationId }: { line: Line; hasSandboxed: boolean; conversationId: string }) {
  const [copied, setCopied] = useState<string>();
  const [failed, setFailed] = useState<string>();
  const linked = usePathname() === sessionPath(conversationId) && !!line.messageId;
  const link = linked ? messageURL(sessionPath(conversationId), line.messageId!) : "";
  const copy = async (text: string) => {
    setFailed(undefined);
    try {
      await copyText(text, document.body);
      setCopied(text);
    } catch {
      setFailed(text);
    }
  };
  const failure = failed === link ? "Could not copy the link." : "Could not copy. Select and copy the text.";
  const success = copied === line.text ? "Copied" : linked && copied === link ? "Link copied" : "";
  const status = failed === undefined ? success : failure;
  return <div data-slot="message-actions" className={cn("invisible flex max-w-full items-center gap-1 group-focus-visible/message:visible [@media(hover:hover)]:group-hover/message:visible [@media(hover:hover)]:group-has-[:focus-visible]/message:visible [@media(hover:none)]:group-focus-within/message:visible", line.role === "user" && "self-end")}>
    <MessageFooter line={line} hasSandboxed={hasSandboxed} />
    <span role="status" className={failed === undefined ? "sr-only" : "text-xs text-destructive"}>{status}</span>
    {line.role === "user" ? <RevertButton line={line} /> : null}
    {linked ? <Button type="button" size="icon-xs" variant="ghost" aria-label="Copy link" title="Copy link" onClick={() => copy(link)}>{status === "Link copied" ? <Check /> : <Link2 />}</Button> : null}
    <Button type="button" size="icon-xs" variant="ghost" aria-label="Copy message" title="Copy message" onClick={() => copy(line.text)}>{status === "Copied" ? <Check /> : <Copy />}</Button>
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
    ...[...new Set(navigations(line))].map((path) => ({ href: path, text: location.origin + serverPath(path), label: `Open ${path}`, icon: <Link2 /> })),
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
    description: denied ? "denied" : call.status === "error" ? "failed" : call.status === "cancelled" ? "cancelled" : undefined };
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
    <CodeModeScript text={line.text.slice("execute ".length)} />
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
  if (line.completionNotes?.length) return <><CompletionNotes notes={line.completionNotes} /><MessageAttachments attachments={line.attachments} conversationId={conversationId} /></>;
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
        <MessageActions line={line} hasSandboxed={hasSandboxed} conversationId={conversationId} />
      </MessageContent>
    </Message>
  );
}

type Turn = ReturnType<typeof transcriptTurns>[number];

export function turnKey(turn: Turn) {
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
  const messageId = conversationId && pagePath() === sessionPath(conversationId) ? new URLSearchParams(search).get("message") : null;
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

export function TurnContent({ turn, detail, conversationId, hasSandboxed, comparison = false }: { turn: Turn; detail: TimelineRows; conversationId: string; hasSandboxed: boolean; comparison?: boolean }) {
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

export function shadowHistory(binding: ShadowBinding | undefined, drafts: Map<string, ComposerDraft>) {
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

export function ShadowCell({ row, index, binding, detail, delegations }: { row: ReturnType<typeof comparisonRows>[number]; index: number; binding: ShadowBinding; detail: TimelineRows; delegations?: Pick<HistoryView, "delegations" | "reviewRationales"> }) {
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
  const route = useRoute(), name = useServer();
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
    {/* Only the chat on screen watches; this transcript stays mounted, hidden, on other pages. */}
    <TypingPill server={name} id={conversationId} shown={route.id === conversationId} />
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

export function useSessionStream(id: string, draft: ComposerDraft, onDraftChange: () => void) {
  const scope = useContext(DraftScope);
  const history = useQuery({ ...queries.history({ id }), enabled: false });
  const refreshHistory = useCallback(() => readHistoryDelta(draft.sessionId, draft, onDraftChange), [draft, onDraftChange]);
  const loadEarlier = useCallback((from?: string) => readEarlierHistory(draft.sessionId, draft, onDraftChange, from), [draft, onDraftChange]);
  const followed = history.data, earlier = draft.delegations, earlierReviews = draft.reviewRationales;
  const delegations = useMemo(() => ({ delegations: [...new Set([...(followed?.delegations ?? []), ...(earlier ?? [])])], reviewRationales: { ...earlierReviews, ...followed?.reviewRationales } }), [followed, earlier, earlierReviews]);
  useEffect(() => {
    if (!id || !scope) return;
    const stream = new EventSource(`${serverURL()}/stream?${new URLSearchParams({ id })}`);
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
  return { busy: draft.busy, lines: draft.lines, refreshHistory, opening: id !== "" && !draft.revision, historyError: draft.historyError, origin: draft.origin, terminal: draft.terminal, delegations, shadows: mergeShadowBindings((draft.earlierShadows ?? []).reduce<ShadowBinding[]>((bindings, page) => mergeShadowBindings(bindings, page.shadows), []), draft.shadows ?? []), more: draft.more ?? false, start: draft.start, loadEarlier, movable: draft.movable ?? false, hasSandboxed: draft.lines.some((line) => line.origin === "sandboxed") };
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
        {origin.kind === "cron" ? `Routine · ${origin.stem}` : `External MCP · ${origin.externalConversationId}`} · {origin.agent}
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
  // This Transcript stays mounted, hidden, on other pages; a created chat opens once the new-chat page shows again.
  useLayoutEffect(() => {
    if (pagePath() === "/" && id === "" && draft.sessionId !== "" && drafts.get("") === draft) {
      drafts.delete("");
      onCreated(draft.sessionId);
      navigate(sessionPath(draft.sessionId));
    }
  });
  const { busy, lines, refreshHistory, opening, historyError, origin, terminal, delegations, shadows, more, start, loadEarlier, movable, hasSandboxed } = useSessionStream(id, draft, onDraftChange);
  const messageId = pagePath() === sessionPath(id) ? new URLSearchParams(search).get("message") : null;
  const matchedOrigin = (previewLines ?? lines).find((line) => line.messageId === messageId)?.origin;
  // A linked message older than the loaded entries loads every entry from it onward.
  const linkedEntry = messageId?.split(":")[0];
  useEffect(() => {
    if (!previewing && more && linkedEntry && /^\d+$/.test(linkedEntry) && BigInt(linkedEntry) < BigInt(start!)) void loadEarlier(linkedEntry);
  }, [previewing, more, linkedEntry, start, loadEarlier]);
  const visibleFilter = { sandboxed: filter.sandboxed || matchedOrigin === "sandboxed", canonical: filter.canonical || matchedOrigin === "canonical" };
  const runRevert = useCallback(async (messageId?: string) => { await revertComposer(draft, messageId, refreshHistory, onDraftChange); }, [draft, refreshHistory, onDraftChange]);
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
        <SessionComposer id={id} draft={draft} drafts={drafts} onDraftChange={onDraftChange} busy={busy} refreshHistory={refreshHistory} queued={queued} />
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

function ThreadPRPanel({ drafts, onDraftChange }: { drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  const id = pathSession(usePathname());
  const search = new URLSearchParams(useSearch());
  const sidebar = useContext(Sidebar);
  const wide = useMedia("(min-width: 64rem)");
  const { composer, added } = usePRComposer(id, drafts, onDraftChange, wide);
  if (!id) return null;
  const version = sidebar.rows.find((row) => row.id === id)?.updatedAt;
  const selected = search.get("pullRequests");
  const close = () => navigate(panelHref());
  return <>
    {wide ? <ThreadPRWatch key={id} id={id} version={version} /> : null}
    {selected === null ? null : <SidePanel label={threadGitHubLabel} close={close}>
      <ThreadPRContent key={`${id} ${selected}`} id={id} version={version} selected={selected} close={close} composer={composer} />
      {added ? <p key={added} role="status" className="shrink-0 border-t p-2 text-xs text-muted-foreground">Added to message</p> : null}
    </SidePanel>}
  </>;
}

const pullRequestName = ({ owner, repo, number }: GitHubPR) => `${owner}/${repo}#${number}`;
const openCodeComposer = (quotable: boolean): PRComposer => ({
  status: quotable ? "ready" : "none",
  quote: (reference, text) => remoteView.quote({ text, label: pullRequestName(reference), subject: `the description of pull request ${pullRequestName(reference)}` }),
  add: (reference, { note, path, range, lines }) => remoteView.quote({ text: lines.join("\n"), comment: note, label: `${pullRequestName(reference)} ${path}:${range.start}-${range.end}`, subject: `pull request ${pullRequestName(reference)}, file ${path} lines ${range.start}-${range.end}` }),
});

function noteBlock({ note, path, range, lines }: DiffNote) {
  const fence = "`".repeat(Math.max(3, ...lines.flatMap((line) => line.match(/`+/g) ?? []).map((run) => run.length + 1)));
  return `${note}\n\n${path} lines ${range.start}-${range.end}\n${fence}diff\n${lines.join("\n")}\n${fence}`;
}

function usePRComposer(id: string, drafts: Map<string, ComposerDraft>, onDraftChange: () => void, wide: boolean): { composer: PRComposer; added: number } {
  const quotable = useSyncExternalStore(subscribeRemoteView, () => remoteView.route === id && remoteView.quotable());
  const [added, setAdded] = useState(0);
  useEffect(() => {
    if (!added) return;
    const timer = setTimeout(() => setAdded(0), 4000);
    return () => clearTimeout(timer);
  }, [added]);
  if (id.startsWith("opencode:")) return { composer: openCodeComposer(quotable), added };
  const draft = drafts.get(id);
  const append = (block: string) => {
    const target = drafts.get(id)!;
    const mentions = target.mentions?.text === target.text ? target.mentions : undefined;
    target.text = appendBlock(target.text, block);
    if (mentions) target.mentions = { ...mentions, text: target.text };
    target.edit++;
    if (wide) target.focus = (target.focus ?? 0) + 1;
    else setAdded((count) => count + 1);
    onDraftChange();
  };
  const busy = !draft || [draft.sending, draft.reverting, !draft.hydrated].some(Boolean);
  return { composer: { status: busy ? "busy" : "ready", quote: (_, text) => append(quoteLines(text)), add: (_, note) => append(noteBlock(note)) }, added };
}

function SidePanel({ label, close, children }: { label: string; close: () => void; children: ReactNode }) {
  const wide = useMedia("(min-width: 64rem)");
  const popup = useRef<HTMLDivElement>(null);
  return wide ? <ResizableAside side="delegation" aria-label={label} className="flex flex-col border-l bg-background">{children}</ResizableAside> : <Sheet open onOpenChange={(open) => { if (!open) close(); }}>
    <SheetContent ref={popup} initialFocus={popup} side="right" showCloseButton={false} className="data-[side=right]:w-full data-[side=right]:sm:max-w-none"><SheetTitle className="sr-only">{label}</SheetTitle>{children}</SheetContent>
  </Sheet>;
}

function TreePanel({ id, drafts, onDraftChange }: { id: string; drafts: Map<string, ComposerDraft>; onDraftChange: () => void }) {
  const show = new URLSearchParams(useSearch()).has("tree");
  const wide = useMedia("(min-width: 64rem)");
  const draft = drafts.get(id);
  if (!show || !draft) return null;
  const close = () => navigate(panelHref());
  const refresh = () => readHistoryDelta(id, draft, onDraftChange);
  const moving = [draft.reverting, draft.sending, !draft.hydrated].some(Boolean);
  const pick = async (node: TreeNode) => {
    if (draft.reverting || draft.sending) return "";
    const moved = await revertComposer(draft, node.id, refresh, onDraftChange, node.through ? "THROUGH" : undefined);
    if (moved && !wide) close();
    return moved ? "" : draft.error;
  };
  return <SidePanel label="Message tree" close={close}><MessageTree key={id} id={id} revision={draft.revision} moving={moving} pick={pick} refresh={refresh} close={close} /></SidePanel>;
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

function uploadComposerFiles(sessionId: string, files: PendingFile[], name = server()): Promise<AttachmentMeta[]> {
  return Promise.all(files.map(async ({ file }) => {
    const response = await fetch(`${serverURL(name)}/api/UploadAttachment?${new URLSearchParams({ conversationId: sessionId, name: file.name })}`, { method: "POST", body: file });
    if (!response.ok) throw new Error(`Upload failed: ${file.name}`);
    return response.json();
  }));
}

async function prepareResumedDraft(id: string, scope: string, drafts: Map<string, ComposerDraft>, changed: () => void, name = server()) {
  const draft: ComposerDraft = drafts.get(id) ?? { text: "", files: [], agent: "", sessionId: id, sending: false, busy: false, lines: [], error: "", edit: 0, submission: 0 };
  drafts.set(id, draft);
  draft.persistenceKey = JSON.stringify([scope, id]);
  if (!draft.hydrated) {
    const content = await draftContent(draft.persistenceKey);
    if (content && !draft.edit && !draft.text && !draft.files.length) Object.assign(draft, content);
    draft.hydrated = true;
  }
  if (draft.sending || draft.reverting) throw new Error("This session is still sending or restoring its draft. Try again when it finishes.");
  const { text, files, edit } = draft;
  if (text !== "" || files.length) {
    draft.sending = true;
    changed();
    try {
      const attachments = await uploadComposerFiles(id, files, name);
      await mutations.prompt({ id, text, delivery: "STASH", attachmentIds: attachments.map((file) => file.id) }, name);
      void queryClient.invalidateQueries({ queryKey: ["queue"] });
      if (draft.edit !== edit) throw new Error("The old draft was stashed, but this session's draft changed. Review it before trying again.");
      draft.text = "";
      draft.mentions = undefined;
      draft.files = [];
      draft.agent = "";
      draft.edit++;
    } finally {
      draft.sending = false;
      changed();
    }
  }
  return draft;
}

async function sendComposer(input: {
  draft: ComposerDraft;
  onDraftChange: () => void;
  text: string;
  files: PendingFile[];
  delivery?: PromptDelivery;
  principal?: string;
  busy: boolean;
  // Whether the agent of a session, by its id, is paused; New session's send may resume a paused sticky session.
  paused?: (id: string) => boolean;
  sessionId: string;
  selected: string;
  currentAgent: string;
  goSession: (id: string, resumed?: boolean) => void;
  resumeSession: (id: string) => Promise<ComposerDraft>;
  prompt: { mutateAsync: (value: Parameters<typeof mutations.prompt>[0]) => ReturnType<typeof mutations.prompt> };
  create: { mutateAsync: (value: { agent?: string }) => Promise<{ id: string; resumed?: boolean }> };
  scrollToEnd: () => boolean;
  setAgentOpen: (value: boolean) => void;
  setSendError: (value: string) => void;
  refreshHistory: () => Promise<unknown>;
}) {
  const name = server();
  let { draft } = input;
  const mentions = draft.mentions?.text === input.text ? draft.mentions : undefined;
  // While the agent is paused, a message goes to the people here at once: no turn waits for it.
  let delivery = input.delivery ?? (input.busy && !input.paused?.(input.sessionId) ? "QUEUE" : "STEER");
  // Match strings.Fields/unicode.IsSpace in frontend/rpc/server.go, not JavaScript's whitespace.
  const command = input.delivery === "STASH" ? null : /^\p{White_Space}*\/(enqueue|stash|steer)(?=\p{White_Space}|$)\p{White_Space}*/u.exec(input.text);
  const followUp: PromptDelivery = command ? command[1] === "enqueue" ? "QUEUE" : command[1] === "stash" ? "STASH" : "STEER" : delivery;
  const stashing = followUp === "STASH";
  const mentionIds = stashing ? [] : [...new Set(mentions?.tags.filter((tag) => matchesComposerMention(input.text, tag)).map((tag) => tag.id))];
  const stopping = !stashing && isStopCommand(input.text);
  if (draft.sending || (input.text.trim() === "" && input.files.length === 0)) {
    return;
  }
  if (mentionIds.length > 5) return input.setSendError("Tag at most five people per message.");
  draft.sending = true;
  let submission = ++draft.submission;
  input.onDraftChange();
  const agent = draft.agent;
  let dispatchedEdit: number | undefined;
  let enqueue = followUp !== "STEER" && !stopping;
  input.scrollToEnd();
  input.setSendError("");
  const optimistic: Line = { id: crypto.getRandomValues(new Uint32Array(4)).join("-"), role: "user", text: command ? input.text.slice(command[0].length) : input.text, principal: input.principal };
  try {
    let sessionId = input.sessionId;
    if (sessionId === "") {
      const created = await input.create.mutateAsync({ agent: input.selected });
      sessionId = created.id;
      if (created.resumed) {
        alertResumed(input.selected);
        const target = await input.resumeSession(sessionId);
        draft.sending = false;
        draft.text = "";
        draft.mentions = undefined;
        draft.files = [];
        draft.agent = "";
        draft.edit++;
        draft = target;
        input = { ...input, draft, busy: draft.busy,
          setSendError: (value) => { draft.error = value; input.onDraftChange(); },
          refreshHistory: () => readHistoryDelta(sessionId, draft, input.onDraftChange),
        };
        draft.text = input.text;
        draft.files = input.files;
        draft.agent = agent;
        draft.edit++;
        draft.sending = true;
        draft.error = "";
        submission = ++draft.submission;
        delivery = input.delivery ?? (input.busy && !input.paused?.(sessionId) ? "QUEUE" : "STEER");
        enqueue = command ? followUp !== "STEER" : delivery !== "STEER" && !stopping;
      } else firstMessages.set(sessionId, optimistic.text.trim().split("\n", 1)[0]);
      input.goSession(sessionId, created.resumed);
    }
    const paused = input.paused?.(sessionId);
    if (!input.busy && !enqueue && !paused) { draft.busy = true; input.onDraftChange(); }
    optimistic.attachments = input.files.map(({ id, file }) => ({ id, name: file.name, mimeType: file.type, size: String(file.size), conversationId: sessionId, file }));
    if (!enqueue && input.busy && !stopping && !paused) {
      draft.parked = [...(draft.parked ?? []), optimistic];
      input.onDraftChange();
    } else if (!enqueue) { draft.lines = [...draft.lines, optimistic]; input.onDraftChange(); }
    const attachments = await uploadComposerFiles(sessionId, input.files, name);
    if (!stashing && !stopping && !/^\p{White_Space}*\/agent(?=\p{White_Space}|$)/u.test(optimistic.text) && input.sessionId !== "" && input.currentAgent !== "" && input.selected !== "" && input.selected !== input.currentAgent) {
      await input.prompt.mutateAsync({ id: sessionId, text: `/agent ${input.selected}` });
      await queryClient.invalidateQueries({ queryKey: ["agents"] });
    }
    if (attachments.length) {
      optimistic.attachments = attachments.map((file, index) => ({ ...file, file: input.files[index].file }));
      draft.lines = draft.lines.map((line) => line.id === optimistic.id ? optimistic : line); input.onDraftChange();
    }
    // Prompt waits for the whole turn. Consume only this submission before waiting.
    draft.text = "";
    draft.mentions = undefined;
    reportTyping(name, input.sessionId, false);
    draft.files = [];
    draft.agent = "";
    dispatchedEdit = ++draft.edit;
    const response = input.prompt.mutateAsync({ id: sessionId, text: input.text, delivery, messageId: optimistic.id, ...(mentionIds.length ? { mentionIds } : {}), ...(attachments.length ? { attachmentIds: attachments.map((file) => file.id) } : {}) });
    draft.sending = false;
    draft.focus = (draft.focus ?? 0) + 1;
    input.onDraftChange();
    input.setAgentOpen(false);
    const { privateText, mentionWarning } = await response;
    if (mentionWarning) input.setSendError(mentionWarning);
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
      draft.lines = [...draft.lines, ...(parked ? [optimistic] : []), { id: `${optimistic.id}:reply`, role: "assistant", text: privateText, complete: true }]; input.onDraftChange();
    }
  } catch (err) {
    draft.parked = draft.parked?.filter((line) => line.id !== optimistic.id);
    if (dispatchedEdit === undefined) draft.sending = false;
    else if (draft.edit === dispatchedEdit && draft.submission === submission) {
      draft.text = input.text;
      draft.mentions = mentions;
      draft.files = input.files;
      draft.agent = agent;
      draft.edit++;
    }
    draft.lines = draft.lines.filter((line) => line.id !== optimistic.id); input.onDraftChange();
    if (draft.submission === submission) {
      input.setSendError(err instanceof Error ? err.message : "send failed");
      if (!stashing) { draft.busy = input.busy; input.onDraftChange(); }
    }
  }
}

// The newest turn's last request when that turn has not saved it, named by its input ID.
function unsavedRequest(lines: Line[]) {
  const latest = lines.findLast((line) => line.entryKey)?.entryKey;
  return lines.findLast((line) => line.role === "user" && line.entryKey === latest && !line.messageId)?.inputId || undefined;
}

async function revertComposer(draft: ComposerDraft, messageId: string | undefined, refresh: () => Promise<unknown>, changed: () => void, boundary?: "THROUGH") {
  if (draft.reverting || draft.sending) return false;
  if (!draft.revertMessageId) messageId ??= unsavedRequest(draft.lines);
  const edit = draft.edit, submission = draft.submission;
  draft.reverting = true;
  draft.historyEpoch = (draft.historyEpoch ?? 0) + 1;
  draft.error = "";
  changed();
  try {
    await queryClient.cancelQueries({ queryKey: ["history"] });
    const result = await mutations.stageRevert({ id: draft.sessionId, messageId, boundary });
    if (result.prompt) {
      const files = await restoreFiles(result.prompt.attachments ?? []);
      if (draft.edit === edit && draft.submission === submission) {
        draft.text = result.prompt.text;
        draft.mentions = undefined;
        draft.files = files;
        draft.restored = { text: draft.text, files };
        draft.edit++;
        draft.focus = (draft.focus ?? 0) + 1;
      }
    } else if (boundary && draft.restored?.text === draft.text && draft.restored.files === draft.files) {
      draft.text = "";
      draft.mentions = undefined;
      draft.files = [];
      draft.edit++;
    }
    return true;
  } catch (err) {
    draft.error = err instanceof Error ? err.message : "History change failed";
    return false;
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
  prompt: { mutateAsync: (value: { id: string; text: string }) => ReturnType<typeof mutations.prompt> };
};

// Queue actions switch an unlisted agent first, holding the draft's sending flag so a send cannot switch at the same time.
async function switchQueueAgent(input: QueueAgentSwitch) {
  if (input.currentAgent === "" || input.selected === "" || input.selected === input.currentAgent) return;
  input.draft.sending = true;
  input.onDraftChange();
  try {
    await input.prompt.mutateAsync({ id: input.id, text: `/agent ${input.selected}` });
    await queryClient.invalidateQueries({ queryKey: ["agents"] });
  } finally {
    input.draft.sending = false;
    input.onDraftChange();
  }
}

async function promoteComposer(input: QueueAgentSwitch & {
  draft: Pick<ComposerDraft, "sending" | "submission" | "busy">;
  itemId: string;
  busy: boolean;
  steerQueueItem: { mutateAsync: (value: { id: string; itemId: string }) => Promise<unknown> };
  setSendError: (value: string) => void;
}) {
  if (input.id === "" || input.draft.sending) {
    return;
  }
  const submission = ++input.draft.submission;
  if (!input.busy) {
    input.draft.busy = true; input.onDraftChange();
  }
  input.setSendError("");
  try {
    await switchQueueAgent(input);
    await input.steerQueueItem.mutateAsync({ id: input.id, itemId: input.itemId });
  } catch (err) {
    if (input.draft.submission === submission) {
      input.setSendError(err instanceof Error ? err.message : "steer failed");
      input.draft.busy = input.busy; input.onDraftChange();
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
    await input.prompt.mutateAsync({ id: input.id, text: "/stop" });
    await input.refreshHistory();
  } catch (err) {
    if (input.draft.submission === submission) input.setSendError(err instanceof Error ? err.message : "stop failed");
  }
}

function pendingInputs(draft: ComposerDraft, items: QueueItem[]) {
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
  refreshHistory,
  queued,
}: {
  id: string;
  draft: ComposerDraft;
  drafts: Map<string, ComposerDraft>;
  onDraftChange: () => void;
  busy: boolean;
  refreshHistory: () => Promise<unknown>;
  queued: QueueItem[];
}) {
  const name = useServer();
  const identity = useQuery(queries.identity());
  const { setCommand } = useContext(SessionCommands);
  const scope = useContext(DraftScope)!;
  const revert = useContext(RevertActions)!;
  const { scrollToEnd, scrollToMessage } = useMessageScroller();
  const prompt = useMutation({ mutationFn: (input: Parameters<typeof mutations.prompt>[0]) => mutations.prompt(input, name), onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["queue"] }); } });
  // No polling: on a slow link a 2 s refresh used a large share of the connection. Agent switches made here
  // refetch it; changes made elsewhere arrive when the window regains focus or the session reopens.
  const agents = useQuery(queries.agents({ conversationId: id }));
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
  const setText = (value: string, tags?: ComposerMention[]) => {
    draft.mentions = tags?.length ? { text: value, tags } : undefined;
    draft.text = value;
    draft.edit++;
    reportTyping(name, id, value !== "");
    setTyped((count) => count + 1);
    const error = draft.persistenceError ?? "";
    void persistComposerDraft(draft)?.finally(() => { if (draft.persistenceError !== error) onDraftChange(); });
  };
  const setFiles = (value: PendingFile[]) => { draft.files = value; draft.edit++; onDraftChange(); };
  const setAgent = (value: string) => {
    const resumed = id === "" ? agents.data?.agents.find((item) => item.name === value)?.stickySession : undefined;
    if (resumed) {
      alertResumed(value);
      return navigate(sessionPath(resumed));
    }
    draft.agent = value; draft.edit++; onDraftChange();
  };
  const [agentOpen, setAgentOpen] = useState(false);
  const [slashOff, setSlashOff] = useState(false);
  const [slashPick, setSlashPick] = useState("");
  const voice = voiceFor(useVoice(), id);
  const sendError = draft.error || voice.error;
  // Clearing the send error also dismisses this composer's failed call.
  const setSendError = (value: string) => { draft.error = value; onDraftChange(); if (!value && voice.state === "error") stopVoice(); };
  const currentAgent = id === "" ? "main" : agents.data?.currentAgent ?? "";
  const catalog = (agents.data?.agents ?? []).filter((item) => id !== "" || item.sticky !== "slack");
  // Sends switch to the shown agent, so an unlisted current agent moves to the first listed one.
  const selected = offeredAgent(catalog, agent, currentAgent) ?? currentAgent;
  const skills = useQuery({ ...queries.skills({ agent: selected }), enabled: selected !== "", placeholderData: undefined });
  const matches = slashOff ? [] : slashMatches(text, skills.data ?? []);
  const pick = Math.max(0, matches.findIndex((item) => item.invocation === slashPick));
  const applySlash = (invocation: string, prefix = 0) => {
    const tags = prefix && draft.mentions?.text === text
      ? draft.mentions.tags.map((tag) => ({ ...tag, start: tag.start + prefix, end: tag.end + prefix }))
      : undefined;
    setText(invocation, tags);
    setSlashOff(invocation !== "/skill ");
    setAgentOpen(false);
  };
  const pausedIn = usePausedChats(), paused = pausedIn(id), question = useQuestion(id, draft, paused, refreshHistory, setSendError);
  const { placeholder, sendLabel } = composerLabels(paused, question.pending, busy, id);
  const goSession = (sessionId: string, resumed = false) => {
    void draftContent(JSON.stringify([scope, ""]), () => ({ text: "", files: [], agent: "" })).catch(() => { draft.persistenceError = "Draft could not be saved locally. Keep this page open."; onDraftChange(); });
    draft.sessionId = sessionId;
    if (!resumed) {
      draft.persistenceKey = JSON.stringify([scope, sessionId]);
      draft.persistedEdit = undefined;
      drafts.set(sessionId, draft);
    }
    onDraftChange();
  };
  // A call from a new session creates it with the shown agent, as Send does.
  const call = async (speaker: string) => {
    let sessionId = draft.sessionId;
    try {
      if (sessionId === "") {
        const created = await create.mutateAsync({ agent: selected });
        sessionId = created.id;
        if (created.resumed) alertResumed(selected);
        goSession(sessionId, created.resumed);
      }
    } catch (err) {
      return setSendError(err instanceof Error ? err.message : "create failed");
    }
    void startVoice(sessionId, speaker);
  };
  const send = (delivery?: PromptDelivery) => {
    const command = /^\p{White_Space}*\/(fork|handoff|undo|tree)\p{White_Space}*$/u.exec(draft.text);
    if (delivery !== "STASH" && command) {
      if (command[1] === "undo") { setText(""); return revert.run(); }
      if (!id) { setSendError("Open a session first."); return Promise.resolve(); }
      if (command[1] === "tree") navigate(panelHref("1", "tree"));
      else setCommand({ mode: command[1] as "fork" | "handoff", source: id });
      setText("");
      return Promise.resolve();
    }
    // A pending question takes Send's text as its custom answer instead of queueing it behind the waiting turn.
    const answering = question.take(delivery, setText);
    if (answering) return answering;
    return sendComposer({
      draft,
      onDraftChange,
      text: draft.text,
      files: draft.files,
      delivery,
      principal: identity.isSuccess && !identity.isFetching ? identity.data.principal : undefined,
      busy,
      paused: pausedIn,
      sessionId: draft.sessionId,
      selected,
      currentAgent,
      goSession,
      resumeSession: (sessionId) => prepareResumedDraft(sessionId, scope, drafts, onDraftChange, name),
      prompt,
      create,
      scrollToEnd,
      setAgentOpen,
      setSendError,
      refreshHistory,
    });
  };
  const promoteQueued = (itemId: string) => promoteComposer({ draft, onDraftChange, id, itemId, busy, selected, currentAgent, prompt, steerQueueItem, setSendError });
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
        historyCommand={() => void revert.run()}
        sessionId={id}
        text={text}
        tags={draft.mentions?.text === text ? draft.mentions.tags : []}
        setText={setText}
        matches={matches}
        pick={pick}
        applySlash={applySlash}
        catalog={catalog}
        selected={selected}
        setAgent={setAgent}
        agentOpen={agentOpen}
        setAgentOpen={setAgentOpen}
        setSlashOff={setSlashOff}
        setSlashPick={setSlashPick}
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

const quoteLines = (selected: string) => selected.replace(/\r\n?/g, "\n").split("\n").map((line) => `> ${line}`).join("\n");
const appendBlock = (text: string, block: string) => (text ? text + "\n\n" : "") + block + "\n\n";

function Composer({
  files,
  setFiles,
  sending,
  messageInput,
  historyCommand,
  sessionId,
  text,
  tags,
  setText,
  matches,
  pick,
  applySlash,
  catalog,
  selected,
  setAgent,
  agentOpen,
  setAgentOpen,
  setSlashOff,
  setSlashPick,
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
  historyCommand: () => void;
  sessionId: string;
  text: string;
  tags: ComposerMention[];
  setText: (value: string, tags?: ComposerMention[]) => void;
  matches: ReturnType<typeof slashMatches>;
  pick: number;
  applySlash: (invocation: string, prefix?: number) => void;
  catalog: { name: string; model?: string; reasoning?: string }[];
  selected: string;
  setAgent: (name: string) => void;
  agentOpen: boolean;
  setAgentOpen: (open: boolean | ((value: boolean) => boolean)) => void;
  setSlashOff: (off: boolean) => void;
  setSlashPick: (pick: string) => void;
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
  // Match OpenCode's embedded packages/app/src/composer/editor/editor.tsx:
  // touch Return adds a line; the separate touch bar sends, queues, steers, or stops.
  const touch = useMedia("(hover: none) and (pointer: coarse)");
  const selectedButton = useRef<HTMLButtonElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const { composer } = useContext(SessionCommands);
  useImperativeHandle(composer, () => (command) => {
    // The agent picker keeps focus so desktop typing searches agents.
    if (command === "agent") return setAgentOpen(true);
    if (command === "undo") return historyCommand();
    if (command === "tree") return navigate(panelHref("1", "tree"));
    if (command === "stop") void stop();
    else applySlash(`/${command} ${text}`, command.length + 2);
    messageInput.current?.focus();
  });
  const empty = text.trim() === "" && files.length === 0;
  const mentions = useComposerMentions(text, tags, messageInput, setText);
  const agent = catalog.find((item) => item.name === selected);
  const actions = <>
    <VoiceToggle sessionId={sessionId} agent={selected} sending={sending} call={call} />
    <ComposerMore sending={sending} empty={empty} stash={() => void send("STASH")} />
    <ComposerSend sessionId={sessionId} touch={touch} busy={busy} sending={sending} empty={empty} stoppingCommand={isStopCommand(text)} sendLabel={sendLabel} mac={mac} send={send} stop={stop} />
  </>;
  const attach = <Tooltip disabled={touch}><TooltipTrigger render={<InputGroupButton size="icon-sm" className={touch ? "size-11" : "size-7"} disabled={sending} />} aria-label="Add files" onMouseDown={(event) => event.preventDefault()} onClick={() => fileInput.current?.click()}><Plus /></TooltipTrigger><TooltipContent>Add files</TooltipContent></Tooltip>;
  const pickerOpen = matches.length > 0;
  const selectedInvocation = matches[pick]?.invocation;
  useEffect(() => {
    selectedButton.current?.scrollIntoView({ block: "nearest" });
  }, [pickerOpen, selectedInvocation]);
  return (
    <div data-native-composer data-touch={touch} className="shrink-0">
      <div className="px-3 pb-3">
      <SelectionQuote container={() => document.getElementById("transcript-scroll")} hidden={sending} quote={(selected) => {
        const value = appendBlock(text, quoteLines(selected));
        flushSync(() => setText(value, tags));
        messageInput.current!.focus();
        messageInput.current!.setSelectionRange(value.length, value.length);
      }} />
      <div className="relative mx-auto w-full max-w-3xl">
        {mentions.menu}
        {pickerOpen && !mentions.hasPeople ? (
          <ul className="absolute inset-x-0 bottom-full z-10 mb-2 max-h-[50dvh] overflow-y-auto rounded-2xl border bg-popover text-popover-foreground shadow-md">
            {matches.map((cmd, index) => (
              <li key={cmd.invocation}>
                <button
                  ref={index === pick ? selectedButton : null}
                  type="button"
                  disabled={sending}
                  className={cn("flex w-full flex-col items-start gap-0.5 px-3 py-2 text-left text-sm", index === pick && "bg-accent")}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => applySlash(cmd.invocation)}
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
            aria-label="Message"
            enterKeyHint={touch ? "enter" : undefined}
            disabled={sending}
            value={text}
            onInput={(event) => {
              setSlashOff(false);
              setAgentOpen(false);
              mentions.edit(event);
              mentions.update(event.currentTarget);
            }}
            onSelect={(event) => mentions.update(event.currentTarget)}
            placeholder={placeholder}
            className="min-h-[60px] max-h-[180px] overflow-y-auto"
            onKeyDown={(event) => {
              if (event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229 || event.repeat) return;
              if (mentions.keyDown(event)) return;
              if (touch && event.key === "Enter" && !event.metaKey && !event.ctrlKey) return;
              if (event.key === "Enter" && event.altKey && (event.metaKey || event.ctrlKey) && !event.shiftKey) {
                event.preventDefault();
                void send("STASH");
                return;
              }
              if (matches.length > 0) {
                if (event.key === "ArrowDown") {
                  event.preventDefault();
                  setSlashPick(matches[(pick + 1) % matches.length].invocation);
                  return;
                }
                if (event.key === "ArrowUp") {
                  event.preventDefault();
                  setSlashPick(matches[(pick + matches.length - 1) % matches.length].invocation);
                  return;
                }
                if (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey)) {
                  event.preventDefault();
                  applySlash(matches[pick].invocation);
                  return;
                }
                if (event.key === "Escape") {
                  event.preventDefault();
                  setSlashOff(true);
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
                void send(!touch && busy && (event.metaKey || event.ctrlKey) ? "STEER" : undefined);
              }
            }}
          />
          <InputGroupAddon align="block-end" className="h-11">
            {!touch ? attach : null}
            <div data-slot="composer-controls" className="flex h-full min-w-0 flex-1 items-center gap-1 overflow-x-auto overscroll-x-contain scrollbar-none">
              <AgentPicker catalog={catalog} selected={selected} setAgent={setAgent} open={agentOpen} setOpen={setAgentOpen} disabled={sending || catalog.length === 0} />
              {agent?.model ? <span title={agent.model} className="max-w-[220px] shrink-0 truncate px-2 text-muted-foreground">{agent.model}</span> : null}
              {agent?.reasoning ? <span className="shrink-0 px-2 text-muted-foreground capitalize">{agent.reasoning}</span> : null}
            </div>
            {!touch ? <div className="flex shrink-0 items-center gap-1">{actions}</div> : null}
          </InputGroupAddon>
        </PromptInput>
      </div>
      </div>
      {touch ? <div data-slot="composer-touch-bar" className="flex h-14 items-center gap-1 border-t bg-background px-2">{attach}{actions}</div> : null}
    </div>
  );
}

function ComposerMore({ sending, empty, stash }: { sending: boolean; empty: boolean; stash: () => void }) {
  return <DropdownMenu>
    <DropdownMenuTrigger aria-label="Composer actions" render={<InputGroupButton size="icon-sm" disabled={sending} />}><Ellipsis /></DropdownMenuTrigger>
    <DropdownMenuContent side="top" className="min-w-44">
      <Menu.Group>
        <Menu.Item nativeButton render={<Button variant="ghost" className="w-full justify-start" />} disabled={sending || empty} onClick={stash}>Stash</Menu.Item>
      </Menu.Group>
    </DropdownMenuContent>
  </DropdownMenu>;
}

function ComposerSend({ sessionId, touch, busy, sending, empty, stoppingCommand, sendLabel, mac, send, stop }: {
  sessionId: string; touch: boolean; busy: boolean; sending: boolean; empty: boolean; stoppingCommand: boolean; sendLabel: string; mac: boolean;
  send: (delivery?: PromptDelivery) => Promise<void>; stop: () => Promise<void>;
}) {
  // Steer and the pause toggle take turns, so the row fits a phone: a paused chat's Send already posts, and the
  // toggle steps aside while Steer shows.
  const paused = usePausedChats()(sessionId), steer = busy && !empty && !stoppingCommand && !paused;
  return <div data-slot="composer-delivery" className="ml-auto flex shrink-0 items-center gap-1">
    {!steer ? <AgentPauseToggle id={sessionId} /> : null}
    {steer ? <Tooltip disabled={touch}>
      <TooltipTrigger render={<InputGroupButton size="sm" disabled={sending} />} aria-label="Steer" onMouseDown={(event) => event.preventDefault()} onClick={() => void send("STEER")}>
        Steer{!touch ? <span className="hidden sm:inline"><ModEnterKeys mac={mac} /></span> : null}
      </TooltipTrigger>
      <TooltipContent>Guide the current response without stopping it</TooltipContent>
    </Tooltip> : null}
    <ComposerSubmit touch={touch} busy={busy} sending={sending} empty={empty} sendLabel={sendLabel} send={send} stop={stop} />
  </div>;
}

function ComposerSubmit({ touch, busy, sending, empty, sendLabel, send, stop }: Omit<ComponentProps<typeof ComposerSend>, "sessionId" | "mac" | "stoppingCommand">) {
  const stopping = busy && empty;
  const label = stopping ? "Stop" : touch && sendLabel === "Send to queue" ? "Queue" : "Send";
  const Icon = sending ? LoaderCircle : stopping ? Square : ArrowUp;
  return <Tooltip disabled={touch}><TooltipTrigger data-composer-submit render={<InputGroupButton size={touch ? "sm" : "icon-sm"} variant={stopping ? "secondary" : "default"} disabled={sending || (!stopping && empty)} />} aria-label={label} onMouseDown={(event) => event.preventDefault()} onClick={() => void (stopping ? stop() : send())}>
    {touch ? label : null}<Icon className={cn(sending && "animate-spin")} />
  </TooltipTrigger><TooltipContent>{stopping ? "Stop response" : sendLabel}</TooltipContent></Tooltip>;
}

// The agent menu follows Model Selector (https://ui.shadcn.com/docs/ai-elements/base/model-selector):
// one radio menu, each agent with its model and effort. Typing a name's first letters moves to it.
function AgentPicker({ catalog, selected, setAgent, open, setOpen, disabled }: { catalog: Agent[]; selected: string; setAgent: (name: string) => void; open: boolean; setOpen: (open: boolean) => void; disabled: boolean }) {
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger disabled={disabled} aria-label="Choose agent" title={selected} render={<InputGroupButton size="sm" className="min-h-11 min-w-0 max-w-[220px] shrink sm:h-7 sm:min-h-0" />}>
        <span className={cn("min-w-0 truncate", !selected && "text-muted-foreground")}>{selected || "Choose agent"}</span><ChevronDown />
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
  const sidebar = useContext(Sidebar);
  const open = useMutation({ mutationFn: mutations.createSession, onSuccess: ({ id }) => {
    sidebar.invalidateQueries();
    void queryClient.invalidateQueries({ queryKey: ["cronJobs"] });
    navigate(sessionPath(id));
  } });
  return job.nextRun ? <Link href={sessionPath(job.nextRun)} className={className}>{children}</Link> : <>
    <button type="button" className={className} disabled={open.isPending} onClick={() => open.mutate({ sourceConversationId: job.origin })}>{open.isPending ? "Opening chat…" : children}</button>
    {open.error ? <p role="alert" className="text-sm text-destructive">{open.error.message}</p> : null}
  </>;
}

function CronRunPreview({ preview, onClose }: { preview: CronJob; onClose: () => void }) {
  const conversationId = preview.origin!;
  const history = useQuery(queries.history({ id: conversationId }));
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
      <div className="mt-2 max-h-64 overflow-y-auto">
        <TranscriptLines rows={previewLines.map((line) => ({ kind: "line", line }))} conversationId={conversationId} hasSandboxed={hasSandboxed} />
        {history.data?.messages.length === 0 ? <p className="text-sm text-muted-foreground">No recorded messages for this run.</p> : null}
      </div>
    </section>
  );
}

// Runs carry no space, so a job outside this space hides its runs too.
function cronInSpace(jobs: CronJob[] = [], inSpace: (item: { space?: string }) => boolean) {
  const away = new Set(jobs.filter((job) => !inSpace(job)).map((job) => job.stem));
  return jobs.filter((job) => !away.has(job.stem));
}

const cronPath = (stem: string) => `/routines/${encodeSessionId(stem)}`;
const cronDay = (day: string) => new Date(day).toLocaleDateString("en-US", { dateStyle: "medium", timeZone: "UTC" });

function cronGap(ms: number) {
  const minutes = Math.round(ms / 60_000), hours = Math.floor(minutes / 60);
  return minutes < 60 ? `${minutes}m` : hours < 24 ? `${hours}h ${minutes % 60}m` : `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

// One cron's runs, the same rows the Routines page lists, as a journal. A failed load keeps what is shown and retries until it loads.
function CronTimeline({ stem }: { stem: string }) {
  const jobs = useQuery({ ...queries.cronJobs(), staleTime: 10_000, retry: true });
  const rows = cronInSpace(jobs.data, useInSpace()).filter((job) => job.stem === stem);
  const [count, setCount] = useState(60);
  const [picked, setPicked] = useState("");
  const wide = useMedia("(min-width: 64rem)");
  const run = useCronRun();
  if (!jobs.data) return <p role="status" className="p-4 text-sm text-muted-foreground">Loading…</p>;
  if (rows.length === 0) return <p className="p-4 text-sm text-muted-foreground">No routine named {stem} in this space.</p>;
  const job = rows.find((item) => item.status !== "ran");
  const runs = rows.filter((item) => item.status === "ran");
  const times = runs.map((run) => Date.parse(run.lastRun));
  const gaps = times.slice(1).map((at, index) => times[index] - at);
  const preview = runs.find((run) => run.origin === picked);
  const showDay = (day: string) => {
    // The day and a page of older runs, so the day can scroll to the top.
    flushSync(() => setCount(Math.max(count, runs.findLastIndex((run) => run.lastRun.startsWith(day)) + 60)));
    document.getElementById(`cron-day-${day}`)?.scrollIntoView();
  };
  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 overflow-y-auto p-4">
      <CronOverview stem={stem} job={job} runs={runs} gaps={gaps} runButton={job ? run.button(stem) : null} />
      {run.error}
      {runs.length ? <CronDayBars runs={runs} through={job ? Date.now() : times[0]} onPick={showDay} /> : null}
      <div className={cn("grid items-start gap-4", wide && preview ? "grid-cols-2" : "grid-cols-1")}>
        <CronJournal runs={runs.slice(0, count)} gaps={gaps} picked={picked} setPicked={setPicked} inline={!wide} />
        {wide && preview ? <div className="sticky top-0"><CronRunPreview preview={preview} onClose={() => setPicked("")} /></div> : null}
      </div>
      {count < runs.length ? <Button variant="outline" className="self-start" onClick={() => setCount(count + 100)}>Show older runs</Button> : null}
      {runs.length === 0 ? <p className="text-sm text-muted-foreground">No runs yet.</p> : null}
      {run.dialog}
    </div>
  );
}

function CronOverview({ stem, job, runs, gaps, runButton }: { stem: string; job?: CronJob; runs: CronJob[]; gaps: number[]; runButton: ReactNode }) {
  const last = relativeTime(runs[0]?.lastRun ?? "");
  return <>
    <PageTitle><Link href="/routines" className="text-muted-foreground hover:underline">Routines</Link> / {stem} <span className="ml-1 rounded-full border px-2 align-middle text-xs font-normal">{job ? "Active" : "Retired"}</span></PageTitle>
    <div className="flex flex-col gap-2 text-xs text-muted-foreground">
      <p className="flex items-center gap-1">{runButton}{[job?.agent, "Times in UTC"].filter(Boolean).join(" · ")}</p>
      {job?.schedule ? <ul aria-label="Schedule" className="flex flex-wrap gap-1">{job.schedule.split(", ").map((at) => <li key={at} className="rounded-md bg-muted px-2 py-0.5 font-mono">{at}</li>)}</ul> : null}
      {job ? <details><summary className="cursor-pointer">Definition</summary><p className="mt-2 font-mono">{job.origin}</p><pre className="mt-2 whitespace-pre-wrap">{job.body || "No body."}</pre></details> : null}
    </div>
    <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
      <CronStat label="Runs" value={String(runs.length)} />
      <CronStat label="Typical gap" value={gaps.length ? cronGap(gaps.toSorted((a, b) => a - b)[gaps.length >> 1]) : "—"} detail={gaps.length ? `Longest ${cronGap(Math.max(...gaps))}` : ""} />
      <CronStat label="Last run" value={!last ? "—" : last === "now" ? "Just now" : `${last} ago`} detail={runs[0] ? `${cronWhen(runs[0].lastRun)} UTC` : ""} />
      <CronStat label="Next run" value={!job ? "Retired" : job.upcoming?.length ? `${cronWhen(job.upcoming[0])} UTC` : "—"} />
    </dl>
    {job?.upcoming?.length ? <section aria-label="Upcoming" className="flex flex-wrap items-center gap-2 text-sm"><h2 className="text-xs font-medium text-muted-foreground">Upcoming</h2>{job.upcoming.map((at) => <span key={at} className="rounded-md border px-2 py-0.5">{cronWhen(at)} UTC</span>)}</section> : null}
  </>;
}

function CronStat({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return <div className="rounded-md border p-3"><dt className="text-xs text-muted-foreground">{label}</dt><dd className="font-medium">{value}</dd>{detail ? <dd className="text-xs text-muted-foreground">{detail}</dd> : null}</div>;
}

// One bar per UTC day from the first run through the given time. Picking a day shows its runs.
function CronDayBars({ runs, through, onPick }: { runs: CronJob[]; through: number; onPick: (day: string) => void }) {
  const counts = Map.groupBy(runs, (run) => run.lastRun.slice(0, 10));
  const most = Math.max(...[...counts.values()].map((day) => day.length));
  const days: string[] = [];
  for (let at = Date.parse(runs.at(-1)!.lastRun.slice(0, 10)); at <= through; at += 86_400_000) days.push(new Date(at).toISOString().slice(0, 10));
  return <div role="group" aria-label="Runs per day" className="flex h-20 shrink-0 items-end gap-0.5 overflow-x-auto pb-1">
    {days.map((day) => {
      const total = counts.get(day)?.length ?? 0;
      return <button key={day} type="button" disabled={total === 0} aria-label={`${cronDay(day)}: ${total} runs`} title={`${cronDay(day)}: ${total} runs`} className="flex h-full min-w-2 flex-1 items-end rounded-sm hover:bg-accent focus-visible:outline-2" onClick={() => onPick(day)}>
        <span className="w-full rounded-sm bg-cyan-600 dark:bg-cyan-400" style={{ height: `${(total / most) * 100}%` }} />
      </button>;
    })}
  </div>;
}

// Runs under a header per UTC day, each with the time since the run before it. Narrow screens preview a run under it.
function CronJournal({ runs, gaps, picked, setPicked, inline }: { runs: CronJob[]; gaps: number[]; picked: string; setPicked: (origin: string) => void; inline: boolean }) {
  const days = Map.groupBy(runs.map((run, index) => ({ run, gap: gaps[index] })), ({ run }) => run.lastRun.slice(0, 10));
  return <section aria-label="Runs" className="flex flex-col gap-4">
    {[...days].map(([day, items]) => <section key={day} aria-labelledby={`cron-day-${day}`}>
      <h2 id={`cron-day-${day}`} className="py-1 text-sm font-medium">{cronDay(day)}</h2>
      <ul className="flex flex-col">
        {items.map(({ run, gap }) => <li key={run.origin}>
          <button type="button" aria-pressed={picked === run.origin} className={cn("flex w-full justify-between gap-4 rounded-md px-2 py-1.5 text-sm hover:bg-accent", picked === run.origin && "bg-accent")} onClick={() => setPicked(picked === run.origin ? "" : run.origin!)}>
            <span>{new Date(run.lastRun).toLocaleTimeString("en-US", { timeStyle: "short", timeZone: "UTC" })}</span>
            <span className="text-xs text-muted-foreground">{gap === undefined ? "First run" : `+${cronGap(gap)}`}</span>
          </button>
          {inline && picked === run.origin ? <CronRunPreview preview={run} onClose={() => setPicked("")} /> : null}
        </li>)}
      </ul>
    </section>)}
  </section>;
}

// Runs a cron after a confirmation, then opens its chat; its button spins until the run finishes.
function useCronRun() {
  const run = useMutation({ mutationFn: mutations.runCron, onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["cronJobs"] }); } });
  const [confirmStem, setConfirmStem] = useState("");
  const confirmTrigger = useRef<HTMLButtonElement>(null);
  return {
    error: run.error ? <p className="text-sm text-destructive">{run.error.message}</p> : null,
    button: (stem: string) => <Button size="icon" variant="ghost" className="size-6 shrink-0" aria-label={run.isPending && run.variables?.stem === stem ? `Running ${stem}` : `Run ${stem}`} title={`Run ${stem}`} disabled={run.isPending} onClick={(event) => { confirmTrigger.current = event.currentTarget; setConfirmStem(stem); }}>
      {run.isPending && run.variables?.stem === stem ? <LoaderCircle aria-hidden="true" className="h-4 w-4 animate-spin motion-reduce:animate-none" /> : <Play aria-hidden="true" className="h-4 w-4" />}
    </Button>,
    dialog: <Dialog open={confirmStem !== ""} onOpenChange={(open) => { if (!open) setConfirmStem(""); }}>
      <DialogContent finalFocus={confirmTrigger}>
        <DialogTitle>Run {confirmStem}?</DialogTitle>
        <DialogDescription>Start this routine now?</DialogDescription>
        <div className="mt-6 flex justify-end gap-2">
          <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
          <Button onClick={() => {
            run.mutate({ stem: confirmStem }, { onSuccess: (id, { stem }) => { if (id === "") return; firstMessages.set(id, stem); navigate(sessionPath(id)); } });
            setConfirmStem("");
          }}>Run</Button>
        </div>
      </DialogContent>
    </Dialog>,
  };
}

function CronPage() {
  const jobs = useQuery({ ...queries.cronJobs(), staleTime: 10_000 });
  const run = useCronRun();
  const [runQuery, setRunQuery] = useState("");
  const [preview, setPreview] = useState<CronJob | null>(null);
  const rows = cronInSpace(jobs.data, useInSpace());
  const start = Date.now() - 12 * 3_600_000;
  const runNeedle = runQuery.trim().toLowerCase();
  const groups = [...Map.groupBy(rows, (job) => job.stem)].filter(([, items]) => runNeedle === "" || items.some((job) => [
    ...Object.values(job).flat(),
    ...[job.lastRun, ...(job.upcoming ?? [])].filter(Boolean).map((at) => `${cronWhen(at)} UTC`),
  ].join(" ").toLowerCase().includes(runNeedle))).map(([stem, items]) => ({
    stem,
    job: items.find((item) => item.status !== "ran"),
    runs: items.filter((item) => item.status === "ran"),
  }));
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Routines</PageTitle>
      <Input aria-label="Search" value={runQuery} onChange={(event) => setRunQuery(event.target.value)} placeholder="Search" />
      {jobs.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {jobs.error ? <p className="text-sm text-destructive">{jobs.error.message}</p> : null}
      {run.error}
      <section className="flex flex-col gap-2">
        <div className="flex gap-4 text-xs text-muted-foreground"><span className="flex items-center gap-1"><span className="size-2 rounded-sm bg-cyan-600 dark:bg-cyan-400" />Ran</span><span className="flex items-center gap-1"><span className="size-2 rounded-sm border-2 border-amber-600 dark:border-amber-400" />Expected</span></div>
        <div className="flex items-end gap-2 text-[10px] tabular-nums text-muted-foreground">
          <span className="w-36 shrink-0" />
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
            <span className="flex w-36 shrink-0 items-center gap-1 text-xs">{job ? run.button(stem) : <span className="size-6 shrink-0" />}<Link href={cronPath(stem)} className="truncate hover:underline">{stem}</Link></span>
            <div className="relative h-4 min-w-0 flex-1 rounded-sm bg-muted">
              <button type="button" className="absolute inset-0 rounded-sm focus-visible:outline-2" aria-label={`Preview latest run of ${stem}`} disabled={jobRuns.length === 0} onClick={() => setPreview(jobRuns[0] ?? null)} />
              {[...(job?.upcoming ?? []).map((at) => ({ key: at, at, label: `Expected ${stem} at ${cronWhen(at)} UTC`, tooltip: `Expected ${stem} at ${cronWhen(at)} UTC`, item: undefined })),
                ...jobRuns.map((item) => ({ key: item.origin, at: item.lastRun, label: `Preview ${item.stem} at ${cronWhen(item.lastRun)} UTC`, tooltip: `Ran ${cronWhen(item.lastRun)} UTC`, item }))].map(({ key, at, label, tooltip, item }) => {
                const pct = ((Date.parse(at) - start) / (36 * 3_600_000)) * 100;
                return Number.isFinite(pct) && pct >= 0 && pct <= 100 ? <CronMarker key={key} label={label} tooltip={tooltip} pct={pct} ran={!!item} onClick={item && (() => setPreview(item))} /> : null;
              })}
            </div>
          </div>
        ))}
      </section>
      {preview && groups.some(({ stem }) => stem === preview.stem) ? <CronRunPreview preview={preview} onClose={() => setPreview(null)} /> : null}
      {run.dialog}
    </div>
  );
}

function VoicePage() {
  const identity = useQuery(queries.identity());
  if (!identity.data) return null;
  return <VoiceCall key={identity.data.username} username={identity.data.username} />;
}

const voiceFor = (voice: VoiceView, id: string): VoiceView => voice.conversationId === id ? voice : { state: "idle", conversationId: id, error: "", notice: "", captions: [] };

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
function VoiceStage({ voice, compact, busy, agents, selected, settingsLoading, error, onPress }: { voice: VoiceView; compact: boolean; busy: boolean; agents: QueryStatus; selected: string; settingsLoading: boolean; error?: string; onPress: () => void }) {
  if (agents === "success" && !selected) return <p role="status" className="m-auto text-center text-sm text-muted-foreground">No voice agent is available. The built-in voice agent may be turned off; an admin can turn it on in Config → Behavior settings.</p>;
  const loading = agents === "pending" || settingsLoading || !selected;
  const active = voice.state !== "idle" && voice.state !== "error";
  return <div className={cn("flex flex-col items-center", compact ? "shrink-0 gap-2" : "flex-1 justify-center gap-10 py-10")}>
    <VoiceOrb state={voice.state === "reconnecting" ? "connecting" : voice.state} data-size={compact ? "small" : "large"} aria-label={active ? "End voice call" : "Start voice call"} aria-pressed={active} disabled={busy || voice.state === "ending" || (!active && loading)} onClick={onPress} />
    <span role="status" aria-live="polite" className="text-sm tracking-wide text-muted-foreground">{voiceStatus[voice.state]}{voice.state === "live" ? <VoiceClock startedAt={voice.startedAt!} /> : null}</span>
    <span aria-live="polite" className="text-center text-sm">{voice.notice}</span>
    {error ? <p role="alert" className="text-center text-sm text-destructive">{error}</p> : null}
  </div>;
}

function useVoiceSettings(username: string) {
  const settings = useQuery(queries.voiceSettings(username));
  const saved = settings.data ?? { agent: "", voice: "", transcript: false };
  const save = (next: VoiceSettings) => queryClient.setQueryData(queries.voiceSettings(username).queryKey, next);
  const version = useRef({ agent: 0, voice: 0, transcript: 0 });
  const update = useMutation({
    scope: { id: JSON.stringify(queries.voiceSettings(username).queryKey) },
    mutationFn: (change: Partial<VoiceSettings>) => mutations.voiceSettings({ action: "UPDATE", ...change }),
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
  return { saved, update, loading: settings.isPending, problem: settings.error?.message };
}

function VoiceCall({ username }: { username: string }) {
  const sidebar = useContext(Sidebar);
  const { saved, update, loading, problem } = useVoiceSettings(username);
  const agents = useQuery(queries.agents());
  const catalog = (agents.data?.agents ?? []).filter((item) => item.sticky === "user");
  const selected = offeredAgent(catalog, saved.agent, "voice") ?? "";
  const agentInfo = catalog.find((item) => item.name === selected);
  const [callId, setCallId] = useState("");
  const snapshot = useVoice(), call = voiceFor(snapshot, callId);
  const live = ["connecting", "reconnecting", "live", "ending"].includes(call.state);
  const id = (live ? callId : "") || agentInfo?.stickySession || "";
  const voice = voiceFor(snapshot, id);
  // The page owns its call: leaving the page hangs it up.
  useEffect(() => () => { if (voiceView().conversationId === id && voiceView().state !== "error") stopVoice(); }, [id]);
  const [picking, setPicking] = useState(false);
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
  const active = !["idle", "error"].includes(voice.state);
  const voices = agentInfo?.voices ?? [];
  const speaker = voiceChoice(agentInfo, saved.voice);
  // Leaving cancels a start still creating its conversation.
  const left = useRef(false);
  useEffect(() => () => { left.current = true; }, []);
  const start = useMutation({ mutationFn: async () => {
    const { id: next } = await mutations.createSession({ agent: selected });
    sidebar.invalidateQueries();
    void queryClient.invalidateQueries({ queryKey: ["agents"] });
    if (left.current) return;
    setCallId(next);
    void startVoice(next, speaker);
  } });
  const error = [voice.error, call.error, problem, ...[start, update].map((mutation) => mutation.error?.message)].find(Boolean);
  return (
    <div className="mx-auto flex h-full min-h-0 w-full max-w-3xl flex-col gap-4 p-4">
      <PageTitle>Voice</PageTitle>
      <div className="flex items-center gap-1.5 sm:gap-2 max-sm:[&>:first-child>svg:first-child]:hidden">
        <AgentPicker catalog={catalog} selected={selected} setAgent={(name) => update.mutate({ agent: name })} open={picking} setOpen={setPicking} disabled={[active, start.isPending, update.isPending, catalog.length === 0].some(Boolean)} />
        {agentInfo && voices.length > 0 ? <VoicePicker voices={voices} value={speaker} disabled={active || start.isPending} onPick={(name) => update.mutate({ voice: name })} /> : null}
        <div className="ml-auto flex items-center gap-1">
          <Button variant={transcript ? "secondary" : "ghost"} size="icon" className="size-11 sm:size-8" aria-label="Show transcript" title="Show transcript" aria-pressed={transcript} onClick={() => update.mutate({ transcript: !transcript })}><Captions /></Button>
          {id ? <Button variant="ghost" size="icon" className="size-11 sm:size-8" role="link" nativeButton={false} render={<Link href={sessionPath(id)} />} aria-label="Open conversation" title="Open conversation"><MessageSquare /></Button> : null}
        </div>
      </div>
      {transcript ? <VoiceTranscript captions={voice.captions} /> : null}
      <VoiceStage voice={voice} compact={transcript} busy={start.isPending} agents={agents.status} selected={selected} settingsLoading={loading} error={error} onPress={() => active ? stopVoice() : start.mutate()} />
    </div>
  );
}

// VoiceToggle calls with the session's agent, or with agent when the session is new.
function VoiceToggle({ sessionId, agent, sending, call }: { sessionId: string; agent: string; sending: boolean; call: (speaker: string) => Promise<void> }) {
  const route = useRoute();
  const { state, notice } = voiceFor(useVoice(), sessionId);
  const calling = state !== "idle" && state !== "error";
  const choices = useQuery(queries.agents({ conversationId: sessionId }));
  const identity = useQuery(queries.identity());
  const settings = useQuery({ ...queries.voiceSettings(identity.data?.username ?? ""), enabled: identity.isSuccess });
  const loadingVoice = voiceInputsPending(choices.status, identity.status, settings.status);
  const speaker = voiceChoice(choices.data?.agents.find((item) => item.name === (sessionId ? choices.data?.currentAgent : agent)), settings.data?.voice);
  // The session pane stays mounted on page routes, so leaving the session ends its call as unmounting does.
  useEffect(() => () => { if (voiceView().conversationId === sessionId) stopVoice(); }, [route.id, sessionId]);
  return <>
    <span aria-live="polite" title={notice} className="hidden max-w-48 truncate text-xs text-muted-foreground empty:absolute sm:inline">{notice}</span>
    <Tooltip><TooltipTrigger render={<Button type="button" variant={calling ? "secondary" : "ghost"} size="icon" className="size-11 sm:size-7" disabled={!calling && (sending || loadingVoice)} />} aria-label="Voice call" aria-pressed={calling} onMouseDown={(event) => event.preventDefault()} onClick={() => void (calling ? stopVoice() : call(speaker))}>
      {state === "connecting" ? <LoaderCircle className="animate-spin" /> : calling ? <MicOff /> : <Mic />}
    </TooltipTrigger><TooltipContent>{calling ? `End voice call (${state})` : "Start voice call"}</TooltipContent></Tooltip>
  </>;
}

function AgentsPage() {
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000 });
  const [editing, setEditing] = useState(false);
  const identity = useQuery(queries.identity()).data, editor = identity !== undefined && !identity.limited, edit = useMutation({ mutationFn: editAgent, onSuccess: () => queryClient.invalidateQueries({ queryKey: ["agentDrafts"] }), onSettled: () => setEditing(false) });
  const [open, setOpen] = useState<string | null>(null);
  const rows = agents.data?.agents ?? [];
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 overflow-y-auto p-4">
      <PageTitle>Agents</PageTitle>
      {editor ? <Link href="/agents/new" className={buttonVariants({ size: "sm", className: "w-fit" })}><Plus />Create New Agent</Link> : null}
      {edit.error ? <p role="alert" className="text-sm text-destructive">{edit.error.message}</p> : null}
      {agents.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {agents.error ? <p className="text-sm text-destructive">{agents.error.message}</p> : null}
      {rows.length === 0 && !agents.isLoading && !agents.error ? <p className="text-sm text-muted-foreground">No agents configured.</p> : null}
      {rows.map((agent) => (
        <section key={agent.name} className="border-b pb-4">
          <div className="flex items-start gap-2">
            <button type="button" className="min-w-0 flex-1 text-left" onClick={() => setOpen(open === agent.name ? null : agent.name)}>
              <h2 className="text-sm font-medium">{agent.name}</h2>
              <p className="text-xs text-muted-foreground">{[agent.model, agent.reasoning, agent.verbosity, agent.origin].filter(Boolean).join(" · ")}</p>
              {agent.description ? <p className="mt-1 text-sm leading-relaxed">{agent.description}</p> : null}
            </button>
            {agent.origin === "built-in" ? <BuiltInNote /> : editor ? <Button size="xs" variant="outline" aria-label={`Edit ${agent.name}`} disabled={editing} onClick={() => { setEditing(true); edit.mutate(agent.name); }}>Edit</Button> : null}
          </div>
          {open === agent.name ? (
            <div className="mt-3 flex flex-col gap-3">
              {([["Shell", agent.customShell], ["Permissions", agent.permissions], ["Prompt", agent.prompt]] as const).map(([title, text]) => text ? (
                <div key={title}>
                  <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
                  <pre className={cn("mt-1 whitespace-pre-wrap text-xs", title === "Prompt" && "leading-relaxed")}>{text}</pre>
                </div>
              ) : null)}
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
  const channels: ConfigItem[] = (view.slackChannels ?? []).map((channel, index) => ({
    key: `${channel.channel ?? ""}-${index}`,
    label: channel.channel ?? "",
    detail: channel.agents ?? [],
  }));
  const servers: ConfigItem[] = (view.mcpServerFacets ?? []).map(({ server = "", facets }) => ({ key: server, label: server, detail: facets }));
  const limited = identity.data?.limited;
  return (
    <>
      {limited ? null : <>
        <ConfigSection title="Runtime">
          <ConfigRow label="Workspace" value={view.workspace ?? ""} />
          <ConfigRow label="Logging" value={view.loggingLevel ?? ""} />
          <ConfigList items={overlays} empty="No overlays" />
        </ConfigSection>
        <ConfigSection title="Models">
          <ConfigRow label="Auto-approver" value={view.autoApproverModel ?? ""} />
        </ConfigSection>
      </>}
      <ConfigSection title="Web">
        <ConfigRow label="Configured user" value={identity.data?.username ?? ""} />
        <ConfigRow label="Tailscale user" value={view.tailscaleUser || "Unavailable"} />
      </ConfigSection>
      <ConfigSection title="Slack">
        <ConfigList items={channels} empty="No channels" />
      </ConfigSection>
      {limited ? null : <>
        <ConfigSection title="MCP">
          <ConfigList items={servers} empty="No servers" />
        </ConfigSection>
        <ConfigSection title="Flags">
          <ConfigRow label="PostHog" value={view.instrumentationEnabled ? "On" : "Off"} />
          <ConfigRow label="External MCP" value={view.mcpExternal ? "On" : "Off"} />
        </ConfigSection>
      </>}
      {view.canEdit ? <ConfigSection title="Behavior settings"><BehaviorSettings view={view} /></ConfigSection> : null}
    </>
  );
}

// Auto-park is this person's choice for every session they chose nothing for on this server, which keeps it, so it
// follows them and reaches OpenCode sessions too. Each session row says whether it applies there.
function AutoParkRow() {
  const name = useServer(), owner = useQuery(queries.identity()).data?.username, { queryKey } = queries.tabs(owner ?? "", name);
  const saved = useQuery({ ...queries.tabs(owner ?? "", name), enabled: false }), invalidate = useContext(SidebarInvalidation), id = useId();
  const change = useMutation({ mutationFn: (autoPark: boolean) => tabsCall(name, { autoPark }), onSuccess: (reply) => {
    queryClient.setQueryData<SavedTabs>(queryKey, (old) => old && { ...old, autoPark: reply.autoPark });
    invalidate();
  } });
  return <div className="flex items-center justify-between gap-4 border-b py-2">
    <div>
      <h3 id={id} className="text-sm">Auto-park sessions</h3>
      <p className="text-sm text-muted-foreground">A message you send parks its session's tab until it is done. A session's tab menu can turn this on or off for that session alone.</p>
    </div>
    <Switch aria-labelledby={id} checked={!!saved.data?.autoPark} disabled={!saved.data || change.isPending} onCheckedChange={(checked) => change.mutate(checked)} />
  </div>;
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
        <div className="flex flex-wrap items-center justify-between gap-4 border-b py-2">
          <span className="text-sm text-muted-foreground">Light or dark</span>
          <ThemeModeChooser />
        </div>
      </ConfigSection>
      <ConfigSection title="Timeline">
        <TimelineDetailCard />
      </ConfigSection>
      <ConfigSection title="Tabs">
        <AutoParkRow />
      </ConfigSection>
      {config.isLoading ? <p className="text-sm text-muted-foreground">Loading…</p> : null}
      {config.error ? <p className="text-sm text-destructive">{config.error.message}</p> : null}
      {config.data ? <ConfigLoaded view={config.data} /> : null}
    </div>
  );
}
