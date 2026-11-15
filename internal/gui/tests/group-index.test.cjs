const assert = require("node:assert/strict");
const { test } = require("node:test");
const { load } = require("./routing-group-source.cjs");
const model = (id, name = id) => ({ id, name, provider: "p", providerName: "Provider" });
const data = () => ({ models: [model("p/base"), model("p/literal:high"), model("p/base", "duplicate")],
  drawers: [model("p/image:high")], deciders: [model("p/judge")], groups: [
    { id: "nested", name: "Nested", members: [] }, { id: "hidden", hidden: true, members: [] },
    { id: "off", members: [], memberInfo: [{ id: "gone/model", providerOff: true, name: "Gone", model: "Model" }] },
  ], accounts: { p: [{ id: "p/acct", name: "Account A" }] } });

test("routing members use bounded model reads, including effort, account and missing IDs", () => {
  const models = Array.from({ length: 1000 }, (_, i) => model("p/model-" + i));
  let reads = 0;
  for (const m of models) { const id = m.id; Object.defineProperty(m, "id", { get: () => { reads++; return id; } }); }
  const api = load({ models, groups: [], drawers: [], deciders: [] });
  for (let i = 0; i < 10; i++) {
    assert.equal(api.modelOf("p/model-999:high#acct"), models[999]);
    assert.equal(api.modelOf("p/missing:high"), undefined);
  }
  assert.ok(reads < 10000, `member lookups rescanned the model catalog: ${reads} ID reads`);
});

test("indexed members preserve literal IDs, effort order, account names and first match", () => {
  const state = data(), api = load(state);
  assert.deepEqual(Array.from(api.splitMember("p/base:HIGH+low# acct ")), ["p/base", "low+high", "acct"]);
  assert.deepEqual(Array.from(api.splitMember("p/literal:high#acct")), ["p/literal:high", "", "acct"]);
  assert.deepEqual(Array.from(api.splitMember("p/image:high")), ["p/image:high", "", ""]);
  assert.equal(api.modelOf("p/base"), state.models[0], "a duplicate must keep find's first match");
  assert.equal(api.modelOf("p/judge:high"), state.deciders[0]);
  assert.equal(api.modelOf("p/image:high"), state.drawers[0]);
  assert.equal(api.memberAccount("p/base:high#acct").name, "Account A");
  assert.equal(api.subOf("group/nested").name, "Nested");
  assert.equal(api.subOf("group/hidden"), undefined);
  assert.equal(api.offOf("gone/model").name, "Gone");
  const next = data(); next.models[0].name = "Renamed"; next.accounts.p[0].name = "Account B";
  api.refresh(next);
  assert.equal(api.modelOf("p/base").name, "Renamed");
  assert.equal(api.memberAccount("p/base#acct").name, "Account B");
});

test("routing indexes are lazy, expire after five minutes away and rebuild from current data", () => {
  const state = data(), api = load(state);
  assert.equal(api.cache(), null);
  api.modelOf("p/base:high");
  assert.ok(api.cache(), "a lookup must build the index");
  const first = api.cache();
  api.visible(false);
  assert.equal(api.delay(), 300000);
  assert.equal(api.pending(), true);
  api.visible(true);
  assert.equal(api.pending(), false, "returning cancels expiry");
  assert.equal(api.cache(), first);
  api.visible(false); api.expire();
  assert.equal(api.cache(), null, "expiry must release the index");
  state.models[0].name = "After expiry";
  api.visible(true);
  assert.equal(api.modelOf("p/base").name, "After expiry");
  assert.notEqual(api.cache(), first);
});
