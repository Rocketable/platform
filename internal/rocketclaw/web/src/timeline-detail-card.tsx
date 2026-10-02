import { useId, useRef, useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import { setTimelineRows, timelineCategories, timelineLevel, timelineLevels, useTimelineDetail, type TimelineCategory, type TimelinePlacement, type TimelineRows } from "./timeline-detail";

// Follows OpenCode 7440ff784408a8a095b58ef908de3fc6ee66ca66, packages/app/src/settings/timeline-detail.tsx.
export function TimelineDetailCard() {
  const rows = useTimelineDetail();
  const level = timelineLevel(rows);
  const heading = useId();
  // Advanced opens for Custom only on first render, so a match while editing does not close it.
  const [custom] = useState(!level);
  // Placement before an eye toggle hid a row, restored when it is shown again.
  const shown = useRef<Partial<Record<TimelineCategory, TimelinePlacement>>>({});
  const update = (id: TimelineCategory, row: TimelineRows[TimelineCategory]) => setTimelineRows({ ...rows, [id]: row });
  return (
    <div className="flex flex-col gap-3 border-b py-2">
      <div>
        <h3 id={heading} className="text-sm">Timeline detail</h3>
        <p className="text-sm text-muted-foreground">Choose how much detail appears in the session timeline.</p>
      </div>
      <Slider aria-labelledby={heading} min={0} max={timelineLevels.length - 1} step={1} value={[timelineLevels.findIndex(({ id }) => id === (level?.id ?? "compact"))]}
        getAriaValueText={() => level?.label ?? "Custom"} onValueChange={(value) => setTimelineRows(timelineLevels[(value as number[])[0]].rows)} />
      <p className="text-sm"><span className="font-medium">{level?.label ?? "Custom"}:</span> <span className="text-muted-foreground">{level?.description ?? "Uses advanced settings."}</span></p>
      <details open={custom}>
        <summary className="cursor-pointer text-sm font-medium">Advanced</summary>
        <table className="mt-2 w-full text-sm">
          <thead>
            <tr className="text-muted-foreground"><th className="text-left font-normal"><span className="sr-only">Activity</span></th><th className="w-20 font-normal">Group</th><th className="w-20 font-normal">Collapse</th></tr>
          </thead>
          <tbody>
            {timelineCategories.map(({ id, label }) => {
              const row = rows[id];
              const hidden = row.placement === "hidden";
              return (
                <tr key={id} className="border-t">
                  <td className="flex items-center gap-2 py-1">
                    <Button type="button" variant="ghost" size="icon-sm" aria-pressed={!hidden} aria-label={`${label} visibility`} onClick={() => {
                      if (!hidden) shown.current[id] = row.placement;
                      update(id, { ...row, placement: hidden ? shown.current[id] ?? "grouped" : "hidden" });
                    }}>{hidden ? <EyeOff /> : <Eye />}</Button>
                    {label}
                  </td>
                  <td className="text-center">{hidden ? null : <Switch aria-label={`${label} group`} checked={row.placement === "grouped"} onCheckedChange={(checked) => update(id, { ...row, placement: checked ? "grouped" : "separate" })} />}</td>
                  <td className="text-center">{hidden || !row.details ? null : <Switch aria-label={`${label} collapse`} checked={row.details === "collapsed"} onCheckedChange={(checked) => update(id, { ...row, details: checked ? "collapsed" : "expanded" })} />}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </details>
    </div>
  );
}
