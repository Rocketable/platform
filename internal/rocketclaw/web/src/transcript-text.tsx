import { Fragment, useState, useSyncExternalStore, type ReactNode } from "react";
import { captureException } from "@sentry/react";
import { useQuery, useQueries } from "@tanstack/react-query";
import { Check, Copy, Maximize2, WrapText } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogTrigger, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { ScrollArea, ScrollBar } from "@/components/ui/scroll-area";
import { queries } from "./api";

export async function copyText(text: string, container: Element) {
  try {
    if (navigator.clipboard) {
      await navigator.clipboard.writeText(text);
    } else {
      // RocketClaw also serves plain HTTP, where the Clipboard API is unavailable.
      const input = document.createElement("textarea");
      input.value = text;
      input.style.position = "fixed";
      input.style.opacity = "0";
      const focus = document.activeElement as HTMLElement;
      container.append(input);
      try {
        input.select();
        if (!document.execCommand("copy")) throw new Error("Copy failed");
      } finally {
        input.remove();
        focus.focus({ preventScroll: true });
      }
    }
  } catch (error) {
    captureException(error);
    throw error;
  }
}

// One browser-wide wrap preference, shared by every mounted block like the timeline detail store.
const WRAP_KEY = "code-wrap";
const wrapListeners = new Set<() => void>();
let wrapped: boolean | undefined;

function publishWrap(next: boolean) {
  wrapped = next;
  for (const listener of wrapListeners) listener();
}

function onWrapStorage(event: StorageEvent) {
  if (event.key === WRAP_KEY) publishWrap(event.newValue === "true");
}

function subscribeWrap(listener: () => void) {
  wrapListeners.add(listener);
  window.addEventListener("storage", onWrapStorage);
  return () => {
    wrapListeners.delete(listener);
  };
}

function highlight(text: string, needle?: string): ReactNode {
  if (!needle) return text;
  const lower = text.toLowerCase(), parts: ReactNode[] = [];
  for (let position = 0;;) {
    const match = lower.indexOf(needle, position);
    if (match < 0) {
      parts.push(text.slice(position));
      return parts;
    }
    parts.push(text.slice(position, match), <mark key={match} className="rounded-sm bg-primary/20 text-foreground ring-1 ring-primary/30">{text.slice(match, match + needle.length)}</mark>);
    position = match + needle.length;
  }
}

export function CodeBlock({ text, label = "Code", compact = false, needle }: { text: string; label?: string; compact?: boolean; needle?: string }) {
  const wrap = useSyncExternalStore(subscribeWrap, () => wrapped ??= localStorage.getItem(WRAP_KEY) === "true", () => false);
  const [copied, setCopied] = useState<string>();
  const [error, setError] = useState(false);
  const actionProps = compact ? { variant: "outline", size: "default", className: "min-h-11" } as const : { variant: "ghost", size: "icon-sm" } as const;
  const copy = <Button type="button" {...actionProps} aria-label={`Copy ${label}`} title={`Copy ${label}`} onClick={async (event) => {
        const container = event.currentTarget.closest('[role="dialog"]') ?? document.body;
        setError(false);
        try {
          await copyText(text, container);
          setCopied(text);
        } catch {
          setError(true);
        }
      }}>{copied === text && !error ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}<span hidden={!compact}>Copy</span></Button>;
  const status = <span role="status" className={error ? "text-xs text-destructive" : "sr-only"}>{error ? "Could not copy. Select and copy the text." : copied === text ? "Copied" : ""}</span>;
  const wrapToggle = <Button type="button" {...actionProps} variant={wrap ? "secondary" : actionProps.variant} aria-label={`Wrap ${label}`} title={`Wrap ${label}`} aria-pressed={wrap} onClick={() => {
        localStorage.setItem(WRAP_KEY, String(!wrap));
        publishWrap(!wrap);
      }}><WrapText data-icon="inline-start" /><span hidden={!compact}>Wrap</span></Button>;
  const pre = <pre aria-label={label} className={`${wrap ? "whitespace-pre-wrap wrap-anywhere" : "whitespace-pre"} p-3 font-mono text-xs leading-5`}><code>{highlight(text, needle)}</code></pre>;
  const scrollBar = wrap ? null : <ScrollBar orientation="horizontal" />;
  return <Dialog>
    <div className={compact ? "flex min-w-0 items-center gap-2" : "my-2 min-w-0 max-w-full overflow-hidden rounded-md border bg-muted/30"}>
    <div className={compact ? "flex flex-wrap items-center gap-2" : "flex items-center justify-between gap-2 border-b px-3 py-1"}>
      <span hidden={compact} className="mr-auto truncate text-xs text-muted-foreground">{label}</span>
      {compact ? null : wrapToggle}
      <DialogTrigger render={<Button type="button" {...actionProps} aria-label={`Expand ${label}`} title={`Expand ${label}`} />}><Maximize2 data-icon="inline-start" /><span hidden={!compact}>Preview</span></DialogTrigger>
      {copy}{status}
    </div>
    <ScrollArea hidden={compact} className="flex max-h-64 flex-col">{pre}{scrollBar}</ScrollArea>
    </div>
    <DialogContent className="flex h-[calc(100dvh-2rem)] min-w-0 flex-col sm:max-w-[calc(100%-2rem)]" aria-describedby={undefined}>
      <div className="flex min-w-0 items-center gap-2 pr-10"><div className="mr-auto min-w-0 break-words"><DialogTitle>{label}</DialogTitle></div>{wrapToggle}{copy}{status}</div>
      <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-md border bg-muted/30"><ScrollArea className="flex min-h-0 flex-1 flex-col">{pre}{scrollBar}</ScrollArea></div>
    </DialogContent>
  </Dialog>;
}

function fencedParts(text: string) {
  const parts: { start: number; text: string; label?: string }[] = [];
  let prose = "", code = "", fence = "", indent = 0, label = "", start = 0;
  for (const match of text.matchAll(/[^\n]*\n|[^\n]+$/g)) {
    const line = match[0];
    const content = line.replace(/\r?\n$/, "");
    if (fence) {
      const closing = /^ {0,3}(`+|~+)[\t ]*$/.exec(content);
      if (closing && closing[1][0] === fence[0] && closing[1].length >= fence.length) {
        parts.push({ start, text: code, label });
        start = match.index + line.length;
        fence = "";
      } else {
        code += line.replace(new RegExp(`^ {0,${indent}}`), "");
      }
    } else {
      const opening = /^( {0,3})(`{3,}|~{3,})(.*)$/.exec(content);
      if (opening && !(opening[2][0] === "`" && opening[3].includes("`"))) {
        if (prose) parts.push({ start, text: prose });
        start = match.index;
        prose = "";
        code = "";
        indent = opening[1].length;
        fence = opening[2];
        label = opening[3].trim().split(/\s+/)[0] || "Code";
      } else {
        prose += line;
      }
    }
  }
  if (fence) parts.push({ start, text: code, label });
  if (prose) parts.push({ start, text: prose });
  return parts;
}

function tagSpan(text: string, needle: string | undefined, key?: number) {
  return <span key={key} className="font-medium text-primary">{highlight(text, needle)}</span>;
}

function SlackTag({ id, sigil, needle }: { id: string; sigil: "@" | "#"; needle?: string }) {
  const name = useQuery<string | undefined>(queries.slackName(id)).data;
  return tagSpan(sigil + (name ?? id), needle);
}

// Markdown links, Slack links, bare URLs, Slack tags, inline code, then bold/italic/strike; only http(s) URLs match.
const inlinePattern = /\[([^\]\n]+)\]\((?:<(https?:\/\/[^>\s]+)>|(https?:\/\/(?:[^()\s]|\([^()\s]*\))+))\)|<(https?:\/\/[^|>\s]+)(?:\|([^>\n]+))?>|(?<!\]\(<?|<)(https?:\/\/[^\s<>]*[^\s<>.,;:!?'")\]*])|<@([UW][A-Z0-9]+)(?:\|([^>\n]+))?>|<#(C[A-Z0-9]+)(?:\|([^>\n]+))?>|<!subteam\^(S[A-Z0-9]+)(?:\|([^>\n]+))?>|<!(here|channel|everyone)>|`([^`\n]+)`|(?<![\w*/])\*\*([^\s*](?:[^\n]*?[^\s*])?)\*\*(?![\w*/])|(?<![\w*/])\*([^\s*](?:[^*\n]*?[^\s*])?)\*(?![\w*/])|(?<![\w_])_([^\s_](?:[^_\n]*?[^\s_])?)_(?![\w_])|(?<![\w~])~([^\s~](?:[^~\n]*?[^\s~])?)~(?![\w~])/g;

function inlineNodes(text: string, needle?: string, interactive = true): ReactNode[] {
  const nodes: ReactNode[] = [];
  let last = 0;
  for (const match of text.matchAll(inlinePattern)) {
    const [, mdLabel, angled, mdUrl, slackUrl, slackLabel, bare, userId, userLabel, channelId, channelLabel, teamId, teamLabel, broadcast, code, strong, single, em, strike] = match;
    const href = (angled ?? mdUrl ?? slackUrl ?? bare)?.replaceAll("&amp;", "&");
    const key = match.index;
    nodes.push(highlight(text.slice(last, match.index), needle));
    if (href) {
      const label = mdLabel ?? slackLabel ?? href;
      nodes.push(interactive ? <a key={key} href={href} className="underline" target="_blank" rel="noopener noreferrer">{highlight(label, needle)}</a> : highlight(label, needle));
    } else if (userId) nodes.push(userLabel ? tagSpan(userLabel[0] === "@" ? userLabel : "@" + userLabel, needle, key) : <SlackTag key={key} id={userId} sigil="@" needle={needle} />);
    else if (channelId) nodes.push(channelLabel ? tagSpan("#" + channelLabel, needle, key) : <SlackTag key={key} id={channelId} sigil="#" needle={needle} />);
    else if (teamId) nodes.push(teamLabel ? tagSpan(teamLabel, needle, key) : <SlackTag key={key} id={teamId} sigil="@" needle={needle} />);
    else if (broadcast) nodes.push(tagSpan("@" + broadcast, needle, key));
    else if (code) nodes.push(<code key={key} className="rounded-sm bg-muted px-1 font-mono text-[0.9em]">{highlight(code, needle)}</code>);
    else if (em) nodes.push(<em key={key}>{inlineNodes(em, needle, interactive)}</em>);
    else if (strike) nodes.push(<s key={key}>{inlineNodes(strike, needle, interactive)}</s>);
    else nodes.push(<strong key={key}>{inlineNodes(strong ?? single, needle, interactive)}</strong>);
    last = match.index + match[0].length;
  }
  nodes.push(highlight(text.slice(last), needle));
  return nodes;
}

export function cleanText(text: string, names: Record<string, string>): string {
  let out = "", last = 0;
  for (const match of text.matchAll(inlinePattern)) {
    const [, mdLabel, angled, mdUrl, slackUrl, slackLabel, bare, userId, userLabel, channelId, channelLabel, teamId, teamLabel, broadcast, code, strong, single, em, strike] = match;
    const href = (angled ?? mdUrl ?? slackUrl ?? bare)?.replaceAll("&amp;", "&");
    out += text.slice(last, match.index);
    if (href) out += mdLabel ?? slackLabel ?? href;
    else if (userId) out += userLabel ? (userLabel[0] === "@" ? userLabel : "@" + userLabel) : "@" + (names[userId] ?? userId);
    else if (channelId) out += "#" + (channelLabel ?? names[channelId] ?? channelId);
    else if (teamId) out += teamLabel ?? "@" + (names[teamId] ?? teamId);
    else if (broadcast) out += "@" + broadcast;
    else if (code) out += code;
    else out += cleanText(strong ?? single ?? em ?? strike, names);
    last = match.index + match[0].length;
  }
  return out + text.slice(last);
}

export function useCleanText(text: string): string {
  const ids = [...new Set([...text.matchAll(/<@([UW][A-Z0-9]+)>|<#(C[A-Z0-9]+)>|<!subteam\^(S[A-Z0-9]+)>/g)].map((match) => match[1] ?? match[2] ?? match[3]))];
  const results = useQueries({ queries: ids.map((id) => queries.slackName(id)) });
  const names: Record<string, string> = {};
  for (const [index, id] of ids.entries()) {
    const name = results[index].data;
    if (name) names[id] = name;
  }
  return cleanText(text, names);
}

function blockNodes(text: string, needle?: string, interactive = true): ReactNode[] {
  const nodes: ReactNode[] = [];
  let kind = "", start = 1, items: { at: number; text: string }[] = [];
  const flush = () => {
    if (!kind) return;
    const inner = (value: string) => inlineNodes(value, needle, interactive);
    const at = items[0].at, list = () => items.map((item) => <li key={item.at}>{inner(item.text)}</li>);
    const joined = () => items.map((item) => item.text).join(kind === "quote" ? "\n" : "");
    if (kind === "quote") nodes.push(<blockquote key={at} className="border-l-2 ps-3 text-muted-foreground">{inner(joined())}</blockquote>);
    else if (kind === "ul") nodes.push(<ul key={at} className="list-disc ps-5">{list()}</ul>);
    else if (kind === "ol") nodes.push(<ol key={at} start={start} className="list-decimal ps-5">{list()}</ol>);
    else nodes.push(<Fragment key={at}>{inner(joined())}</Fragment>);
    kind = "";
    items = [];
  };
  for (const match of text.matchAll(/[^\n]*\n|[^\n]+$/g)) {
    const line = match[0], body = line.replace(/\r?\n$/, "");
    const quote = /^[\t ]*(?:>|&gt;) (.*)$/.exec(body);
    const ul = quote ? undefined : /^[\t ]*(?:[-*•]) (.*)$/.exec(body);
    const ol = quote || ul ? undefined : /^[\t ]*(\d+)\. (.*)$/.exec(body);
    const next = quote ? "quote" : ul ? "ul" : ol ? "ol" : "p";
    if (next !== kind) {
      flush();
      if (ol) start = Number(ol[1]);
    }
    kind = next;
    items.push({ at: match.index, text: quote?.[1] ?? ul?.[1] ?? ol?.[2] ?? line });
  }
  flush();
  return nodes;
}

export function InlineText({ text, needle }: { text: string; needle?: string }) {
  return inlineNodes(text.split("\n", 1)[0], needle, false);
}

export function TranscriptText({ text, needle, interactive = true }: { text: string; needle?: string; interactive?: boolean }) {
  return <>{fencedParts(text).map((part) => part.label
    ? interactive
      ? <CodeBlock key={part.start} text={part.text} label={part.label} needle={needle} />
      : <pre key={part.start} aria-label={part.label} className="my-2 max-w-full whitespace-pre-wrap wrap-anywhere rounded-md border bg-muted/30 p-3 font-mono text-xs leading-5"><code>{highlight(part.text, needle)}</code></pre>
    : <div key={part.start} className="whitespace-pre-wrap break-words">{blockNodes(part.text, needle, interactive)}</div>)}</>;
}
