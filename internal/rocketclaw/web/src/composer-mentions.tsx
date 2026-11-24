import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent, type RefObject } from "react";
import { useQuery } from "@tanstack/react-query";
import { rpc } from "./api";
import type { SlackNamesResponse } from "./types";

export type ComposerMention = { id: string; label: string; start: number; end: number };

export function matchesComposerMention(text: string, { start, end, label }: ComposerMention) {
  return text.slice(start, end) === label
    && !/[\p{L}\p{M}\p{N}_]$/u.test(text.slice(0, start))
    && !/^[\p{L}\p{M}\p{N}_]/u.test(text.slice(end));
}

export function useComposerMentions(text: string, tags: ComposerMention[], input: RefObject<HTMLTextAreaElement | null>, setText: (value: string, tags?: ComposerMention[]) => void) {
  const [caret, setCaret] = useState({ text, position: 0 }), [pick, setPick] = useState(0), [dismissed, setDismissed] = useState("");
  const selection = useRef<{ start: number; end: number } | undefined>(undefined);
  useEffect(() => {
    const field = input.current!;
    const capture = (event: InputEvent) => {
      selection.current = event.inputType.startsWith("history") ? undefined : { start: field.selectionStart, end: field.selectionEnd };
    };
    field.addEventListener("beforeinput", capture);
    return () => field.removeEventListener("beforeinput", capture);
  }, [input]);
  const cursor = caret.text === text ? caret.position : 0;
  const query = /(?:^|\s)@([^\s@<>]+)$/.exec(text.slice(0, cursor))?.[1] ?? "";
  const active = query !== "" && query !== dismissed;
  const names = useQuery({
    queryKey: ["slackPeople", query],
    queryFn: async ({ signal }) => Object.entries((await rpc<SlackNamesResponse>("SlackNames", { ids: [], query }, signal)).names ?? {}).sort(([, a], [, b]) => a.localeCompare(b)),
    enabled: active,
    retry: false,
  });
  const people = active ? names.data ?? [] : [];
  const index = pick % Math.max(people.length, 1);
  const remainingTags = (value: string, start: number, oldEnd: number) => {
    const delta = value.length - text.length;
    if (text.slice(0, start) !== value.slice(0, start) || text.slice(oldEnd) !== value.slice(oldEnd + delta)) return [];
    return tags.flatMap((tag) => tag.end <= start ? [tag] :
      tag.start >= oldEnd ? [{ ...tag, start: tag.start + delta, end: tag.end + delta }] : [])
      .filter((tag) => matchesComposerMention(value, tag));
  };

  const edit = (event: FormEvent<HTMLTextAreaElement>) => {
    const { value, selectionStart: caret } = event.currentTarget;
    const previous = selection.current;
    selection.current = undefined;
    const { inputType, data } = event.nativeEvent as InputEvent;
    const insertedAt = caret - (data?.length ?? 0);
    const start = insertedAt >= 0 && data && value.slice(insertedAt, caret) === data ? insertedAt : caret;
    if (!previous) {
      setText(value, (inputType === "insertReplacementText" || inputType === "insertCompositionText") && start < caret
        ? remainingTags(value, start, caret - (value.length - text.length)) : []);
      return;
    }
    setText(value, remainingTags(value, Math.min(previous.start, start),
      Math.max(previous.end, caret - (value.length - text.length))));
  };

  const select = ([id, name]: [string, string]) => {
    const start = cursor - query.length - 1;
    const label = `@${name.replace(/[\r\n]/g, " ")}`;
    const end = start + label.length;
    const value = text.slice(0, start) + label + " " + text.slice(cursor);
    setText(value, [...remainingTags(value, start, cursor), { id, label, start, end }]);
    setCaret({ text: value, position: end + 1 });
    requestAnimationFrame(() => {
      input.current?.focus();
      input.current?.setSelectionRange(end + 1, end + 1);
    });
  };

  const keyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (people.length === 0 || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return false;
    switch (event.key) {
      case "ArrowDown": setPick((pick) => pick + 1); break;
      case "ArrowUp": setPick(index + people.length - 1); break;
      case "Tab":
      case "Enter": select(people[index]); break;
      case "Escape": setDismissed(query); break;
      default: return false;
    }
    event.preventDefault();
    return true;
  };

  return {
    edit,
    update: (field: HTMLTextAreaElement) => { setCaret({ text: field.value, position: field.selectionStart }); setPick(0); setDismissed(""); },
    hasPeople: people.length !== 0,
    keyDown,
    menu: people.length > 0 && (
      <ul aria-label="People" className="absolute inset-x-0 bottom-full z-10 mb-2 max-h-52 overflow-y-auto rounded-xl border bg-popover px-1 py-1 text-popover-foreground shadow-md">
        {people.map((person, row) => <li key={person[0]}><button type="button" className={`w-full rounded px-2 py-1 text-left text-sm hover:bg-accent ${row === index ? "bg-accent" : ""}`} onMouseDown={(event) => event.preventDefault()} onClick={() => select(person)}>@{person[1]}</button></li>)}
      </ul>
    ),
  };
}
