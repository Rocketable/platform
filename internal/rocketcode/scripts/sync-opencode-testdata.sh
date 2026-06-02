#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
opencode_root="${OPENCODE_ROOT:-/Users/ucirello/go/src/github.com/Rocketable/opencode}"
dest="$repo_root/testdata/opencode"
raw_src_dir="$dest/raw/packages/opencode/src"
raw_test_dir="$dest/raw/packages/opencode/test/tool"
oracle_dir="$dest/oracle"

required_files=(
  "packages/opencode/src/tool/apply_patch.ts"
  "packages/opencode/src/tool/apply_patch.txt"
  "packages/opencode/src/patch/index.ts"
  "packages/opencode/src/util/bom.ts"
  "packages/opencode/test/tool/apply_patch.test.ts"
)

git -C "$opencode_root" fetch origin dev
sha="$(git -C "$opencode_root" rev-parse origin/dev)"
origin_url="$(git -C "$opencode_root" remote get-url origin)"

mkdir -p "$raw_src_dir/tool" "$raw_src_dir/patch" "$raw_src_dir/util" "$raw_test_dir" "$oracle_dir"

for file in "${required_files[@]}"; do
  target="$dest/raw/$file"
  mkdir -p "$(dirname "$target")"
  git -C "$opencode_root" show "$sha:$file" > "$target"
done

cat > "$dest/manifest.json" <<EOF
{
  "repo": "${origin_url}",
  "branch": "origin/dev",
  "commit": "${sha}"
}
EOF

cat > "$dest/raw/package.json" <<'EOF'
{
  "type": "module"
}
EOF

cat > "$oracle_dir/run-apply-patch.mjs" <<'EOF'
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { pathToFileURL } from "node:url"

const repoRoot = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..")
const rawRoot = path.join(repoRoot, "raw", "packages", "opencode", "src")

function prepareRuntimeModules() {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), "rocketcode-opencode-"))

  const effectShim = path.join(tempDir, "effect-shim.mjs")
  fs.writeFileSync(effectShim, 'export const Effect = { fn() { return (fn) => fn } }\n', "utf-8")

  const fsShim = path.join(tempDir, "filesystem-shim.mjs")
  fs.writeFileSync(fsShim, "export const AppFileSystem = {}\n", "utf-8")

  const zodShim = path.join(tempDir, "zod-shim.mjs")
  fs.writeFileSync(
    zodShim,
    'const noop = () => ({ describe() { return {} } })\nexport default { object(shape) { return shape }, string: noop }\n',
    "utf-8",
  )

  const utilIndex = path.join(tempDir, "util-index.runtime.ts")
  fs.writeFileSync(utilIndex, 'export const Log = { create() { return { info() {}, warn() {}, error() {} } } }\n', "utf-8")

  const bomRuntime = path.join(tempDir, "bom.runtime.ts")
  let bomSource = fs.readFileSync(path.join(rawRoot, "util", "bom.ts"), "utf-8")
  bomSource = bomSource.replace('import { Effect } from "effect"', 'import { Effect } from "./effect-shim.mjs"')
  bomSource = bomSource.replace(
    'import { AppFileSystem } from "@opencode-ai/core/filesystem"',
    'import { AppFileSystem } from "./filesystem-shim.mjs"',
  )
  fs.writeFileSync(bomRuntime, bomSource, "utf-8")

  const patchRuntime = path.join(tempDir, "patch.runtime.ts")
  let patchSource = fs.readFileSync(path.join(rawRoot, "patch", "index.ts"), "utf-8")
  patchSource = patchSource.replace('import z from "zod"', 'import z from "./zod-shim.mjs"')
  patchSource = patchSource.replace('import { Log } from "../util"', 'import { Log } from "./util-index.runtime.ts"')
  patchSource = patchSource.replace('import * as Bom from "../util/bom"', 'import * as Bom from "./bom.runtime.ts"')
  patchSource = patchSource.replace('export * as Patch from "."', "")
  fs.writeFileSync(patchRuntime, patchSource, "utf-8")

  return {
    patchURL: pathToFileURL(patchRuntime).href,
    bomURL: pathToFileURL(bomRuntime).href,
  }
}

const runtime = prepareRuntimeModules()
const patchModule = await import(runtime.patchURL)
const bomModule = await import(runtime.bomURL)
const { parsePatch, deriveNewContentsFromChunks } = patchModule
const { join, split } = bomModule

function generateUnifiedDiff(oldContent, newContent) {
  const oldLines = oldContent.split("\n")
  const newLines = newContent.split("\n")
  let diff = "@@ -1 +1 @@\n"
  const maxLen = Math.max(oldLines.length, newLines.length)
  let hasChanges = false
  for (let i = 0; i < maxLen; i++) {
    const oldLine = oldLines[i] || ""
    const newLine = newLines[i] || ""
    if (oldLine !== newLine) {
      if (oldLine) diff += `-${oldLine}\n`
      if (newLine) diff += `+${newLine}\n`
      hasChanges = true
      continue
    }
    if (oldLine) diff += ` ${oldLine}\n`
  }
  return hasChanges ? diff : ""
}

function countDiffLines(oldContent, newContent) {
  const oldLines = oldContent.split("\n")
  const newLines = newContent.split("\n")
  const maxLen = Math.max(oldLines.length, newLines.length)
  let additions = 0
  let deletions = 0
  for (let i = 0; i < maxLen; i++) {
    const oldLine = oldLines[i] || ""
    const newLine = newLines[i] || ""
    if (oldLine === newLine) continue
    if (oldLine) deletions++
    if (newLine) additions++
  }
  return { additions, deletions }
}

function snapshotTree(rootDir) {
  const entries = []
  function walk(rel) {
    const abs = rel ? path.join(rootDir, rel) : rootDir
    const dirEntries = fs.readdirSync(abs, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))
    for (const entry of dirEntries) {
      const nextRel = rel ? path.join(rel, entry.name) : entry.name
      const normalized = nextRel.replaceAll(path.sep, "/")
      if (entry.isDirectory()) {
        entries.push({ path: normalized + "/", type: "dir" })
        walk(nextRel)
        continue
      }
      const data = fs.readFileSync(path.join(rootDir, nextRel))
      entries.push({ path: normalized, type: "file", contentBase64: data.toString("base64") })
    }
  }
  walk("")
  return entries
}

function fail(message) {
  throw new Error(message)
}

function execute(worktree, patchText) {
  if (!patchText) fail("patchText is required")

  let hunks
  try {
    hunks = parsePatch(patchText).hunks
  } catch (error) {
    fail(`apply_patch verification failed: ${error}`)
  }

  if (hunks.length === 0) {
    const normalized = patchText.replace(/\r\n/g, "\n").replace(/\r/g, "\n").trim()
    if (normalized === "*** Begin Patch\n*** End Patch") fail("patch rejected: empty patch")
    fail("apply_patch verification failed: no hunks found")
  }

  const fileChanges = []
  for (const hunk of hunks) {
    const filePath = path.resolve(worktree, hunk.path)
    if (hunk.type === "add") {
      const oldContent = ""
      const nextContent = hunk.contents.length === 0 || hunk.contents.endsWith("\n") ? hunk.contents : `${hunk.contents}\n`
      const next = split(nextContent)
      const diff = generateUnifiedDiff(oldContent, next.text)
      const counts = countDiffLines(oldContent, next.text)
      fileChanges.push({ filePath, oldContent, newContent: next.text, type: "add", bom: next.bom, diff, ...counts })
      continue
    }

    if (hunk.type === "update") {
      let stats
      try {
        stats = fs.statSync(filePath)
      } catch {
        fail(`apply_patch verification failed: Failed to read file to update: ${filePath}`)
      }
      if (stats.isDirectory()) fail(`apply_patch verification failed: Failed to read file to update: ${filePath}`)
      const source = split(fs.readFileSync(filePath, "utf-8"))
      let fileUpdate
      try {
        fileUpdate = deriveNewContentsFromChunks(filePath, hunk.chunks)
      } catch (error) {
        fail(`apply_patch verification failed: ${error}`)
      }
      const movePath = hunk.move_path ? path.resolve(worktree, hunk.move_path) : undefined
      const diff = generateUnifiedDiff(source.text, fileUpdate.content)
      const counts = countDiffLines(source.text, fileUpdate.content)
      fileChanges.push({
        filePath,
        oldContent: source.text,
        newContent: fileUpdate.content,
        type: movePath ? "move" : "update",
        movePath,
        bom: fileUpdate.bom,
        diff,
        ...counts,
      })
      continue
    }

    try {
      const source = split(fs.readFileSync(filePath, "utf-8"))
      const diff = generateUnifiedDiff(source.text, "")
      const counts = countDiffLines(source.text, "")
      fileChanges.push({ filePath, oldContent: source.text, newContent: "", type: "delete", bom: source.bom, diff, ...counts })
    } catch (error) {
      fail(`apply_patch verification failed: ${error.message}`)
    }
  }

  for (const change of fileChanges) {
    switch (change.type) {
      case "add":
      case "update":
        fs.mkdirSync(path.dirname(change.filePath), { recursive: true })
        fs.writeFileSync(change.filePath, join(change.newContent, change.bom), "utf-8")
        break
      case "move":
        fs.mkdirSync(path.dirname(change.movePath), { recursive: true })
        fs.writeFileSync(change.movePath, join(change.newContent, change.bom), "utf-8")
        fs.rmSync(change.filePath, { force: true })
        break
      case "delete":
        fs.rmSync(change.filePath)
        break
    }
  }

  const lines = fileChanges.map((change) => {
    if (change.type === "add") return `A ${path.relative(worktree, change.filePath).replaceAll(path.sep, "/")}`
    if (change.type === "delete") return `D ${path.relative(worktree, change.filePath).replaceAll(path.sep, "/")}`
    const target = change.movePath ?? change.filePath
    return `M ${path.relative(worktree, target).replaceAll(path.sep, "/")}`
  })

  return {
    ok: true,
    diff: fileChanges.map((change) => `${change.diff}\n`).join(""),
    files: fileChanges.map((change) => ({
      filePath: change.filePath,
      relativePath: path.relative(worktree, change.movePath ?? change.filePath).replaceAll(path.sep, "/"),
      type: change.type,
      patch: change.diff,
      additions: change.additions,
      deletions: change.deletions,
      movePath: change.movePath,
    })),
    output: `Success. Updated the following files:\n${lines.join("\n")}`,
    tree: snapshotTree(worktree),
  }
}

const input = JSON.parse(fs.readFileSync(0, "utf-8"))
try {
  const result = execute(input.worktree, input.patchText)
  process.stdout.write(JSON.stringify(result))
} catch (error) {
  process.stdout.write(JSON.stringify({ ok: false, error: error.message, tree: snapshotTree(input.worktree) }))
}
EOF

printf 'synced OpenCode apply_patch snapshot at %s\n' "$sha"
