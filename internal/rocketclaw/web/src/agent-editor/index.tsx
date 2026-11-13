// The agent editor: a workbench of settings, the test chat and the assistant over one draft stored on the server.
import { useEffect, useState, type ReactNode } from "react";
import { Menu } from "@base-ui/react/menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Check, ChevronDown, Copy, FileCode2, Files, Lock, Pencil, Plus, Share2, ShieldCheck, Wand2 } from "lucide-react";
import { cn } from "@/lib/utils";
import Link, { navigate, serverURL, usePathname } from "@/navigation";
import { Button, buttonVariants } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Input } from "@/components/ui/input";
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { mutations, queries, rpc } from "@/api";
import { copyText } from "@/transcript-text";
import type { AgentDraft, AgentDraftPresence, AgentDraftView } from "@/types";

// This page's editor, as its presence and its ops name it.
const editor = crypto.getRandomValues(new Uint32Array(2)).join("-");
let sent = 0;
const opId = () => `${editor}-${++sent}`;

const saveStates: Record<AgentDraft["saveState"], string> = { editing: "Draft · saved on the server", saving: "Saving…", waiting: "Waiting for merge", live: "Live" };
const drafts = (path: string) => `/agents/drafts/${path}`;
const menuItem = "flex cursor-default items-center gap-1.5 rounded-md px-2 py-1.5 text-sm outline-none data-highlighted:bg-accent";

// Editing an agent reopens the newest draft of it still in editing, or else starts one as a copy of the agent.
export async function editAgent(name: string) {
  const { drafts: list } = await rpc<{ drafts: AgentDraft[] }>("ListAgentDrafts");
  navigate(drafts(list.find((draft) => draft.name === name && draft.saveState === "editing")?.id ?? (await mutations.createAgentDraft({ name, copy: true })).id));
}

// The editor at /agents/new and /agents/drafts/<id> is only for people without space limits, because a draft can give
// itself any permission and its test chats run real tools; the server refuses everyone else too.
export function AgentEditorPage({ show }: { show: boolean }) {
  const pathname = usePathname();
  const identity = useQuery(queries.identity());
  if (!show || !identity.data) return null;
  if (identity.data.limited) return <Refusal />;
  const id = pathname.startsWith(drafts("")) ? pathname.slice(drafts("").length) : "";
  return id ? <DraftEditor key={id} id={id} /> : <Workbench title="Create New Agent" settings={<StartPane />} />;
}

function Refusal() {
  return <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 overflow-y-auto p-4">
    <h1 className="text-lg font-semibold">Create New Agent</h1>
    <p className="flex items-start gap-2 rounded-lg border p-3 text-sm"><Lock className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      People limited to spaces cannot use the agent editor: a draft can give itself any permission, and its test chats run real tools. You can still browse agents on the read-only Agents page.</p>
    <Link href="/agents" className={buttonVariants({ variant: "outline", className: "w-fit" })}>Back to the Agents page</Link>
  </div>;
}

// A draft follows its stream, which only wakes the page, by reading what changed since the revision it has. The page
// also says it is here every 10 seconds, since the server lists editors seen in the last 30.
function useAgentDraft(id: string) {
  const client = useQueryClient();
  const view = useQuery({ queryKey: ["agentDraft", id], queryFn: ({ signal }) => rpc<AgentDraftView>("GetAgentDraft", { id, since: client.getQueryData<AgentDraftView>(["agentDraft", id])?.draft.revision ?? "0" }, signal) });
  useEffect(() => {
    const stream = new EventSource(`${serverURL()}/stream?${new URLSearchParams({ id: `draft:${id}` })}`);
    stream.onopen = stream.onmessage = () => void client.invalidateQueries({ queryKey: ["agentDraft", id] });
    const here = () => void mutations.setAgentDraftPresence({ id, presence: { client: editor, variant: "", place: "" } }).catch(() => {});
    here();
    const timer = setInterval(here, 10_000);
    return () => { stream.close(); clearInterval(timer); };
  }, [id, client]);
  return view;
}

function DraftEditor({ id }: { id: string }) {
  const view = useAgentDraft(id);
  const draft = view.data?.draft;
  if (!draft) return <p role="status" className="m-auto p-4 text-sm text-muted-foreground">{view.error?.message ?? "Loading…"}</p>;
  return <Workbench title={draft.existing ? `Edit ${draft.name}` : "Create New Agent"} draft={draft} presence={view.data!.presence} settings={<Variants draft={draft} />} />;
}

// Settings on the left, the test chat in the middle and the assistant on the right, under one header.
function Workbench({ title, draft, presence, settings }: { title: string; draft?: AgentDraft; presence?: AgentDraftPresence[]; settings: ReactNode }) {
  return <div className="flex min-h-0 w-full flex-1 flex-col">
    <header className="flex min-h-14 w-full shrink-0 flex-wrap items-center gap-2 border-b px-4 py-2">
      <Link href="/agents" aria-label="All agents" className={buttonVariants({ variant: "ghost", size: "icon-sm" })}><ArrowLeft /></Link>
      <h1 className="shrink-0 text-lg font-semibold">{title}</h1>
      {draft ? <span className="rounded-md border px-1.5 text-[11px] text-muted-foreground">{saveStates[draft.saveState]}</span> : null}
      <div className="ml-auto flex shrink-0 items-center gap-2">
        {draft ? <ModeSwitch draft={draft} /> : null}
        <DraftsMenu current={draft?.id} />
        {presence ? <Presence presence={presence} /> : null}
        {draft ? <ShareButton /> : null}
      </div>
    </header>
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto lg:grid lg:grid-cols-[minmax(340px,390px)_minmax(0,1fr)_minmax(280px,300px)] lg:overflow-hidden">
      <section aria-label="Settings" className="flex min-h-0 min-w-0 flex-col border-b lg:border-r lg:border-b-0">{settings}</section>
      <Pane label="Test chat" note="talks to the unsaved draft">Once there is a draft, talk to it here before anything is saved. Each variant gets its own column, and when you edit an existing agent its live version answers too.</Pane>
      <Pane label="Assistant" note="suggests, never saves">The assistant fills the settings and instructions from a description and suggests changes you accept or reject.</Pane>
    </div>
  </div>;
}

function Pane({ label, note, children }: { label: string; note: string; children: ReactNode }) {
  return <section aria-label={label} className="flex min-h-48 min-w-0 flex-col border-b lg:border-b-0 lg:not-last:border-r">
    <div className="flex h-11 shrink-0 items-center gap-2 border-b px-3"><h2 className="text-sm font-medium">{label}</h2><span className="text-xs text-muted-foreground">{note}</span></div>
    <p className="m-auto max-w-sm p-8 text-center text-sm text-muted-foreground">{children}</p>
  </section>;
}

// Every variant's agent file, the first being the agent's own; all share the draft's mode.
function Variants({ draft }: { draft: AgentDraft }) {
  return <div className="flex flex-col gap-1 p-4">
    <h2 className="text-sm font-medium">Variants</h2>
    {draft.variants.map((variant, i) => <p key={variant.name} className="font-mono text-xs text-muted-foreground">{i ? `${draft.name}/${variant.name}` : draft.name}.md · {draft.mode}</p>)}
  </div>;
}

// Easy or advanced is the whole draft's: switching converts every variant together.
function ModeSwitch({ draft }: { draft: AgentDraft }) {
  const client = useQueryClient();
  const setMode = useMutation({ mutationFn: (mode: string) => mutations.applyAgentDraftOp({ id: draft.id, op: { baseRevision: draft.revision, opId: opId(), kind: "set_mode", value: mode } }),
    onSuccess: () => client.invalidateQueries({ queryKey: ["agentDraft", draft.id] }) });
  return <div className="flex items-center gap-1.5">
    <ButtonGroup aria-label="Easy or advanced, for the whole draft">
      {(["easy", "advanced"] as const).map((mode) => <Button key={mode} size="sm" variant={draft.mode === mode ? "secondary" : "outline"} aria-pressed={draft.mode === mode} disabled={setMode.isPending} onClick={() => draft.mode === mode || setMode.mutate(mode)}>{mode === "easy" ? "Easy" : "Advanced"}</Button>)}
    </ButtonGroup>
    <span className="text-xs text-muted-foreground">{draft.variants.length > 1 ? `all ${draft.variants.length} variants` : "whole draft"}</span>
    {setMode.error ? <span role="alert" className="text-xs text-destructive">{setMode.error.message}</span> : null}
  </div>;
}

function DraftsMenu({ current }: { current?: string }) {
  const [open, setOpen] = useState(false);
  const list = useQuery({ ...queries.agentDrafts(), enabled: open });
  return <DropdownMenu open={open} onOpenChange={setOpen}>
    <DropdownMenuTrigger render={<Button size="sm" variant="ghost" />}><Files />Drafts<ChevronDown /></DropdownMenuTrigger>
    <DropdownMenuContent align="end" className="w-80">
      <p className="px-2 py-1 text-xs text-muted-foreground">Drafts are stored on the server. Reopen them from any device.</p>
      {list.data?.drafts.map((draft) => <Menu.Item key={draft.id} onClick={() => navigate(drafts(draft.id))} className={cn(menuItem, "flex-col items-start gap-0", draft.id === current && "bg-muted")}>
        <span className="font-mono">{draft.name}.md</span>
        <span className="text-xs text-muted-foreground">{saveStates[draft.saveState]} · {draft.mode} · {draft.variants.length} {draft.variants.length > 1 ? "variants" : "variant"}</span>
      </Menu.Item>)}
      <div className="my-1 h-px bg-border" />
      <Menu.Item onClick={() => navigate("/agents/new")} className={menuItem}><Plus className="size-4" />Start a new agent</Menu.Item>
    </DropdownMenuContent>
  </DropdownMenu>;
}

function Presence({ presence }: { presence: AgentDraftPresence[] }) {
  return <div className="flex items-center -space-x-1">
    {[...new Set(presence.map((place) => place.username!))].map((name) => <span key={name} role="img" aria-label={`${name} is here`} title={name} className="inline-flex size-6 items-center justify-center rounded-full bg-muted text-[10px] font-semibold uppercase ring-2 ring-background">{name.slice(0, 2)}</span>)}
  </div>;
}

// The page's address is the draft's link: teammates without space limits open the same draft.
function ShareButton() {
  const [copied, setCopied] = useState<boolean>();
  return <Button size="sm" variant="outline" title="Copy this draft's link for a teammate" onClick={() => void copyText(location.href, document.body).then(() => setCopied(true), () => setCopied(false))}>
    {copied ? <Check /> : <Share2 />}{copied === undefined ? "Share" : copied ? "Link copied" : "Copy the address instead"}
  </Button>;
}

type Origin = "describe" | "template" | "copy" | "blank";
const namePlaceholders: Record<Origin, string> = { describe: "my-agent", blank: "my-agent", template: "Empty uses the starter's name", copy: "Empty adds -copy to the agent's name" };
const origins: [Origin, ReactNode, string, string][] = [["describe", <Wand2 />, "Describe it", "Tell the assistant what it should do"], ["template", <FileCode2 />, "Template", "A ready-made starter"], ["copy", <Copy />, "Copy an agent", "Start from an existing one, or edit it"], ["blank", <Pencil />, "Blank", "You fill in every setting"]];

function Choice({ on, onClick, icon, title, note }: { on: boolean; onClick: () => void; icon?: ReactNode; title: string; note: string }) {
  return <button type="button" role="radio" aria-checked={on} aria-label={title} onClick={onClick} className={cn("flex cursor-pointer flex-col items-start gap-1 rounded-lg border p-3 text-left transition-colors hover:bg-muted/50", on && "border-foreground/40 bg-muted/60")}>
    <span className="flex items-center gap-1.5 text-sm font-medium [&_svg]:size-4">{icon}{title}</span>
    <span className="text-xs leading-snug text-muted-foreground">{note}</span>
  </button>;
}

// The start pane asks easy or advanced first, then how to start.
function StartPane() {
  const [mode, setMode] = useState("");
  const [origin, setOrigin] = useState<Origin>();
  return <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto p-4">
    <p className="flex items-start gap-1.5 text-xs leading-relaxed text-muted-foreground"><ShieldCheck className="mt-0.5 size-3.5 shrink-0" />You have no space limits, so you can create and edit agents. People limited to a space keep the read-only Agents page.</p>
    <div role="radiogroup" aria-label="Easy agent or advanced agent?" className="flex flex-col gap-2">
      <h2 className="text-sm font-medium">1 · Easy agent or advanced agent?</h2>
      <div className="grid grid-cols-2 gap-2">
        <Choice on={mode === "easy"} onClick={() => setMode("easy")} title="Easy agent" note="Guided settings: apps, skills, folder and reviewer rules, with safe defaults for the rest." />
        <Choice on={mode === "advanced"} onClick={() => setMode("advanced")} title="Advanced agent" note="Every setting, including permission rules, shell and sudo." />
      </div>
    </div>
    {mode ? <div role="radiogroup" aria-label="How do you want to start?" className="flex flex-col gap-2">
      <h2 className="text-sm font-medium">2 · How do you want to start?</h2>
      <div className="grid grid-cols-2 gap-2">{origins.map(([value, icon, title, note]) => <Choice key={value} on={origin === value} onClick={() => setOrigin(value)} icon={icon} title={title} note={note} />)}</div>
      {origin ? <StartForm key={origin} mode={mode} origin={origin} /> : null}
    </div> : null}
  </div>;
}

// A new draft needs its agent's name. A copy starts as the agent it copies and is then renamed, so the original stays.
async function startDraft({ name, mode, template, copy }: { name: string; mode: string; template?: string; copy?: string }) {
  if (!copy) return (await mutations.createAgentDraft({ name, mode, template })).id;
  const draft = await mutations.createAgentDraft({ name: copy, copy: true });
  await mutations.applyAgentDraftOp({ id: draft.id, op: { baseRevision: draft.revision, opId: opId(), kind: "rename", value: name } });
  return draft.id;
}

function StartForm({ mode, origin }: { mode: string; origin: Origin }) {
  const [name, setName] = useState("");
  const client = useQueryClient(), listed = () => client.invalidateQueries({ queryKey: ["agentDrafts"] });
  const start = useMutation({ mutationFn: startDraft, onSuccess: (id) => { navigate(drafts(id)); return listed(); } });
  const edit = useMutation({ mutationFn: editAgent, onSuccess: listed });
  const blank = origin === "describe" || origin === "blank";
  return <div className="flex flex-col gap-3 pt-2">
    {origin === "describe" ? <p className="text-xs text-muted-foreground">Name the agent, then describe it to the assistant on the right. It fills the settings and instructions as a suggestion you accept or reject.</p> : null}
    <label className="flex flex-col gap-1 text-xs font-medium">Agent name
      <Input value={name} onChange={(event) => setName(event.target.value)} placeholder={namePlaceholders[origin]} />
    </label>
    {blank ? <Button className="w-fit" variant="outline" disabled={!name || start.isPending} onClick={() => start.mutate({ name, mode })}>{origin === "blank" ? "Start blank" : "Start and describe"}</Button> : null}
    {origin === "template" ? <TemplateList onUse={(template) => start.mutate({ name: name || template, mode, template })} /> : null}
    {origin === "copy" ? <AgentList onCopy={(agent) => start.mutate({ name: name || `${agent}-copy`, mode, copy: agent })} onEdit={edit.mutate} /> : null}
    {start.error || edit.error ? <p role="alert" className="text-xs text-destructive">{(start.error ?? edit.error)!.message}</p> : null}
  </div>;
}

function TemplateList({ onUse }: { onUse: (template: string) => void }) {
  const templates = useQuery(queries.agentDrafts()).data?.templates;
  return <div className="flex flex-col gap-1">
    <p className="text-xs text-muted-foreground">A starter brings its own settings, instructions and mode; change any of them once the draft opens.</p>
    <div className="flex flex-col divide-y rounded-lg border">
      {templates?.map((template) => <div key={template} className="flex items-center gap-2 px-3 py-2"><span className="font-mono text-sm">{template}</span><Button size="sm" variant="outline" className="ml-auto" aria-label={`Use template ${template}`} onClick={() => onUse(template)}>Use</Button></div>)}
    </div>
  </div>;
}

function AgentList({ onCopy, onEdit }: { onCopy: (agent: string) => void; onEdit: (agent: string) => void }) {
  const agents = useQuery(queries.agents()).data?.agents;
  return <div className="flex flex-col divide-y rounded-lg border">
    {agents?.map((agent) => <div key={agent.name} className="flex items-center gap-2 px-3 py-2">
      <span className="min-w-0 truncate font-mono text-sm">{agent.name}</span>
      <Button size="sm" variant="ghost" className="ml-auto" aria-label={`Edit ${agent.name}`} onClick={() => onEdit(agent.name)}>Edit</Button>
      <Button size="sm" variant="outline" aria-label={`Copy ${agent.name}`} onClick={() => onCopy(agent.name)}>Copy</Button>
    </div>)}
  </div>;
}
