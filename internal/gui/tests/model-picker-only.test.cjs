// Run with Node's test runner; no browser. Every model a user picks is
// picked in the one model picker, the Agents page's (app.js's
// openModelPicker / openPicker): its search, its rail of providers and
// routing groups, its logos and badges. The owner, 11-27, of a plain
// "Model" menu on Library › billion-context: 「从根上避免图一这种自定义的
// Modal Picker再次出现，一定统一使用图二这种Modal Picker」.
//
// So no page draws a model choice in the app's plain menu: an
// openProtoMenu (or sessPick) call titled for models, searching models,
// or listing a models array's items is red here, unless KEPT says why that
// list isn't a choice of a model to use. A new model choice goes through
// openModelPicker (or modelButton for its trigger), never KEPT.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { test } = require("node:test");

const assets = path.resolve(__dirname, "../assets");

// menus that name models and aren't a choice of a model to use, each with
// why; matched by file and a string the call holds
const KEPT = [
  ["plugins/gateway/keys.js", '"Models this key may use"', "the models, providers' every model, accounts, groups and re: patterns a gateway key is held to: an access list, several at once; moving it onto the shared picker is #2496"],
  ["plugins/gateway/keys.js", '"Models this limit counts"', "the models a key's limit counts, All models and providers' wildcards among them: a scope, several at once"],
  ["app.js", 't("Compact at {n}"', "which of one provider's model ids a Compact at size applies to, in its editor: a scope, several at once"],
  ["app.js", '"Model name in replies"', "how magpie names a model in its replies, a naming style, not a model"],
  ["app.js", '"What this model makes"', "a model's kind (chat, images…), not a model"],
  ["modelorder.js", '"API this model is asked on"', "the API a model is asked on, not a model"],
  ["privacymodel.js", '"The local model"', "the on-device detector the privacy filter runs, downloaded files, not a model served to agents"],
  ["plugins/usage/page.js", '"All models"', "a filter over the model ids usage records name, many no longer served, not a choice of a model to use"],
  ["modellimits.js", '"Pick from models.dev"', "the models.dev entry a model's context and prices are read from, catalog metadata, not a model to use"],
  ["plugins/gateway/settings.js", '"Every model"', "whether a context cap applies to every model or those without their own, not a model"],
];

// the files with code a page runs, tests and translations left out
function files(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== "vendor") files(p, out); } else if (e.name.endsWith(".js") && !/i18n/.test(e.name)) out.push(p);
  }
  return out;
}

// the whole call from its "(" on: strings, templates and comments skipped
function callText(src, at) {
  let depth = 0;
  for (let i = at; i < src.length && i < at + 8000; i++) {
    const c = src[i];
    if (c === '"' || c === "'" || c === "`") {
      for (i++; i < src.length && src[i] !== c; i++) if (src[i] === "\\") i++;
      continue;
    }
    if (c === "/" && src[i + 1] === "/") { i = src.indexOf("\n", i); continue; }
    if (c === "(") depth++;
    else if (c === ")" && --depth === 0) return src.slice(at, i + 1);
  }
  return src.slice(at, at + 8000);
}

// the strings a call is given at its own level: its head, its search
function topStrings(call) {
  const out = [];
  let depth = 0;
  for (let i = 0; i < call.length; i++) {
    const c = call[i];
    if (c === '"' || c === "'") {
      const s = i;
      for (i++; i < call.length && call[i] !== c; i++) if (call[i] === "\\") i++;
      if (depth === 1) out.push(call.slice(s + 1, i));
      continue;
    }
    if (c === "`") { for (i++; i < call.length && call[i] !== "`"; i++) if (call[i] === "\\") i++; continue; }
    if ("([{".includes(c)) depth++;
    else if (")]}".includes(c)) depth--;
  }
  return out;
}

const MODEL_WORD = /\bmodels?\b/i;
const SEARCH = /^(search|find|filter)\b.*\bmodel/i;
// a list made from a models array: lib.subagentModels, d.models,
// (models || []).map, s.titleModels…
const FROM_MODELS = /[mM]odels\)?(\s*\|\|\s*\[\]\))?\s*\.(map|flatMap|filter)\(/;

function offenders() {
  const out = [];
  for (const file of files(assets)) {
    const rel = path.relative(assets, file).split(path.sep).join("/");
    const src = fs.readFileSync(file, "utf8");
    for (const m of src.matchAll(/\b(openProtoMenu|sessPick)\(/g)) {
      const before = src.slice(Math.max(0, m.index - 9), m.index);
      if (/function\s*$/.test(before)) continue; // the menu's own definition
      const call = callText(src, m.index + m[1].length);
      const line = src.slice(0, m.index).split("\n").length;
      // the lines just before the call build its options
      const lead = src.slice(0, m.index).split("\n").slice(-12).join("\n");
      const strs = topStrings(call);
      const why = [];
      // a class list (model-account-menu) is no title
      const head = strs.find((s) => MODEL_WORD.test(s) && !/^[a-z0-9 -]*-[a-z0-9 -]*$/.test(s));
      if (head) why.push(`titled "${head}"`);
      const search = strs.find((s) => SEARCH.test(s));
      if (search) why.push(`searching "${search}"`);
      if (FROM_MODELS.test(call) || FROM_MODELS.test(lead) && /\b(opts|options|items|list)\b/.test(call)) why.push("listing a models array");
      if (!why.length) continue;
      if (KEPT.some(([f, s]) => f === rel && call.includes(s))) continue;
      out.push(`${rel}:${line}: ${m[1]} ${why.join(", ")}`);
    }
  }
  return out;
}

test("every model choice is the one model picker (openModelPicker)", () => {
  const bad = offenders();
  assert.deepEqual(bad, [], "a model is picked in a menu of a page's own; draw it with openModelPicker / modelButton (app.js):\n" + bad.join("\n"));
});

test("every kept menu is still there, so KEPT names no list that is gone", () => {
  for (const [f, s] of KEPT) {
    const src = fs.readFileSync(path.join(assets, f), "utf8");
    assert(src.includes(s), `${f} no longer holds ${s}: take it out of KEPT`);
  }
});

// a model search of a page's own is a picker of its own in the making:
// each one is the model picker's or one KEEP_SEARCH says isn't a choice
const MODEL_SEARCH = /^(search|find|filter)\b(,? (a|for|or type any))? models?\b/i;
const KEEP_SEARCH = [
  ["app.js", "Filter, or type any model id…", "the model picker's own search"],
  ["app.js", "Search models", "an agent's model list, which models its picker shows, and Compact at's scope (KEPT)"],
  ["modelfind.js", "Search models", "Model settings' search over a provider's models, to set them up"],
  ["plugins/gateway/client.js", "Find a model…", "the gateway's model list, to read and copy ids"],
  ["plugins/gateway/keys.js", "Search models, or type re: and a pattern", "a key's access list (KEPT)"],
  ["plugins/usage/client.js", "Search models, providers, sessions", "the usage ledger's search"],
];

test("every model search is the model picker's or kept", () => {
  const bad = [];
  for (const file of files(assets)) {
    const rel = path.relative(assets, file).split(path.sep).join("/");
    for (const m of fs.readFileSync(file, "utf8").matchAll(/"([^"\n]{1,60})"/g)) {
      if (MODEL_SEARCH.test(m[1]) && !KEEP_SEARCH.some(([f, s]) => f === rel && s === m[1])) bad.push(`${rel}: "${m[1]}"`);
    }
  }
  assert.deepEqual(bad, [], "a model search of a page's own; pick models with openModelPicker (app.js):\n" + bad.join("\n"));
});

// the guard itself: a menu of models as Library's was, red
test("the guard finds a model menu of a page's own", () => {
  const call = 'openProtoMenu(b, d.models.map((m) => ({ v: m.id, name: m.name })), v, pick, "Model", "", "left", false, "Search models")';
  const strs = topStrings(callText(call, "openProtoMenu".length));
  assert(strs.some((s) => MODEL_WORD.test(s)) && strs.some((s) => SEARCH.test(s)));
  assert(FROM_MODELS.test(call));
});
