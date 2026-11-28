import { useEffect, useMemo, useRef, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { RefreshCw, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { parseGitHubReference, type PullRequest } from "./github-pr";
import { GitHubPRContent, PrivatePreviewRoot, type PRComposer } from "./github-pr-preview";
import { GitHubIssueContent } from "./github-issue-preview";
import { navigate, panelHref } from "./navigation";
import { discoverOpenCodePR, readOpenCodePR } from "./opencode-pr";
import { changedPR, loadThreadPRs, openCodePRAPI } from "./thread-pr";

export const threadGitHubLabel = "Pull requests and issues";

function useThreadPRs(id: string, version?: string, initial = "") {
  const remote = id.startsWith("opencode:");
  const found = useQuery({ queryKey: ["threadPRs", id, version], queryFn: ({ signal }) => loadThreadPRs(id, signal), placeholderData: keepPreviousData, retry: false, staleTime: Infinity, refetchOnWindowFocus: false });
  const checkout = useQuery({ queryKey: ["checkoutPR", id, version], enabled: remote, queryFn: ({ signal }) => {
    const { api, sessionID } = openCodePRAPI(id);
    return discoverOpenCodePR(api, sessionID, signal);
  }, placeholderData: keepPreviousData, retry: false, staleTime: Infinity, refetchOnWindowFocus: false });
  const mentioned = useMemo(() => new Set(found.data?.mentioned), [found.data]);
  const urls = useMemo(() => [...new Set([...(found.data?.created ?? []), ...mentioned, ...(checkout.data?.url ? [checkout.data.url] : [])])].filter((url) => parseGitHubReference(url, "pull")).concat(found.data?.issues ?? []), [found.data, mentioned, checkout.data]);
  const [selected, setSelected] = useState(initial);
  const url = urls.includes(selected) ? selected : urls[0];
  const issue = useMemo(() => url ? parseGitHubReference(url, "issue") : null, [url]);
  const checkoutOnly = !issue && !found.data?.created.includes(url!) && !mentioned.has(url!);
  const reference = useMemo(() => url ? parseGitHubReference(url, "pull") : null, [url]);
  const { api, directory } = checkout.data ?? {};
  const source = useMemo(() => api === undefined ? undefined : { api, directory: directory! }, [api, directory]);
  const loading = found.isFetching || remote && checkout.isFetching;
  const finding = found.isPending || remote && checkout.isPending;
  return { found, checkout, remote, urls, mentioned, url, checkoutOnly, issue, reference, source, loading, finding, setSelected };
}

export function ThreadPRWatch({ id, version }: { id: string; version?: string }) {
  const { found, source, loading } = useThreadPRs(id, version);
  const created = found.data?.created ?? [];
  const heads = useQuery({ queryKey: ["threadPRHeads", id, version, created], enabled: !loading && !found.error, retry: false, staleTime: Infinity, refetchOnWindowFocus: false, queryFn: ({ signal }) => Promise.all(created.map(async (url): Promise<[string, string | undefined]> => {
    const { owner, repo, number } = parseGitHubReference(url, "pull")!;
    return [url, source ? await readOpenCodePR<PullRequest>(source, `repos/${owner}/${repo}/pulls/${number}`, signal).then((pr) => pr.head.sha, () => undefined) : undefined];
  })) });
  const seen = useRef<Map<string, string | undefined>>(undefined);
  useEffect(() => {
    if (!heads.data) return;
    const result = changedPR(seen.current, heads.data);
    seen.current = result.seen;
    if (result.changed && !new URLSearchParams(location.search).has("pagePreview")) navigate(panelHref(result.changed, "pullRequests"));
  }, [heads.data]);
  return null;
}

export function ThreadPRContent({ id, version, selected, close, composer }: { id: string; version?: string; selected?: string; close: () => void; composer: PRComposer }) {
  const { found, checkout, remote, urls, mentioned, url, checkoutOnly, issue, reference, source, loading, finding, setSelected } = useThreadPRs(id, version, selected);
  const checkoutError = checkout.error || checkout.data && "error" in checkout.data;
  const [refresh, setRefresh] = useState(0);
  return <div className="ph-no-capture flex min-h-0 flex-1 flex-col">
    <header className="flex shrink-0 items-center gap-2 border-b p-2 text-xs lg:pr-12">
      <span className="font-medium">{threadGitHubLabel}</span>
      <ThreadGitHubPicker urls={urls} url={url} mentioned={mentioned} setSelected={setSelected} />
      <Tooltip><TooltipTrigger render={<Button type="button" variant="ghost" size="icon-sm" className="ml-auto" disabled={loading} />} aria-label="Refresh" onClick={() => { void found.refetch(); if (remote) void checkout.refetch(); setRefresh(refresh + 1); }}><RefreshCw /></TooltipTrigger><TooltipContent>Refresh</TooltipContent></Tooltip>
      <Button type="button" variant="ghost" size="icon-sm" aria-label="Close pull requests and issues" onClick={close}><X /></Button>
    </header>
    <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto p-3 text-sm">
      {finding && <p role="status">Finding pull requests and issues…</p>}
      {found.error && <p role="alert">Could not read this thread's pull requests and issues.</p>}
      {checkoutError && <p role="alert">Could not read the current checkout's pull request using this OpenCode server's gh login.</p>}
      {!finding && !found.error && !checkoutError && !urls.length && <p role="status">No pull requests or issues found for this thread.</p>}
      {(!remote || source) && (issue
        ? <GitHubIssueContent key={JSON.stringify([url, source])} reference={issue} source={source} refresh={refresh} close={close} />
        : reference && <>
        {checkoutOnly && <span className="shrink-0 truncate text-xs text-muted-foreground" title="Checkout results describe the folder's current branch, which other threads may share.">{`Current checkout · ${checkout.data?.branch}`}</span>}
        <GitHubPRContent key={JSON.stringify([url, source])} reference={reference} source={source} refresh={refresh} close={close} composer={composer} />
      </>)}
    </div>
  </div>;
}

function ThreadGitHubPicker({ urls, url, mentioned, setSelected }: { urls: string[]; url: string; mentioned: Set<string>; setSelected: (url: string) => void }) {
  if (urls.length < 2) return null;
  const issue = parseGitHubReference(url, "issue");
  return <Select value={url} onValueChange={(value) => setSelected(value ?? "")}>
    <SelectTrigger size="sm" aria-label="Thread pull request or issue"><SelectValue>{issue ? `Issue #${issue.number}` : `#${parseGitHubReference(url, "pull")!.number}`}</SelectValue></SelectTrigger>
    <SelectContent className="w-auto" render={<PrivatePreviewRoot />}><SelectGroup>{urls.map((value) => {
      const issueRef = parseGitHubReference(value, "issue");
      const ref = issueRef ?? parseGitHubReference(value, "pull")!;
      return <SelectItem key={value} value={value}>{ref.owner}/{ref.repo} #{ref.number}{issueRef ? " · issue" : mentioned.has(value) ? " · mentioned" : ""}</SelectItem>;
    })}</SelectGroup></SelectContent>
  </Select>;
}
