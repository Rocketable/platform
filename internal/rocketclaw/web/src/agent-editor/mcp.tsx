import { useRef, useState } from "react";
import { Lock, Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import type { AgentDraft, AgentDraftExternalMCP, AgentDraftOpInput } from "@/types";

export type Apply = (op: AgentDraftOpInput) => Promise<unknown>;

// The variable a metadata key becomes, as the server names it: Go uppercases one character at a time, so a
// character whose uppercase is longer, like "ß", stays as it is.
export const envName = (key: string) =>
  `$ROCKETCLAW_METADATA_${Array.from(key, (c) => (Array.from(c.toUpperCase()).length === 1 ? c.toUpperCase() : c)).join("").replace(/[^A-Z0-9]/gu, "_")}`;

// Whether the test chat's first message, first, fixed key's variable, as the server compares keys.
export const fixed = (first: AgentDraftExternalMCP | undefined, key: string) => Object.keys(first?.metadata ?? {}).some((name) => envName(name) === envName(key));

// What External MCP mode allows and needs now, given first, the mode and pairs the test chat's first message fixed:
// every variant must be sticky: off; the metadata variables the settings and instructions read are required keys,
// missing while empty; as the server does, it refuses two keys of one variable, of which the agent gets one value, and
// a key the conversation id overwrites, both of which the message would show; and a changed mode or a changed or
// removed fixed variable needs a new chat.
export function mcpState(draft: AgentDraft, first?: AgentDraftExternalMCP) {
  const mcp = draft.externalMcp ?? {}, metadata = mcp.metadata ?? {}, on = !!mcp.on;
  const allowed = draft.variants.every(({ settings }) => /^["']?off["']?$/.test(settings.find(({ key }) => key === "sticky")?.value.trim() ?? "off"));
  const required = [...new Set([...JSON.stringify(draft.variants).matchAll(/\$\{?ROCKETCLAW_METADATA_([A-Z0-9_]+)/g)].map((match) => match[1].toLowerCase()))];
  const missing = on ? required.filter((key) => !Object.entries(metadata).some(([name, value]) => value && envName(name) === envName(key))) : [];
  let refused = "";
  const keys = new Map<string, string>();
  for (const key of on ? Object.keys(metadata).sort() : []) {
    const variable = envName(key), other = keys.get(variable);
    if (variable === envName("external_conversation_id")) refused ||= `External MCP sets ${variable} to the conversation's id: remove the metadata key ${key}.`;
    else if (other !== undefined) refused ||= `Metadata keys ${other} and ${key} both become ${variable}: keep one of them.`;
    keys.set(variable, key);
  }
  let restart = "";
  if (first && !!first.on !== on) restart = on ? "Restart as External MCP" : "Restart as a Web chat";
  else if (Object.entries(first?.metadata ?? {}).some(([key, value]) => !Object.entries(metadata).some(([name, sent]) => envName(name) === envName(key) && sent === value))) restart = "Restart with new metadata";
  return { on, allowed, required, missing, refused, restart };
}

// The server keeps the whole setting as last written, so a change, from the panel or the suggestion, made before the
// page has read back the one before it builds on that one rather than on the older setting shown, and is sent once
// that one is stored, so the page's changes land in the order made.
export function useMcpWrites(draft: AgentDraft, apply: Apply) {
  const sending = useRef<{ count: number; value?: AgentDraftExternalMCP; last: Promise<unknown> }>({ count: 0, last: Promise.resolve() });
  return (change: (base: AgentDraftExternalMCP) => AgentDraftExternalMCP) => {
    const now = sending.current, base = now.value ?? draft.externalMcp ?? {}, value = { ...base, ...change(base) };
    now.value = value;
    now.count++;
    now.last = now.last.then(() => apply({ kind: "set_external_mcp", externalMcp: value })).finally(() => { if (!--now.count) now.value = undefined; });
  };
}

export function McpPanel({ draft, first, state, set }: { draft: AgentDraft; first?: AgentDraftExternalMCP; state: ReturnType<typeof mcpState>; set: ReturnType<typeof useMcpWrites> }) {
  const mcp = draft.externalMcp ?? {}, metadata = mcp.metadata ?? {};
  const required = new Set(state.required), keys = [...new Set([...Object.keys(metadata), ...required])].sort();
  return <div className="flex shrink-0 flex-col gap-2 border-b px-3 py-2 text-xs">
    <div className="flex items-center gap-2">
      <Switch size="sm" aria-label="Test as External MCP" checked={state.on} disabled={!state.allowed && !state.on} onCheckedChange={(on) => set(() => ({ on }))} />
      <span className="font-medium">Test as External MCP</span>
      <span className="text-muted-foreground">{state.allowed ? "Every column answers as an External MCP call, without the ask-user tool." : "External MCP only serves sticky: off agents: set Shared chat to off in every variant."}</span>
    </div>
    {state.on ? <>
      <label className="flex items-center gap-2"><span className="w-24 shrink-0 text-muted-foreground">Caller name</span>
        <Input key={mcp.caller} aria-label="Caller name" defaultValue={mcp.caller} placeholder="the External MCP user the call comes from" onBlur={(event) => event.target.value !== (mcp.caller ?? "") && set(() => ({ caller: event.target.value }))} />
      </label>
      {keys.map((key) => <div key={key} className="flex items-center gap-2">
        <span className="w-24 shrink-0 truncate font-mono">{key}</span>
        <Input key={metadata[key]} aria-label={`Value of ${key}`} defaultValue={metadata[key]} placeholder={required.has(key) ? "required" : ""} onBlur={(event) => event.target.value !== (metadata[key] ?? "") && set((base) => ({ metadata: { ...base.metadata, [key]: event.target.value } }))} />
        {fixed(first, key) ? <Lock aria-label="Fixed by the chat's first message" className="size-3.5 shrink-0" /> : null}
        {required.has(key) ? null : <Button size="icon-xs" variant="ghost" aria-label={`Remove ${key}`} onClick={() => set((base) => ({ metadata: Object.fromEntries(Object.entries(base.metadata ?? {}).filter(([name]) => name !== key)) }))}><X /></Button>}
      </div>)}
      <NewPair onAdd={(key, value) => set((base) => ({ metadata: { ...base.metadata, [key]: value } }))} />
      <p className="text-muted-foreground">The keys the chat's first message sends stay fixed for the chat, as External MCP keeps them; a key added later goes with each later message only.</p>
    </> : null}
  </div>;
}

function NewPair({ onAdd }: { onAdd: (key: string, value: string) => void }) {
  const [pair, setPair] = useState({ key: "", value: "" });
  return <div className="flex items-center gap-2">
    <Input aria-label="New metadata key" placeholder="key" value={pair.key} onChange={(event) => setPair({ ...pair, key: event.target.value })} />
    <Input aria-label="New metadata value" placeholder="value" value={pair.value} onChange={(event) => setPair({ ...pair, value: event.target.value })} />
    <Button size="xs" variant="ghost" disabled={!pair.key.trim()} onClick={() => { onAdd(pair.key.trim(), pair.value); setPair({ key: "", value: "" }); }}><Plus />Add pair</Button>
  </div>;
}
