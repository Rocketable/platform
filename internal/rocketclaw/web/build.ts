import { copyFile, mkdir, readdir, rename, rm } from "node:fs/promises";
import postcss from "postcss";
import tailwind from "@tailwindcss/postcss";

const outdir = new URL("../internal/web/dist/", import.meta.url).pathname;
await mkdir(outdir, { recursive: true });
for (const name of await readdir(outdir)) await rm(`${outdir}/${name}`, { recursive: true });
const result = await Bun.build({ entrypoints: ["./src/main.tsx"], outdir, target: "browser", minify: true, sourcemap: "linked", splitting: true, naming: "assets/[name]-[hash].[ext]", define: { "process.env.NODE_ENV": '"production"' } });
if (!result.success) throw new AggregateError(result.logs, "SPA build failed");
// Bun's debugId comment lacks Sentry's runtime registration; let the CLI own both IDs and maps.
for (const output of result.outputs) {
  if (output.path.endsWith(".js")) await Bun.write(output.path, (await output.text()).replace(/^\/\/# debugId=.*$/gm, ""));
  if (output.path.endsWith(".map")) {
    const map = await output.json();
    delete map.debugId;
    await Bun.write(output.path, JSON.stringify(map));
  }
}
await Bun.$`bunx --no-install sentry-cli sourcemaps inject ${outdir + "assets"}`;
const maps = new URL("../../../.tmp/sentry-sourcemaps/", import.meta.url).pathname;
await mkdir(maps, { recursive: true });
for (const name of await readdir(maps)) await rm(maps + name);
for (const name of await readdir(outdir + "assets")) {
  if (name.endsWith(".js")) await copyFile(outdir + "assets/" + name, maps + name);
  if (name.endsWith(".map")) await rename(outdir + "assets/" + name, maps + name);
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
