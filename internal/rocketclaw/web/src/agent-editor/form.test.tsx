import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AgentSettingHelp } from "@/types";
import { suggestion } from "./assistant";
import type { CoEdit } from "./coedit";
import { FormTab } from "./form";

const setting = (key: string, label: string): AgentSettingHelp => ({ key, label, group: "Tools", explanation: "", defaultText: "", allowed: "", control: "file", choices: [], required: false, common: true });

test("the MCP settings list each configured server with its facets", () => {
  const client = new QueryClient();
  client.setQueryData(["config"], { mcpServerFacets: [{ server: "alpha", facets: ["read", "write"] }, { server: "zeta", facets: ["sudo"] }] });
  const variant = { name: "default", settings: [], body: "" };
  const help = [setting("mcp", "Outside tool servers"), setting("rocketclaw", "RocketClaw tools"), setting("approver", "Reviewer"), setting("approver.mcp", "Reviewer's outside tool servers")];
  const html = renderToStaticMarkup(<QueryClientProvider client={client}>
    <FormTab help={help} coedit={{} as CoEdit} variant={variant} presence={[]} onFile={() => {}} suggested={suggestion("easy", variant, [])} problems={[]} />
  </QueryClientProvider>);
  const lists = [...html.matchAll(/<ul aria-label="Configured MCP servers"[^>]*>(.*?)<\/ul>/g)].map(([, list]) =>
    [...list.matchAll(/<li[^>]*>(.*?)<\/li>/g)].map(([, item]) => [...item.matchAll(/>([^<]+)</g)].map(([, text]) => text)));
  expect(lists).toEqual([[["alpha", "read", "write"], ["zeta", "sudo"]], [["alpha", "read", "write"], ["zeta", "sudo"]]]);
});

test("helpers show agent entries next to models, and unknown keys show as failing to load", () => {
  const html = (value: string) => {
    const variant = { name: "default", settings: [{ key: "subagents", value }, { key: "temperature", value: "0.2" }], body: "" };
    return renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}>
      <FormTab help={[{ ...setting("subagents", "Helpers"), control: "list" }]} coedit={{} as CoEdit} variant={variant} presence={[]} onFile={() => {}} suggested={suggestion("easy", variant, [])} problems={[]} />
    </QueryClientProvider>);
  };
  for (const value of ["- gpt-5\n- agent: writer", "[gpt-5, {agent: writer}]"]) expect(/<input[^>]*aria-label="Helpers"[^>]*value="([^"]*)"/.exec(html(value))?.[1]).toBe("gpt-5, agent: writer");
  const unknown = /<section aria-label="Unknown settings"[^>]*>(.*?)<\/section>/.exec(html("auto"))?.[1] ?? "";
  expect(unknown.replaceAll(/<[^>]+>/g, "")).toBe("Unknown settings · the agent fails to load with these; remove them in the File tabtemperature: 0.2");
});
