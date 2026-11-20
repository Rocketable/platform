import { useEffect, useEffectEvent, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { GitHubError, loadIssue, loadIssueComments, type GitHubIssue, type GitHubPR, type IssueComment } from "./github-pr";
import { TokenEntry } from "./github-pr-preview";
import type { OpenCodePRSource } from "./opencode-pr";
import { MarkdownText } from "./transcript-text";

function useIssuePreview(reference: GitHubPR, source: OpenCodePRSource | undefined, refresh: number) {
  const token = useRef("");
  const controller = useRef<AbortController>(null);
  const [credential, setCredential] = useState("");
  const [issue, setIssue] = useState<GitHubIssue>();
  const [comments, setComments] = useState<IssueComment[]>([]);
  const [page, setPage] = useState(0);
  const [loadingRequest, setLoadingRequest] = useState<AbortController | null>(null);
  const [error, setError] = useState("");

  async function read(nextPage: number, readToken = token.current) {
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    token.current = readToken;
    setLoadingRequest(request);
    setError("");
    try {
      if (nextPage === 1) {
        const details = await loadIssue(reference, readToken, request.signal, source);
        request.signal.throwIfAborted();
        setIssue(details);
        setComments([]);
        setPage(0);
        if (!details.comments) return;
      }
      const batch = await loadIssueComments(reference, readToken, request.signal, nextPage, source);
      request.signal.throwIfAborted();
      setComments((previous) => [...new Map([...previous, ...batch].map((comment) => [comment.id, comment])).values()]);
      setPage(batch.length < 100 ? -1 : nextPage);
    } catch (error) {
      if (!request.signal.aborted) setError(error instanceof GitHubError ? error.message : "Could not read GitHub's response.");
    } finally {
      setLoadingRequest((current) => current === request ? null : current);
    }
  }

  const reload = useEffectEvent(() => void read(1));
  useEffect(() => {
    reload();
    return () => controller.current?.abort();
  }, [reference, source, refresh]);

  return {
    issue, comments, loading: loadingRequest !== null, error, credential, setCredential, more: issue && comments.length < issue.comments && page >= 0,
    loadMore: () => void read(page + 1),
    useToken: (value: string) => { setCredential(""); void read(1, value); },
  };
}

function IssueDetails({ issue, comments, reference }: { issue: GitHubIssue; comments: IssueComment[]; reference: GitHubPR }) {
  return <>
    <div className="flex flex-wrap items-center gap-2">
      <Badge variant={issue.state === "closed" ? "secondary" : "outline"}>{issue.state === "closed" ? "Closed" : "Open"}</Badge>
      {issue.user?.login && <span className="text-xs text-muted-foreground">{issue.user.login}</span>}
      {issue.labels.map((label) => {
        const name = typeof label === "string" ? label : label.name;
        return <Badge key={name} variant="secondary">{name}</Badge>;
      })}
    </div>
    <h2 className="text-base font-semibold">{issue.title}</h2>
    <MarkdownText text={issue.body?.trim() || "_No description provided._"} repository={reference} hideImages />
    <h3 className="border-t pt-3 font-medium">Comments</h3>
    {!issue.comments && <p className="text-muted-foreground">No comments.</p>}
    {comments.map((comment) => <article key={comment.id} className="border-t py-2">
      <header className="flex flex-wrap gap-2 text-xs text-muted-foreground">{comment.user?.login}<time dateTime={comment.created_at}>{new Date(comment.created_at).toLocaleString()}</time></header>
      <MarkdownText text={comment.body} repository={reference} hideImages />
    </article>)}
  </>;
}

export function GitHubIssueContent({ reference, source, refresh, close }: { reference: GitHubPR; source?: OpenCodePRSource; refresh: number; close: () => void }) {
  const { owner, repo, number } = reference;
  const { issue, comments, loading, error, credential, setCredential, more, loadMore, useToken } = useIssuePreview(reference, source, refresh);
  return <div className="flex min-w-0 flex-col gap-3 break-words">
    <a href={`https://github.com/${owner}/${repo}/issues/${number}`} className="w-fit underline" target="_blank" rel="noopener noreferrer">{owner}/{repo} #{number} on GitHub</a>
    {loading && <p role="status">{issue ? "Loading comments…" : "Loading issue…"} <Button type="button" variant="outline" onClick={close}>Cancel</Button></p>}
    {error && <p role="alert">{error}</p>}
    {!source && !loading && error && <TokenEntry credential={credential} setCredential={setCredential} useToken={useToken} permission="Issues" />}
    {issue && <IssueDetails issue={issue} comments={comments} reference={reference} />}
    {more && !loading && <Button type="button" variant="outline" onClick={loadMore}>Load more comments</Button>}
  </div>;
}
