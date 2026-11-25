import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FieldError } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { DecisionModelPicker, ModelPicker } from "./agent-editor/model-picker";
import { mutations, queries } from "./api";
import type { ConfigView } from "./types";

type Channel = { channel: string; agents?: string[]; allowed_user_ids?: string[]; wake_when?: string; engagement?: string };
type EmbeddedAgent = { disabled?: boolean; model?: string };
type Behavior = {
  auto_approver_model: string;
  slack: { channels: Channel[] | null; ambient_decision_model?: string };
  logging: { level: string };
  overlays: string[] | null;
  embedded_agents?: { main?: EmbeddedAgent };
  title_model?: string;
  title_decision_model?: string;
};
type Option = { value: string; label: string };

const option = (value: string): Option => ({ value, label: value });
const levels = ["debug", "info", "warn", "error"].map(option);

export function BehaviorSettings({ view }: { view: ConfigView }) {
  const [draft, setDraft] = useState<Behavior>();
  const client = useQueryClient();
  const save = useMutation({ mutationFn: mutations.saveBehaviorSettings, onSuccess: (config) => {
    client.setQueryData(queries.config().queryKey, config);
    setDraft(undefined);
  } });
  if (!draft) {
    return <Button variant="outline" size="sm" className="self-start" onClick={() => setDraft(JSON.parse(view.behaviorJson!) as Behavior)}>Edit</Button>;
  }
  return (
    <form className="flex flex-col gap-4" onSubmit={(event) => { event.preventDefault(); save.mutate({ documentJson: JSON.stringify(draft) }); }}>
      <fieldset disabled={save.isPending} className="flex flex-col gap-6">
        <BehaviorFields draft={draft} disabled={save.isPending} onChange={(change) => setDraft({ ...draft, ...change })} />
      </fieldset>
      {save.error ? <FieldError>{save.error.message}</FieldError> : null}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</Button>
        <Button type="button" variant="ghost" size="sm" disabled={save.isPending} onClick={() => { setDraft(undefined); save.reset(); }}>Cancel</Button>
      </div>
    </form>
  );
}

function BehaviorFields({ draft, disabled, onChange }: { draft: Behavior; disabled: boolean; onChange: (change: Partial<Behavior>) => void }) {
  return (
    <>
      <Group title="Slack">
        <Labeled label="Ambient decision model" hint="When set, it screens allowed users' messages without a mention in Joined Threads and Watched Channels, and the agent answers those it does not rule out. Empty turns ambient listening off.">
          <DecisionModelPicker label="Ambient decision model" value={draft.slack.ambient_decision_model ?? ""} onModel={(ambient_decision_model) => onChange({ slack: { ...draft.slack, ambient_decision_model } })} />
        </Labeled>
        <Channels channels={draft.slack.channels ?? []} disabled={disabled} onChange={(channels) => onChange({ slack: { ...draft.slack, channels } })} />
      </Group>
      <Group title="Server">
        <Setting label="Auto-approver model"><Input aria-label="Auto-approver model" className="w-64" placeholder="Default" value={draft.auto_approver_model} onChange={(event) => onChange({ auto_approver_model: event.target.value })} /></Setting>
        <Setting label="Log level"><Choice label="Log level" value={draft.logging.level} options={levels} disabled={disabled} onPick={(level) => onChange({ logging: { ...draft.logging, level } })} /></Setting>
      </Group>
      <Group title="Overlays">
        <Overlays overlays={draft.overlays ?? []} onChange={(overlays) => onChange({ overlays })} />
      </Group>
      <Group title="Built-in agents">
        <EmbeddedAgentSettings name="main" agent={draft.embedded_agents?.main ?? {}} disabled={disabled} onChange={(agent) => onChange({ embedded_agents: { ...draft.embedded_agents, main: agent } })} />
      </Group>
      <Group title="Session titles">
        <Labeled label="Title model" hint="Names Web sessions. Empty uses openai/gpt-6-luna#low.">
          <ModelPicker label="Title model" value={draft.title_model ?? ""} onModel={(title_model) => onChange({ title_model })} />
        </Labeled>
        <Labeled label="Title decision model" hint="When set, every fourth message a person sends checks whether a generated title still fits. Empty turns this off.">
          <DecisionModelPicker label="Title decision model" value={draft.title_decision_model ?? ""} onModel={(title_decision_model) => onChange({ title_decision_model })} />
        </Labeled>
      </Group>
    </>
  );
}

function EmbeddedAgentSettings({ name, agent, disabled, onChange }: { name: string; agent: EmbeddedAgent; disabled: boolean; onChange: (agent: EmbeddedAgent) => void }) {
  return (
    <div role="group" aria-label={`${name} agent`} className="flex flex-col gap-2">
      <Setting label={name}><Switch aria-label={name} checked={!agent.disabled} disabled={disabled} onCheckedChange={(on) => onChange({ ...agent, disabled: !on })} /></Setting>
      <ModelPicker label={`${name} model`} value={agent.model ?? ""} onModel={(model) => onChange({ ...agent, model })} />
    </div>
  );
}

function Labeled({ label, hint, children }: { label: string; hint: string; children: ReactNode }) {
  return <div role="group" aria-label={label} className="flex flex-col gap-1.5 border-b py-2"><span className="text-sm text-muted-foreground">{label}</span><span className="text-xs text-muted-foreground">{hint}</span>{children}</div>;
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return <div className="flex flex-col gap-2"><h3 className="text-sm font-medium">{title}</h3>{children}</div>;
}

function Setting({ label, children }: { label: string; children: ReactNode }) {
  return <div className="flex items-center justify-between gap-4 border-b py-2"><span className="text-sm text-muted-foreground">{label}</span>{children}</div>;
}

function Choice({ label, value, options, placeholder, disabled, onPick }: { label: string; value: string | null; options: Option[]; placeholder?: string; disabled: boolean; onPick: (value: string) => void }) {
  return (
    <Select items={options} value={value} disabled={disabled} onValueChange={(next) => { if (typeof next === "string") onPick(next); }}>
      <SelectTrigger aria-label={label}><SelectValue placeholder={placeholder} /></SelectTrigger>
      <SelectContent><SelectGroup>{options.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectGroup></SelectContent>
    </Select>
  );
}

let lastKey = 0;
function useKeys(count: number) {
  const [list, setList] = useState(() => Array.from({ length: count }, () => ++lastKey));
  return { list, add: () => setList([...list, ++lastKey]), remove: (index: number) => setList(list.toSpliced(index, 1)) };
}

function Overlays({ overlays, onChange }: { overlays: string[]; onChange: (overlays: string[]) => void }) {
  const keys = useKeys(overlays.length);
  return (
    <div className="flex flex-col gap-2">
      {overlays.map((overlay, index) => (
        <div key={keys.list[index]} className="flex items-center gap-2">
          <Input aria-label={`Overlay ${index + 1}`} placeholder="Overlay" value={overlay} onChange={(event) => onChange(overlays.with(index, event.target.value))} />
          <Button type="button" variant="ghost" size="icon-sm" aria-label={`Remove overlay ${index + 1}`} onClick={() => { keys.remove(index); onChange(overlays.toSpliced(index, 1)); }}><X /></Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => { keys.add(); onChange([...overlays, ""]); }}><Plus />Add overlay</Button>
    </div>
  );
}

function Channels({ channels, disabled, onChange }: { channels: Channel[]; disabled: boolean; onChange: (channels: Channel[]) => void }) {
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000 });
  const names = (agents.data?.agents ?? []).filter((agent) => agent.sticky === "slack").map((agent) => agent.name);
  const keys = useKeys(channels.length);
  return (
    <div className="flex flex-col gap-3">
      {channels.map((channel, index) => (
        <ChannelCard key={keys.list[index]} number={index + 1} channel={channel} names={names} disabled={disabled} onChange={(next) => onChange(channels.with(index, next))} onRemove={() => { keys.remove(index); onChange(channels.toSpliced(index, 1)); }} />
      ))}
      <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => { keys.add(); onChange([...channels, { channel: "", agents: [], allowed_user_ids: [] }]); }}><Plus />Add channel</Button>
    </div>
  );
}

function ChannelCard({ number, channel, names, disabled, onChange, onRemove }: { number: number; channel: Channel; names: string[]; disabled: boolean; onChange: (channel: Channel) => void; onRemove: () => void }) {
  const agents = channel.agents ?? [];
  const users = channel.allowed_user_ids ?? [];
  const chosen = new Set(agents);
  return (
    <div className="flex flex-col gap-2 rounded-lg border p-3">
      <div className="flex items-center gap-2">
        <Input aria-label={`Channel ${number}`} placeholder="#channel" value={channel.channel} onChange={(event) => onChange({ ...channel, channel: event.target.value })} />
        <Button type="button" variant="ghost" size="icon-sm" aria-label={`Remove channel ${number}`} onClick={onRemove}><X /></Button>
      </div>
      <Chips label="Agents" items={agents} first="default" onChange={(next) => onChange({ ...channel, agents: next })}>
        <Choice label={`Add agent to channel ${number}`} value={null} placeholder="Add agent" options={names.filter((name) => !chosen.has(name)).map(option)} disabled={disabled} onPick={(name) => onChange({ ...channel, agents: [...agents, name] })} />
      </Chips>
      <Chips label="Allowed user IDs" items={users} onChange={(next) => onChange({ ...channel, allowed_user_ids: next })}>
        <TextAdder label={`Add user ID to channel ${number}`} onAdd={(id) => onChange({ ...channel, allowed_user_ids: [...new Set([...users, id])] })} />
      </Chips>
      <Input aria-label={`Wake when for channel ${number}`} placeholder="Wake when, e.g. someone asks how to deploy" value={channel.wake_when ?? ""} onChange={(event) => onChange({ ...channel, wake_when: event.target.value })} />
      <Input aria-label={`Engagement for channel ${number}`} placeholder="Engagement, e.g. answer briefly and link the docs" value={channel.engagement ?? ""} onChange={(event) => onChange({ ...channel, engagement: event.target.value })} />
    </div>
  );
}

function Chips({ label, items, first, onChange, children }: { label: string; items: string[]; first?: string; onChange: (items: string[]) => void; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      {items.map((item, index) => (
        <span key={item} className="inline-flex items-center gap-1 rounded-md bg-muted py-0.5 pl-1.5 text-xs">
          {item}{index === 0 && first ? <span className="text-muted-foreground">({first})</span> : null}
          <Button type="button" variant="ghost" size="icon-xs" aria-label={`Remove ${item}`} onClick={() => onChange(items.toSpliced(index, 1))}><X /></Button>
        </span>
      ))}
      {children}
    </div>
  );
}

function TextAdder({ label, onAdd }: { label: string; onAdd: (text: string) => void }) {
  const [text, setText] = useState("");
  const add = () => {
    if (text.trim() !== "") onAdd(text.trim());
    setText("");
  };
  return <Input aria-label={label} placeholder="Add user ID" className="h-7 w-40" value={text} onChange={(event) => setText(event.target.value)} onBlur={add} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); add(); } }} />;
}
