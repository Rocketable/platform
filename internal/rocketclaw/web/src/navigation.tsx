import { useSyncExternalStore, type ComponentProps } from "react";

// Remote RocketClaws this page operates, named in the local config (internal/rocketclaw/internal/web/assets.go).
export const rocketclaws = (): { name: string; url: string }[] => JSON.parse(document.getElementById("rocketclaws")?.textContent || "[]");

// The server the page works on: a remote's name for paths under /r/<name>, or "" for the local server.
export const server = () => /^\/r\/([^/]+)/.exec(location.pathname)?.[1] ?? "";

// A server's origin for calls; an unknown remote's calls fail rather than reach the local server.
export const serverURL = (name = server()) => name ? rocketclaws().find((remote) => remote.name === name)?.url ?? "https://unknown.invalid" : "";

// A server's own path as this page's path.
export const at = (path: string, name = server()) => name ? `/r/${name}${path}` : path;

// The page's path on its server, without the /r/<name> prefix.
export const pagePath = () => location.pathname.slice(server() ? server().length + 3 : 0) || "/";

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

// href is a path on server, the page's server unless named.
export default function Link({ href, server: name, onClick, ...props }: ComponentProps<"a"> & { server?: string }) {
  return <a {...props} href={href?.startsWith("/") ? at(href, name) : href} onClick={(event) => {
    onClick?.(event);
    if (!event.defaultPrevented && event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && (!props.target || props.target === "_self") && href) {
      event.preventDefault();
      navigate(href, false, name);
    }
  }} />;
}
