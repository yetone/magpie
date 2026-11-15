const assert = require("node:assert/strict");
const { test } = require("node:test");
const { load } = require("./routing-group-source.cjs");
function data() {
  const id = "p/base:high#acct";
  return { models: [{ id: "p/base", name: "Astra", provider: "p", providerName: "Provider" }],
    groups: [{ id: "g", name: "Main", ref: "auto/main", members: [id, "group/nested", "off/base"],
      patterns: [{ pattern: "p/glob-*" }], fast: [id], memberInfo: [
        { id, free: true }, { id: "off/base", providerOff: true, name: "Disabled", model: "Lost" },
      ] }, { id: "nested", name: "Nested", members: [] }],
    accounts: { p: [{ id: "p/acct", name: "Account A" }] }, drawers: [], deciders: [] };
}

test("successive queries reuse group search text instead of rebuilding member labels", () => {
  const state = data(), g = state.groups[0], api = load(state);
  let reads = 0;
  const name = state.models[0].name;
  Object.defineProperty(state.models[0], "name", { get: () => { reads++; return name; } });
  assert.equal(api.groupMatches(g, ["astra"]), true);
  const firstReads = reads;
  for (const query of ["a", "as", "ast", "astra", "missing", "provider"]) api.groupMatches(g, [query]);
  assert.equal(reads, firstReads, "typing another query rebuilt the group's model labels");
});

test("cached search preserves AND, substrings, refs, patterns, accounts and member labels", () => {
  const state = data(), api = load(state), g = state.groups[0];
  for (const words of [["main", "astra"], ["roup/g"], ["auto/main"], ["p/glob-*"], ["account a"],
    ["high reasoning", "fast", "free"], ["routing group", "nested"], ["disabled", "lost", "switched off"]]) {
    assert.equal(api.groupMatches(g, words), true, words.join(" + "));
  }
  assert.equal(api.groupMatches(g, ["astra", "missing"]), false);
});

test("search text follows data replacement, language changes and five-minute eviction", () => {
  const state = data(), api = load(state), g = state.groups[0];
  assert.equal(api.groupMatches(g, ["routing group"]), true);
  api.lang("zh");
  assert.equal(api.groupMatches(g, ["路由组"]), true);
  assert.equal(api.groupMatches(g, ["routing group"]), false);
  const next = data();
  next.models[0].name = "Sol"; next.accounts.p[0].name = "Account B";
  next.groups[1].name = "Renamed"; next.groups[0].memberInfo[0].free = false; next.groups[0].fast = [];
  api.refresh(next);
  const ng = next.groups[0];
  assert.equal(api.groupMatches(ng, ["sol", "account b", "renamed"]), true);
  for (const word of ["astra", "account a", "fast", "free"]) assert.equal(api.groupMatches(ng, [word]), false, word);
  api.visible(false); api.expire(); api.visible(true);
  assert.equal(api.groupMatches(ng, ["sol", "account b"]), true);
});
