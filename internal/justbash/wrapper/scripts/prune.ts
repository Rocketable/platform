// prune removes what the embedded runtime never loads from a production install of just-bash.
// Usage: bun scripts/prune.ts <stage dir>
import { readdirSync, rmSync, statSync } from "node:fs";
import { join } from "node:path";

const stage = process.argv[2];
if (!stage) {
  throw new Error("usage: bun scripts/prune.ts <stage dir>");
}

const modules = join(stage, "node_modules");

// Whole paths the runtime never loads: the CLIs and the CommonJS and browser builds.
for (const path of [
  "package.json",
  "bun.lock",
  "node_modules/.bin",
  "node_modules/.cache",
  "node_modules/just-bash/dist/bin",
  "node_modules/just-bash/dist/bundle/index.cjs",
  "node_modules/just-bash/dist/bundle/browser.js",
]) {
  rmSync(join(stage, path), { recursive: true, force: true });
}

// sql.js ships many builds; just-bash's worker loads only the Node WebAssembly one.
const sqlDist = join(modules, "sql.js", "dist");
for (const name of readdirSync(sqlDist)) {
  if (name !== "sql-wasm.js" && name !== "sql-wasm.wasm") {
    rmSync(join(sqlDist, name), { recursive: true, force: true });
  }
}

// Packages nothing loads at runtime, at any depth: type packages and TypeScript (js-exec strips types
// with Node's own stripper).
const dropPackages = new Set(["typescript", "@types", "undici-types", ".bin"]);
const dropDirs = new Set(["test", "tests", "__tests__", "docs", "example", "examples", ".github"]);
const dropFile = /(\.d\.[cm]?ts|\.map|\.go|^(readme|changelog|history)(\.[a-z]+)?)$/i;

function walk(dir: string) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      if (dropDirs.has(name) || (dir.endsWith("node_modules") && dropPackages.has(name))) {
        rmSync(path, { recursive: true, force: true });
      } else {
        walk(path);
      }
    } else if (dropFile.test(name)) {
      rmSync(path, { force: true });
    }
  }
}
walk(modules);
