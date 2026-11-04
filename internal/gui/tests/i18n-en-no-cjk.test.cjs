// Run with Node's test runner; needs no browser. English is the key of
// every string (t() falls back to it), so a Chinese word left in one shows
// to every English reader (fangleeway on Discord: taking Claude Code off
// magpie said "「接入」 goes off" in the English window). No English string
// holds Chinese, Japanese or Korean text or their punctuation: the keys of
// i18n.js and of each plugin's i18n.js, the advice templates (NOTICES), the
// literals passed to t() in app.js and the plugins' scripts, and
// index.html's data-t text. The few that show such text on purpose are
// listed below with why.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
// Han, kana, Hangul, CJK punctuation (「」、。) and full-width punctuation
// (，：（）！？), but not the full-width ＋ the Add buttons draw as a sign
const CJK = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}　-〿！-＊，-／：-？]/u;
const ON_PURPOSE = new Map([
  ["Aa 0123 · 中文", "the font preview shows Chinese in the picked font"],
  ["万 / 亿", "the Chinese number units, named by themselves"],
  ["After the word passport, 护照 or パスポート", "the words masking looks for, as it matches them"],
  ["A date after birthday, DOB, born or 生日; a date alone stays", "the words masking looks for, as it matches them"],
  ["After S/N, Serial or 序列号", "the words masking looks for, as it matches them"],
]);
const bad = (list) => [...new Set(list)].filter((s) => CJK.test(s) && !ON_PURPOSE.has(s));

test("no English key in i18n.js or a plugin's i18n.js holds CJK text", () => {
  const { files } = strings();
  const found = [];
  for (const { file, dict } of files) for (const [lang, words] of Object.entries(dict)) for (const k of bad(Object.keys(words))) found.push(`${file} ${lang}: ${k}`);
  assert.deepEqual([...new Set(found.map((f) => f.replace(/ [\w-]+: /, ": ")))], []);
});

test("no advice template holds CJK text", () => {
  const src = fs.readFileSync(path.join(assets, "i18n.js"), "utf8");
  const NOTICES = new Function(src.slice(0, src.indexOf("\n};") + 3) + src.slice(src.indexOf("const NOTICES"), src.indexOf("\n];", src.indexOf("const NOTICES")) + 3) + "; return NOTICES;")();
  assert.ok(NOTICES.length > 10, "NOTICES read");
  assert.deepEqual(bad(NOTICES), []);
});

test("no English literal given to t() holds CJK text", () => {
  const files = [path.join(assets, "app.js")];
  for (const id of fs.readdirSync(path.join(assets, "plugins"))) {
    const dir = path.join(assets, "plugins", id);
    if (!fs.statSync(dir).isDirectory()) continue;
    for (const f of fs.readdirSync(dir)) if (f.endsWith(".js") && f !== "i18n.js") files.push(path.join(dir, f));
  }
  const found = [];
  for (const file of files) {
    const src = fs.readFileSync(file, "utf8");
    let n = 0;
    for (const m of src.matchAll(/(?<![\w$.])t\(\s*("(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*')/g)) {
      n++;
      const s = m[1][0] === '"' ? JSON.parse(m[1]) : m[1].slice(1, -1);
      if (bad([s]).length) found.push(`${path.relative(assets, file)}: ${s}`);
    }
    if (file.endsWith("app.js")) assert.ok(n > 1000, `app.js's t() literals read (${n})`);
  }
  assert.deepEqual(found, []);
});

test("no data-t text in index.html holds CJK text", () => {
  const html = fs.readFileSync(path.join(assets, "index.html"), "utf8");
  const said = [
    ...[...html.matchAll(/<[^>]*\sdata-t(?=[\s>])[^>]*>([^<]*)/g)].map((m) => m[1]),
    ...[...html.matchAll(/\sdata-tt="([^"]*)"/g)].map((m) => m[1]),
  ].map((s) => s.trim()).filter(Boolean);
  assert.ok(said.length > 20, `index.html's data-t read (${said.length})`);
  assert.deepEqual(bad(said), []);
});
