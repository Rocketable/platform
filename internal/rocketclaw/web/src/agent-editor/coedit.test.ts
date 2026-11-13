import { expect, test } from "bun:test";
import { applySplices, CoEdit, transform, type Splice } from "./coedit";
import type { AgentDraft, AgentDraftOp } from "@/types";

type Vector = { name: string; text: string; a: Splice[]; b: Splice[]; aFirst: boolean; aPastB: Splice[]; bPastA: Splice[]; result: string };
const vectors: { cases: Vector[] } = await Bun.file(new URL("../../../backend/testdata/body_transform.json", import.meta.url)).json();

test.each(vectors.cases.map((vector) => [vector.name, vector] as const))("%s gives the text Go gives", (_, vector) => {
  expect(transform(vector.a, vector.b, vector.aFirst)).toEqual([vector.aPastB, vector.bPastA]);
  expect(applySplices(applySplices(vector.text, vector.b), vector.aPastB)).toBe(vector.result);
  expect(applySplices(applySplices(vector.text, vector.a), vector.bPastA)).toBe(vector.result);
});

// A server like the real one for one variant: it moves an op past those committed after its base, setting last write
// wins, and parses a file of "key: value" lines, diffing its instructions against the base's.
class Server {
  log: (AgentDraftOp & { body: string; settings: Record<string, string> })[] = [{ revision: "1", baseRevision: "0", opId: "", kind: "create", body: "", settings: {} }];
  constructor(body: string) { this.log[0].body = body; }
  get state() { return this.log.at(-1)!; }
  commit(op: AgentDraftOp) {
    const base = this.log[Number(op.baseRevision) - 1];
    let ops = [op];
    if (op.kind === "edit_file") {
      const [, head, body] = /^---\n([\s\S]*?)\n---\n([\s\S]*)$/.exec(op.value!)!;
      if (head.includes("[")) return { revision: "0", invalidLine: 2 };
      const set = Object.entries(Object.fromEntries(head.split("\n").map((line) => line.split(": ") as [string, string]))).filter(([key, value]) => base.settings[key] !== value);
      const [from, to] = [[...base.body], [...body]];
      let p = 0, s = 0;
      while (p < from.length && from[p] === to[p]) p++;
      while (s < from.length - p && from.at(-1 - s) === to.at(-1 - s)) s++;
      ops = [...set.map(([key, value]) => ({ ...op, kind: "set_field", key, value })), ...(from.join("") === body ? [] : [{ ...op, kind: "edit_body", splices: [{ at: p, remove: from.length - p - s, insert: to.slice(p, to.length - s).join("") }] }])];
    }
    for (const change of ops) {
      let splices = change.splices ?? [];
      for (const later of this.log.slice(Number(op.baseRevision))) if (later.kind === "edit_body") [splices] = transform(splices, later.splices!, false);
      const body = change.kind === "edit_body" ? applySplices(this.state.body, splices) : this.state.body;
      const settings = change.kind === "set_field" ? { ...this.state.settings, [change.key!]: change.value! } : this.state.settings;
      this.log.push({ ...change, revision: String(this.log.length + 1), splices, body, settings });
    }
    return { revision: String(this.log.length), invalidLine: 0 };
  }
  view(since: number): { draft: AgentDraft; ops: AgentDraftOp[] } {
    const { body, settings } = this.state;
    return { draft: { id: "d", revision: String(this.log.length), name: "triage", mode: "advanced", variants: [{ name: "a", settings: Object.entries(settings).map(([key, value]) => ({ key, value })), body }], testChat: "", saveState: "editing", existing: false }, ops: this.log.slice(since) };
  }
}

// An editor whose requests reach the server only when the test delivers them, and whose reads happen when it says.
function editor(server: Server, name: string) {
  const requests: { op: AgentDraftOp; answer: (reply: { revision: string; invalidLine: number }) => Promise<void>; deliver: () => Promise<void> }[] = [];
  let sequence = 0;
  const coedit = new CoEdit({
    send: (sent) => new Promise((resolve) => {
      const op = structuredClone(sent), answer = async (reply: { revision: string; invalidLine: number }) => { resolve(reply); await Bun.sleep(0); };
      requests.push({ op, answer, deliver: () => answer(server.commit(op)) });
    }),
    fetch: () => {},
    present: () => {},
    changed: () => {},
  }, () => `${name}-${++sequence}`);
  const read = () => coedit.receive(server.view(coedit.rev));
  read();
  return { coedit, requests, read, body: () => coedit.view!.variants[0].body, setting: (key: string) => coedit.view!.variants[0].settings.find((setting) => setting.key === key)?.value };
}

test("an insert typed inside a teammate's in-flight delete survives in every view", async () => {
  const server = new Server("XY"), alice = editor(server, "alice"), bob = editor(server, "bob");
  bob.coedit.editBody("a", [{ at: 0, remove: 2 }]);
  bob.coedit.flush();
  alice.coedit.editBody("a", [{ at: 1, insert: "a" }]);
  alice.coedit.flush();
  await alice.requests[0].deliver();
  bob.read();
  expect(bob.body()).toBe("a");
  await bob.requests[0].deliver();
  bob.read();
  alice.read();
  expect([server.state.body, alice.body(), bob.body()]).toEqual(["a", "a", "a"]);
});

test("a File submit waits for the form op in flight, and the form's insert lands once", async () => {
  const server = new Server("XY"), alice = editor(server, "alice");
  alice.coedit.editBody("a", [{ at: 0, insert: "a" }]);
  alice.coedit.flush();
  const filed = alice.coedit.submitFile("a", alice.coedit.rev, "---\nmodel: gpt\n---\nXY");
  expect(alice.requests.map((request) => request.op.kind)).toEqual(["edit_body"]);
  await alice.requests[0].deliver();
  alice.read();
  expect(alice.requests.map((request) => request.op.kind)).toEqual(["edit_body", "edit_file"]);
  await alice.requests[1].deliver();
  alice.read();
  expect(await filed).toBe(0);
  expect([server.state.body, alice.body(), alice.setting("model")]).toEqual(["aXY", "aXY", "gpt"]);
});

test("a File text that is not valid yet changes nothing and is answered with its line", async () => {
  const server = new Server("XY"), alice = editor(server, "alice");
  const filed = alice.coedit.submitFile("a", alice.coedit.rev, "---\nmodel: [\n---\nXY");
  await alice.requests[0].deliver();
  expect(await filed).toBe(2);
  expect(server.log).toHaveLength(1);
});

test("a setting written while another write to it is pending shows the server's last write", async () => {
  const server = new Server(""), alice = editor(server, "alice"), bob = editor(server, "bob");
  bob.coedit.setField("a", "model", "bob-model");
  bob.coedit.flush();
  alice.coedit.setField("a", "model", "alice-model");
  alice.coedit.flush();
  await alice.requests[0].deliver();
  bob.read();
  expect(bob.setting("model")).toBe("bob-model");
  await bob.requests[0].deliver();
  bob.read();
  alice.read();
  expect([server.state.settings.model, alice.setting("model"), bob.setting("model")]).toEqual(["bob-model", "bob-model", "bob-model"]);
});

test("the editor's own op among the fetched ones is its ack and applies once", async () => {
  const server = new Server("XY"), alice = editor(server, "alice");
  alice.coedit.editBody("a", [{ at: 0, insert: "hi" }]);
  alice.coedit.flush();
  alice.coedit.editBody("a", [{ at: 4, insert: "!" }]);
  const reply = server.commit(alice.requests[0].op);
  alice.read();
  expect(alice.body()).toBe("hiXY!");
  await alice.requests[0].answer(reply);
  expect(alice.requests.map((request) => request.op.splices)).toEqual([[{ at: 0, insert: "hi" }], [{ at: 4, insert: "!" }]]);
  await alice.requests[1].deliver();
  alice.read();
  expect([server.state.body, alice.body()]).toEqual(["hiXY!", "hiXY!"]);
});
