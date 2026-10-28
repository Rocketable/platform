// The wrapper runs one bash command in just-bash for a RocketClaw agent and exits with its exit code.
// RocketClaw's launcher runs it as: node wrapper.mjs <hex agent name> <command>.
import { create, fromBinary, toBinary, type DescMessage, type MessageInitShape, type MessageShape } from "@bufbuild/protobuf";
import * as grpc from "@grpc/grpc-js";
import {
  FetchRequestSchema,
  FetchResponseSchema,
  RunCommandRequestSchema,
  RunCommandResponseSchema,
  SetupRequestSchema,
  SetupResponseSchema,
} from "./gen/justbash_pb.ts";

const [major = 0, minor = 0] = process.versions.node.split(".").map(Number);
if (major < 20 || (major === 20 && minor < 19)) {
  fail(`just-bash needs node 20.19 or later on PATH; found ${process.versions.node}`);
}

const [agentHex = "", command = ""] = process.argv.slice(2);
const agent = Buffer.from(agentHex, "hex").toString("utf8");
const socket = process.env.ROCKETCLAW_JUSTBASH_SOCKET;
const token = process.env.ROCKETCLAW_JUSTBASH_TOKEN ?? "";
if (!socket) {
  fail("just-bash runs only inside a RocketClaw turn");
}

// Loaded after the version check, so an old Node reports the check instead of failing to load just-bash.
const jb = await import("just-bash");

// Payloads up to just-bash's normal maxOutputSize cross the socket.
const maxMessage = 256 << 20;
const client = new grpc.Client(`unix:${socket}`, grpc.credentials.createInsecure(), {
  "grpc.max_receive_message_length": maxMessage,
  "grpc.max_send_message_length": maxMessage,
});

// call makes one Sandbox RPC. It runs as trusted host code: just-bash's defense layer blocks the timers
// gRPC uses when it runs inside a command's or fetch's async context.
function call<I extends DescMessage, O extends DescMessage>(
  method: string,
  input: I,
  output: O,
  init: MessageInitShape<I>,
  signal?: AbortSignal,
): Promise<MessageShape<O>> {
  return jb.DefenseInDepthBox.runTrustedAsync(
    () =>
      new Promise((resolve, reject) => {
        const pending = client.makeUnaryRequest(
          `/justbash.Sandbox/${method}`,
          (m: MessageShape<I>) => Buffer.from(toBinary(input, m)),
          (b: Buffer) => fromBinary(output, b),
          create(input, init),
          (err, res) => (err || !res ? reject(new Error(err?.details || err?.message || "empty response")) : resolve(res)),
        );
        signal?.addEventListener("abort", () => pending.cancel());
      }),
  );
}

// RocketClaw's errors already start with "just-bash:".
const setup = await call("Setup", SetupRequestSchema, SetupResponseSchema, { token, agent }).catch((err: Error) =>
  fail(err.message.startsWith("just-bash:") ? err.message : `just-bash: ${err.message}`),
);

// Mounts keep their real absolute paths, so paths match the rest of RocketClaw. just-bash rejects a
// mount inside another, and the outer mount already exposes it.
const inside = (path: string, dir: string) => path === dir || path.startsWith(dir.endsWith("/") ? dir : `${dir}/`);
const mounts: string[] = [];
for (const dir of [setup.workingDirectory, process.env.TMPDIR ?? ""].filter(Boolean).sort((a, b) => a.length - b.length)) {
  if (!mounts.some((m) => inside(dir, m))) {
    mounts.push(dir);
  }
}

const fs = new jb.MountableFs({ base: new jb.InMemoryFs() });
for (const dir of mounts) {
  fs.mount(dir, new jb.ReadWriteFs({ root: dir }));
}

const cwd = mounts.some((m) => inside(process.cwd(), m)) ? process.cwd() : setup.workingDirectory;

const customCommands = setup.commands.map((name) =>
  jb.defineCommand(name, async (args, ctx) => {
    try {
      const res = await call(
        "RunCommand",
        RunCommandRequestSchema,
        RunCommandResponseSchema,
        {
          token,
          agent,
          name,
          args,
          env: Object.fromEntries(ctx.env),
          cwd: ctx.cwd,
          stdin: Buffer.from(jb.latin1FromBytes(ctx.stdin), "latin1"),
        },
        ctx.signal,
      );
      return {
        ...jb.bytesOutput(jb.unsafeBytesFromLatin1(Buffer.from(res.stdout).toString("latin1"))),
        stderr: Buffer.from(res.stderr).toString("utf8"),
        exitCode: res.exitCode,
      };
    } catch (err) {
      return { stdout: "", stderr: `${name}: ${(err as Error).message}\n`, exitCode: 126 };
    }
  }),
);

const fetch: import("just-bash").SecureFetch = async (url, options = {}) => {
  const headers = Object.fromEntries(new Headers(options.headers));
  const body = options.body === undefined ? undefined : typeof options.body === "string" ? Buffer.from(options.body) : options.body;
  const res = await call(
    "Fetch",
    FetchRequestSchema,
    FetchResponseSchema,
    {
      token,
      agent,
      url,
      method: options.method ?? "GET",
      headers,
      body,
      followRedirects: options.followRedirects ?? true,
      maxRedirects: options.maxRedirects ?? 20,
      timeoutMs: BigInt(options.timeoutMs ?? 0),
    },
    options.signal,
  );
  return {
    status: res.status,
    statusText: res.statusText,
    headers: res.headers,
    body: res.body,
    url: res.url,
    redirectChain: res.redirectChain.map((r) => ({ status: r.status, statusText: r.statusText, headers: r.headers })),
  };
};

// The normal profile stops Python, JavaScript and SQLite after 30 s; RocketCode's bash timeout governs instead.
const hour = 60 * 60 * 1000;
const bash = new jb.Bash({
  fs,
  cwd,
  env: { ...(process.env as Record<string, string>), ...setup.env },
  python: true,
  javascript: true,
  customCommands,
  fetch,
  executionLimits: { maxPythonTimeoutMs: hour, maxJsTimeoutMs: hour, maxSqliteTimeoutMs: hour },
});

const result = await bash.exec(command);
client.close();
await write(process.stdout, Buffer.from(result.stdout, jb.stdoutKind(result) === "bytes" ? "latin1" : "utf8"));
await write(process.stderr, Buffer.from(result.stderr, "utf8"));
process.exit(result.exitCode);

function write(stream: NodeJS.WriteStream, data: Buffer): Promise<void> {
  return new Promise((resolve) => (data.length === 0 ? resolve() : stream.write(data, () => resolve())));
}

function fail(message: string): never {
  process.stderr.write(`${message}\n`);
  process.exit(127);
}
