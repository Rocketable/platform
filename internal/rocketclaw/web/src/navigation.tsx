import { createContext, use, useSyncExternalStore, type ComponentProps, type MouseEvent } from "react";

// Each entry records its depth so browser Back and Forward can tell their direction apart.
export function navigate(path: string, replace = false) {
  history[replace ? "replaceState" : "pushState"]({ index: (history.state?.index ?? 0) + (replace ? 0 : 1) }, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

// Receives the pathname of an in-app link opened with a modifier or middle click.
export const OpenTab = createContext<(path: string) => void>(() => {});

function subscribe(listener: () => void) {
  window.addEventListener("popstate", listener);
  return () => window.removeEventListener("popstate", listener);
}

export function usePathname() {
  return useSyncExternalStore(subscribe, () => location.pathname);
}

export function useSearch() {
  return useSyncExternalStore(subscribe, () => location.search);
}

export default function Link({ href, onClick, ...props }: ComponentProps<"a">) {
  const openTab = use(OpenTab);
  const local = (!props.target || props.target === "_self") && href;
  // Tabs hold pathnames, so a link within this tab's own location keeps the browser's new-window default.
  const background = (event: MouseEvent, target: string) => {
    const pathname = new URL(target, location.href).pathname;
    if (pathname === location.pathname) return;
    event.preventDefault();
    openTab(pathname);
  };
  return <a {...props} href={href} onClick={(event) => {
    onClick?.(event);
    if (event.defaultPrevented || event.button !== 0 || event.shiftKey || event.altKey || !local) return;
    if (event.metaKey || event.ctrlKey) return background(event, local);
    event.preventDefault();
    navigate(href);
  }} onAuxClick={(event) => {
    if (event.button === 1 && local) background(event, local);
  }} />;
}
