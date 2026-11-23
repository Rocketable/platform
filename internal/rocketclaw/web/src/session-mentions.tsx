import { useQuery } from "@tanstack/react-query";
import { useState, type KeyboardEvent, type RefObject } from "react";
import { rpc } from "./api";
import { useServer } from "./navigation";
import type { SlackNamesResponse } from "./types";

export type SessionMention = { id: string; label: string; start: number; end: number };

export function shiftMentions(mentions: SessionMention[] | undefined, before: string, after: string) {
  if (!mentions?.length) return mentions;
  let start = 0;
  while (start < before.length && start < after.length && before[start] === after[start]) start++;
  let end = before.length, next = after.length;
  while (end > start && next > start && before[end - 1] === after[next - 1]) { end--; next--; }
  return mentions.flatMap((mention) => {
    if (mention.end <= start) return [mention];
    if (mention.start < end) return [];
    return [{ ...mention, start: mention.start + next - end, end: mention.end + next - end }];
  });
}

export function encodeMentions(text: string, selections: SessionMention[] | undefined) {
  const mentions = (selections ?? []).filter(({ start, end, label }) => text.slice(start, end) === `@${label}`)
    .sort((left, right) => right.start - left.start);
  for (const mention of mentions) text = text.slice(0, mention.start) + `<@${mention.id}>` + text.slice(mention.end);
  return mentions.length ? { text, mentionIds: mentions.map(({ id }) => id) } : { text };
}

export function useSessionMentions(
  sessionId: string, text: string, input: RefObject<HTMLTextAreaElement | null>,
  insert: (text: string, mention: SessionMention) => void,
) {
  const name = useServer();
  const [caret, setCaret] = useState(0);
  const [cursor, setCursor] = useState(0);
  const position = Math.min(caret, text.length);
  const trigger = /(?:^|\s)@([^\s@]*)$/.exec(text.slice(0, position))?.[1];
  const result = useQuery({
    queryKey: [{ server: name }, "mention-people", sessionId, trigger],
    queryFn: ({ signal }) => rpc<SlackNamesResponse>("SlackNames", { ids: [], sharedSessionId: sessionId, query: trigger }, signal, name),
    enabled: !!sessionId && trigger !== undefined,
    staleTime: 30_000,
    retry: false,
  });
  const people = Object.entries(result.data?.names ?? {}).sort(([, left], [, right]) => left.localeCompare(right)).slice(0, 8);
  const index = Math.min(cursor, people.length - 1);
  const update = (next: number) => { setCaret(next); setCursor(0); };
  const choose = ([id, label]: [string, string]) => {
    if (trigger === undefined) return;
    const start = position - trigger.length - 1, end = start + label.length + 1;
    insert(text.slice(0, start) + `@${label} ` + text.slice(position), { id, label, start, end });
    update(end + 1);
    requestAnimationFrame(() => input.current?.setSelectionRange(end + 1, end + 1));
  };
  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (!people.length || trigger === undefined) return false;
    switch (event.key) {
    case "ArrowDown": setCursor((index + 1) % people.length); break;
    case "ArrowUp": setCursor((index + people.length - 1) % people.length); break;
    case "Enter":
      if (event.shiftKey || event.metaKey || event.ctrlKey || event.altKey) return false;
      choose(people[index]); break;
    case "Tab": choose(people[index]); break;
    case "Escape": update(0); break;
    default: return false;
    }
    event.preventDefault();
    return true;
  };
  return { menu: <MentionChoices people={people} index={index} choose={choose} />, update, keyDown };
}

function MentionChoices({ people, index, choose }: {
  people: [string, string][]; index: number; choose: (person: [string, string]) => void;
}) {
  if (!people.length) return null;
  return <ul aria-label="Mention a teammate" className="absolute inset-x-0 bottom-full z-20 mb-2 max-h-56 overflow-auto rounded-2xl border bg-popover p-1 text-popover-foreground shadow-md">
    {people.map((person, position) => <li key={person[0]}>
      <button type="button" aria-current={position === index} className="block w-full rounded-xl px-3 py-2 text-left text-sm aria-current:bg-accent"
        onMouseDown={(event) => event.preventDefault()} onClick={() => choose(person)}>@{person[1]}</button>
    </li>)}
  </ul>;
}
