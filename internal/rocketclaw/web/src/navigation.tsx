import { useSyncExternalStore, type ComponentProps } from "react";

export const rocketclaws = (): { name: string; url: string }[] => JSON.parse(document.getElementById("rocketclaws")?.textContent || "[]");

export const server = () => /^\/r\/([^/]+)/.exec(location.pathname)?.[1] ?? "";

export const serverURL = (name = server()) => name ? rocketclaws().find((remote) => remote.name === name)?.url ?? "https://unknown.invalid" : "";

export const at = (path: string, name = server()) => name ? `/r/${name}${path}` : path;

export const pagePath = () => location.pathname.slice(server() ? server().length + 3 : 0) || "/";

export function panelHref(id?: string, panel: "delegation" | "skill" | "shadow" | "pullRequests" | "pagePreview" | "tree" | "goal" = "delegation", reason?: string, turn?: string) {
  const params = new URLSearchParams(location.search);
  params.delete("delegation");
  params.delete("skill");
  params.delete("pullRequests");
  params.delete("pagePreview");
  params.delete("tree");
  params.delete("goal");
  params.delete("reason");
  params.delete("answer");
  if (id) params.set(panel, id);
  if (reason !== undefined) params.set("reason", reason);
  if (turn) params.set("answer", turn);
  return `${pagePath()}${params.size ? `?${params}` : ""}`;
}

export function navigate(path: string, replace = false, name = server()) {
  history[replace ? "replaceState" : "pushState"](null, "", at(path, name));
  window.dispatchEvent(new PopStateEvent("popstate"));
}

function subscribe(listener: () => void) {
  window.addEventListener("popstate", listener);
  return () => window.removeEventListener("popstate", listener);
}

export function usePathname() {
  return useSyncExternalStore(subscribe, pagePath);
}

export function useServer() {
  return useSyncExternalStore(subscribe, server);
}

export function useSearch() {
  return useSyncExternalStore(subscribe, () => location.search);
}

export default function Link({ href, server: name, onClick, ...props }: ComponentProps<"a"> & { server?: string }) {
  return <a {...props} href={href?.startsWith("/") ? at(href, name) : href} onClick={(event) => {
    onClick?.(event);
    if (!event.defaultPrevented && event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && (!props.target || props.target === "_self") && href) {
      event.preventDefault();
      navigate(href, false, name);
    }
  }} />;
}
