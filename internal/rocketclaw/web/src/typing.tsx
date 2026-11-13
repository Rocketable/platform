import { Fragment, useEffect, useState } from "react";
import { serverURL } from "./navigation";

// This tab's reports leave at once, so one that hangs holds back nothing, and are numbered: the server drops one that
// arrives after a later one, so a false is never undone by an earlier true. They go by plain fetch: a refused or
// failed report changes nothing on screen and is not worth a Sentry report.
const tab = crypto.getRandomValues(new Uint32Array(4)).join("-");
let sent = 0;
// When this tab last reported typing in each chat, by server and chat; a chat is absent once it reported false.
const reported = new Map<string, number>();

// reportTyping tells server whether this person is typing in chat id: typing on the first keystroke, then at most
// every 2 s while it goes on, and not typing once when the draft empties after that. The draft's text never travels.
export function reportTyping(server: string, id: string, typing: boolean) {
  const key = JSON.stringify([server, id]), last = reported.get(key);
  if (!id || (typing ? last !== undefined && Date.now() - last < 2_000 : last === undefined)) return;
  if (typing) reported.set(key, Date.now());
  else reported.delete(key);
  sent += 1;
  void fetch(`${serverURL(server)}/api/ReportTyping`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, typing, tab, seq: sent }) }).catch(() => undefined);
}

// useTypists names the other people typing in chat id, in the order they started. A typist leaves on their false,
// or 5 s after their last true. Every event and timer compares the times again, so a timer a background tab runs
// late cannot keep a typist past the next event.
function useTypists(server: string, id: string) {
  const [typists, setTypists] = useState(() => new Map<string, number>());
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const stream = new EventSource(`${serverURL(server)}/typing?${new URLSearchParams({ id })}`);
    stream.onmessage = (event) => {
      const typist = JSON.parse(String(event.data)) as { principal: string; typing: boolean };
      if (!typist.principal) return;
      const at = Date.now();
      setNow(at);
      setTypists((current) => {
        const next = new Map(current);
        // A typist back after they disappeared starts again, after whoever started meanwhile.
        if (!typist.typing || at - (current.get(typist.principal) ?? -Infinity) >= 5_000) next.delete(typist.principal);
        if (typist.typing) next.set(typist.principal, at);
        return next;
      });
    };
    return () => stream.close();
  }, [server, id]);
  const shown = [...typists].filter(([, at]) => now - at < 5_000);
  const expires = Math.min(...shown.map(([, at]) => at + 5_000));
  useEffect(() => {
    if (expires === Infinity) return;
    const timer = setTimeout(() => setNow(Date.now()), expires - Date.now());
    return () => clearTimeout(timer);
  }, [expires, now]);
  return shown.map(([name]) => name);
}

function Typists({ server, id, where }: { server: string; id: string; where: string }) {
  const names = useTypists(server, id);
  if (!names.length) return null;
  // Four or more name the first two: "Ana, Cy and 2 others are typing".
  const named = names.length > 3 ? names.slice(0, 2) : names;
  const others = names.length > 3 ? ` and ${names.length - 2} others` : "";
  return <span className="flex max-w-full items-center gap-1.5 rounded-full bg-foreground py-1 pr-3 pl-1 text-xs text-background shadow-md">
    <span aria-hidden="true" className="grid size-5 shrink-0 place-items-center rounded-full bg-background font-semibold text-foreground">{[...names[0]][0]}</span>
    <span className="flex min-w-0 whitespace-pre">
      {named.map((name, index) => <Fragment key={name}><span className="min-w-0 truncate">{name}</span>{index < named.length - 1 ? <span className="shrink-0">{index === named.length - 2 && !others ? " and " : ", "}</span> : null}</Fragment>)}
      <span className="shrink-0">{others}{names.length > 1 ? " are typing" : " is typing"}{where}</span>
    </span>
    <span aria-hidden="true" className="flex shrink-0 gap-0.5">
      {[0, 150, 300].map((delay) => <span key={delay} style={{ animationDelay: `${delay}ms` }} className="size-1 animate-pulse rounded-full bg-current motion-reduce:animate-none" />)}
    </span>
  </span>;
}

// TypingPill floats over the top of a chat's transcript and shows who else is typing there. Its live region stays
// while the chat is mounted, because screen readers can skip one inserted with its text in place; it watches only
// while the chat is shown. top places it below whatever the chat shows above its transcript; main says that chat id
// is the main session of a subagent's on screen, which takes no prompts.
export function TypingPill({ server, id, shown, top = "top-3", main = false }: { server: string; id: string; shown: boolean; top?: string; main?: boolean }) {
  return <div role="status" aria-live="polite" className={`pointer-events-none absolute inset-x-3 ${top} z-10 flex justify-center`}>
    {shown && id ? <Typists key={`${server} ${id}`} server={server} id={id} where={main ? " in the main session" : ""} /> : null}
  </div>;
}
