import { CodeView, parsePatchFiles, type CodeViewItem } from "@pierre/diffs";
import { FileTree, useFileTree } from "@pierre/trees/react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { PRFile } from "./github-pr";
import { PrivatePreviewRoot } from "./github-pr-preview";

const unavailable = "Patch unavailable (binary, too large, or omitted by GitHub).";

// Layout reference: t3code apps/web/src/components/pullRequest/PullRequestCodeTab.tsx
// at 1d61dd3: one diff scroller, with the file tree on its right and below it on narrow panes.
export function PRCode({ files }: { files: PRFile[] }) {
  const [style, setStyle] = useState<"unified" | "split">("unified");
  const host = useRef<HTMLSpanElement>(null);
  const viewer = useRef<CodeView | null>(null);
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
    const options = { diffStyle: style, theme: { light: "pierre-light", dark: "pierre-dark" }, stickyHeaders: true, enableLineSelection: true } as const;
    const applyTheme = () => viewer.current!.setOptions({ ...options, themeType: document.documentElement.classList.contains("dark") ? "dark" : "light" });
    applyTheme();
    const observer = new MutationObserver(applyTheme);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, [style]);
  return <span className="@container/pr-code flex min-h-0 min-w-0 flex-1 flex-col">
    <span className="flex shrink-0 items-center justify-between gap-2 border-y px-4 py-2">
      <span className="text-xs text-muted-foreground">Changed files</span>
      <Select value={style} onValueChange={(value) => setStyle(value as "unified" | "split")}>
        <SelectTrigger aria-label="Diff layout"><SelectValue>{style === "unified" ? "Unified" : "Split"}</SelectValue></SelectTrigger>
        <SelectContent render={<PrivatePreviewRoot />}><SelectGroup><SelectItem value="unified">Unified</SelectItem><SelectItem value="split">Split</SelectItem></SelectGroup></SelectContent>
      </Select>
    </span>
    <span className="flex min-h-0 min-w-0 flex-1 flex-col @min-[36rem]/pr-code:flex-row">
      <span ref={host} role="region" aria-label="File patch" tabIndex={0} className="block h-[26rem] min-h-48 min-w-0 flex-1 overflow-auto [contain:layout_style]" />
      <span role="region" aria-label="Changed files" className="block h-44 shrink-0 overflow-hidden border-t @min-[36rem]/pr-code:h-[26rem] @min-[36rem]/pr-code:w-[35%] @min-[36rem]/pr-code:border-t-0 @min-[36rem]/pr-code:border-l">
        <FileTree model={model} aria-label="Pull request files" style={{ height: "100%" }} />
      </span>
    </span>
  </span>;
}
