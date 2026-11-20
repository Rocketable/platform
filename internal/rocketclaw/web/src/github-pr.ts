import { readOpenCodePR, type OpenCodePRSource } from "./opencode-pr";

export type GitHubPR = { owner: string; repo: string; number: number };
export type PullRequest = { title: string; body: string | null; state: string; draft: boolean; merged: boolean; changed_files: number; head: { sha: string; ref?: string }; base: { sha: string; ref?: string }; user?: { login: string }; updated_at?: string; additions?: number; deletions?: number };
export type PRFile = { filename: string; previous_filename?: string; status: string; additions: number; deletions: number; patch?: string };
export type GitHubIssue = { title: string; body: string | null; state: "open" | "closed"; user: { login: string } | null; labels: (string | { name: string })[]; comments: number; pull_request?: unknown };
export type IssueComment = { id: number; body: string; user: { login: string } | null; created_at: string };

export function githubIssue(href: string): GitHubPR | null {
  const match = /^https:\/\/github\.com\/([a-z\d][a-z\d-]*)\/([a-z\d_.-]+)\/issues\/([1-9]\d*)\/?(?:[?#][^\s]*)?$/i.exec(href);
  if (!match || [".", ".."].includes(match[2]) || !Number.isSafeInteger(Number(match[3]))) return null;
  return { owner: match[1], repo: match[2], number: Number(match[3]) };
}

export function githubPR(href: string): GitHubPR | null {
  // Match the original path, not URL's normalized path (which erases traversal).
  const match = /^https:\/\/github\.com\/([a-z\d][a-z\d-]*)\/([a-z\d_.-]+)\/pull\/([1-9]\d*)(?:\/(?:files|commits|checks))?\/?(?:[?#][^\s]*)?$/i.exec(href);
  if (!match || [".", ".."].includes(match[2]) || !Number.isSafeInteger(Number(match[3]))) return null;
  return { owner: match[1], repo: match[2], number: Number(match[3]) };
}

async function request<T>(path: string, token: string, signal: AbortSignal, source?: OpenCodePRSource): Promise<T> {
  try {
    if (source) return await readOpenCodePR<T>(source, path, signal);
    const response = await fetch(`https://api.github.com/${path}`, {
      method: "GET", signal, credentials: "omit", referrerPolicy: "no-referrer", cache: "no-store", redirect: "error",
      headers: { Accept: "application/vnd.github+json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    });
    if (!response.ok) {
      const message = response.status === 404 ? "Not found or private. You can use your own read token."
        : response.status === 401 ? "Token was not accepted."
        : response.status === 429 || response.headers.get("x-ratelimit-remaining") === "0" ? "GitHub rate limit reached. Try again later."
        : response.status === 403 ? "GitHub denied access. Check token permissions or try again later."
        : "GitHub is unavailable. Try again.";
      throw new GitHubError(message);
    }
    return await response.json() as T;
  } catch (error) {
    signal.throwIfAborted();
    if (source) throw new GitHubError("Could not read GitHub using this OpenCode server's gh login.");
    throw error instanceof GitHubError ? error : new GitHubError(`Could not load this ${path.includes("/issues/") ? "issue" : "pull request"}. Check your connection or open it on GitHub.`);
  }
}

export class GitHubError extends Error {}

export async function loadPR(ref: GitHubPR, token: string, signal: AbortSignal, source?: OpenCodePRSource): Promise<PullRequest> {
  const { title, body, state, draft, merged, changed_files, head, base, user, updated_at, additions, deletions } = await request<PullRequest>(`repos/${ref.owner}/${ref.repo}/pulls/${ref.number}`, token, signal, source);
  return { title, body, state, draft, merged, changed_files, head: { sha: head.sha, ...(head.ref && { ref: head.ref }) }, base: { sha: base.sha, ...(base.ref && { ref: base.ref }) }, ...(user && { user: { login: user.login } }), ...(updated_at && { updated_at }), ...(additions !== undefined && { additions, deletions }) };
}

export async function loadPRFiles(ref: GitHubPR, token: string, signal: AbortSignal, pr: PullRequest, page: number, source?: OpenCodePRSource): Promise<PRFile[]> {
  const checkRevision = async () => {
    const current = await loadPR(ref, token, signal, source);
    if (current.head.sha !== pr.head.sha || current.base.sha !== pr.base.sha || current.changed_files !== pr.changed_files) throw new GitHubError("Pull request changed. Refresh before loading more files.");
  };
  await checkRevision();
  const files = await request<PRFile[]>(`repos/${ref.owner}/${ref.repo}/pulls/${ref.number}/files?per_page=100&page=${page}`, token, signal, source);
  await checkRevision();
  return files.map(({ filename, previous_filename, status, additions, deletions, patch }) => ({ filename, previous_filename, status, additions, deletions, patch }));
}

export async function loadIssue({ owner, repo, number }: GitHubPR, token: string, signal: AbortSignal, source?: OpenCodePRSource) {
  const issue = await request<GitHubIssue>(`repos/${owner}/${repo}/issues/${number}`, token, signal, source);
  if (issue.pull_request) throw new GitHubError("This link points to a pull request, not an issue.");
  return issue;
}

export function loadIssueComments({ owner, repo, number }: GitHubPR, token: string, signal: AbortSignal, page: number, source?: OpenCodePRSource) {
  return request<IssueComment[]>(`repos/${owner}/${repo}/issues/${number}/comments?per_page=100&page=${page}`, token, signal, source);
}
