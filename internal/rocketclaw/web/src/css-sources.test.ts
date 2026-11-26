import { expect, test } from "bun:test";
import { mkdir, mkdtemp, rm, symlink } from "node:fs/promises";
import { join } from "node:path";
import tailwind from "@tailwindcss/postcss";
import postcss from "postcss";

test("the Web stylesheet scans app files but not installed packages", async () => {
  const web = join(import.meta.dir, "..");
  const scratch = join(web, "../../..", ".tmp");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "css-sources-"));
  try {
    const app = join(project, "app");
    const modules = join(project, "node_modules");
    await Promise.all([mkdir(app), mkdir(modules)]);
    for (const name of ["tailwindcss", "tw-animate-css", "shadcn"]) {
      await symlink(join(web, "node_modules", name), join(modules, name));
    }
    const input = await Bun.file(join(web, "app/globals.css")).text();
    const from = join(app, "globals.css");
    await Bun.write(from, input);
    await Bun.write(join(app, "index.html"), `<div class="${["z-[", 718, "]"].join("")}"></div>`);
    await Bun.write(join(modules, "dependency.html"), `<div class="${["z-[", 719, "]"].join("")}"></div>`);

    const { css } = await postcss([tailwind({ base: project, optimize: true })]).process(input, { from });
    expect(Array.from(css.matchAll(/z-index:\s*(718|719)\b/g), ([, value]) => value)).toEqual(["718"]);
  } finally {
    await rm(project, { recursive: true });
  }
});
