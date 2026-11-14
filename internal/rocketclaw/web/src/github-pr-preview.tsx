import { lazy, Suspense, useEffect, useMemo, useRef, useState, type ComponentProps, type ReactNode } from "react";
import { ArrowLeft, ExternalLink, GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft, RefreshCw } from "lucide-react";
import { cn } from "cn";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { GitHubError, githubPR, loadPR, loadPRFiles, type GitHubPR, type PRFile, type PullRequest } from "./github-pr";
import type { OpenCodePRSource } from "./opencode-pr";

const layouts = ["Side panel", "Dialog", "Inline", "Full-screen"] as const;
const PRCode = lazy(() => import("./github-pr-code").then((module) => ({ default: module.PRCode })));
type Layout = (typeof layouts)[number];
type PreviewResult = { pr?: PullRequest; files: PRFile[]; page: number; ended: boolean; loading: boolean; error: string };

export function PrivatePreviewRoot(props: ComponentProps<"div">) {
  return <div {...props} className={cn(props.className, "ph-no-capture")} />;
}

export function GitHubPRLink({ href, children }: { href: string | undefined; children: ReactNode }) {
  const ref = useMemo(() => href ? githubPR(href) : null, [href]);
  const anchor = <a href={href} className="underline" target="_blank" rel="noopener noreferrer">{children}</a>;
  return ref ? <span className="ph-no-capture">{anchor} <PreviewAction key={href} reference={ref} /></span> : anchor;
}

function PreviewAction({ reference }: { reference: GitHubPR }) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    window.addEventListener("popstate", close);
    window.addEventListener("pagehide", close);
    // A cached conversation can stay mounted while its app tab is hidden.
    const observer = new MutationObserver(() => { if (!trigger.current?.checkVisibility()) close(); });
    for (let node = trigger.current?.parentElement; node; node = node.parentElement) observer.observe(node, { attributes: true, attributeFilter: ["hidden", "style", "class"] });
    return () => {
      window.removeEventListener("popstate", close);
      window.removeEventListener("pagehide", close);
      observer.disconnect();
    };
  }, [open]);
  return <><Button ref={trigger} type="button" variant="outline" size="sm" aria-label="Preview pull request" onClick={() => setOpen(true)}>Preview</Button>
    {open && <Preview reference={reference} close={() => { setOpen(false); trigger.current?.focus(); }} />}</>;
}

function usePreview(reference: GitHubPR, source?: OpenCodePRSource, refresh = 0) {
  const [tab, setTab] = useState("summary");
  const [credential, setCredential] = useState("");
  const token = useRef("");
  const controller = useRef<AbortController | null>(null);
  const [result, setResult] = useState<PreviewResult>({ files: [], page: 0, ended: false, loading: true, error: "" });
  useEffect(() => {
    const request = new AbortController();
    controller.current = request;
    // A refresh keeps the PR in place until its new copy arrives, and keeps it if reading fails.
    void loadPR(reference, token.current, request.signal, source).then((pr) => {
      if (!request.signal.aborted) setResult({ pr, files: [], page: 0, ended: false, loading: false, error: "" });
    }).catch((error: unknown) => {
      if (!request.signal.aborted) setResult((current) => ({ ...current, loading: false, error: error instanceof GitHubError ? error.message : "Could not read GitHub's response." }));
    });
    return () => controller.current!.abort();
  }, [reference, source, refresh]);

  async function load(files: boolean, readToken = token.current) {
    controller.current!.abort();
    const request = new AbortController();
    controller.current = request;
    token.current = readToken;
    if (!files) setCredential("");
    setResult((current) => files ? { ...current, loading: true, error: "" } : { files: [], page: 0, ended: false, loading: true, error: "" });
    try {
      if (files) {
        const page = await loadPRFiles(reference, readToken, request.signal, result.pr!, result.page + 1, source);
        if (!request.signal.aborted) setResult((current) => ({ ...current, files: [...current.files, ...page], page: current.page + 1, ended: page.length < 100, loading: false }));
      } else {
        const pr = await loadPR(reference, readToken, request.signal, source);
        if (!request.signal.aborted) setResult((current) => ({ ...current, pr, loading: false }));
      }
    } catch (error) {
      if (!request.signal.aborted) setResult((current) => ({ ...current, loading: false, error: error instanceof GitHubError ? error.message : "Could not read GitHub's response." }));
    }
  }
  function cancel() {
    controller.current!.abort();
    setResult((current) => ({ ...current, loading: false }));
  }
  return { result, load, cancel, credential, setCredential, tab, setTab };
}

function Preview({ reference, close }: { reference: GitHubPR; close: () => void }) {
  const [layout, setLayout] = useState<Layout>("Side panel");
  const state = usePreview(reference);
  const inlineFocus = useRef<HTMLSpanElement>(null);
  // Let the departing popup return focus before transferring it to the inline region.
  useEffect(() => { if (layout === "Inline") queueMicrotask(() => inlineFocus.current?.focus()); }, [layout]);
  const controls = <span className="flex flex-wrap items-center gap-2">
    <Select value={layout} onValueChange={(value) => setLayout(value as Layout)}>
      <SelectTrigger aria-label="Preview layout"><SelectValue /></SelectTrigger>
      <SelectContent render={<PrivatePreviewRoot />}><SelectGroup>{layouts.map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}</SelectGroup></SelectContent>
    </Select>
    <Tooltip><TooltipTrigger render={<Button type="button" variant="ghost" size="icon-sm" disabled={state.result.loading} />} aria-label="Refresh" onClick={() => void state.load(false)}><RefreshCw /></TooltipTrigger><TooltipContent>Refresh</TooltipContent></Tooltip>
    <Button type="button" variant="outline" onClick={close}>Close preview</Button>
  </span>;
  const body = <PreviewBody reference={reference} state={state} close={close} />;
  if (layout === "Inline") return <span ref={inlineFocus} tabIndex={-1} role="region" aria-label="Pull request preview" className="ph-no-capture my-2 flex max-h-[80dvh] w-full min-w-0 flex-col gap-3 rounded-lg border bg-background p-3 font-sans text-sm font-normal whitespace-normal text-foreground" onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); close(); } }}>
    <span className="font-medium">Pull request preview</span>{controls}{body}
  </span>;
  const changeOpen = (open: boolean) => { if (!open) close(); };
  if (layout === "Side panel") return <Sheet open onOpenChange={changeOpen}><SheetContent render={<PrivatePreviewRoot />} className="min-w-0 data-[side=right]:w-full data-[side=right]:sm:max-w-2xl" aria-describedby={undefined} showCloseButton={false}>
    <div className="flex min-h-0 flex-1 flex-col gap-3 p-4"><SheetTitle>Pull request preview</SheetTitle>{controls}{body}</div>
  </SheetContent></Sheet>;
  return <Dialog open onOpenChange={changeOpen}><DialogContent render={<PrivatePreviewRoot />} className={cn("flex min-w-0 flex-col", layout === "Full-screen" ? "h-dvh max-w-full sm:max-w-full" : "max-h-[85dvh] sm:max-w-3xl")} aria-describedby={undefined} showCloseButton={false}>
    <DialogTitle>Pull request preview</DialogTitle>{controls}{body}
  </DialogContent></Dialog>;
}

export function GitHubPRContent({ reference, source, refresh, close }: { reference: GitHubPR; source?: OpenCodePRSource; refresh: number; close: () => void }) {
  const state = usePreview(reference, source, refresh);
  return <PreviewBody reference={reference} state={state} close={close} source={source} />;
}

function PreviewBody({ reference, state, close, source }: { reference: GitHubPR; state: ReturnType<typeof usePreview>; close: () => void; source?: OpenCodePRSource }) {
  const { result, cancel } = state;
  const { pr } = result;
  return <span className="flex min-h-0 min-w-0 flex-1 flex-col gap-3 overflow-y-auto break-words">
    {result.loading && <span role="status">{pr ? "Loading files…" : "Loading pull request…"} <Button type="button" variant="outline" onClick={pr ? cancel : close}>Cancel</Button></span>}
    {result.error && <span role="alert">{result.error}</span>}
    <span className="flex min-w-0 shrink-0 items-center gap-2 text-xs text-muted-foreground">
      {pr && <PRStatus pr={pr} />}
      <Tooltip><TooltipTrigger render={<a href={`https://github.com/${reference.owner}/${reference.repo}/pull/${reference.number}`} target="_blank" rel="noopener noreferrer" className="flex min-w-0 items-center gap-1 hover:underline"><span className="truncate">{reference.owner}/{reference.repo} #{reference.number}</span><ExternalLink className="size-3 shrink-0" /></a>} /><TooltipContent>Open on GitHub</TooltipContent></Tooltip>
    </span>
    {!source && !result.loading && (!pr || Boolean(result.error)) && <TokenEntry state={state} />}
    {pr && <PreviewDetails pr={pr} state={state} />}
    {source && <span className="shrink-0 text-xs text-muted-foreground">Read with this OpenCode server's GitHub login.</span>}
  </span>;
}

// Visual reference: t3code apps/web/src/components/pullRequest/PullRequestDetailLayout.tsx
// at 1d61dd3: title, actor/time, base ← head and counts above a segmented tab bar.
function PreviewDetails({ pr, state }: { pr: PullRequest; state: ReturnType<typeof usePreview> }) {
  const { result, load, tab, setTab } = state;
  return <Tabs render={<span />} value={tab} onValueChange={(value) => setTab(String(value))} className="min-w-0 shrink-0">
    <PRHeader pr={pr} />
    <span className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-y py-2">
      <TabsList render={<span />} aria-label="Pull request tabs"><TabsTrigger value="summary">Summary</TabsTrigger><TabsTrigger value="code">Code</TabsTrigger></TabsList>
      <span className="text-xs text-muted-foreground">{result.files.length} of {pr.changed_files} files loaded</span>
    </span>
    <TabsContent render={<span />} value="summary" className="block overflow-auto whitespace-pre-wrap"><span className="block py-3">{pr.body || "No description."}</span></TabsContent>
    <TabsContent render={<span />} value="code" keepMounted className="flex min-h-0 min-w-0 flex-1 flex-col data-[hidden]:hidden">
      <Suspense fallback={<span role="status">Loading diff viewer…</span>}>{result.files.length > 0 ? <PRCode files={result.files} /> : <span className="py-4 text-muted-foreground">No files loaded.</span>}</Suspense>
    </TabsContent>
    <span className="flex shrink-0 flex-col gap-2 py-2 text-xs text-muted-foreground">
      <span>Patches may be incomplete. Open GitHub for the full diff.</span>
      {result.files.length >= 3000 && <span>GitHub's 3,000-file limit reached; this preview may be incomplete.</span>}
      {result.ended && result.files.length < pr.changed_files && <span>GitHub returned fewer files than reported. This preview is incomplete.</span>}
      {result.files.length < Math.min(pr.changed_files, 3000) && !result.ended && <Button type="button" variant="outline" size="sm" disabled={result.loading} onClick={() => { setTab("code"); void load(true); }}>{result.page ? "Load more files" : "Load files"}</Button>}
    </span>
  </Tabs>;
}

function PRStatus({ pr }: { pr: PullRequest }) {
  const [Icon, status, variant] = pr.merged ? [GitMerge, "Merged", "secondary"] as const : pr.state === "closed" ? [GitPullRequestClosed, "Closed", "destructive"] as const : pr.draft ? [GitPullRequestDraft, "Draft", "outline"] as const : [GitPullRequest, "Open", "secondary"] as const;
  return <Badge variant={variant}><Icon data-icon="inline-start" />{status}</Badge>;
}

function PRHeader({ pr }: { pr: PullRequest }) {
  const base = pr.base.ref ?? pr.base.sha.slice(0, 7), head = pr.head.ref ?? pr.head.sha.slice(0, 7);
  return <span className="flex flex-col gap-2 pb-3">
    <span className="text-base leading-snug font-semibold tracking-tight">{pr.title}</span>
    <span className="flex flex-wrap gap-2 text-xs text-muted-foreground">{pr.user?.login}{pr.updated_at && <time dateTime={pr.updated_at}>Updated {new Date(pr.updated_at).toLocaleString()}</time>}<span className="ml-auto tabular-nums">{pr.changed_files} {pr.changed_files === 1 ? "file" : "files"}{pr.additions !== undefined && ` · +${pr.additions} −${pr.deletions}`}</span></span>
    <span className="flex min-w-0 items-center gap-2 font-mono text-xs text-muted-foreground"><span className="max-w-[40%] truncate" title={base}>{base}</span><ArrowLeft aria-label="receives changes from" className="size-3.5 shrink-0" /><span className="min-w-0 truncate" title={head}>{head}</span></span>
  </span>;
}

function TokenEntry({ state }: { state: ReturnType<typeof usePreview> }) {
  const { load, credential, setCredential } = state;
  return <span className="flex flex-col gap-2">
       <span>Private PR or rate limited? Use your own fine-grained token with Pull requests: read for private access or a higher request quota. It stays in this open preview only; closing or navigating away clears it.</span>
      {window.isSecureContext ? <><Input type="password" aria-label="Personal read token" autoComplete="off" value={credential} onChange={(event) => setCredential(event.target.value)} />
        <Button type="button" variant="outline" disabled={!credential} onClick={() => void load(false, credential)}>Use token</Button></> : <span>Token entry requires a secure browser context with a trusted certificate. Open this app over HTTPS.</span>}
  </span>;
}
