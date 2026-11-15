// The real routing lookup/search functions, without a DOM or a live API.
// Counting model reads tests algorithmic work without a wall-clock limit.
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const assets = process.env.MAGPIE_GROUP_ASSETS || path.resolve(__dirname, "../assets");
const source = fs.readFileSync(path.join(assets, "plugins/routing/client.js"), "utf8");
const app = fs.readFileSync(path.join(assets, "app.js"), "utf8");
const section = (s, a, b) => { const start = s.indexOf(a); return start < 0 ? "" : s.slice(start, s.indexOf(b, start)); };
function load(data) {
  let visible = true, timer = null, timerDelay = 0;
  const context = vm.createContext({
    groups: data, locale: "en", shown: () => visible, gShown: () => false,
    setTimeout: (fn, ms) => { timer = fn; timerDelay = ms; return 1; }, clearTimeout: () => { timer = null; },
    t: (s, vars) => Object.entries(vars || {}).reduce((s, [k, v]) => s.replaceAll("{" + k + "}", v), context.locale === "zh" && s === "routing group" ? "路由组" : s),
  });
  vm.runInContext([
    section(app, "function modelTells(", "// tellTag:"),
    section(source, "  // Built on first lookup,", "  // poolOf is"),
    section(source, "  const LEVELS =", "  // a group's editor,"),
    section(source, "  const offOf =", "  function renderGroups("),
    section(source, "  function groupMatches(", "  // rowOrFlag:"),
    `globalThis.api = { splitMember, modelOf, memberAccount, memberLabel, subOf, offOf, freeIn, groupMatches,
      refresh: (x) => { groups = x; }, lang: (x) => { locale = x; },
      cache: () => typeof groupIndex === "undefined" ? null : groupIndex,
      keep: () => { if (typeof keepGroupCaches === "function") keepGroupCaches(); } };`,
  ].join("\n"), context);
  return { ...context.api, visible: (on) => { visible = on; context.api.keep(); },
    expire: () => { const fn = timer; timer = null; fn?.(); }, delay: () => timerDelay, pending: () => !!timer };
}
module.exports = { load };
