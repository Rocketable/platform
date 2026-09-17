export function runPreload(
  fetchAgents: () => Promise<unknown>,
  fetchSkills: () => Promise<unknown>,
  fetchCron: () => Promise<unknown>,
  fetchConfig: () => Promise<unknown>,
  onReady: () => void,
) {
  let cancelled = false;
  const preload = async () => {
    await Promise.all([fetchAgents(), fetchSkills()]);
    if (cancelled) return;
    await fetchCron();
    if (cancelled) return;
    await fetchConfig();
    if (!cancelled) onReady();
  };
  if (typeof requestIdleCallback === "function") {
    const idle = requestIdleCallback(() => { void preload(); });
    return () => { cancelled = true; cancelIdleCallback(idle); };
  }
  const timer = setTimeout(() => { void preload(); }, 0);
  return () => { cancelled = true; clearTimeout(timer); };
}
