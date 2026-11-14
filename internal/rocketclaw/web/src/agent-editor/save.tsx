import { useState, type ReactNode } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, GitPullRequest, Save, X } from "lucide-react";
import { mutations, queries } from "@/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { AgentDraft, AgentDraftOpInput, AgentDraftPending, AgentDraftProblem, AgentDraftReview, DraftLayers, SaveDraftResponse } from "@/types";
import { sentence, variantTitle } from "./chat";
import { opId, type CoEdit } from "./coedit";
import { FileDiff, Review } from "./review";

// A save refused for settings that do not load, which the Form shows by their fields.
export type Refusal = { refused: string; problems: AgentDraftProblem[] };
type Method = "push" | "pull";
type Choice = { variant: string; destination: string; method?: Method };
// A save's answer; who is the person whose save was running when this one was refused.
type Outcome = SaveDraftResponse & { who?: string };

const title = (layer: string) => layer === "workspace" ? "Workspace" : layer === "built-in" ? "Built-in" : layer;

function PendingLink({ pending }: { pending: AgentDraftPending }) {
  const text = pending.number ? `Pull request #${pending.number}` : pending.branch === pending.base ? `Commit ${pending.commit.slice(0, 12)} on ${pending.base}` : `Branch ${pending.branch}`;
  return pending.link ? <a href={pending.link} target="_blank" rel="noreferrer" className="underline underline-offset-2">{text}</a> : <span className="font-mono">{text}</span>;
}

// The draft stopped waiting for a pull request, because gh saw it closed, by Stop waiting or by an edit, when its last
// save state change ended a wait.
function stopped(coedit: CoEdit, draft: AgentDraft) {
  const [before, last] = coedit.log.filter((op) => op.kind === "set_save_state").slice(-2).map((op) => op.value);
  return draft.saveState === "editing" && draft.pending?.branch !== draft.pending?.base && before === "waiting" && last === "editing";
}

// The header's part of saving: the pull request the draft waits for with Stop waiting, the one it stopped waiting
// for, and Save.
export function SaveHeader({ coedit, draft, onRefusal }: { coedit: CoEdit; draft: AgentDraft; onRefusal: (refusal?: Refusal) => void }) {
  const [open, setOpen] = useState(false);
  const client = useQueryClient();
  const stop = useMutation({ mutationFn: () => mutations.stopWaiting({ id: draft.id }), onSuccess: () => client.invalidateQueries({ queryKey: ["agentDraft", draft.id] }) });
  const { pending } = draft;
  return <>
    {pending && draft.saveState === "waiting" ? <span className="flex items-center gap-2 text-xs"><PendingLink pending={pending} />
      <Button size="xs" variant="outline" disabled={stop.isPending} onClick={() => stop.mutate()}>Stop waiting</Button>
      {stop.error ? <span role="alert" className="text-destructive">{stop.error.message}</span> : null}
    </span> : null}
    {pending && stopped(coedit, draft) ? <span className="text-xs text-muted-foreground">{pending.ended === "closed" ? "The pull request was closed on GitHub." : <><PendingLink pending={pending} /> is still open on GitHub.</>}</span> : null}
    <Button size="sm" onClick={() => { setOpen(true); onRefusal(); }}><Save />Save…</Button>
    <Dialog open={open} onOpenChange={setOpen}>
      {open ? <SaveDialog coedit={coedit} draft={draft} onClose={() => setOpen(false)} onRefused={(refusal) => { setOpen(false); onRefusal(refusal); }} /> : null}
    </Dialog>
  </>;
}

// Under the header: that the draft waits for its merge, and the last save's settings that do not load.
export function SaveNotice({ draft, refusal, onDismiss }: { draft: AgentDraft; refusal?: Refusal; onDismiss: () => void }) {
  const help = useQuery(queries.agentSettings(draft.mode)).data ?? [];
  return <>
    {draft.saveState === "waiting" ? <p className="flex items-start gap-2 border-b bg-amber-50/60 px-4 py-2 text-xs dark:bg-amber-950/30"><GitPullRequest className="mt-px size-3.5 shrink-0" />
      Once the pull request merges, RocketClaw reloads and {draft.name} goes live. Your next edit stops waiting, and the pull request stays open on GitHub.</p> : null}
    {refusal ? <div role="alert" className="flex items-start gap-2 border-b px-4 py-2 text-xs text-destructive">
      <p className="flex-1">Not saved. {refusal.refused} {refusal.problems.map((problem) => sentence(problem, help)).join(" ")}</p>
      <Button size="icon-xs" variant="ghost" aria-label="Dismiss" onClick={onDismiss}><X /></Button>
    </div> : null}
  </>;
}

// Saves the choice, first committing ops on the reviewed file's revision when there is one. A refusal without
// problems or a review finds whose save is running; a failure after a push names the commit pushed.
function useSave(coedit: CoEdit, draft: AgentDraft, choice: Choice, onRefused: (refusal: Refusal) => void) {
  const client = useQueryClient(), fresh = () => client.refetchQueries({ queryKey: ["agentDraft", draft.id] });
  return useMutation({
    mutationFn: async ({ review, ops = [] }: { review?: AgentDraftReview; ops?: AgentDraftOpInput[] }): Promise<Outcome> => {
      const pushed = draft.pending?.commit;
      await Promise.all(ops.map((op) => mutations.applyAgentDraftOp({ id: draft.id, op: { ...op, baseRevision: review!.revision, opId: opId() } })));
      try {
        const answer = await mutations.saveDraft({ id: draft.id, variant: choice.variant, destination: choice.destination, pullRequest: choice.method === "pull", ...(review && { reviewed: true, reviewedFile: review.live }) });
        if (!answer.refused || answer.problems?.length || answer.review) return answer;
        await fresh();
        const last = coedit.log.findLast((op) => op.kind === "set_save_state");
        return { ...answer, who: last?.value === "saving" ? last.author : undefined };
      } catch (error) {
        await fresh();
        const pending = coedit.server?.pending;
        if (pending?.commit && pending.commit !== pushed && pending.destination === choice.destination) throw new Error(`Pushed commit ${pending.commit.slice(0, 12)} to ${pending.branch}, but RocketClaw could not reload: ${(error as Error).message}. The draft is back to editing.`, { cause: error });
        throw error;
      }
    },
    onSuccess: (answer) => { if (answer.problems?.length) onRefused({ refused: answer.refused!, problems: answer.problems }); },
    onSettled: () => client.invalidateQueries({ queryKey: ["agentDraft", draft.id] }),
  });
}

function SaveDialog({ coedit, draft, onClose, onRefused }: { coedit: CoEdit; draft: AgentDraft; onClose: () => void; onRefused: (refusal: Refusal) => void }) {
  const [choice, setChoice] = useState<Choice>({ variant: draft.variants[0].name, destination: "" });
  // The layers stay listed while another destination's answer loads; what it says about the destination waits for it.
  const query = useQuery({ ...queries.draftLayers({ id: draft.id, variant: choice.variant, destination: choice.destination || "workspace", revision: draft.revision }), placeholderData: keepPreviousData });
  const layers = query.isPlaceholderData ? undefined : query.data;
  const save = useSave(coedit, draft, choice, onRefused), review = save.data?.review;
  const done = Boolean(save.data?.saveState) && !save.isPending;
  if (review && !save.isPending) return <DialogContent className="max-h-[calc(100dvh-4rem)] overflow-y-auto sm:max-w-2xl">
    <Review key={review.revision} review={review} name={draft.name} variant={choice.variant} onSave={(ops) => save.mutate({ review, ops })} />
  </DialogContent>;
  return <DialogContent className="max-h-[calc(100dvh-4rem)] overflow-y-auto sm:max-w-3xl">
    <DialogHeader>
      <DialogTitle>Save {draft.name}</DialogTitle>
      <DialogDescription>One variant goes to one place; the other variants stay in the draft.</DialogDescription>
    </DialogHeader>
    {done ? null : <Choices draft={draft} choice={choice} layers={choice.destination ? layers : undefined} all={query.data?.layers ?? []} onChoice={setChoice} />}
    {save.isPending ? <p role="status" className="text-sm text-muted-foreground">Saving…</p> : null}
    {save.error ? <p role="alert" className="text-sm text-destructive">{save.error.message}</p> : null}
    {save.data && !save.isPending ? <Result draft={draft} outcome={save.data} hiddenBy={layers?.hiddenBy} /> : null}
    <DialogFooter>
      {done ? <Button onClick={onClose}>Done</Button> : <>
        <Button variant="outline" onClick={onClose}>Cancel</Button>
        <SaveButton choice={choice} layers={layers} busy={save.isPending} onSave={() => save.mutate({})} />
      </>}
    </DialogFooter>
  </DialogContent>;
}

function SaveButton({ choice, layers, busy, onSave }: { choice: Choice; layers?: DraftLayers; busy: boolean; onSave: () => void }) {
  const overlay = choice.destination !== "" && choice.destination !== "workspace";
  const verb = !choice.destination ? "Save" : !overlay ? "Save to workspace" : !choice.method ? "Choose push or pull request" : choice.method === "push" ? `Push to ${layers?.branch}` : "Open pull request";
  return <Button disabled={busy || !choice.destination || (overlay && !choice.method) || !layers || Boolean(layers.unpushable)} onClick={onSave}>{verb}</Button>;
}

function Result({ draft, outcome, hiddenBy }: { draft: AgentDraft; outcome: Outcome; hiddenBy?: string }) {
  const messages: Record<string, ReactNode> = {
    live: `Saved and reloaded: ${draft.name} is live.`,
    waiting: <>Waiting for merge: {draft.pending ? <PendingLink pending={draft.pending} /> : "the pull request"}. Once it merges, RocketClaw reloads and {draft.name} goes live.</>,
    editing: hiddenBy ? `Saved, but ${title(hiddenBy)} hides it, so ${draft.name} is not live. The draft is back to editing.` : "Saved and live; the draft has newer edits and is back to editing.",
  };
  if (outcome.refused) return <p role="alert" className="text-sm text-destructive">{outcome.who ? `${outcome.who} is saving this draft already.` : outcome.refused}</p>;
  return <p role="status" className="rounded-lg border p-3 text-sm">{messages[outcome.saveState!]}</p>;
}

function Choices({ draft, choice, layers, all, onChoice }: { draft: AgentDraft; choice: Choice; layers?: DraftLayers; all: DraftLayers["layers"]; onChoice: (choice: Choice) => void }) {
  const before = all.find(({ layer }) => layer === choice.destination)?.file;
  return <div className="flex flex-col gap-4">
    <Options label="1 · Which variant" options={draft.variants.map(({ name }) => [name, variantTitle(draft, name), ""])} chosen={choice.variant} name={(label) => `Save variant ${label}`} onChoose={(variant) => onChoice({ ...choice, variant })} />
    <Destinations name={draft.name} layers={all} chosen={choice.destination} onChoose={(destination) => onChoice({ ...choice, destination, method: undefined })} />
    {layers ? <Warnings name={draft.name} layers={layers} /> : null}
    {layers?.branch && choice.destination !== "workspace" ? <Options label="3 · How (asked every time)" options={[["push", `Push to ${layers.branch}`, "Goes live once pushed, with no review."], ["pull", "Open pull request", "Goes live after someone merges it."]]}
      chosen={choice.method} name={(label) => label} onChoose={(method) => onChoice({ ...choice, method: method as Method })} /> : null}
    {layers ? <div className="flex flex-col gap-1.5">
      <p className="text-xs text-muted-foreground">{before === undefined ? `Saving creates ${draft.name}.md there:` : "Saving rewrites the file there: settings go in the standard order, and comments are dropped."}</p>
      <FileDiff label="The file this save writes" before={before ?? ""} after={layers.next} />
    </div> : null}
  </div>;
}

function Options({ label, options, chosen, name, onChoose }: { label: string; options: [string, string, string][]; chosen?: string; name: (label: string) => string; onChoose: (value: string) => void }) {
  return <div role="radiogroup" aria-label={label} className="flex flex-col gap-1.5">
    <h3 className="text-xs font-medium text-muted-foreground">{label}</h3>
    <div className="flex flex-wrap gap-2">
      {options.map(([value, text, note]) => <button key={value} type="button" role="radio" aria-checked={chosen === value} aria-label={name(text)} onClick={() => onChoose(value)}
        className={cn("flex cursor-pointer flex-col items-start gap-0.5 rounded-lg border px-3 py-2 text-left text-sm hover:bg-muted/50", chosen === value && "border-foreground/40 bg-muted/60")}>
        <span className="font-medium">{text}</span>{note ? <span className="text-xs text-muted-foreground">{note}</span> : null}
      </button>)}
    </div>
  </div>;
}

// The layers from the one that wins down, each with whether it provides the agent now or holds a copy it hides.
function Destinations({ name, layers, chosen, onChoose }: { name: string; layers: DraftLayers["layers"]; chosen: string; onChoose: (layer: string) => void }) {
  const provider = layers.findLast(({ file }) => file !== undefined)?.layer;
  return <div role="radiogroup" aria-label="Where to save" className="flex flex-col gap-1.5">
    <h3 className="text-xs font-medium text-muted-foreground">2 · Where <span className="font-normal">· a higher layer wins when two hold {name}.md</span></h3>
    <div className="flex flex-col divide-y rounded-lg border">
      {[...layers].reverse().map(({ layer, ref, file }) => <button key={layer} type="button" role="radio" aria-checked={chosen === layer} aria-label={`Destination ${title(layer)}`} disabled={layer === "built-in"} onClick={() => onChoose(layer)}
        className={cn("flex cursor-pointer items-center gap-2 px-3 py-2 text-left text-sm disabled:cursor-not-allowed disabled:text-muted-foreground", chosen === layer && "bg-muted/60")}>
        <span className={cn("size-3 shrink-0 rounded-full border", chosen === layer && "border-4 border-foreground")} />
        <span className="font-medium">{title(layer)}</span>{ref ? <code className="text-[11px] text-muted-foreground">@{ref}</code> : null}
        <span className="ml-auto text-xs text-muted-foreground">{[layer === provider && "provides it now", layer !== provider && file !== undefined && "has a hidden copy", layer === "built-in" && "ships with RocketClaw, read-only"].filter(Boolean).join(" · ")}</span>
      </button>)}
    </div>
  </div>;
}

function Warnings({ name, layers }: { name: string; layers: DraftLayers }) {
  const notes = [layers.hiddenBy && `Hidden by ${title(layers.hiddenBy)}: its ${name}.md wins, so this save will not be the ${name} that runs.`,
    layers.replaces && `Replaces ${layers.replaces === "built-in" ? `the built-in ${name}` : `${name} from ${layers.replaces}`}: its future updates will no longer reach this workspace.`,
    layers.recreated && `Startup copies the built-in ${name} into workspace agents/ again, and that copy hides this one.`, layers.unpushable].filter((note): note is string => Boolean(note));
  return notes.map((note) => <p key={note} className="flex items-start gap-1.5 text-xs text-amber-800 dark:text-amber-300"><CircleAlert className="mt-px size-3.5 shrink-0" />{note}</p>);
}
