import { CodeView, cleanLastNewline, parsePatchFiles, type CodeViewItem, type FileDiffMetadata, type SelectedLineRange } from "@pierre/diffs";
import { FileTree, useFileTree } from "@pierre/trees/react";
import { FolderTree } from "lucide-react";
import { useEffect, useEffectEvent, useMemo, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import type { GitHubPR, PRFile } from "./github-pr";
import { PrivatePreviewRoot, type PRComposer } from "./github-pr-preview";
import { setPreference, usePreference } from "./transcript-text";

const unavailable = "Patch unavailable (binary, too large, or omitted by GitHub).";
const TREE_KEY = "pull-request-file-tree";

// Layout reference: t3code apps/web/src/components/pullRequest/PullRequestCodeTab.tsx
// at 1d61dd3: one diff scroller, with the file tree on its right and below it on narrow panes.
export function PRCode({ files, more, reference, composer }: { files: PRFile[]; more?: () => void; reference: GitHubPR; composer: PRComposer }) {
  const [style, setStyle] = useState<"unified" | "split">("unified");
  const tree = usePreference(TREE_KEY) === "true";
  const host = useRef<HTMLSpanElement>(null);
  const viewer = useRef<CodeView | null>(null);
  const [end] = useState(() => document.createElement("span"));
  const note = useDiffNote(viewer, reference, composer);
  const select = useEffectEvent((range: SelectedLineRange | null, { item }: { item: CodeViewItem<undefined> }) => {
    if (range && item.type === "diff" && composer.status !== "none") note.open({ id: item.id, diff: item.fileDiff, range });
  });
  const reachEnd = useEffectEvent(() => more?.());
  const armed = more !== undefined;
  useEffect(() => {
    if (!armed) return;
    const observer = new IntersectionObserver(([entry]) => { if (entry.isIntersecting) reachEnd(); }, { root: host.current, rootMargin: "240px" });
    observer.observe(end);
    return () => observer.disconnect();
  }, [armed, end]);
  const paths = useMemo(() => files.map((file) => file.filename), [files]);
  const { model } = useFileTree({ paths, initialExpansion: "open", onSelectionChange: (selected) => {
    const path = selected.at(-1);
    if (path && !model.getItem(path)!.isDirectory()) viewer.current!.scrollTo({ type: "item", id: path, align: "start" });
  } });
  useEffect(() => { model.resetPaths(paths); }, [model, paths]);
  useEffect(() => {
    const instance = new CodeView();
    viewer.current = instance;
    instance.setup(host.current!);
    return () => instance.cleanUp();
  }, []);
  useEffect(() => {
    const instance = viewer.current!;
    const items = files.map<CodeViewItem<undefined>>((file) => {
      const existing = instance.getItem(file.filename);
      if (existing) return existing;
      try {
        if (file.patch) {
          // GitHub returns hunk text without file headers. Fixed names keep path text out of the patch grammar.
          const diff = parsePatchFiles(`--- file\n+++ file\n${file.patch}\n`, { throwOnError: true })[0].files[0];
          diff.name = file.filename;
          diff.prevName = file.previous_filename;
          if (file.status === "added") diff.type = "new";
          else if (file.status === "removed") diff.type = "deleted";
          else if (file.previous_filename) diff.type = "rename-changed";
          return { id: file.filename, type: "diff", fileDiff: diff };
        }
      } catch {}
      return { id: file.filename, type: "file", file: { name: file.filename, contents: unavailable, lang: "text" } };
    });
    instance.setItems(items);
  }, [files]);
  useEffect(() => {
    const options = { diffStyle: style, theme: { light: "pierre-light", dark: "pierre-dark" }, stickyHeaders: true, enableLineSelection: true, renderCodeViewFooter: () => end,
      onLineSelectionEnd: (range: SelectedLineRange | null, context: { item: CodeViewItem<undefined> }) => select(range, context), renderAnnotation: () => note.slot } as const;
    const applyTheme = () => viewer.current!.setOptions({ ...options, themeType: document.documentElement.classList.contains("dark") ? "dark" : "light" });
    applyTheme();
    const observer = new MutationObserver(applyTheme);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, [style, end, note.slot]);
  return <span className="@container/pr-code flex min-h-0 min-w-0 flex-1 flex-col">
    {note.box}
    <span className="flex shrink-0 items-center justify-between gap-2 border-y px-4 py-2">
      <span className="text-xs text-muted-foreground">Changed files</span>
      <span className="flex items-center gap-2">
        <Select value={style} onValueChange={(value) => setStyle(value as "unified" | "split")}>
          <SelectTrigger aria-label="Diff layout"><SelectValue>{style === "unified" ? "Unified" : "Split"}</SelectValue></SelectTrigger>
          <SelectContent render={<PrivatePreviewRoot />}><SelectGroup><SelectItem value="unified">Unified</SelectItem><SelectItem value="split">Split</SelectItem></SelectGroup></SelectContent>
        </Select>
        <Button type="button" variant={tree ? "secondary" : "ghost"} size="icon-sm" aria-label="File tree" title="File tree" aria-pressed={tree} onClick={() => setPreference(TREE_KEY, String(!tree))}><FolderTree /></Button>
      </span>
    </span>
    <span className="flex min-h-0 min-w-0 flex-1 flex-col @min-[36rem]/pr-code:flex-row">
      <span ref={host} role="region" aria-label="File patch" tabIndex={0} className="block h-[26rem] min-h-48 min-w-0 flex-1 overflow-auto [contain:layout_style]" />
      {tree && <span role="region" aria-label="Changed files" className="block h-44 shrink-0 overflow-hidden border-t @min-[36rem]/pr-code:h-[26rem] @min-[36rem]/pr-code:w-[35%] @min-[36rem]/pr-code:border-t-0 @min-[36rem]/pr-code:border-l">
        <FileTree model={model} aria-label="Pull request files" style={{ height: "100%" }} />
      </span>}
    </span>
  </span>;
}

type Note = { id: string; diff: FileDiffMetadata; range: SelectedLineRange };

function useDiffNote(viewer: RefObject<CodeView | null>, reference: GitHubPR, composer: PRComposer) {
  const [note, setNote] = useState<Note>();
  const [slot] = useState(() => document.createElement("div"));
  const annotate = (id: string, range?: SelectedLineRange) => {
    const item = viewer.current!.getItem(id) as Extract<CodeViewItem<undefined>, { type: "diff" }>;
    viewer.current!.updateItem({ ...item, annotations: range ? [{ side: range.endSide ?? range.side ?? "additions", lineNumber: range.end }] : [], version: (item.version ?? 0) + 1 });
  };
  const open = (next: Note) => {
    if (note) annotate(note.id);
    annotate(next.id, next.range);
    setNote(next);
  };
  const close = () => {
    annotate(note!.id);
    viewer.current!.clearSelectedLines();
    setNote(undefined);
  };
  if (!note || composer.status === "none") return { slot, open, box: null };
  const side = note.range.side ?? "additions";
  const range = { start: Math.min(note.range.start, note.range.end), end: Math.max(note.range.start, note.range.end), side };
  const add = (text: string) => {
    composer.add(reference, { note: text, path: note.id, range, lines: selectedLines(note.diff, note.range) });
    close();
  };
  const box = <NoteBox key={JSON.stringify(note.range)} label={`${note.id} lines ${range.start}-${range.end}`} ready={composer.status === "ready"} add={add} cancel={close} />;
  return { slot, open, box: createPortal(box, slot) };
}

function selectedLines(diff: FileDiffMetadata, range: SelectedLineRange) {
  const rows = diff.hunks.flatMap((hunk) => {
    let deletions = hunk.deletionStart, additions = hunk.additionStart;
    return hunk.hunkContent.flatMap((part): { text: string; deletions?: number; additions?: number }[] => part.type === "context"
      ? Array.from({ length: part.lines }, (_, at) => ({ text: ` ${cleanLastNewline(diff.additionLines[part.additionLineIndex + at])}`, deletions: deletions++, additions: additions++ }))
      : [...Array.from({ length: part.deletions }, (_, at) => ({ text: `-${cleanLastNewline(diff.deletionLines[part.deletionLineIndex + at])}`, deletions: deletions++ })),
        ...Array.from({ length: part.additions }, (_, at) => ({ text: `+${cleanLastNewline(diff.additionLines[part.additionLineIndex + at])}`, additions: additions++ }))]);
  });
  const side = range.side ?? "additions";
  const [first, last] = [rows.findIndex((row) => row[side] === range.start), rows.findIndex((row) => row[range.endSide ?? side] === range.end)].sort((a, b) => a - b);
  return rows.slice(first, last + 1).map((row) => row.text);
}

function NoteBox({ label, ready, add, cancel }: { label: string; ready: boolean; add: (note: string) => void; cancel: () => void }) {
  const [note, setNote] = useState("");
  const input = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const frame = requestAnimationFrame(() => input.current?.focus({ preventScroll: true }));
    return () => cancelAnimationFrame(frame);
  }, []);
  const enabled = ready && note.trim() !== "";
  const confirm = () => { if (enabled) add(note.trim()); };
  return <div className="flex flex-col gap-1.5 border-y bg-background px-3 py-2 font-sans text-foreground caret-foreground" onPointerDown={(event) => event.stopPropagation()}>
    <Textarea ref={input} aria-label="Note" placeholder={`Note on ${label}`} value={note} onChange={(event) => setNote(event.target.value)} onKeyDown={(event) => {
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); cancel(); }
      if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) { event.preventDefault(); confirm(); }
    }} />
    <span className="flex items-center justify-end gap-1">
      <span className="mr-auto text-xs text-muted-foreground">⌘/Ctrl Enter to add</span>
      <Button type="button" variant="ghost" size="xs" onClick={cancel}>Cancel</Button>
      <Button type="button" size="xs" disabled={!enabled} onClick={confirm}>Add to agent</Button>
    </span>
  </div>;
}
