import { describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import path from "node:path";
import { protoSHA256 } from "./grpc";

const install = `Install the TypeScript proto generator via web app deps:

  cd web && bun install
  bunx proto-loader-gen-types --help

It ships with @grpc/proto-loader@0.8.1.
`;

describe("protoc toolchain", () => {
  test("web embeds proto/web.proto", () => {
    expect(existsSync(path.resolve(import.meta.dir, "../proto/web.proto"))).toBe(true);
    expect(protoSHA256()).toHaveLength(64);
  });

  test("proto-loader-gen-types is available", async () => {
    const proc = Bun.spawn(["bunx", "proto-loader-gen-types", "--help"], { stdout: "pipe", stderr: "pipe" });
    const [stdout, stderr, exit] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
    expect(exit, `${stdout}\n${stderr}\n\n${install}`).toBe(0);
  });
});
