// The docs' index: the commands that build it work. `node search-index.mjs`
// and `node llms-full.mjs` are what a writer runs after changing a page,
// and each of them writes everything built from the pages. They exited 13
// with "unsettled top-level await" and wrote nothing: the script waited
// for the module it was running, which waited for it.
import test from "node:test";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
import { promisify } from "node:util";

const run = promisify(execFile);
const site = new URL(".", import.meta.url);
const pub = new URL("./public/", import.meta.url);

// what the commands write: the llms file, the three search indexes and the
// sums the Go suite holds the pages by
const written = ["public/llms-full.txt", "public/search/en.json", "public/search/ja.json", "public/search/zh.json", "docs-index.sums"];

for (const script of ["search-index.mjs", "llms-full.mjs"]) {
  test(`docs-index: \`node ${script}\` writes what the pages build`, async () => {
    const before = new Map();
    for (const f of written) before.set(f, await readFile(new URL(f, site), "utf8"));
    const { stdout, stderr } = await run(process.execPath, [script], { cwd: site.pathname });
    assert.equal(stderr.trim(), "", `${script} said something on stderr`);
    assert.equal(stdout.trim(), "", `${script} said something on stdout`);
    for (const f of written) {
      assert.equal(await readFile(new URL(f, site), "utf8"), before.get(f), `${script} changed ${f}: the committed index is stale, run the command and commit what it writes`);
    }
  });
}
