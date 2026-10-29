#!/usr/bin/env node
// Runs one bash command in just-bash for a RocketClaw agent. stdin holds one JSON line,
// {"config": {...}, "env": {...}}, then the command. A bad setup prints its error and exits 1.
import { spawn } from "node:child_process";
import { mkdirSync, realpathSync } from "node:fs";
import { isAbsolute, join, relative } from "node:path";
import { Bash, bytesOutput, defineCommand, InMemoryFs, latin1FromBytes, MountableFs, ReadWriteFs, unsafeBytesFromLatin1 } from "just-bash";

type Target = string | { target: string; methods?: string[] };
type Config = {
  workspace: string;
  runtimeDir: string;
  workingDirectory: string;
  commands?: Record<string, string>;
  allowedNetworkTargets?: "auto" | Target[];
};

const fail = (message: string): never => {
  process.stderr.write(`just-bash setup: ${message}\n`);
  process.exit(1);
};

const chunks: Buffer[] = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const input = Buffer.concat(chunks).toString("utf8");
const newline = input.indexOf("\n");
const { config, env } = JSON.parse(input.slice(0, newline)) as { config: Config; env: Record<string, string> };
const command = input.slice(newline + 1);

// workingDirectory (required): $VARS from the host env, relative to the workspace. It must not be or contain
// the workspace or RocketClaw's directory, nor lie inside it: the agent could rewrite its own definition.
const within = (path: string, dir: string) => {
  const rel = relative(dir, path);
  return rel !== ".." && !rel.startsWith("../") && !isAbsolute(rel);
};
if (typeof config.workingDirectory !== "string" || config.workingDirectory.trim() === "") fail("workingDirectory is required");
const expanded = config.workingDirectory.replace(/\$\{?(\w+)\}?/g, (_, name) => env[name] ?? "");
const wanted = isAbsolute(expanded) ? expanded : join(config.workspace, expanded);
mkdirSync(wanted, { recursive: true });
const folder = realpathSync(wanted);
const [workspace, runtimeDir] = [realpathSync(config.workspace), realpathSync(config.runtimeDir)];
if (within(workspace, folder) || within(runtimeDir, folder) || within(folder, runtimeDir)) {
  fail(`workingDirectory ${folder} must not be or contain the workspace or ${runtimeDir}, nor lie inside ${runtimeDir}`);
}

// Only the folder reaches the host, at its real path. Everything else, TMPDIR included, is in memory and
// gone when the call ends.
const fs = new MountableFs({ base: new InMemoryFs() });
fs.mount(folder, new ReadWriteFs({ root: folder }));
await fs.mkdir("/tmp", { recursive: true });

// Each approved command runs on the host with only the host env: nothing the script sets reaches it.
const customCommands = Object.entries(config.commands ?? {}).map(([name, prefix]) =>
  defineCommand(name, (args, ctx) =>
    new Promise((resolve) => {
      const cwd = within(ctx.cwd, folder) ? ctx.cwd : folder;
      const child = spawn("/bin/bash", ["--noprofile", "--norc", "-c", `${prefix} "$@"`, name, ...args], { cwd, env, signal: ctx.signal });
      const out: Buffer[] = [];
      const err: Buffer[] = [];
      child.stdout.on("data", (b) => out.push(b));
      child.stderr.on("data", (b) => err.push(b));
      child.on("error", (e) => err.push(Buffer.from(`${name}: ${e.message}\n`)));
      child.on("close", (code, signal) =>
        // The output stays bytes, so binary data survives pipes and redirects.
        resolve({ ...bytesOutput(unsafeBytesFromLatin1(Buffer.concat(out).toString("latin1"))), stderr: Buffer.concat(err).toString("utf8"), exitCode: code ?? 128 + (signal === "SIGKILL" ? 9 : 15) }),
      );
      // A host command may exit without reading its input, as with real bash; unread input is dropped.
      child.stdin.on("error", () => {});
      child.stdin.end(Buffer.from(latin1FromBytes(ctx.stdin), "latin1"));
    }),
  ),
);

// Only what just-bash's network allow-list offers: origins and URL prefixes, one method list for all.
const targets = config.allowedNetworkTargets ?? "auto";
const network =
  targets === "auto"
    ? { dangerouslyAllowFullInternetAccess: true, denyPrivateRanges: true }
    : {
        allowedMethods: ["GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"],
        allowedUrlPrefixes: targets.flatMap((entry) => {
          const { target, methods } = typeof entry === "string" ? { target: entry, methods: undefined } : entry;
          if (methods) fail(`allowedNetworkTargets: ${target}: per-entry methods are not supported by just-bash`);
          if (target.includes("://")) return [target];
          if (target.startsWith("*.") || target.includes("/")) fail(`allowedNetworkTargets: ${target}: wildcards and CIDR ranges are not supported by just-bash`);
          return [`https://${target}`, `http://${target}`];
        }),
      };

const cwd = within(process.cwd(), folder) ? process.cwd() : folder;
// Interpreters get the overall execution limit, so RocketCode's bash timeout governs them too.
const hour = 60 * 60 * 1000;
const bash = (() => {
  try {
    return new Bash({
      fs, cwd, env: { ...env, TMPDIR: "/tmp" }, customCommands, network, python: true, javascript: true,
      executionLimits: { maxPythonTimeoutMs: hour, maxJsTimeoutMs: hour, maxSqliteTimeoutMs: hour },
    });
  } catch (e) {
    return fail((e as Error).message);
  }
})();

const result = await bash.exec(command);
// shortcut: exec() decodes stdout to text, so binary written straight to stdout is garbled (files and pipes
// keep exact bytes); exact bytes there need an upstream just-bash change.
process.stdout.write(result.stdout);
process.stderr.write(result.stderr);
process.exitCode = result.exitCode;
