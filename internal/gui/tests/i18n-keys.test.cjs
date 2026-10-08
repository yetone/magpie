// Run with Node's test runner; needs no browser. A key written twice in one
// language of i18n.js is a trap: the later one silently wins, so an edit to
// the first does nothing. Every key appears once per language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");

test("no language in i18n.js has a key twice", async () => {
  const src = await fs.readFile(path.resolve(__dirname, "../assets/i18n.js"), "utf8");
  const body = src.slice(0, src.indexOf("\n};"));
  const twice = [];
  let lang, seen;
  body.split("\n").forEach((line, i) => {
    const head = line.match(/^  "?([\w-]+)"?: \{/);
    if (head) return void ((lang = head[1]), (seen = new Map()));
    const key = line.match(/^    ("(?:[^"\\]|\\.)*")\s*:/);
    if (!key || !lang) return;
    if (seen.has(key[1])) twice.push(`${lang} ${key[1]}: lines ${seen.get(key[1])} and ${i + 1}`);
    else seen.set(key[1], i + 1);
  });
  assert.deepEqual(twice, []);
});
