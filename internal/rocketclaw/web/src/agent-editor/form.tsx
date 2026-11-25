import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FileCode2, RotateCcw, Sparkles } from "lucide-react";
import { queries } from "@/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { AgentDraftPresence, AgentDraftProblem, AgentDraftVariant, AgentSettingHelp } from "@/types";
import type { suggestion } from "./assistant";
import { codePoints, editor, spliceBetween, type CoEdit } from "./coedit";
import { useCaret } from "./file";
import { ModelPicker } from "./model-picker";

type Shared = { coedit: CoEdit; variant: AgentDraftVariant; presence: AgentDraftPresence[]; onFile: (key: string) => void; suggested: ReturnType<typeof suggestion>; problems: AgentDraftProblem[] };

const editing = (presence: AgentDraftPresence[], variant: string, place: (place: string) => boolean) =>
  [...new Set(presence.filter((here) => here.client !== editor && here.variant === variant && place(here.place)).map((here) => here.username!))];

function scalar(yaml: string) {
  if (/^"(?:[^"\\\n]|\\["\\/bfnrt]|\\u[\da-fA-F]{4})*"$/.test(yaml)) return JSON.parse(yaml) as string;
  if (/^'[^\n]*'$/.test(yaml)) return yaml.slice(1, -1).replaceAll("''", "'");
  if (/^\|-?\n/.test(yaml)) return yaml.replace(/^\|(-?)\n([\s\S]*)$/, (_, strip: string, lines: string) => lines.replace(/^ {2}/gm, "") + (strip ? "" : "\n"));
  return /\n|: |:$| #|^[-?:,[\]{}#&*!|>'"%@`](\s|$)|^[[{&*!|>%@`'"]/.test(yaml) ? undefined : yaml;
}

const yamlScalar = (text: string) => /^(?:[^\s\-?:,[\]{}#&*!|>'"%@`]|[-?:](?=\S))[^\n]*$/.test(text) && !/: | #|[\s:]$/.test(text) ? text : JSON.stringify(text);

function listText(yaml: string) {
  const items = /^\[(.*)\]$/.exec(yaml)?.[1].split(",") ?? (/^(- .*\n?)+$/.test(yaml) ? yaml.split("\n").map((line) => line.slice(2)) : [yaml]);
  const texts = items.map((item) => {
    const agent = /^\{?\s*agent: ([^{}]*?)\s*\}?$/.exec(item.trim())?.[1], text = scalar(agent ?? item.trim());
    return agent === undefined || text === undefined ? text : `agent: ${text}`;
  });
  return texts.every((text) => text !== undefined) ? texts.join(", ") : undefined;
}

const itemYAML = (item: string) => item.startsWith("agent:") ? `agent: ${yamlScalar(item.slice(6).trim())}` : yamlScalar(item);

const listYAML = (text: string) => text.trim() === "auto" ? "auto" : text.split(",").map((item) => item.trim()).filter(Boolean).map((item) => `- ${itemYAML(item)}`).join("\n");

export function FormTab({ help, ...shared }: Shared & { help: AgentSettingHelp[] }) {
  const { variant } = shared, set = new Set(variant.settings.map(({ key }) => key));
  const rows = help.filter(({ key }) => key !== "mode" && !key.includes("."));
  const visible = (setting: AgentSettingHelp) => setting.required || setting.common || set.has(setting.key) || shared.suggested.keys.has(setting.key);
  const shown = rows.filter(visible), groups = Map.groupBy(rows.filter((setting) => !visible(setting)), (setting) => setting.group);
  const known = new Set(help.map(({ key }) => key)), others = variant.settings.filter(({ key }) => key !== "mode" && !known.has(key));
  const row = (setting: AgentSettingHelp) => <Row key={setting.key} setting={setting} nested={help.filter(({ key }) => key.startsWith(`${setting.key}.`))} {...shared} />;
  return <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto p-4">
    <h3 className="text-xs text-muted-foreground"><span className="font-medium text-foreground">In the file</span> · required, common and changed settings</h3>
    {shown.map(row)}
    <Instructions {...shared} />
    {[...groups].map(([group, settings]) => <details key={group} className="rounded-lg border px-3 py-2">
      <summary className="cursor-pointer text-sm font-medium">{group} <span className="font-normal text-muted-foreground">· {settings.length} left at default</span></summary>
      <div className="flex flex-col gap-5 pt-3">{settings.map(row)}</div>
    </details>)}
    {others.length ? <section aria-label="Unknown settings" className="flex flex-col gap-2">
      <h3 className="text-sm font-medium text-destructive">Unknown settings <span className="font-normal text-muted-foreground">· the agent fails to load with these; remove them in the File tab</span></h3>
      {others.map(({ key, value }) => <pre key={key} className="overflow-x-auto rounded-md bg-muted px-2 py-1 font-mono text-xs">{key}: {value}</pre>)}
    </section> : null}
  </div>;
}

function Row({ setting, nested, coedit, variant, presence, onFile, suggested, problems }: Shared & { setting: AgentSettingHelp; nested: AgentSettingHelp[] }) {
  const value = variant.settings.find(({ key }) => key === setting.key)?.value, who = editing(presence, variant.name, (place) => place === setting.key), proposed = suggested.keys.get(setting.key);
  const onSet = (yaml: string) => { coedit.setField(variant.name, setting.key, yaml); coedit.soon(); };
  return <div role="group" aria-label={setting.label} onFocus={() => coedit.present(variant.name, setting.key)} className={cn("flex flex-col gap-1 rounded-md", who.length > 0 && "ring-2 ring-cyan-500/40 ring-offset-4 ring-offset-background")}>
    <div className="flex items-baseline gap-1.5">
      <span className="text-sm font-medium">{setting.label}{setting.required ? <span className="text-destructive">*</span> : null}</span>
      <code className="text-[11px] text-muted-foreground">{setting.key}</code>
      {who.map((name) => <span key={name} className="rounded bg-cyan-500/15 px-1 text-[10px] text-cyan-700 dark:text-cyan-300">{name} is editing</span>)}
      {value !== undefined ? <Button size="xs" variant="ghost" className="ml-auto" aria-label={`Back to the default ${setting.label}`} onClick={() => onSet("")}><RotateCcw />Default</Button> : null}
    </div>
    <p className="text-xs text-muted-foreground">{setting.explanation}</p>
    {mcpSetting(setting.key) ? <ConfiguredServers /> : null}
    <Control setting={setting} value={value} onSet={onSet} onFile={() => onFile(setting.key)} />
    {proposed === undefined ? null : <p className="flex items-center gap-1 text-xs break-all text-violet-700 dark:text-violet-300"><Sparkles className="size-3 shrink-0" />Suggested: {proposed || "the default"}</p>}
    {problems.filter(({ key }) => key === setting.key).map(({ sentence }) => <p key={sentence} className="text-xs text-destructive">Not saved. Allowed: {sentence}</p>)}
    {value === undefined ? <p className={cn("text-xs", setting.required ? "text-destructive" : "text-muted-foreground")}>{setting.required ? setting.defaultText : `Left out: ${setting.defaultText}`}</p> : null}
    {nested.map((inner) => <div key={inner.key} className="flex flex-col gap-1 border-l pl-2 text-xs text-muted-foreground">
      <p><span className="font-medium text-foreground">{inner.label}</span> <code>{inner.key}</code>: {inner.explanation} Left out: {inner.defaultText}</p>
      {mcpSetting(inner.key) ? <ConfiguredServers /> : null}
    </div>)}
  </div>;
}

const mcpSetting = (key: string) => /(^|\.)mcp$/.test(key);

function ConfiguredServers() {
  const servers = useQuery(queries.config()).data?.mcpServerFacets ?? [];
  if (!servers.length) return null;
  return <ul aria-label="Configured MCP servers" className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
    {servers.map(({ server = "", facets = [] }) => <li key={server} className="flex items-center gap-1">
      <span className="font-medium text-foreground">{server}</span>
      {facets.map((facet) => <span key={facet} className="rounded-md bg-muted px-1.5 py-0.5 text-muted-foreground">{facet}</span>)}
    </li>)}
  </ul>;
}

function Control({ setting, value, onSet, onFile }: { setting: AgentSettingHelp; value?: string; onSet: (yaml: string) => void; onFile: () => void }) {
  const text = value === undefined ? "" : setting.control === "list" ? listText(value) : scalar(value);
  if (setting.control === "file" || text === undefined) return <div className="flex flex-col items-start gap-1">
    {value ? <pre className="max-w-full overflow-x-auto rounded-md bg-muted px-2 py-1 font-mono text-xs">{value}</pre> : null}
    <Button size="xs" variant="outline" onClick={onFile}><FileCode2 />Edit in File</Button>
  </div>;
  if (setting.control === "switch") return <Switch aria-label={setting.label} checked={/^(true|on|yes|1)$/i.test(text)} onCheckedChange={(on) => onSet(String(on))} />;
  if (setting.control === "model") return <ModelPicker label={setting.label} value={text} onModel={(ref) => onSet(ref && yamlScalar(ref))} />;
  if (setting.control === "choice") return <ButtonGroup aria-label={setting.label}>
    {setting.choices.map((choice) => <Button key={choice} size="sm" variant={text === choice ? "secondary" : "outline"} aria-pressed={text === choice} onClick={() => onSet(choice)}>{choice}</Button>)}
  </ButtonGroup>;
  return <TextField label={setting.label} value={text} onText={(typed) => onSet(setting.control === "list" ? listYAML(typed) : typed && yamlScalar(typed))} />;
}

function TextField({ label, value, onText }: { label: string; value: string; onText: (text: string) => void }) {
  const [typed, setTyped] = useState<string>();
  return <Input aria-label={label} value={typed ?? value} onBlur={() => setTyped(undefined)} onChange={(event) => { setTyped(event.target.value); onText(event.target.value); }} />;
}

function Instructions({ coedit, variant, presence, suggested }: Shared) {
  const caret = useCaret(variant.body), who = editing(presence, variant.name, (place) => /^\d+$/.test(place));
  return <label className="flex flex-col gap-1 text-sm font-medium">
    <span>Instructions {who.map((name) => <span key={name} className="ml-1 rounded bg-cyan-500/15 px-1 text-[10px] font-normal text-cyan-700 dark:text-cyan-300">{name} is editing</span>)}</span>
    {suggested.body ? <span className="flex items-center gap-1 text-xs font-normal text-violet-700 dark:text-violet-300"><Sparkles className="size-3" />Suggested: new instructions, shown in the File tab and the assistant’s card.</span> : null}
    <Textarea ref={caret.ref} value={variant.body} spellCheck={false} className="min-h-40"
      onChange={(event) => { caret.typed(event); coedit.editBody(variant.name, spliceBetween(variant.body, event.target.value)); coedit.soon(); }}
      onSelect={(event) => { caret.onSelect(event); coedit.present(variant.name, String(codePoints(event.currentTarget.value.slice(0, event.currentTarget.selectionStart)))); }} />
  </label>;
}
