import { describe, expect, test } from "bun:test";
import { Glob } from "bun";
import path from "node:path";

describe("react-doctor", () => {
  test(
    "scan is clean",
    async () => {
      const root = path.resolve(import.meta.dir, "..");
      const files: string[] = [];
      for await (const file of new Glob("**/*.{tsx,jsx}").scan({ cwd: root, onlyFiles: true })) {
        if (file.startsWith("node_modules/") || file.startsWith(".next/") || file.startsWith("dist/")) {
          continue;
        }
        files.push(file);
      }
      files.sort();
      const proc = Bun.spawn(
        ["bunx", "--bun", "react-doctor@latest", "--no-telemetry", "--blocking", "warning", "--no-color", ...files],
        { cwd: root, stdout: "pipe", stderr: "pipe" },
      );
      const [stdout, stderr, exit] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
      if (exit !== 0) {
        throw new Error(`react-doctor exited ${exit}\n${stdout}\n${stderr}`);
      }
      expect(exit).toBe(0);
    },
    120_000,
  );
});
