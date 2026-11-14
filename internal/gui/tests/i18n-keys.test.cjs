// Run with Node's test runner; needs no browser. A key written twice in one
// language is a trap: the later one silently wins, so an edit to the first
// does nothing. Every key appears once per language, in i18n.js or in one
// plugin's i18n.js (strings.cjs), never in two of them, and a plugin's
// file says every one of its keys in every language.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { strings } = require("./strings.cjs");

test("no language has a key twice", () => {
  const { files, twice } = strings();
  for (const { file, src } of files) {
    const body = file === "i18n.js" ? src.slice(0, src.indexOf("\n};")) : src;
    let lang, seen;
    body.split("\n").forEach((line, i) => {
      const head = line.match(/^  "?([\w-]+)"?: \{/);
      if (head) return void ((lang = head[1]), (seen = new Map()));
      const key = line.match(/^    ("(?:[^"\\]|\\.)*")\s*:/);
      if (!key || !lang) return;
      if (seen.has(key[1])) twice.push(`${file} ${lang} ${key[1]}: lines ${seen.get(key[1])} and ${i + 1}`);
      else seen.set(key[1], i + 1);
    });
  }
  assert.deepEqual(twice, []);
});

test("a plugin's words are said in every language", () => {
  for (const { file, dict } of strings().files.slice(1)) {
    assert.deepEqual(Object.keys(dict).sort(), ["de", "ja", "zh", "zh-TW"], file);
    const zh = Object.keys(dict.zh);
    assert.ok(zh.length, `${file} says nothing`);
    for (const lang of ["zh-TW", "ja", "de"]) assert.deepEqual(Object.keys(dict[lang]).sort(), [...zh].sort(), `${file} ${lang}`);
  }
});

test("a plugin's words are its own", () => {
  // A word the core or another plugin says too belongs in i18n.js: in a
  // plugin's file, it is English wherever that plugin is off, and a
  // word a later change starts saying elsewhere ("Check again", Library's
  // and then Settings' search keys) went untranslated there
  const fs = require("node:fs"), path = require("node:path");
  const repo = path.resolve(__dirname, "../../.."), assets = path.join(repo, "internal/gui/assets");
  const texts = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) {
        // third_party is vendored code, which says nothing to the window
        if (!["node_modules", ".git", "site", "tests", "testdata", "docs", "third_party"].includes(e.name)) walk(p);
      } else if (/\.(go|js|html|css)$/.test(e.name) && e.name !== "i18n.js") {
        let text = fs.readFileSync(p, "utf8");
        // a comment line says nothing to the window: Go's "(View routing,
        // the …" and "Use the library's sets …" cite a plugin's words
        // (#5332, #5327, #5426, #5482)
        if (/\.(go|js)$/.test(e.name)) text = text.split("\n").filter((l) => !/^\s*\/\//.test(l)).join("\n");
        texts.push([p, text]);
      }
    }
  };
  walk(repo);
  // said as words, not inside longer ones: a comment's "fetchClaudeRuns
  // asks" doesn't say Library's "Runs as" (#3897)
  const letter = /[\p{L}\p{N}_]/u;
  const says = (s, w) => {
    for (let i = s.indexOf(w); i >= 0; i = s.indexOf(w, i + 1)) {
      const glued = (letter.test(w[0]) && letter.test(s[i - 1] ?? "")) || (letter.test(w.at(-1)) && letter.test(s[i + w.length] ?? ""));
      if (!glued) return true;
    }
    return false;
  };
  const shared = [];
  for (const { file, dict } of strings().files.slice(1)) {
    const own = path.join(assets, path.dirname(file)) + path.sep;
    const rest = texts.filter(([p]) => !p.startsWith(own));
    for (const k of Object.keys(dict.zh)) {
      const at = rest.find(([, s]) => says(s, k) || says(s, JSON.stringify(k).slice(1, -1)));
      if (at) shared.push(`${file} ${JSON.stringify(k)}: ${path.relative(repo, at[0])} says it too`);
    }
  }
  assert.deepEqual(shared, []);
});

test("every word the window says has a key", () => {
  // Usage's empty Sessions text kept "Codex's, OpenCode's" after i18n.js's
  // key gained Hermes, so it showed in English in every language (Outvi V)
  const fs = require("node:fs"), path = require("node:path");
  const { zh } = strings().I18N;
  const assets = path.resolve(__dirname, "../assets"), files = [];
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith(".js") && e.name !== "i18n.js") files.push(p);
    }
  };
  walk(assets);
  const english = new Set(["magpie", "ID"]), missing = [];
  for (const f of files) {
    const src = fs.readFileSync(f, "utf8"), re = /\bt\(\s*"((?:[^"\\]|\\.)*)"\s*[,)]/g;
    for (let m; (m = re.exec(src)); ) {
      const k = JSON.parse(`"${m[1]}"`);
      if (!(k in zh) && !english.has(k)) missing.push(`${path.relative(assets, f)}:${src.slice(0, m.index).split("\n").length} ${JSON.stringify(k)}`);
    }
  }
  assert.deepEqual(missing, []);
});
