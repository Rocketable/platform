// Live co-editing of a draft. Settings are last write wins; the instructions are text changed by splices, which every
// client moves past concurrent ones exactly as the server does (backend/agent_drafts.go), so all views converge.
import type { AgentDraft, AgentDraftOp, AgentDraftSplice } from "@/types";

export type Splice = AgentDraftSplice;
type Reply = { revision: string; invalidLine?: number };
type Entry = { op: AgentDraftOp; done?: boolean; settled?: (invalidLine: number) => void };
// How a client reaches the server: send commits an op, fetch asks for what committed since the client's revision,
// present says where this editor is (its here), and changed says the client's view changed.
type Link = { send: (op: AgentDraftOp) => Promise<Reply>; fetch: () => void; present: () => void; changed: () => void };

// This page's editor, as its presence and its ops name it.
export const editor = crypto.getRandomValues(new Uint32Array(2)).join("-");
let sent = 0;
export const opId = () => `${editor}-${++sent}`;

// Positions count code points, as the server's do.
export const codePoints = (text: string) => Array.from(text).length;

export function applySplices(text: string, splices: Splice[]) {
  let points = [...text];
  for (const { at, remove = 0, insert = "" } of splices) points = [...points.slice(0, at), ...insert, ...points.slice(at + remove)];
  return points.join("");
}

// The one splice that turns from into to, which is exact for a single typed change.
export function spliceBetween(from: string, to: string): Splice[] {
  const [a, b] = [[...from], [...to]];
  let prefix = 0, suffix = 0;
  while (prefix < a.length && prefix < b.length && a[prefix] === b[prefix]) prefix++;
  while (suffix < a.length - prefix && suffix < b.length - prefix && a[a.length - 1 - suffix] === b[b.length - 1 - suffix]) suffix++;
  return from === to ? [] : [{ at: prefix, remove: a.length - prefix - suffix, insert: b.slice(prefix, b.length - suffix).join("") }];
}

// a moved past b and b moved past a, both made on the same text: inserts at one spot keep server order (aFirst: a came
// first), text typed inside a deleted passage stays where it was, a delete splits around an insert inside it, and
// overlapping deletes drop the overlap. The results are splices that each remove or insert.
export function transform(a: Splice[], b: Splice[], aFirst: boolean): [Splice[], Splice[]] {
  const primitive = (splices: Splice[]) => splices.flatMap(({ at, remove, insert }) => [...(remove ? [{ at, remove }] : []), ...(insert ? [{ at, insert }] : [])]);
  [a, b] = [primitive(a), primitive(b)];
  if (!a.length || !b.length) return [a, b];
  if (a.length > 1) {
    const [a1, b1] = transform(a.slice(0, 1), b, aFirst), [a2, b2] = transform(a.slice(1), b1, aFirst);
    return [[...a1, ...a2], b2];
  }
  if (b.length > 1) {
    const [a1, b1] = transform(a, b.slice(0, 1), aFirst), [a2, b2] = transform(a1, b.slice(1), aFirst);
    return [a2, [...b1, ...b2]];
  }
  return [past(a[0], b[0], aFirst), past(b[0], a[0], !aFirst)];
}

function past(a: Splice, b: Splice, aFirst: boolean): Splice[] {
  const [aRemove, bRemove, inserted] = [a.remove ?? 0, b.remove ?? 0, codePoints(b.insert ?? "")];
  const removedBefore = Math.max(0, Math.min(a.at, b.at + bRemove) - b.at);
  if (a.insert && b.insert) return [{ ...a, at: b.at < a.at || (b.at === a.at && !aFirst) ? a.at + inserted : a.at }];
  if (a.insert) return [{ ...a, at: a.at - removedBefore }];
  if (b.insert && b.at <= a.at) return [{ ...a, at: a.at + inserted }];
  if (b.insert && b.at < a.at + aRemove) return [{ at: a.at, remove: b.at - a.at }, { at: a.at + inserted, remove: aRemove - (b.at - a.at) }];
  if (b.insert) return [a];
  const overlap = Math.max(0, Math.min(a.at + aRemove, b.at + bRemove) - Math.max(a.at, b.at));
  return overlap === aRemove ? [] : [{ at: a.at - removedBefore, remove: aRemove - overlap }];
}

// One draft's co-editing client. The view is the server's draft at the client's revision with the client's pending
// ops applied: one in flight, then a buffer the next edits join. Each read of the server moves the pending
// instructions edits past the ops others committed since, and the client's own op among them is its ack.
export class CoEdit {
  rev = 0;
  server?: AgentDraft;
  view?: AgentDraft;
  // Every op committed since the page opened the draft, oldest first.
  log: AgentDraftOp[] = [];
  error = "";
  here = { variant: "", place: "" };
  // The last mode change committed while the page was open, and the draft before the read that brought it.
  modeChange?: { op: AgentDraftOp; before: AgentDraft };
  private pending: Entry[] = [];
  private waiting: (() => void)[] = [];
  // How to send each File-tab text not submitted yet, doing nothing while that tab's earlier submit is in flight.
  private unsent = new Set<() => void>();
  private sending = false;
  private answered?: number;
  private timer?: ReturnType<typeof setTimeout>;

  constructor(private readonly link: Link, private readonly opId: () => string) {}

  // A read that brings no change leaves the view as it is.
  receive({ draft, ops }: { draft: AgentDraft; ops: AgentDraftOp[] }) {
    const fresh = ops.filter((op) => Number(op.revision) > this.rev);
    if (Number(draft.revision) < this.rev || (!fresh.length && Number(draft.revision) === this.rev)) return;
    for (const op of fresh) {
      this.log.push(op);
      if (op.kind === "set_mode" && this.server) this.modeChange = { op, before: this.server };
      if (this.sending && op.opId === this.pending[0].op.opId) this.pending[0].done = true;
      else this.rebase(op);
    }
    [this.server, this.rev] = [draft, Number(draft.revision)];
    this.settle();
  }

  editBody(variant: string, splices: Splice[]) {
    const open = this.open("edit_body", variant);
    if (open) open.op.splices!.push(...splices);
    else if (splices.length) this.pending.push({ op: { baseRevision: "", opId: this.opId(), kind: "edit_body", variant, splices } });
    this.update();
  }

  // value is the setting's YAML, empty to remove it.
  setField(variant: string, key: string, value: string) {
    const open = this.open("set_field", variant);
    if (open?.op.key === key) open.op.value = value;
    else this.pending.push({ op: { baseRevision: "", opId: this.opId(), kind: "set_field", variant, key, value } });
    this.update();
  }

  // A File-tab edit: the whole file as made on revision base, whose settings change from the file from. It is sent
  // once every earlier op is acknowledged, and settles with 0 once its changes are in the view, with the line that is
  // not valid yet, or with -1 on failure.
  submitFile(variant: string, base: number, text: string, from: string) {
    return new Promise<number>((settled) => {
      this.pending.push({ op: { baseRevision: String(base), opId: this.opId(), kind: "edit_file", variant, value: text, fromFile: from }, settled });
      this.flush();
    });
  }

  // A refused op is dropped alone: the ops behind it are still sent.
  flush() {
    const head = this.pending[0];
    if (this.sending || !head) return;
    this.sending = true;
    if (head.op.kind !== "edit_file") head.op.baseRevision = String(this.rev);
    const drop = (settled: number) => {
      this.sending = false;
      this.pending.shift();
      head.settled?.(settled);
    };
    this.link.send(head.op).then(({ revision, invalidLine }) => {
      this.error = "";
      if (invalidLine) drop(invalidLine);
      else this.answered = Number(revision);
    }, (error: Error) => {
      this.error = error.message;
      drop(-1);
    }).then(() => {
      this.settle();
      this.link.fetch();
    });
  }

  // Sends the pending edits and the File tabs' unsent text now, and resolves once every edit pending then or since is
  // acknowledged and no tab holds unsent text.
  drained() {
    clearTimeout(this.timer);
    for (const submit of this.unsent) submit();
    this.flush();
    return new Promise<void>((resolve) => {
      this.waiting.push(resolve);
      this.release();
    });
  }

  // A File tab holds text submit sends; the returned function says it no longer does. A tab replaces its hold in one
  // render, so the waits are checked once that is done.
  holdFile(submit: () => void) {
    this.unsent.add(submit);
    if (this.waiting.length) submit();
    return () => {
      this.unsent.delete(submit);
      queueMicrotask(() => this.release());
    };
  }

  // Sends the pending edits once typing pauses for 200 ms, so typing goes as a few ops rather than one per key.
  soon() {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.flush(), 200);
  }

  // Says where this editor is: a setting key or an offset in a variant's instructions; with no arguments, again.
  present(variant = this.here.variant, place = this.here.place) {
    this.here = { variant, place };
    this.link.present();
  }

  private open(kind: string, variant: string) {
    const tail = this.pending.at(-1);
    return tail && tail.op.kind === kind && tail.op.variant === variant && !(this.sending && tail === this.pending[0]) ? tail : undefined;
  }

  // A File-tab edit keeps the variant's name on its base revision: the server renames it as it moves it.
  private rebase(op: AgentDraftOp) {
    let splices = op.splices ?? [];
    for (const { op: mine } of this.pending.filter((entry) => !entry.done && entry.op.variant === op.variant)) {
      if (op.kind === "rename_variant" && mine.kind !== "edit_file") mine.variant = op.value;
      if (op.kind === "edit_body" && mine.kind === "edit_body") [mine.splices, splices] = transform(mine.splices!, splices, false);
    }
  }

  private settle() {
    const head = this.pending[0];
    if (this.sending && this.answered !== undefined && this.rev >= this.answered) {
      [this.sending, this.answered] = [false, undefined];
      this.pending.shift();
      head.settled?.(0);
    }
    this.flush();
    this.release();
    this.update();
  }

  private release() {
    if (!this.pending.length && !this.unsent.size) for (const resolve of this.waiting.splice(0)) resolve();
  }

  private update() {
    if (!this.server) return;
    const variants = this.server.variants.map((variant) => ({ ...variant, settings: [...variant.settings] }));
    for (const { op, done } of this.pending) {
      const variant = variants.find((shown) => shown.name === op.variant);
      if (done || !variant) continue;
      if (op.kind === "edit_body") variant.body = applySplices(variant.body, op.splices!);
      if (op.kind !== "set_field") continue;
      const listed = variant.settings.some((setting) => setting.key === op.key);
      variant.settings = !op.value ? variant.settings.filter((setting) => setting.key !== op.key)
        : listed ? variant.settings.map((setting) => setting.key === op.key ? { ...setting, value: op.value! } : setting) : [...variant.settings, { key: op.key!, value: op.value }];
    }
    this.view = { ...this.server, variants };
    this.link.changed();
  }
}
