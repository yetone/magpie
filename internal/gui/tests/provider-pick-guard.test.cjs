// Run with Node's test runner; see README.md.
// The reader picks providers one way: the shared provider combobox,
// provcombo.js (the owner, 11-27: "最好是个Combo Box，另外要有供应商的
// Icon", and "stop hand-rolled pickers at the root and always use the one
// shared picker"). The app's plain menu (openProtoMenu) and the filter
// button (sessPick, openSessCombo) draw no logo and no search, so a
// provider choice drawn through them fails here, by file and line.
// Each site moved onto the combobox is named, and must still use it.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");

const root = path.resolve(__dirname, "../assets");
const files = [];
(function walk(d) {
  for (const e of fs.readdirSync(d, { withFileTypes: true })) {
    const p = path.join(d, e.name);
    if (e.isDirectory()) { if (!["third_party", "vendor", "node_modules"].includes(e.name)) walk(p); }
    else if (p.endsWith(".js") && !p.endsWith("i18n.js") && !p.endsWith("provcombo.js")) files.push(p);
  }
})(root);
const src = Object.fromEntries(files.map((f) => [path.relative(root, f).split(path.sep).join("/"), fs.readFileSync(f, "utf8")]));

// calls: each call of name in s, from its name to its closing paren,
// strings and template literals skipped over
function calls(s, name) {
  const out = [];
  for (let i = s.indexOf(name + "("); i >= 0; i = s.indexOf(name + "(", i + 1)) {
    if (/[\w$.]/.test(s[i - 1] || "") || /function\s+$/.test(s.slice(Math.max(0, i - 12), i))) continue;
    let j = i + name.length + 1, depth = 0, q = null;
    for (; j < s.length; j++) {
      const c = s[j];
      if (q) { if (c === "\\") j++; else if (c === q) q = null; continue; }
      if (c === '"' || c === "'" || c === "`") q = c;
      else if ("([{".includes(c)) depth++;
      else if (")]}".includes(c)) { if (!depth) break; depth--; }
    }
    out.push({ at: i, text: s.slice(i, j + 1), line: s.slice(0, i).split("\n").length });
  }
  return out;
}

// what makes a menu's rows providers: they come from a list of them
// (/api/providers, the found groups' skippable, Usage's deleted and
// merged ones, a plugin's subscriptions), or the menu is headed as one
const PROVIDER = [
  /\bproviders\??\.providers\b/, /\bproviders\s*\.\s*(map|filter)\(/, /\.skippable\b/, /usage\/deleted\/into/,
  /\bmerged\.map\(/, /\bsubName\(/,
  /"(All providers|Every provider|Provider|Providers|Merged providers|Merge into|Sign in to which subscription|Search providers…)"/,
];
// the items an identifier names, when the call takes them (or maps
// them) so: its const or let a few lines up
function itemsOf(s, c) {
  const m = /^\w+\(\s*[\w$.]+\s*,\s*(?:"[^"]*"\s*,\s*[\w$.]+\s*,\s*)?([A-Za-z_$][\w$]*)\s*[,).]/.exec(c.text);
  if (!m) return "";
  const before = s.slice(0, c.at).split("\n").slice(-15).join("\n");
  const d = new RegExp(`(?:const|let)\\s+(?:[^;]*,\\s*)?${m[1]}\\s*=([^;]*)`).exec(before);
  return d ? d[1] : "";
}

test("no provider choice is drawn outside the shared provider combobox", () => {
  const bad = [];
  for (const [f, s] of Object.entries(src)) {
    for (const name of ["openProtoMenu", "sessPick", "openSessCombo"]) {
      for (const c of calls(s, name)) {
        const hay = c.text + "\n" + itemsOf(s, c);
        const hit = PROVIDER.find((r) => r.test(hay));
        if (!hit) continue;
        bad.push(`${f}:${c.line} ${name}(…) picks providers (${hit.exec(hay)[0]}); draw it with provCombo (provcombo.js)`);
      }
    }
    // a helper over sessPick, handed its "All providers" first
    for (const m of s.matchAll(/\b(\w+)\(\s*"[^"]*"\s*,\s*"(All providers|Every provider)"/g)) {
      bad.push(`${f}:${s.slice(0, m.index).split("\n").length} ${m[1]}("…", "${m[2]}") picks providers; draw it with provCombo (provcombo.js)`);
    }
  }
  assert.deepEqual(bad, []);
});

// the sites moved onto the combobox (11-27), each by what draws it
const SITES = [
  ["plugins/routing/client.js", /provCombo\("provField", skipBox,/, "Routing › Settings › Leave out of found groups"],
  ["syncfiles.js", /provCombo\("provField", exPick,/, "Sync › Not synced"],
  ["app.js", /provCombo\("provPick", pick, \{ items, value: panelUseProvider,/, "the panel's Usage › Provider filter"],
  ["plugins/usage/page.js", /provCombo\("provPick", \$\("#ledProvider"\),/, "Usage › requests › Provider filter"],
  ["plugins/usage/page.js", /provCombo\("provPop", b, \{[^]*?head: "Merged providers"/, "Usage › a provider's merged ones (Separate)"],
  ["plugins/usage/page.js", /provCombo\("provPop", b, \{[^]*?head: "Merge into"/, "Usage › a deleted provider › Merge into…"],
  ["plugins/redact/page.js", /provCombo\("provPick", pv,[^]*?all: "Every provider"/, "Privacy › requests › Provider filter"],
  ["plugins.js", /provCombo\("provPop", b, \{[^]*?head: "Sign in to which subscription"/, "Plugins › Sign in (a plugin with several subscriptions)"],
];

test("each site moved onto the provider combobox still uses it", () => {
  const gone = SITES.filter(([f, r]) => !r.test(src[f] || "")).map(([f, , what]) => `${what} (${f})`);
  assert.deepEqual(gone, []);
  assert.ok(fs.existsSync(path.join(root, "provcombo.js")), "provcombo.js is there");
  assert.match(src["app.js"], /import\("\.\/provcombo\.js"\)/, "app.js imports it on first use");
});
