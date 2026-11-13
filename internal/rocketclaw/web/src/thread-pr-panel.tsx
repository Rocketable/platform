import { useMemo, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { githubPR } from "./github-pr";
import { GitHubPRContent, PrivatePreviewRoot } from "./github-pr-preview";
import { discoverOpenCodePR } from "./opencode-pr";
import { loadThreadPRs, openCodePRAPI } from "./thread-pr";

function useThreadPRs(id: string, version?: string) {
  const remote = id.startsWith("opencode:");
  const created = useQuery({ queryKey: ["threadPRs", id, version], queryFn: ({ signal }) => loadThreadPRs(id, signal), placeholderData: keepPreviousData, retry: false, gcTime: 0, refetchOnWindowFocus: false });
  const checkout = useQuery({ queryKey: ["checkoutPR", id, version], enabled: remote, queryFn: ({ signal }) => {
    const { api, sessionID } = openCodePRAPI(id);
    return discoverOpenCodePR(api, sessionID, signal);
  }, placeholderData: keepPreviousData, retry: false, gcTime: 0, refetchOnWindowFocus: false });
  const urls = useMemo(() => [...new Set([...(created.data?.urls ?? []), ...(checkout.data?.url ? [checkout.data.url] : [])])].filter((url) => githubPR(url)), [created.data, checkout.data]);
  const [selected, setSelected] = useState("");
  const url = urls.includes(selected) ? selected : urls[0];
  const reference = useMemo(() => url ? githubPR(url) : null, [url]);
  const { api, directory } = checkout.data ?? {};
  const source = useMemo(() => api === undefined ? undefined : { api, directory: directory! }, [api, directory]);
  const loading = created.isFetching || remote && checkout.isFetching;
  return { created, checkout, remote, urls, url, reference, source, loading, setSelected };
}

export function ThreadPRContent({ id, version, close }: { id: string; version?: string; close: () => void }) {
  const { created, checkout, remote, urls, url, reference, source, loading, setSelected } = useThreadPRs(id, version);
  const checkoutError = checkout.error || checkout.data && "error" in checkout.data;
  return <div className="ph-no-capture flex min-h-0 flex-1 flex-col">
    <header className="flex shrink-0 items-center gap-2 border-b p-2 text-xs lg:pr-12">
      <span className="min-w-0 flex-1 font-medium">Pull requests</span>
      <Button type="button" variant="ghost" size="sm" disabled={loading} onClick={() => { void created.refetch(); if (remote) void checkout.refetch(); }}>Find again</Button>
      <Button type="button" variant="ghost" size="icon-sm" aria-label="Close pull requests" onClick={close}><X /></Button>
    </header>
    <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto p-3 text-sm">
      {loading && <p role="status">Finding pull requests…</p>}
      {created.error && <p role="alert">Could not read this thread's pull requests.</p>}
      {checkoutError && <p role="alert">Could not read the current checkout's pull request using this OpenCode server's gh login.</p>}
      {!loading && !created.error && !checkoutError && !urls.length && <p role="status">No pull requests found for this thread.</p>}
      {reference && <>
        <span className="flex shrink-0 flex-wrap items-center gap-2 text-xs">
        <Select value={url} onValueChange={(value) => setSelected(value ?? "")}>
          <SelectTrigger aria-label="Thread pull request"><SelectValue>#{reference.number}</SelectValue></SelectTrigger>
          <SelectContent render={<PrivatePreviewRoot />}><SelectGroup>{urls.map((value) => {
            const ref = githubPR(value)!;
            return <SelectItem key={value} value={value}>{ref.owner}/{ref.repo} #{ref.number}</SelectItem>;
          })}</SelectGroup></SelectContent>
        </Select>
        <span className="text-muted-foreground" title={remote ? "Checkout results describe the folder's current branch, which other threads may share." : undefined}>{created.data?.urls.includes(url!) ? "Created in this thread" : `Current checkout · ${checkout.data?.branch}`}</span>
        </span>
        {(!remote || source) && <GitHubPRContent key={JSON.stringify([url, source])} reference={reference} source={source} close={close} />}
      </>}
    </div>
  </div>;
}
