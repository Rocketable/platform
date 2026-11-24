import { useEffect, useId, useMemo, useRef, useState, type RefObject } from "react";
import { keepPreviousData, useMutation, useMutationState, useQuery, useQueryClient, type MutationState } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, LoaderCircle, ScrollText, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { mutations, queries } from "./api";
import type { TreeNode } from "./types";

export const floorReason = "Revert can't go back past a message from a routine run, External MCP, or Slack";

type TreeRow = { node: TreeNode; point: boolean; connectors: string };
type Deletion = Awaited<ReturnType<typeof mutations.deleteBranch>>;

const plainText = (text: string) => text.replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/\*\*|~~|`/g, "").replace(/\*(\S(?:[^*]*\S)?)\*/g, "$1").replace(/(^|\s)#{1,6}\s+/g, "$1");

function treeRows(nodes: TreeNode[], folded: Set<string>, mine: boolean) {
  const children = Map.groupBy(nodes.toSorted((a, b) => Number(b.onPath) - Number(a.onPath)), (node) => node.parentId);
  const shown = (parent: string): TreeNode[] => {
    const kids = children.get(parent) ?? [];
    return (kids.length > 1 && folded.has(parent) ? kids.filter((kid) => kid.onPath) : kids).flatMap((kid) => !mine || kid.role === "user" ? [kid] : shown(kid.id));
  };
  const place = (parent: string, guide: string) => shown(parent).map((node, index, all) => ({ node, guide, mark: all.length < 2 ? "" : index < all.length - 1 ? "├" : "└" })).reverse();
  const rows: TreeRow[] = [];
  const pending = place("", "");
  for (let next = pending.pop(); next; next = pending.pop()) {
    const { node, guide, mark } = next;
    rows.push({ node: { ...node, text: plainText(node.text) }, point: (children.get(node.id)?.length ?? 0) > 1, connectors: `${guide}${mark}`.slice(-6) });
    pending.push(...place(node.id, guide + (mark && (mark === "├" ? "│" : " "))));
  }
  return rows;
}

function deletionLines({ messages, nestedBranches, subagentSessions, queuedMessages, current }: Deletion) {
  const count = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
  return [count(messages, "message"), count(nestedBranches, "nested branch", "nested branches"), count(subagentSessions, "subagent session"),
    ...current ? [`It is the current branch, so the chat moves back to where it splits off, and ${count(queuedMessages, "queued message")} will run from there.`] : []];
}

type Summary = MutationState<{ appended: boolean }, Error, { node: TreeNode; instructions: string }>;

function useBranchSummaries(id: string, refresh: () => Promise<unknown>) {
  const client = useQueryClient();
  const mutationKey = ["summarizeBranch", id];
  const summary = useMutation({ mutationKey, mutationFn: ({ node, instructions }: { node: TreeNode; instructions: string }) => mutations.summarizeBranch({ id, branchId: node.branchId, instructions }),
    onSuccess: async ({ appended }) => { if (appended) await Promise.all([client.invalidateQueries({ queryKey: ["messageTree"] }), refresh()]); } });
  const runs = useMutationState({ filters: { mutationKey }, select: (mutation) => mutation.state as Summary });
  const latest = runs.at(-1);
  return {
    pending: new Set(runs.filter((run) => run.status === "pending").map((run) => run.variables!.node.branchId)),
    notice: latest?.status === "success" && !latest.data!.appended ? `Nothing was added: the current path already holds the branch that starts at “${latest.variables!.node.text}”.` : "",
    error: latest?.status === "error" ? latest.error!.message : "",
    summarize: (node: TreeNode, instructions: string) => summary.mutate({ node, instructions }),
  };
}

function useMessageTree(id: string, revision?: string) {
  const tree = useQuery({ ...queries.messageTree({ id }, revision), placeholderData: keepPreviousData, retry: false });
  const [mine, setMine] = useState(false);
  const [folded, setFolded] = useState(() => new Set<string>());
  const [deleting, setDeleting] = useState<TreeNode>();
  const [summarizing, setSummarizing] = useState<TreeNode>();
  const [failure, setFailure] = useState("");
  const rows = useMemo(() => treeRows(tree.data ?? [], folded, mine), [tree.data, folded, mine]);
  const fold = (node: string) => setFolded((current) => current.symmetricDifference(new Set([node])));
  const current = useRef<HTMLButtonElement>(null);
  const loaded = tree.isSuccess;
  useEffect(() => {
    if (!loaded) return;
    current.current?.focus({ preventScroll: true });
    current.current?.scrollIntoView({ block: "center" });
  }, [loaded]);
  return { tree, rows, mine, setMine, folded, fold, deleting, setDeleting, summarizing, setSummarizing, failure, setFailure, current };
}

function SummarizeButton({ node, summarizing, summarize }: { node: TreeNode; summarizing: boolean; summarize: () => void }) {
  if (!node.branchId || node.onPath) return null;
  return <Button variant="ghost" size="icon-xs" className="shrink-0" aria-label="Summarize branch" title={summarizing ? "Summarizing branch…" : "Summarize branch into the current path"} disabled={summarizing} onClick={summarize}>
    {summarizing ? <LoaderCircle className="animate-spin" /> : <ScrollText />}
  </Button>;
}

function Connectors({ marks }: { marks: string }) {
  return <span aria-hidden="true" data-connectors={marks} className="flex shrink-0 self-stretch text-muted-foreground/70">
    {[...marks].map((mark, index) => <span key={marks.slice(0, index + 1)} className={cn("relative w-4", mark !== " " && "before:absolute before:left-3 before:border-l", mark === "└" ? "before:top-0 before:h-1/2" : "before:inset-y-0",
      "├└".includes(mark) && "after:absolute after:top-1/2 after:right-0 after:left-3 after:border-t")} />)}
  </span>;
}

function TreeLine({ row: { node, point, connectors }, folded, fold, moving, pick, remove, summarize, summarizing, current }: { row: TreeRow; folded: boolean; fold: () => void; moving: boolean; pick: () => void; remove: () => void; summarize: () => void; summarizing: boolean; current: RefObject<HTMLButtonElement | null> }) {
  const speaker = node.outside ? "Outside" : node.role === "user" ? "You" : "Agent";
  const pickable = node.pickable && !moving;
  return <li className="flex min-w-0 items-center gap-1">
    <span className="flex shrink-0 items-center self-stretch">
      <Connectors marks={connectors} />
      {point ? <Button variant="ghost" size="icon-xs" className="shrink-0" aria-label="Other branches" aria-expanded={!folded} onClick={fold}>{folded ? <ChevronRight /> : <ChevronDown />}</Button>
        : <span className={cn("relative size-6 shrink-0 text-muted-foreground/70", /[├└]$/.test(connectors) && "after:absolute after:inset-x-0 after:top-1/2 after:border-t")} />}
    </span>
    <button type="button" ref={node.current ? current : undefined} aria-current={node.current || undefined} aria-disabled={!pickable || undefined} title={node.pickable ? undefined : floorReason}
      className={cn("flex min-h-8 min-w-0 flex-1 items-baseline gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-accent aria-disabled:cursor-not-allowed aria-disabled:opacity-60 aria-[current]:bg-accent aria-[current]:font-medium", !node.onPath && "text-muted-foreground")}
      onClick={() => { if (pickable) pick(); }}>
      <span className="shrink-0 text-xs text-muted-foreground">{speaker}</span>{" "}<span className="min-w-0 truncate">{node.text}</span>
    </button>
    <SummarizeButton node={node} summarizing={summarizing} summarize={summarize} />
    {node.deletable ? <Button variant="ghost" size="icon-xs" className="shrink-0" aria-label="Delete branch" title="Delete branch" onClick={remove}><Trash2 /></Button> : null}
  </li>;
}

function DeleteBranchDialog({ id, node, close, refresh }: { id: string; node: TreeNode; close: () => void; refresh: () => Promise<unknown> }) {
  const client = useQueryClient();
  const counts = useQuery({ queryKey: ["deleteBranch", id, node.branchId], queryFn: () => mutations.deleteBranch({ id, messageId: node.branchId, dryRun: true }), gcTime: 0, retry: false });
  const remove = useMutation({ mutationFn: () => mutations.deleteBranch({ id, messageId: node.branchId }), onSuccess: async () => {
    close();
    await Promise.all([client.invalidateQueries({ queryKey: ["messageTree"] }), client.invalidateQueries({ queryKey: ["queue"] }), refresh()]);
  } });
  const error = counts.error ?? remove.error;
  return <Dialog open onOpenChange={(open) => { if (!open && !remove.isPending) close(); }}>
    <DialogContent>
      <DialogTitle>Delete branch</DialogTitle>
      <DialogDescription>Permanently delete the branch that starts at “{node.text}” and everything that continues from it?</DialogDescription>
      {counts.data ? <ul className="list-disc pl-5 text-sm">{deletionLines(counts.data).map((line) => <li key={line}>{line}</li>)}</ul> : null}
      {counts.isPending ? <p role="status" className="text-sm text-muted-foreground">Counting what the branch holds…</p> : null}
      {error ? <p role="alert" className="text-sm text-destructive">{error.message}</p> : null}
      <div className="mt-6 flex justify-end gap-2">
        <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
        <Button disabled={!counts.data || remove.isPending} onClick={() => remove.mutate()}>{remove.isPending ? "Deleting…" : "Delete branch"}</Button>
      </div>
    </DialogContent>
  </Dialog>;
}

function SummarizeBranchDialog({ node, close, start }: { node: TreeNode; close: () => void; start: (instructions: string) => void }) {
  const [instructions, setInstructions] = useState("");
  const field = useId();
  return <Dialog open onOpenChange={(open) => { if (!open) close(); }}>
    <DialogContent>
      <DialogTitle>Summarize branch</DialogTitle>
      <DialogDescription>Add a summary of the branch that starts at “{node.text}” to the current path. The agent sees it from its next turn.</DialogDescription>
      <Label htmlFor={field}>Instructions (optional)</Label>
      <Textarea id={field} value={instructions} onChange={(event) => setInstructions(event.target.value)} />
      <div className="mt-6 flex justify-end gap-2">
        <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
        <Button onClick={() => { start(instructions.trim()); close(); }}>Summarize</Button>
      </div>
    </DialogContent>
  </Dialog>;
}

export function MessageTree({ id, revision, moving, pick, refresh, close }: { id: string; revision?: string; moving: boolean; pick: (node: TreeNode) => Promise<string>; refresh: () => Promise<unknown>; close: () => void }) {
  const { tree, rows, mine, setMine, folded, fold, deleting, setDeleting, summarizing, setSummarizing, failure, setFailure, current } = useMessageTree(id, revision);
  const { notice, pending, error, summarize } = useBranchSummaries(id, refresh);
  const label = useId();
  return <>
    <header className="flex shrink-0 items-center gap-2 border-b p-2 text-xs lg:pr-12">
      <span className="min-w-0 flex-1 truncate font-medium">Message tree</span>
      <span id={label}>Your messages only</span>
      <Switch aria-labelledby={label} checked={mine} onCheckedChange={setMine} />
      <Button variant="ghost" size="icon-sm" aria-label="Close message tree" onClick={close}><X /></Button>
    </header>
    <ScrollArea className="min-h-0 flex-1"><div className="p-2">
      {tree.isPending ? <p role="status" className="text-sm text-muted-foreground">Loading message tree…</p> : null}
      {[tree.error?.message, failure, error].filter(Boolean).map((message) => <p key={message} role="alert" className="text-sm text-destructive">{message}</p>)}
      {tree.data?.length === 0 ? <p role="status" className="text-sm text-muted-foreground">No messages yet</p> : null}
      {notice ? <p role="status" className="text-sm text-muted-foreground">{notice}</p> : null}
      <ul aria-label="Messages and replies" className="flex flex-col">
        {rows.map((row) => <TreeLine key={row.node.id} row={row} folded={folded.has(row.node.id)} fold={() => fold(row.node.id)} moving={moving} pick={() => void pick(row.node).then(setFailure)} remove={() => setDeleting(row.node)}
          summarize={() => setSummarizing(row.node)} summarizing={pending.has(row.node.branchId)} current={current} />)}
      </ul>
    </div></ScrollArea>
    {deleting ? <DeleteBranchDialog id={id} node={deleting} close={() => setDeleting(undefined)} refresh={refresh} /> : null}
    {summarizing ? <SummarizeBranchDialog node={summarizing} close={() => setSummarizing(undefined)} start={(instructions) => summarize(summarizing, instructions)} /> : null}
  </>;
}
