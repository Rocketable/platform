import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FieldError } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { mutations, queries } from "./api";
import type { ConfigView } from "./types";

// Behavior is the server's Behavior Settings document. Fields this screen does not show are saved as received.
type Channel = { channel: string; agents?: string[]; allowed_user_ids?: string[] };
type Behavior = {
  models: Record<string, string> | null;
  auto_approver_model: string;
  slack: { channels: Channel[] | null };
  web_users?: Record<string, string> | null;
  server_display_name: string;
  logging: { level: string };
  instrumentation: { hide_inputs: boolean; hide_outputs: boolean };
  web: { sentry: { environment: string; traces_sample_rate: number | null } };
  overlays: string[] | null;
};
// Draft keeps the two maps as rows, so a row being typed may be empty or repeat a name.
type Draft = Behavior & { modelRows: string[][]; userRows: string[][] };
type Option = { value: string; label: string };

const option = (value: string): Option => ({ value, label: value });
const levels = ["debug", "info", "warn", "error"].map(option);

// validIP accepts IPv4 and IPv6 address text; the server still parses it on save.
const badRate = (rate: number | null) => rate !== null && !(rate >= 0 && rate <= 1);
const validIP = (ip: string) => /^[\d.]+$/.test(ip) ? URL.parse(`http://${ip}`)?.hostname === ip : /^[\da-f:.]+$/i.test(ip) && URL.canParse(`http://[${ip}]`);

// BehaviorSettings edits the Behavior Settings with a control per setting. The page shows the
// saved settings until the server answers a save; a refused save keeps the edits.
export function BehaviorSettings({ view }: { view: ConfigView }) {
  const [draft, setDraft] = useState<Draft>();
  const client = useQueryClient();
  const save = useMutation({ mutationFn: mutations.saveBehaviorSettings, onSuccess: (config) => {
    client.setQueryData(queries.config().queryKey, config);
    setDraft(undefined);
  } });
  if (!draft) {
    const edit = () => {
      const behavior = JSON.parse(view.behaviorJson!) as Behavior;
      setDraft({ ...behavior, modelRows: Object.entries(behavior.models ?? {}), userRows: Object.entries(behavior.web_users ?? {}) });
    };
    return <Button variant="outline" size="sm" className="self-start" onClick={edit}>Edit</Button>;
  }
  const { modelRows, userRows, ...behavior } = draft;
  const ipErrors = userRows.map(([ip]) => validIP(ip) ? undefined : "Not an IP address");
  const invalid = ipErrors.some(Boolean) || badRate(behavior.web.sentry.traces_sample_rate);
  const submit = () => save.mutate({ documentJson: JSON.stringify({ ...behavior, models: Object.fromEntries(modelRows), web_users: Object.fromEntries(userRows) }) });
  return (
    <form className="flex flex-col gap-4" onSubmit={(event) => { event.preventDefault(); submit(); }}>
      <fieldset disabled={save.isPending} className="flex flex-col gap-6">
        <BehaviorFields draft={draft} ipErrors={ipErrors} disabled={save.isPending} onChange={(change) => setDraft({ ...draft, ...change })} />
      </fieldset>
      {save.error ? <FieldError>{save.error.message}</FieldError> : null}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={save.isPending || invalid}>{save.isPending ? "Saving…" : "Save"}</Button>
        <Button type="button" variant="ghost" size="sm" disabled={save.isPending} onClick={() => { setDraft(undefined); save.reset(); }}>Cancel</Button>
      </div>
    </form>
  );
}

function BehaviorFields({ draft, ipErrors, disabled, onChange }: { draft: Draft; ipErrors: (string | undefined)[]; disabled: boolean; onChange: (change: Partial<Draft>) => void }) {
  const { instrumentation, web } = draft;
  const approvers = [{ value: "", label: "(default)" }, ...[...new Set([...draft.modelRows.map(([, model]) => model!), draft.auto_approver_model])].filter(Boolean).map(option)];
  const sentry = (change: Partial<Behavior["web"]["sentry"]>) => onChange({ web: { ...web, sentry: { ...web.sentry, ...change } } });
  return (
    <>
      <Group title="Models">
        <Rows noun="model" columns={["Name", "Model"]} rows={draft.modelRows} onChange={(modelRows) => onChange({ modelRows })} />
        <Setting label="Auto-approver model"><Choice label="Auto-approver model" value={draft.auto_approver_model} options={approvers} disabled={disabled} onPick={(value) => onChange({ auto_approver_model: value })} /></Setting>
      </Group>
      <Group title="Slack channels">
        <Channels channels={draft.slack.channels ?? []} disabled={disabled} onChange={(channels) => onChange({ slack: { ...draft.slack, channels } })} />
      </Group>
      <Group title="Web users">
        <Rows noun="web user" columns={["IP address", "Username"]} rows={draft.userRows} errors={ipErrors} onChange={(userRows) => onChange({ userRows })} />
      </Group>
      <Group title="Server">
        <Setting label="Display name"><Input aria-label="Display name" className="w-64" value={draft.server_display_name} onChange={(event) => onChange({ server_display_name: event.target.value })} /></Setting>
        <Setting label="Log level"><Choice label="Log level" value={draft.logging.level} options={levels} disabled={disabled} onPick={(level) => onChange({ logging: { ...draft.logging, level } })} /></Setting>
        <Setting label="Hide inputs"><Switch aria-label="Hide inputs" checked={instrumentation.hide_inputs} disabled={disabled} onCheckedChange={(checked) => onChange({ instrumentation: { ...instrumentation, hide_inputs: checked } })} /></Setting>
        <Setting label="Hide outputs"><Switch aria-label="Hide outputs" checked={instrumentation.hide_outputs} disabled={disabled} onCheckedChange={(checked) => onChange({ instrumentation: { ...instrumentation, hide_outputs: checked } })} /></Setting>
        <Setting label="Sentry environment"><Input aria-label="Sentry environment" className="w-64" value={web.sentry.environment} onChange={(event) => sentry({ environment: event.target.value })} /></Setting>
        <Setting label="Sentry sample rate"><SampleRate value={web.sentry.traces_sample_rate} onChange={(traces_sample_rate) => sentry({ traces_sample_rate })} /></Setting>
      </Group>
      <Group title="Overlays">
        <Rows noun="overlay" columns={["Overlay"]} rows={(draft.overlays ?? []).map((overlay) => [overlay])} onChange={(rows) => onChange({ overlays: rows.map(([overlay]) => overlay) })} />
      </Group>
    </>
  );
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return <div className="flex flex-col gap-2"><h3 className="text-sm font-medium">{title}</h3>{children}</div>;
}

function Setting({ label, children }: { label: string; children: ReactNode }) {
  return <div className="flex items-center justify-between gap-4 border-b py-2"><span className="text-sm text-muted-foreground">{label}</span>{children}</div>;
}

// SampleRate is a share from 0 to 1; empty leaves it unset.
function SampleRate({ value, onChange }: { value: number | null; onChange: (value: number | null) => void }) {
  const invalid = badRate(value);
  return (
    <div className="flex flex-col items-end gap-1">
      <Input type="number" aria-label="Sentry sample rate" className="w-32" min={0} max={1} step={0.01} placeholder="Unset" value={value ?? ""} aria-invalid={invalid} onChange={(event) => onChange(event.target.value === "" ? null : Number(event.target.value))} />
      {invalid ? <FieldError>Between 0 and 1</FieldError> : null}
    </div>
  );
}

function Choice({ label, value, options, placeholder, disabled, onPick }: { label: string; value: string | null; options: Option[]; placeholder?: string; disabled: boolean; onPick: (value: string) => void }) {
  return (
    <Select items={options} value={value} disabled={disabled} onValueChange={(next) => { if (typeof next === "string") onPick(next); }}>
      <SelectTrigger aria-label={label}><SelectValue placeholder={placeholder} /></SelectTrigger>
      <SelectContent><SelectGroup>{options.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectGroup></SelectContent>
    </Select>
  );
}

// Rows and channels have no identity of their own, so each gets a key when it appears on the screen.
let lastKey = 0;
function useKeys(count: number) {
  const [list, setList] = useState(() => Array.from({ length: count }, () => ++lastKey));
  return { list, add: () => setList([...list, ++lastKey]), remove: (index: number) => setList(list.toSpliced(index, 1)) };
}

// Rows edits a list of rows of text cells, one input per column.
function Rows({ noun, columns, rows, errors = [], onChange }: { noun: string; columns: string[]; rows: string[][]; errors?: (string | undefined)[]; onChange: (rows: string[][]) => void }) {
  const keys = useKeys(rows.length);
  const set = (index: number, column: number, value: string) => onChange(rows.with(index, rows[index]!.with(column, value)));
  return (
    <div className="flex flex-col gap-2">
      {rows.map((row, index) => (
        <div key={keys.list[index]} className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            {row.map((cell, column) => <Input key={columns[column]} aria-label={`${columns[column]} ${index + 1}`} placeholder={columns[column]} value={cell} aria-invalid={column === 0 && !!errors[index]} onChange={(event) => set(index, column, event.target.value)} />)}
            <Button type="button" variant="ghost" size="icon-sm" aria-label={`Remove ${noun} ${index + 1}`} onClick={() => { keys.remove(index); onChange(rows.toSpliced(index, 1)); }}><X /></Button>
          </div>
          {errors[index] ? <FieldError>{errors[index]}</FieldError> : null}
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => { keys.add(); onChange([...rows, columns.map(() => "")]); }}><Plus />Add {noun}</Button>
    </div>
  );
}

function Channels({ channels, disabled, onChange }: { channels: Channel[]; disabled: boolean; onChange: (channels: Channel[]) => void }) {
  const agents = useQuery({ ...queries.agents(), staleTime: 60_000 });
  const names = (agents.data?.agents ?? []).map((agent) => agent.name);
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

// ChannelCard is one Slack channel. Its agents come from the agent list; the first one is the default.
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

// TextAdder adds what was typed on Enter or on leaving the box, so nothing typed is lost on Save.
function TextAdder({ label, onAdd }: { label: string; onAdd: (text: string) => void }) {
  const [text, setText] = useState("");
  const add = () => {
    if (text.trim() !== "") onAdd(text.trim());
    setText("");
  };
  return <Input aria-label={label} placeholder="Add user ID" className="h-7 w-40" value={text} onChange={(event) => setText(event.target.value)} onBlur={add} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); add(); } }} />;
}
