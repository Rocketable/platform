import { copyFile, mkdir, readdir, rm } from "node:fs/promises";
import path from "node:path";
import postcss from "postcss";
import tailwind from "@tailwindcss/postcss";

const outdir = new URL("../internal/web/dist/", import.meta.url).pathname;
await mkdir(outdir, { recursive: true });
for (const name of await readdir(outdir)) await rm(`${outdir}/${name}`, { recursive: true });
const result = await Bun.build({ entrypoints: ["./src/main.tsx"], outdir, target: "browser", minify: true, sourcemap: "linked", splitting: true, naming: { entry: "assets/[name]-[hash].[ext]", chunk: "assets/chunk-[hash].[ext]" }, define: { "process.env.NODE_ENV": '"production"' } });
if (!result.success) throw new AggregateError(result.logs, "SPA build failed");
const maps = new URL("../../../.tmp/posthog-sourcemaps/", import.meta.url).pathname;
await rm(maps, { recursive: true, force: true });
await mkdir(maps, { recursive: true });
for (const output of result.outputs.filter((output) => output.path.endsWith(".js"))) {
  const map = await Bun.file(`${output.path}.map`).json();
  const chunk = `!function(){try{var n=(new Error).stack;n&&((globalThis._posthogChunkIds=globalThis._posthogChunkIds||{})[n]=${JSON.stringify(map.debugId)})}catch(e){}}();\n`;
  await Bun.write(output.path, `${chunk}${await output.text()}\n//# chunkId=${map.debugId}\n`);
  await Bun.write(maps + path.basename(output.path) + ".map", JSON.stringify({ ...map, chunk_id: map.debugId, mappings: `;${map.mappings}` }));
  await rm(`${output.path}.map`);
  await copyFile(output.path, maps + path.basename(output.path));
}
const css = await postcss([tailwind({ optimize: true })]).process(await Bun.file("app/globals.css").text(), { from: "app/globals.css" });
const font = Bun.file("src/fonts/inter-latin.woff2");
const fontName = `assets/inter-${Bun.hash(await font.arrayBuffer()).toString(16)}.woff2`;
await Bun.write(outdir + fontName, font);
await Bun.write(outdir + "assets/inter-OFL.txt", Bun.file("src/fonts/OFL.txt"));
const styles = `@font-face{font-family:Inter;font-style:normal;font-weight:100 900;font-display:swap;src:url(/${fontName}) format("woff2")}\n${css.css}`;
const cssName = `assets/styles-${Bun.hash(styles).toString(16)}.css`;
await Bun.write(outdir + cssName, styles);
const entry = result.outputs.find((output) => output.kind === "entry-point")!;
const script = entry.path.slice(outdir.length);
await Bun.write(outdir + "index.html", (await Bun.file("index.html").text()).replace("</head>", `<link rel="stylesheet" href="/${cssName}" /><script type="module" src="/${script}"></script></head>`));
console.log(`Built SPA: ${outdir}index.html`);
