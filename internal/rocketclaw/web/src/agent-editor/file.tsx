import { Fragment, useEffect, useLayoutEffect, useRef, useState, type Ref, type SyntheticEvent } from "react";
import { cn } from "@/lib/utils";
import type { AgentDraftPresence, AgentDraftVariant, AgentSettingHelp } from "@/types";
import { SuggestedFile, type suggestion } from "./assistant";
import { codePoints, editor, spliceBetween, transform, type CoEdit } from "./coedit";

// A frontmatter edit not yet in the draft: made on revision base, whose instructions were body, from the frontmatter
// shown at the first keystroke, then the last one the server took; busy while a submit is in flight, one at a time.
// raw is the whole file while it has no closing --- line, and rawBody the instructions then; invalid is the line the
// server could not parse yet, or -1 when the edit failed.
type Local = { base: number; body: string; from: string; text: string; sent?: string; busy?: boolean; invalid?: number; raw?: string; rawBody?: string };

// The required settings the variant leaves out, each with the help's sentence for what leaving it out means.
export const missing = (help: AgentSettingHelp[], variant: AgentDraftVariant) => help.filter((setting) => setting.required && !variant.settings.some(({ key }) => key === setting.key));

const delimited = /^---\r?\n[\s\S]*?\r?\n---(?:\r?\n|$)/;

// An agent file's frontmatter with both --- lines, and its instructions, split as the agent loader splits them.
const splitFile = (text: string): [string, string] | undefined => {
  const head = delimited.exec(text)?.[0];
  return head === undefined ? undefined : [head, text.slice(head.length)];
};

// A setting's YAML is written under its key when it is a mapping or a list of one entry per line.
const block = (yaml: string) => !/^["'[{|>]/.test(yaml) && /^-( |$)|: |:$/.test(yaml.split("\n")[0]);

// The variant's agent file as the server renders it: mode for an easy agent, the settings in order, the others after
// them as they stand, then the instructions.
function renderFile(mode: string, variant: AgentDraftVariant, order: string[]) {
  const rank = (key: string) => order.indexOf(key) + 1 || order.length + 1;
  const lines = [...variant.settings].sort((a, b) => rank(a.key) - rank(b.key)).map(({ key, value }) => block(value) ? `${key}:\n${value.replace(/^/gm, "  ")}` : `${key}: ${value}`);
  return `---\n${[...(mode === "easy" ? ["mode: easy"] : []), ...lines].join("\n") || "{}"}\n---\n${variant.body}`;
}

// Keeps the caret in place when someone else's change replaces the text of a focused field. The caret is taken from
// each edit too, since the select event after a key comes later and may follow a teammate's change.
export function useCaret(text: string) {
  const ref = useRef<HTMLTextAreaElement>(null), state = useRef({ text, start: 0, end: 0, typed: false });
  useLayoutEffect(() => {
    const field = ref.current, held = state.current, before = held.text;
    held.text = text;
    if (held.typed || field !== document.activeElement || before === text) {
      held.typed = false;
      return;
    }
    let same = 0;
    while (same < before.length && before[same] === text[same]) same++;
    const moved = (at: number) => at > same ? Math.max(same, at + text.length - before.length) : at;
    [held.start, held.end] = [moved(held.start), moved(held.end)];
    field!.setSelectionRange(held.start, held.end);
  }, [text]);
  const onSelect = (event: SyntheticEvent<HTMLTextAreaElement>) => Object.assign(state.current, { start: event.currentTarget.selectionStart, end: event.currentTarget.selectionEnd });
  return { ref, typed: (event: SyntheticEvent<HTMLTextAreaElement>) => { onSelect(event).typed = true; }, onSelect };
}

// A frontmatter edit goes to the server as the whole file, which parses it, so the Web needs no YAML.
function useFileEdit(coedit: CoEdit, mode: string, variant: AgentDraftVariant, order: string[]) {
  const [local, setLocal] = useState<Local>();
  const [head, body] = splitFile(renderFile(mode, variant, order))!;
  const text = local?.raw ?? (local?.text ?? head) + body;
  // Unsent text goes 200 ms after the last key, or at once when a test message is sent, which waits for it.
  useEffect(() => {
    if (!local || local.raw !== undefined || local.text === local.sent) return;
    if (local.busy) return coedit.holdFile(() => {});
    let due = true;
    const submit = () => {
      if (!due) return;
      due = false;
      setLocal((now) => now && { ...now, sent: local.text, busy: true });
      void coedit.submitFile(variant.name, local.base, local.text + local.body, local.from + local.body).then((invalid) => setLocal((now) => {
        const settled = now && { ...now, busy: false, from: invalid ? now.from : local.text };
        return settled?.text !== local.text ? settled : invalid ? { ...settled, invalid } : undefined;
      }));
    };
    const timer = setTimeout(submit, 200), release = coedit.holdFile(submit);
    return () => { clearTimeout(timer); release(); };
  }, [local, coedit, variant.name]);
  const change = (next: string) => {
    const parts = splitFile(next), shown = local?.rawBody ?? body;
    const started = local ?? { base: coedit.rev, body: coedit.server?.variants.find(({ name }) => name === variant.name)?.body ?? "", from: head, text: head };
    if (!parts) return setLocal({ ...started, raw: next, rawBody: shown, invalid: 1 });
    // Instructions typed while the file had no closing --- line move past what teammates typed meanwhile.
    const typed = spliceBetween(shown, parts[1]);
    coedit.editBody(variant.name, local?.raw === undefined ? typed : transform(typed, spliceBetween(shown, body), false)[0]);
    coedit.soon();
    setLocal(parts[0] === head && !local?.sent ? undefined : { ...started, text: parts[0], raw: undefined, rawBody: undefined, invalid: parts[0] === local?.text ? local.invalid : undefined });
  };
  return { text, head: local?.raw === undefined ? (local?.text ?? head) : "", invalid: local?.invalid, change };
}

export function FileTab({ coedit, mode, variant, order, help, presence, focus, suggested }: { coedit: CoEdit; mode: string; variant: AgentDraftVariant; order: string[]; help: AgentSettingHelp[]; presence: AgentDraftPresence[]; focus?: string; suggested: ReturnType<typeof suggestion> }) {
  const file = useFileEdit(coedit, mode, variant, order), caret = useCaret(file.text), overlay = useRef<HTMLDivElement>(null);
  const [line, setLine] = useState("");
  useEffect(() => {
    const field = caret.ref.current!, at = field.value.indexOf(`\n${focus}:`) + 1;
    if (!focus) return;
    field.focus();
    field.setSelectionRange(at, at);
  }, [caret.ref, focus]);
  const select = (event: SyntheticEvent<HTMLTextAreaElement>) => {
    caret.onSelect(event);
    const at = event.currentTarget.selectionStart, before = file.text.slice(0, at), key = /(?:^|\n)([A-Za-z][\w.-]*):[^\n]*(?:\n[ -][^\n]*)*$/.exec(before)?.[1] ?? "";
    const inBody = Boolean(file.head) && at >= file.head.length;
    setLine(inBody ? "" : key);
    coedit.present(variant.name, inBody ? String(codePoints(before.slice(file.head.length))) : key);
  };
  const carets = presence.filter((here) => here.client !== editor && here.variant === variant.name && /^\d+$/.test(here.place) && file.head)
    .map((here) => ({ at: file.head.length + [...variant.body].slice(0, Number(here.place)).join("").length, name: here.username! }));
  const about = help.find(({ key }) => key === line), needed = missing(help, variant);
  return <div className="flex min-h-0 flex-1 flex-col">
    {suggested.keys.size || suggested.body ? <SuggestedFile before={renderFile(mode, variant, order)} after={renderFile(suggested.mode, suggested.variant, order)} /> : null}
    <div className="relative min-h-0 flex-1">
      <Overlay ref={overlay} text={file.text} carets={carets} invalid={file.invalid} />
      <textarea ref={caret.ref} aria-label="Agent file" value={file.text} spellCheck={false} onSelect={select}
        onChange={(event) => { caret.typed(event); file.change(event.target.value); }} onScroll={(event) => { overlay.current!.scrollTop = event.currentTarget.scrollTop; }}
        className="absolute inset-0 size-full resize-none overflow-y-auto bg-transparent p-3 [scrollbar-gutter:stable] font-mono text-xs leading-5 break-words whitespace-pre-wrap outline-none" />
    </div>
    <footer className="flex shrink-0 flex-col gap-0.5 border-t px-3 py-2 text-xs">
      {file.invalid ? <p role="alert" className="text-destructive">{file.invalid > 0 ? `Not yet valid: line ${file.invalid} doesn't parse yet. Only you see this; teammates keep the last valid file.` : `Couldn't save the file: ${coedit.error}`}</p> : null}
      {needed.map((setting) => <p key={setting.key} className="text-destructive">{setting.label} (<code>{setting.key}</code>): {setting.defaultText}</p>)}
      {about ? <p className="text-muted-foreground"><span className="font-medium text-foreground">{about.label}</span>: {about.explanation} Left out: {about.defaultText}</p> : null}
    </footer>
  </div>;
}

// Draws teammates' carets and the line that does not parse yet behind the file's transparent text.
function Overlay({ ref, text, carets, invalid = 0 }: { ref: Ref<HTMLDivElement>; text: string; carets: { at: number; name: string }[]; invalid?: number }) {
  const lines = text.split("\n"), start = invalid > 0 ? lines.slice(0, invalid - 1).join("\n").length + Math.min(1, invalid - 1) : -1, end = start + (lines[invalid - 1]?.length ?? 0);
  const cuts = [...new Set([0, ...carets.map(({ at }) => at), ...(start < 0 ? [] : [start, end]), text.length])].sort((a, b) => a - b);
  return <div ref={ref} aria-hidden className="pointer-events-none absolute inset-0 overflow-hidden p-3 [scrollbar-gutter:stable] font-mono text-xs leading-5 break-words whitespace-pre-wrap text-transparent">
    {cuts.slice(0, -1).map((cut, i) => <Fragment key={cut}>
      {carets.filter(({ at }) => at === cut).map(({ name }) => <span key={name} className="relative border-l-2 border-cyan-500"><span className="absolute -top-3.5 left-0 rounded bg-cyan-500 px-1 text-[9px] leading-3.5 text-white">{name}</span></span>)}
      <span className={cn(cut >= start && cut < end && "underline decoration-destructive decoration-wavy")}>{text.slice(cut, cuts[i + 1])}</span>
    </Fragment>)}
  </div>;
}
