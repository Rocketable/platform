// History: every change to the draft by person and field, each of which can be undone for everyone by restoring the
// draft as it was before it.
import { useState } from "react";
import { RotateCcw, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { AgentDraftOp, AgentSettingHelp } from "@/types";
import type { CoEdit } from "./coedit";

type Help = Record<"easy" | "advanced", AgentSettingHelp[]>;

const kinds: Record<string, string> = { edit_body: "Instructions", add_variant: "a new variant", rename_variant: "a variant's name", remove_variant: "a removed variant", set_mode: "Easy or advanced", set_external_mcp: "External MCP testing", set_test_chat: "the test chat", set_save_state: "saving", rename: "the agent's name" };
const label = (help: Help, key: string) => [...help.easy, ...help.advanced].find((setting) => setting.key === key)?.label ?? key;
const field = (help: Help, op: AgentDraftOp) => op.kind === "set_field" ? label(help, op.key!) : kinds[op.kind];

// Runs of ops by one person to one field read as one change.
function changes(log: AgentDraftOp[]) {
  const runs: AgentDraftOp[][] = [];
  for (const op of log) {
    const last = runs.at(-1)?.[0];
    if (last && last.author === op.author && last.kind === op.kind && last.variant === op.variant && last.key === op.key) runs.at(-1)!.push(op);
    else runs.push([op]);
  }
  return runs.reverse();
}

export function History({ coedit, help, restore }: { coedit: CoEdit; help: Help; restore: (from: string) => void }) {
  const several = (coedit.view?.variants.length ?? 0) > 1;
  return <ol aria-label="History" className="flex min-h-0 flex-1 flex-col divide-y overflow-y-auto">
    {changes(coedit.log).map(([first, ...rest]) => {
      const what = `${field(help, first)}${several && first.variant ? ` (variant ${first.variant})` : ""}`, who = first.author?.split("@")[0] ?? "";
      return <li key={first.revision} className="flex items-center gap-2 px-4 py-2 text-xs">
        <span className="min-w-0 flex-1"><span className="font-medium">{who}</span> {first.kind === "create" ? "started the draft" : <>changed <span className="font-medium">{what}</span></>}
          <span className="block text-muted-foreground">{new Date((rest.at(-1) ?? first).at!).toLocaleString()}{rest.length ? ` · ${rest.length + 1} edits` : ""}</span></span>
        {Number(first.revision) > 1 ? <Button size="xs" variant="outline" aria-label={`Restore the draft from before ${who} changed ${what}`} onClick={() => restore(String(Number(first.revision) - 1))}><RotateCcw />Restore</Button> : null}
      </li>;
    })}
  </ol>;
}

// After a switch between easy and advanced, which settings went by the server's rule (the help lists them only for
// the other mode, or, for easy, not at all) and which required ones the new mode still needs.
export function ModeBanner({ coedit, help, restore }: { coedit: CoEdit; help: Help; restore: (from: string) => void }) {
  const [dismissed, setDismissed] = useState<string>();
  const change = coedit.modeChange, mode = change?.op.value as keyof Help | undefined;
  if (!change || !mode || dismissed === change.op.revision) return null;
  const allowed = (key: string, of: keyof Help) => help[of].some((setting) => setting.key === key);
  const dropped = [...new Set(change.before.variants.flatMap((variant) => variant.settings.map(({ key }) => key)))].filter((key) => !allowed(key, mode) && (mode === "easy" || allowed(key, "easy")));
  const needed = help[mode].filter((setting) => setting.required && !coedit.view!.variants[0].settings.some(({ key }) => key === setting.key));
  return <div role="status" className="flex items-start gap-2 border-b bg-muted/40 px-4 py-2 text-xs">
    <p className="flex-1">{change.op.author?.split("@")[0]} switched the whole draft to <b>{mode}</b>. {dropped.length ? `Dropped: ${dropped.map((key) => label(help, key)).join(", ")}.` : "Nothing was dropped."}
      {needed.length ? ` Now needed: ${needed.map((setting) => setting.label).join(", ")}.` : ""}</p>
    <Button size="xs" variant="outline" onClick={() => restore(String(Number(change.op.revision) - 1))}><RotateCcw />Restore</Button>
    <Button size="icon-xs" variant="ghost" aria-label="Dismiss" onClick={() => setDismissed(change.op.revision)}><X /></Button>
  </div>;
}
