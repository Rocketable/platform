import { useState } from "react";
import { Check, Copy, Maximize2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogTrigger, DialogContent, DialogTitle } from "@/components/ui/dialog";

export function CodeBlock({ text, label = "Code", compact = false }: { text: string; label?: string; compact?: boolean }) {
  const [copied, setCopied] = useState<string>();
  const [error, setError] = useState(false);
  const actionProps = compact ? { variant: "outline", size: "default", className: "min-h-11" } as const : { variant: "ghost", size: "icon-sm" } as const;
  const copy = <Button type="button" {...actionProps} aria-label={`Copy ${label}`} title={`Copy ${label}`} onClick={async (event) => {
        const container = event.currentTarget.closest('[role="dialog"]') ?? document.body;
        setError(false);
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
          setCopied(text);
        } catch {
          setError(true);
        }
      }}>{copied === text && !error ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}<span hidden={!compact}>Copy</span></Button>;
  const status = <span role="status" className={error ? "text-xs text-destructive" : "sr-only"}>{error ? "Could not copy. Select and copy the text." : copied === text ? "Copied" : ""}</span>;
  return <Dialog>
    <div className={compact ? "flex min-w-0 items-center gap-2" : "my-2 min-w-0 max-w-full overflow-hidden rounded-md border bg-muted/30"}>
    <div className={compact ? "flex flex-wrap items-center gap-2" : "flex items-center justify-between gap-2 px-3 py-1"}>
      <span hidden={compact} className="mr-auto truncate text-xs text-muted-foreground">{label}</span>
      <DialogTrigger render={<Button type="button" {...actionProps} aria-label={`Expand ${label}`} title={`Expand ${label}`} />}><Maximize2 data-icon="inline-start" /><span hidden={!compact}>Preview</span></DialogTrigger>
      {copy}{status}
    </div>
    <pre hidden={compact} tabIndex={0} aria-label={label} className="max-h-64 overflow-auto border-t p-3 font-mono text-xs leading-5 whitespace-pre"><code>{text}</code></pre>
    </div>
    <DialogContent className="flex h-[calc(100dvh-2rem)] min-w-0 flex-col sm:max-w-[calc(100%-2rem)]" aria-describedby={undefined}>
      <div className="flex min-w-0 items-center gap-2 pr-10"><div className="mr-auto min-w-0 break-words"><DialogTitle>{label}</DialogTitle></div>{copy}{status}</div>
      <pre tabIndex={0} aria-label={label} className="min-h-0 flex-1 overflow-auto rounded-md border bg-muted/30 p-3 font-mono text-xs leading-5 whitespace-pre"><code>{text}</code></pre>
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

export function TranscriptText({ text }: { text: string }) {
  return <>{fencedParts(text).map((part) => part.label
    ? <CodeBlock key={part.start} text={part.text} label={part.label} />
    : <div key={part.start} className="whitespace-pre-wrap break-words">{part.text}</div>)}</>;
}
