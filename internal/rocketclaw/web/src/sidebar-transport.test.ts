import { expect, test } from "bun:test";
import { listSessions } from "./api";
import type { AgentChoices } from "./types";

// Go holds the second row until this real HTTP client cancels enumeration.
test.skipIf(!process.env.ROCKETCLAW_TEST_HTTP_URL)("first Go row and composer choices arrive before the blocked tail", async () => {
  const url = process.env.ROCKETCLAW_TEST_HTTP_URL!;
  const abort = new AbortController();
  const call = async (method: string, input = {}) => {
    const response = await fetch(`${url}/api/${method}`, { method: "POST", body: JSON.stringify(input) });
    expect(response.status).toBe(200);
    return response.json();
  };
  try {
    expect(await call("Identity")).toEqual({ username: "alice" });
    expect(await call("Protocol")).toEqual({ protoSha256: new Bun.CryptoHasher("sha256").update(await Bun.file(new URL("../proto/web.proto", import.meta.url)).arrayBuffer()).digest("hex") });
    const iterator = listSessions(abort.signal, `${url}/api/ListSessions`);
    expect(await iterator.next()).toMatchObject({ done: false, value: {
      sessions: [{ id: "slack-thread:C1:1.1", title: "C1", agent: "main", settled: true, updatedAt: "1970-01-01T00:00:02.123456Z", allowedAgents: ["main"] }],
      owner: "alice", upstreamSuccess: false, summariesComplete: true,
    } });
    const pending = iterator.next();
    const choices: AgentChoices = await call("ListAgents", { conversationId: "slack-thread:C1:1.1" });
    expect(choices.currentAgent).toBe("main");
    expect(choices.agents.map((agent) => agent.name)).toEqual(["main"]);
    expect(await call("Prompt", { id: "slack-thread:C1:1.1", text: "send while listing" })).toEqual({ privateText: "" });
    abort.abort();
    await expect(pending).rejects.toBeDefined();
  } finally { abort.abort(); }
}, 5000);
