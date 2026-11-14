import { useState } from "react";
import { useMutation, useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Quote, ShieldAlert, Sparkles, X } from "lucide-react";
import { mutations, queries } from "@/api";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import type { AgentDraft, AgentDraftProposal, AgentDraftVariant, AgentSettingHelp } from "@/types";
import { variantTitle } from "./chat";
import { DiffLines } from "./review";

type Ask = Parameters<typeof mutations.proposeDraftChanges>[0];
// A test answer pointed at: the variant that gave it, its column's title and its text.
export type Fix = { variant: string; title: string; text: string };

// Settings in these groups decide what the agent may touch and run.
const accessGroups = ["Tools", "Folder and shell"];

// Asks outlive the page that made them, as the start pane's does, so the panel finds them by key.
export function useAsk() {
  const client = useQueryClient();
  return useMutation({ mutationKey: ["proposeDraftChanges"], mutationFn: mutations.proposeDraftChanges, onSuccess: (_, ask) => client.invalidateQueries({ queryKey: ["agentDraft", ask.id] }) });
}

// The variant and mode as accepting every open suggestion would leave them, and the keys and instructions changed.
export function suggestion(mode: string, variant: AgentDraftVariant, proposals: AgentDraftProposal[]) {
  const open = proposals.filter((proposal) => proposal.state === "open");
  const changes = open.flatMap((proposal) => proposal.settings).filter((change) => change.key === "mode" || change.variant === variant.name);
  const settings = new Map(variant.settings.map(({ key, value }) => [key, value]));
  for (const change of changes) {
    if (change.key === "mode") mode = change.valueYaml;
    else if (change.valueYaml) settings.set(change.key, change.valueYaml);
    else settings.delete(change.key);
  }
  const body = open.flatMap((proposal) => proposal.bodies).findLast((replacement) => replacement.variant === variant.name)?.body;
  return { mode, variant: { ...variant, settings: [...settings].map(([key, value]) => ({ key, value })), body: body ?? variant.body }, keys: new Map(changes.map((change) => [change.key, change.valueYaml])), body: body !== undefined };
}

export function SuggestedFile({ before, after }: { before: string; after: string }) {
  return <section aria-label="Suggested change to the file" className="max-h-64 shrink-0 overflow-y-auto border-b font-mono text-xs leading-5">
    <p className="sticky top-0 flex items-center gap-1 bg-background px-3 py-1 font-sans text-violet-700 dark:text-violet-300"><Sparkles className="size-3" />Suggested by the assistant: the file once accepted</p>
    <DiffLines strike before={before} after={after} />
  </section>;
}

// Hidden, the panel is a button that shows it again; pointing at an answer shows it too.
export function Assistant({ draft, proposals, fix, onFix, hidden, onHide }: { draft: AgentDraft; proposals: AgentDraftProposal[]; fix?: Fix; onFix: (fix?: Fix) => void; hidden: boolean; onHide: (hidden: boolean) => void }) {
  const help = useQuery(queries.agentSettings(draft.mode)).data ?? [];
  const asks = useMutationState({ filters: { mutationKey: ["proposeDraftChanges"], predicate: (mutation) => (mutation.state.variables as Ask | undefined)?.id === draft.id },
    select: (mutation) => ({ id: mutation.mutationId, request: (mutation.state.variables as Ask).request, status: mutation.state.status, error: mutation.state.error }) });
  if (hidden) return <section aria-label="Assistant" className="flex shrink-0 border-b p-1.5 lg:flex-col lg:border-b-0">
    <Button size="icon-sm" variant="ghost" aria-label="Show the assistant" onClick={() => onHide(false)}><Sparkles /></Button>
  </section>;
  const last = asks.at(-1);
  return <section aria-label="Assistant" className="flex min-h-96 min-w-0 flex-col lg:min-h-0 lg:w-75">
    <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b px-3 py-1">
      <Sparkles className="size-4 text-violet-600" /><h2 className="text-sm font-medium">Assistant</h2><span className="truncate text-xs text-muted-foreground">suggests, never saves</span>
      <Button size="icon-xs" variant="ghost" className="ml-auto" aria-label="Hide the assistant" onClick={() => { onHide(true); onFix(); }}><X /></Button>
    </div>
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-3 text-xs">
      <p className="text-muted-foreground">Ask for a change, or press Fix this on a test answer. The assistant reads the settings and the test chat and only suggests: nothing changes until someone accepts, and it never saves or runs tools.</p>
      {proposals.filter((proposal) => proposal.state !== "rejected").map((proposal) => <Card key={proposal.id} draft={draft} proposal={proposal} help={help} />)}
      {asks.filter(({ status }) => status === "pending").map(({ id, request }) => <p key={id} role="status" className="text-muted-foreground">Thinking about “{request}”…</p>)}
      {last?.status === "error" ? <p role="alert" className="text-destructive">{last.error?.message}</p> : null}
    </div>
    <Composer id={draft.id} fix={fix} onFix={onFix} />
  </section>;
}

function Composer({ id, fix, onFix }: { id: string; fix?: Fix; onFix: (fix?: Fix) => void }) {
  const [text, setText] = useState("");
  const ask = useAsk();
  const send = () => {
    ask.mutate({ id, request: text.trim() || "Fix this answer.", answer: fix?.text, variant: fix?.variant });
    setText("");
    onFix();
  };
  return <div className="flex shrink-0 flex-col gap-2 border-t p-3">
    {fix ? <div className="flex items-start gap-1.5 rounded-md border px-2 py-1 text-xs"><Quote className="mt-0.5 size-3 shrink-0 text-violet-600" />
      <p className="line-clamp-3 min-w-0 flex-1"><span className="font-medium">Fixing {fix.title}’s answer:</span> {fix.text}</p>
      <Button size="icon-xs" variant="ghost" aria-label="Stop fixing this answer" onClick={() => onFix()}><X /></Button>
    </div> : null}
    <Textarea aria-label="Ask the assistant" placeholder={fix ? "What is wrong with this answer?" : "Ask for a change"} value={text} onChange={(event) => setText(event.target.value)}
      onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); if (fix || text.trim()) send(); } }} />
    <Button size="sm" className="self-end" disabled={!fix && !text.trim()} onClick={send}>Suggest</Button>
  </div>;
}

function Card({ draft, proposal, help }: { draft: AgentDraft; proposal: AgentDraftProposal; help: AgentSettingHelp[] }) {
  const client = useQueryClient();
  const decide = useMutation({ mutationFn: (accept: boolean) => mutations.decideProposal({ id: draft.id, proposalId: proposal.id, accept }), onSuccess: () => client.invalidateQueries({ queryKey: ["agentDraft", draft.id] }) });
  return <article className="flex flex-col rounded-lg border">
    <p className="flex items-start gap-1.5 border-b px-3 py-2"><Sparkles className="mt-0.5 size-3 shrink-0 text-violet-600" />{proposal.message}</p>
    <ul className="flex flex-col divide-y">
      {proposal.settings.map((change) => <Change key={`${change.variant}/${change.key}`} draft={draft} change={change} setting={help.find(({ key }) => key === change.key)} title={variantTitle(draft, change.variant)} />)}
      {proposal.bodies.map((replacement) => <li key={replacement.variant} className="flex flex-col gap-1 px-3 py-1.5">
        <span className="font-medium">Instructions · {variantTitle(draft, replacement.variant)}</span>
        <pre className="max-h-32 overflow-y-auto rounded bg-emerald-50 px-2 py-1 font-mono text-[11px] whitespace-pre-wrap dark:bg-emerald-950/40">{replacement.body}</pre>
        <span className="text-muted-foreground">{replacement.reason}</span>
      </li>)}
    </ul>
    <div className="flex items-center gap-2 border-t px-3 py-2">
      {proposal.state === "open" ? <>
        <Button size="sm" disabled={decide.isPending} onClick={() => decide.mutate(true)}><Check />Accept</Button>
        <Button size="sm" variant="outline" disabled={decide.isPending} onClick={() => decide.mutate(false)}>Reject</Button>
      </> : <span className="flex items-center gap-1 text-muted-foreground"><Check className="size-3" />Accepted</span>}
      {decide.error ? <span role="alert" className="text-destructive">{decide.error.message}</span> : null}
    </div>
  </article>;
}

// A setting change, old and new values, and why; one that changes what the agent may do says so.
function Change({ draft, change, setting, title }: { draft: AgentDraft; change: AgentDraftProposal["settings"][number]; setting?: AgentSettingHelp; title: string }) {
  const old = change.key === "mode" ? draft.mode : draft.variants.find(({ name }) => name === change.variant)?.settings.find(({ key }) => key === change.key)?.value;
  const values = <span className="font-mono text-[11px] break-words whitespace-pre-wrap"><span className="text-muted-foreground line-through">{old ?? `default: ${setting?.defaultText ?? ""}`}</span> → {change.valueYaml || "default"}</span>;
  return <li className="flex flex-col gap-0.5 px-3 py-1.5">
    <span className="font-medium">{setting?.label ?? change.key}{change.key === "mode" ? " · whole draft" : ` · ${title}`}</span>
    {accessGroups.includes(setting?.group ?? "") ? <p className="flex flex-col gap-0.5 rounded bg-amber-50 px-2 py-1 text-amber-950 dark:bg-amber-950/40 dark:text-amber-100">
      <span className="flex items-center gap-1 font-medium"><ShieldAlert className="size-3" />Changes what this agent may do</span>{values}
    </p> : values}
    <span className="text-muted-foreground">{change.reason}</span>
  </li>;
}
