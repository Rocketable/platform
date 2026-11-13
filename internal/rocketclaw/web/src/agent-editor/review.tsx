import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { queries } from "@/api";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { AgentDraftOpInput, AgentDraftReview } from "@/types";
import { lineDiff } from "./diff";
import { settingLabel } from "./history";

type Pick = "mine" | "theirs";

// The lines of after, and those of before it drops, marked + and -; strike crosses out the dropped ones.
export function DiffLines({ before, after, strike = false }: { before: string; after: string; strike?: boolean }) {
  return lineDiff(before.split("\n"), after.split("\n")).map(({ mark, text, key }) => <div key={key} className={cn("px-3 break-words whitespace-pre-wrap", mark === "+" && "bg-emerald-50 dark:bg-emerald-950/40", mark === "-" && "bg-red-50 dark:bg-red-950/40", mark === "-" && strike && "line-through")}>{`${mark} ${text}`}</div>);
}

export function FileDiff({ label, before, after }: { label: string; before: string; after: string }) {
  return <section aria-label={label} className="max-h-64 overflow-y-auto rounded-lg border py-1 font-mono text-xs leading-5"><DiffLines before={before} after={after} /></section>;
}

// The ops that take the file's side of each setting picked so, and its instruction changes when body is theirs.
function picked(review: AgentDraftReview, variant: string, picks: Record<string, Pick | undefined>): AgentDraftOpInput[] {
  const settings = (review.settings ?? []).filter(({ key, live = "", draft = "" }) => picks[key] === "theirs" && live !== draft && (key !== "mode" || live));
  return [...settings.map(({ key, live = "" }) => key === "mode" ? { kind: "set_mode", value: live } : { kind: "set_field", variant, key, value: live }),
    ...(picks[""] === "theirs" && review.splices?.length ? [{ kind: "edit_body", variant, splices: review.splices }] : [])];
}

// Keep mine starts on settings only the draft changed and Take theirs on those only the file changed; a setting both
// changed differently is flagged and waits for a pick. "" is the instructions, which start on the file's changes.
export function Review({ review, name, variant, onSave }: { review: AgentDraftReview; name: string; variant: string; onSave: (ops: AgentDraftOpInput[]) => void }) {
  const easy = useQuery(queries.agentSettings("easy")).data ?? [], advanced = useQuery(queries.agentSettings("advanced")).data ?? [];
  const settings = review.settings ?? [];
  const [picks, setPicks] = useState<Record<string, Pick | undefined>>(() => ({ "": "theirs", ...Object.fromEntries(settings.map((setting) => [setting.key, setting.both ? undefined : setting.theirs ? "theirs" : "mine"])) }));
  const rows: [string, string, string][] = [...settings.map(({ key, live, draft, both }): [string, string, string] => [key, settingLabel({ easy, advanced }, key), `${both ? "Changed in both. " : ""}File: ${live || "not set"} · Draft: ${draft || "not set"}`]),
    ...(review.splices?.length ? [["", "Instructions", "The file's changes to the instructions, placed around yours."] as [string, string, string]] : [])];
  return <>
    <DialogHeader>
      <DialogTitle>{name} changed since this draft began</DialogTitle>
      <DialogDescription>Someone changed the agent file this save would replace. Choose what to keep from your draft and what to take from the file, then save again.</DialogDescription>
    </DialogHeader>
    <FileDiff label="What changed in the file" before={review.base ?? ""} after={review.live ?? ""} />
    <div className="flex flex-col divide-y rounded-lg border">
      {rows.map(([key, title, note]) => <div key={key} className="flex items-center gap-3 px-3 py-2">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5 text-xs">
          <span className="text-sm font-medium">{title}</span>
          <span className={cn("break-all", note.startsWith("Changed in both") ? "text-amber-800 dark:text-amber-300" : "text-muted-foreground")}>{note}</span>
        </div>
        <ButtonGroup>
          {(["mine", "theirs"] as const).map((pick) => <Button key={pick} size="xs" variant={picks[key] === pick ? "secondary" : "outline"} aria-pressed={picks[key] === pick} aria-label={`${pick === "mine" ? "Keep mine" : "Take theirs"} for ${title}`} onClick={() => setPicks({ ...picks, [key]: pick })}>{pick === "mine" ? "Keep mine" : "Take theirs"}</Button>)}
        </ButtonGroup>
      </div>)}
    </div>
    <DialogFooter>
      <Button variant="outline" onClick={() => onSave([])}>Keep my draft as is</Button>
      <Button disabled={Object.values(picks).includes(undefined)} onClick={() => onSave(picked(review, variant, picks))}>Bring in and save</Button>
    </DialogFooter>
  </>;
}
