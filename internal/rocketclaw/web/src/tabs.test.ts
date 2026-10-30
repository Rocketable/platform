import { expect, spyOn, test } from "bun:test";
import { API } from "typescript/unstable/async";
import { isFunctionDeclaration } from "typescript/unstable/ast/is";
import type { SavedTabs } from "./types";

// Exercise the actual cache publication without a UI-only export, as the picker tests do.
const api = new API({ cwd: import.meta.dir });
const source = (await (await api.updateSnapshot({ openProject: "../tsconfig.json" })).getProjects()[0].program.getSourceFile(`${import.meta.dir}/ui.tsx`))!;
await api.close();
const call = source.statements.find((node) => isFunctionDeclaration(node) && node.name?.text === "tabsCall")!.getText(source);
const javascript = new Bun.Transpiler({ loader: "tsx" }).transformSync(`
  import { QueryClient } from ${JSON.stringify(import.meta.resolve("@tanstack/react-query"))};
  import { mutations } from ${JSON.stringify(new URL("./api.ts", import.meta.url).href)};
  const queryClient = new QueryClient();
  ${call}
  export { tabsCall, queryClient };
`);
const { tabsCall, queryClient } = await import(`data:text/javascript;base64,${Buffer.from(javascript).toString("base64")}`);

test("personal save replies retain intervening Shared edits and mutes, including refused saves", async () => {
  const key = ["tabs", "tester"];
  const initial: SavedTabs = { spaces: [{ id: "main", name: "Main" }], state: { tabs: [], groups: [], space: "main", last: {}, sharedOrder: [] }, revision: "1", shared: [], mutes: [] };
  for (const state of [true, false]) for (const stale of [false, true]) for (const changed of ["neither", "shared", "mutes"] as const) {
    queryClient.clear();
    queryClient.setQueryData(key, initial);
    const response = Promise.withResolvers<Response>();
    const fetchMock = spyOn(globalThis, "fetch").mockImplementation(Object.assign(() => response.promise, { preconnect: fetch.preconnect }));
    const reply: SavedTabs = { ...initial, revision: "2", stale, state: { ...initial.state, last: { main: "/voice" } }, shared: [{ space: "main", path: "/voice" }], mutes: [{ path: "/s/old", until: "" }] };
    const current: SavedTabs = { ...initial, shared: changed === "shared" ? [{ space: "main", path: "/search/new" }] : initial.shared, mutes: changed === "mutes" ? [{ path: "/s/new", until: "" }] : initial.mutes };
    try {
      const pending = tabsCall(state ? { state: initial.state, revision: 1 } : { mute: { path: "/s/old", until: "" } });
      queryClient.setQueryData(key, current);
      response.resolve(Response.json(reply));
      expect(await pending).toEqual(reply);
      const applied = queryClient.getQueryData(key) as SavedTabs;
      expect(applied.state).toEqual(state && stale ? reply.state : initial.state);
      expect(applied.revision).toBe(state && stale ? "2" : "1");
      expect(applied.shared).toEqual(!state && stale || state && changed === "shared" ? current.shared : reply.shared);
      expect(applied.mutes).toEqual(!state && stale || state && changed === "mutes" ? current.mutes : reply.mutes);
    } finally { fetchMock.mockRestore(); queryClient.clear(); }
  }
});
