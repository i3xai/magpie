// docs-index.mjs writes everything built from the docs pages:
// public/llms-full.txt (llms-full.mjs), public/search/<lang>.json
// (search-index.mjs) and docs-index.sums, a sha256sum line for each file
// they are built from. `node llms-full.mjs` and `node search-index.mjs`
// each run all of it. internal/testenv fails every Go package's test run
// while a page's sum differs from docs-index.sums, so a Go change that
// edits a docs page and runs only Go tests can't leave the two files
// stale (five times on main from 11-13 to 11-17).
import { createHash } from "node:crypto";
import { readFile, readdir, writeFile } from "node:fs/promises";

const pub = new URL("./public/", import.meta.url);
export const SUMS = new URL("./docs-index.sums", import.meta.url);

// inputs are the files under public/ both builds read, in name order
export async function inputs() {
  const names = ["llms.txt"];
  for (const dir of ["docs/", "docs/zh/", "docs/ja/"]) {
    for (const f of await readdir(new URL(dir, pub))) if (f.endsWith(".html") && f !== "contributors.html") names.push(dir + f);
  }
  return names.sort();
}

// sums is docs-index.sums' text: "<sha256>  <path under public/>" a line
export async function sums() {
  const lines = [];
  for (const n of await inputs()) lines.push(createHash("sha256").update(await readFile(new URL(n, pub))).digest("hex") + "  " + n);
  return lines.join("\n") + "\n";
}

// The two builders are given to it and not imported: each of them writes
// what it builds through this file, and importing the one that is running
// the script waits for that script's own top-level await, which waits for
// this. `node search-index.mjs` and `node llms-full.mjs` exited 13 with
// "unsettled top-level await" and wrote nothing, so the command the docs
// tell a writer to run did not build the index.
export async function writeAll({ llmsFull, searchIndex }) {
  await writeFile(new URL("llms-full.txt", pub), await llmsFull());
  for (const [lang, json] of Object.entries(await searchIndex())) await writeFile(new URL(`search/${lang}.json`, pub), json);
  await writeFile(SUMS, await sums());
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const { build: llmsFull } = await import("./llms-full.mjs");
  const { build: searchIndex } = await import("./search-index.mjs");
  await writeAll({ llmsFull, searchIndex });
}
