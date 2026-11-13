import { useState } from "react";
import { useMutation, useQuery, type UseQueryResult } from "@tanstack/react-query";
import { ArrowUpRight, FlaskConical, LoaderCircle } from "lucide-react";
import { mutations, queries } from "@/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// models.dev's providers by name, each with its models; costs are dollars per million tokens.
type Suggestion = { id: string; name: string; reasoning_options?: { type: string; values?: string[] }[]; limit: { context: number }; cost?: { input: number; output: number } };
type Catalog = Record<string, { models: Record<string, Suggestion> }>;

async function modelsDev({ signal }: { signal: AbortSignal }) {
  const response = await fetch("https://models.dev/api.json", { signal });
  if (!response.ok) throw new Error(`models.dev answered ${response.status}`);
  return await response.json() as Catalog;
}

const compact = new Intl.NumberFormat("en", { notation: "compact" });
// A model models.dev does not know gets the efforts the model setting's help names.
const efforts = (model?: Suggestion) => model ? model.reasoning_options?.find(({ type }) => type === "effort")?.values ?? [] : ["low", "medium", "high"];
const facts = (model: Suggestion) => [model.cost ? `$${model.cost.input} in · $${model.cost.output} out per 1M tokens` : "price unknown", `${compact.format(model.limit.context)} context`,
  efforts(model).length ? `effort ${efforts(model).join(", ")}` : "no effort levels"].join(" · ");

// A model reference is provider/model#effort; without a provider it means openai.
function parse(ref: string) {
  const [name, effort = ""] = ref.split("#"), slash = name.indexOf("/");
  return { provider: slash < 0 ? "openai" : name.slice(0, slash), model: name.slice(slash + 1), effort, qualified: slash >= 0 };
}

// value is the model reference, and onModel gets the new one, empty to leave the setting out. models.dev's file is
// about 6 MB, so it is fetched once the picker is first used, not with the page.
export function ModelPicker({ label, value, onModel }: { label: string; value: string; onModel: (ref: string) => void }) {
  const current = parse(value);
  const [picked, setPicked] = useState(current.provider), [typed, setTyped] = useState<string>(), [open, setOpen] = useState(false), [used, setUsed] = useState(false);
  const providers = useQuery(queries.config()).data?.providers ?? [];
  const catalog = useQuery({ queryKey: ["modelsDev"], staleTime: Infinity, enabled: used, queryFn: modelsDev });
  const test = useMutation({ mutationFn: (model: string) => mutations.testModel({ model }) });
  const provider = current.model ? current.provider : picked, known = catalog.data?.[provider]?.models[current.model];
  const commit = (to: string, model: string, effort: string) => onModel(model && `${current.qualified || to !== "openai" ? `${to}/` : ""}${model}${effort && `#${effort}`}`);
  const answer = test.variables === value && test.isSuccess ? test.data : undefined, failure = test.variables === value ? test.error?.message ?? answer?.error : undefined;
  return <div className="relative flex flex-col gap-1.5" onFocus={() => setUsed(true)}>
    <div className="flex gap-1.5">
      <Choose label={`${label} provider`} value={provider} items={providers.map((name) => [name, name])} onPick={(to) => { setPicked(to); commit(to, current.model, current.effort); }} />
      <Input role="combobox" aria-expanded={open} aria-label={label} placeholder="Pick or type a model" value={typed ?? current.model} onFocus={() => setOpen(true)} onBlur={() => { setOpen(false); setTyped(undefined); }}
        onChange={(event) => { setTyped(event.target.value); setPicked(provider); commit(provider, event.target.value.trim(), current.effort); }} />
    </div>
    <div className="flex flex-wrap items-center gap-1.5">
      <Choose label={`${label} effort`} value={current.effort} disabled={!current.model} items={[["", "Effort: default"], ...efforts(known).map((effort) => [effort, `Effort: ${effort}`])]} onPick={(effort) => commit(provider, current.model, effort)} />
      <Button size="sm" variant="outline" disabled={!current.model || test.isPending} onClick={() => test.mutate(value)}>{test.isPending ? <LoaderCircle className="animate-spin" /> : <FlaskConical />}Test</Button>
      <span className="text-xs text-muted-foreground">Test sends one small paid request, only when you click.</span>
    </div>
    {open ? <Suggestions provider={provider} catalog={catalog} typed={typed} onPick={(model) => { setOpen(false); setTyped(undefined); commit(provider, model.id, efforts(model).includes(current.effort) ? current.effort : ""); }} /> : null}
    {failure ? <p role="alert" className="text-xs text-destructive">{failure}</p> : null}
    {answer && !failure ? <p role="status" className="text-xs text-emerald-700 dark:text-emerald-400">Works. {provider} answered in {(Number(answer.elapsedMs ?? 0) / 1000).toFixed(1)} s.</p> : null}
  </div>;
}

// Suggestions match the provider's name in models.dev and what is typed; each links to slopalytics to compare it.
function Suggestions({ provider, catalog, typed = "", onPick }: { provider: string; catalog: UseQueryResult<Catalog>; typed?: string; onPick: (model: Suggestion) => void }) {
  const models = Object.values(catalog.data?.[provider]?.models ?? {}).filter(({ id, name }) => `${id} ${name}`.toLowerCase().includes(typed.trim().toLowerCase()));
  const note = catalog.isError ? "models.dev did not answer, so there are no suggestions. Type the exact model name, then Test it." : catalog.isPending ? "Loading suggestions from models.dev…"
    : catalog.data?.[provider] ? "No suggestion from models.dev matches. Any name works; Test checks it."
      : `models.dev has no provider called “${provider}”, so there are no suggestions. Type the exact model name it serves, then Test it.`;
  // A note sits in the page's flow below Test, so it never covers or moves it; only a list of suggestions opens over it.
  if (!models.length) return <p className="text-xs leading-relaxed text-muted-foreground">{note}</p>;
  return <div onMouseDown={(event) => event.preventDefault()} className="absolute top-9 right-0 left-0 z-30 max-h-80 overflow-y-auto rounded-lg bg-popover p-1 text-xs text-popover-foreground shadow-md ring-1 ring-foreground/10">
    <p className="px-2 py-1 text-muted-foreground">Suggestions from models.dev for “{provider}” · any name works</p>
    <ul aria-label={`Suggestions from models.dev for ${provider}`}>{models.map((model) => <li key={model.id} className="flex flex-col items-start rounded-md px-2 py-1.5 hover:bg-accent">
      <button type="button" className="flex flex-col items-start gap-0.5 text-left" onClick={() => onPick(model)}>
        <span className="text-sm">{model.name}<code className="ml-1.5 text-[11px] text-muted-foreground">{model.id}</code></span>
        <span className="text-muted-foreground">{facts(model)}</span>
      </button>
      <a href="https://slopalytics.com/" target="_blank" rel="noreferrer" aria-label={`Compare ${model.name} on slopalytics`} className="flex items-center gap-0.5 self-end text-[11px] text-muted-foreground hover:underline">Compare on slopalytics<ArrowUpRight className="size-3" /></a>
    </li>)}</ul>
  </div>;
}

function Choose({ label, value, items, disabled, onPick }: { label: string; value: string; items: string[][]; disabled?: boolean; onPick: (value: string) => void }) {
  const choices = items.map(([choice, name]) => ({ value: choice, label: name }));
  return <Select items={choices} value={value} disabled={disabled} onValueChange={(choice) => onPick(String(choice))}>
    <SelectTrigger aria-label={label} size="sm"><SelectValue /></SelectTrigger>
    <SelectContent><SelectGroup>{choices.map((choice) => <SelectItem key={choice.value} value={choice.value}>{choice.label}</SelectItem>)}</SelectGroup></SelectContent>
  </Select>;
}
