// Each line of after, and of before that after drops, in order, by their longest common run of lines.
// ponytail: O(n·m) time and space in the two files' lines, fine for agent files of a few hundred lines; the
// backend's Myers diff (shortestAgentDraftEdits) is the upgrade past that.
export function lineDiff(before: string[], after: string[]) {
  const n = before.length, m = after.length, common = Array.from({ length: n + 1 }, () => Array.from({ length: m + 1 }, () => 0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) common[i][j] = before[i] === after[j] ? common[i + 1][j + 1] + 1 : Math.max(common[i + 1][j], common[i][j + 1]);
  const lines: { mark: string; text: string; key: string }[] = [];
  for (let i = 0, j = 0; i < n || j < m;) {
    const key = `${i}:${j}`;
    if (i < n && j < m && before[i] === after[j]) {
      lines.push({ mark: " ", text: before[i], key });
      i++;
      j++;
    } else if (i < n && (j === m || common[i + 1][j] >= common[i][j + 1])) lines.push({ mark: "-", text: before[i++], key });
    else lines.push({ mark: "+", text: after[j++], key });
  }
  return lines;
}
