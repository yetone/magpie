// node --test site/livedb.test.mjs: a shared artifact's document store
// (livedb.js), the port of internal/artifact/db.go and caps.go's db rules,
// with its cases ported from db_test.go and guide_test.go. Cloudflare's
// SqlStorage is stood in for by node:sqlite, which Node 22.13 and later
// run without --experimental-sqlite (older 22.x need the flag).
import { test } from "node:test";
import assert from "node:assert/strict";
import { DatabaseSync } from "node:sqlite";
import { LEVEL, parseRules, canRead, checkPath, readCall, Store } from "./livedb.js";

// sqlStorage is a Durable Object's SqlStorage over node:sqlite, and its
// ctx.storage.transactionSync. fail, when set, is asked of each exec and
// throws for the ones it says yes to.
function sqlStorage() {
  const db = new DatabaseSync(":memory:");
  const box = { db, fail: null };
  box.sql = {
    exec(q, ...b) {
      if (box.fail && box.fail(q, b)) throw new Error("SQLITE_IOERR: injected");
      const st = db.prepare(q);
      let rows = [];
      let written = 0;
      if (/^\s*(SELECT|PRAGMA|WITH)\b/i.test(q)) rows = st.all(...b);
      else written = Number(st.run(...b).changes);
      return {
        toArray: () => rows,
        one() {
          if (rows.length !== 1) throw new Error(`Expected exactly one result from SQL query, but got ${rows.length}.`);
          return rows[0];
        },
        rowsWritten: written,
        [Symbol.iterator]: () => rows[Symbol.iterator](),
      };
    },
  };
  box.transactionSync = (fn) => {
    db.exec("BEGIN");
    try {
      const r = fn();
      db.exec("COMMIT");
      return r;
    } catch (e) {
      db.exec("ROLLBACK");
      throw e;
    }
  };
  return box;
}

const AGENT = { id: "", level: LEVEL.owner, agent: true };
const T0 = Date.UTC(2026, 10, 25, 9, 30, 0, 120);

function newStore(opts = {}) {
  const box = sqlStorage();
  const store = new Store(box.sql, opts.noTx ? undefined : box.transactionSync);
  const rules = parseRules(opts.decl || {});
  const as = (viewer) => (c, now = T0) => store.call(viewer, rules, c, now);
  return { box, store, rules, as, agent: as(AGENT) };
}

const err = (r) => (r.status === 200 ? "" : r.body.error.code);
const ids = (r) => {
  assert.equal(r.status, 200, JSON.stringify(r.body));
  return r.body.docs.map((d) => d.id).join(",");
};

test("paths: documents have an even number of segments, collections odd, in the letters a page may use", () => {
  for (const [p, doc, ok] of [
    ["tasks", false, true],
    ["tasks/t1", true, true],
    ["tasks/t1/notes", false, true],
    ["tasks", true, false],
    ["tasks/t1", false, false],
    ["", false, false],
    ["tasks//t1", true, false],
    ["tasks/..", true, false],
    ["tasks/.", true, false],
    ["tasks/a b", true, false],
    ["tasks/é", true, false],
    ["tasks/" + "a".repeat(201), true, false],
    ["tasks/" + "a".repeat(200), true, true],
    ["a/".repeat(16) + "a", false, false],
    ["a/".repeat(15) + "a", true, true],
    ["data/users/u_1:x@y+z~", false, true],
    ["data/users/u_1:x@y+z~/k", true, true],
  ]) {
    let e = null;
    try {
      checkPath(p, doc);
    } catch (x) {
      e = x;
    }
    assert.equal(e === null, ok, `checkPath(${JSON.stringify(p)}, ${doc}): ${e && e.message}`);
    if (e) assert.equal(e.code, "invalid_argument");
  }
  // ExampleCheckPath's words, and Go's %q
  assert.throws(() => checkPath("tasks", true), { code: "invalid_argument", message: `"tasks" has 1 segments, so it is a collection: a document's path has an even number` });
  assert.throws(() => checkPath("a/b", false), { message: `"a/b" has 2 segments, so it is a document: a collection's path has an odd number` });
  assert.throws(() => checkPath("", false), { message: `a path has 1 to 16 segments; "" has 0` });
  assert.throws(() => checkPath("t/a b", true), { message: `segment "a b": segments use letters, digits and _ - . ~ : @ + only` });
  assert.throws(() => checkPath("t/..", true), { message: `".." can't be a segment` });
  assert.throws(() => checkPath("t/" + "a".repeat(999), true), { message: "a path is at most 1000 bytes" });
  // a page's call answers the same
  const { agent } = newStore();
  const r = agent({ op: "get", path: "tasks" });
  assert.deepEqual(r, { status: 400, body: { error: { code: "invalid_argument", message: `"tasks" has 1 segments, so it is a collection: a document's path has an even number` } }, changed: [] });
});

// TestDBWrites: a document is set, merged into, read back and deleted;
// update needs it there; a delete of one not there is done; each write
// bumps its version.
test("writes: set, update merges, delete, versions, and the bodies refused", () => {
  const { agent, store } = newStore();
  assert.deepEqual(agent({ op: "get", path: "tasks/t1" }), { status: 200, body: { doc: null }, changed: [] });
  let r = agent({ op: "update", path: "tasks/t1", data: { a: 1 } });
  assert.equal(err(r), "invalid_argument");
  assert.equal(r.status, 400);
  assert.equal(r.body.error.message, "update needs tasks/t1 to exist; set makes it");
  r = agent({ op: "set", path: "tasks/t1", data: { title: "one", meta: { x: 1, y: 2 }, tags: ["a", "b"] } });
  assert.deepEqual(r, { status: 200, body: {}, changed: ["tasks/t1"] });
  r = agent({ op: "update", path: "tasks/t1", data: { meta: { y: 3 }, tags: ["c"] } }, T0 + 1000);
  assert.deepEqual(r, { status: 200, body: {}, changed: ["tasks/t1"] });
  const d = agent({ op: "get", path: "tasks/t1" }).body.doc;
  assert.deepEqual(d, { path: "tasks/t1", id: "t1", data: { meta: { x: 1, y: 3 }, tags: ["c"], title: "one" }, version: 2, updated: "2026-11-25T09:30:01.12Z" });
  // kept as Go marshals it: keys sorted
  assert.equal(JSON.stringify(d.data), `{"meta":{"x":1,"y":3},"tags":["c"],"title":"one"}`);
  assert.equal(store.dump()[0].version, 2);

  for (const [c, msg] of [
    [{ op: "set", path: "tasks/t1" }, "a document's body is a JSON object"],
    [{ op: "set", path: "tasks/t1", data: null }, "a document's body is a JSON object"],
    [{ op: "update", path: "tasks/t1" }, "an update's body is a JSON object"],
    [{ op: "set", path: "tasks", data: {} }, null],
  ]) {
    r = agent(c);
    assert.equal(err(r), "invalid_argument", JSON.stringify(c));
    if (msg) assert.equal(r.body.error.message, msg);
  }
  const deep = {};
  for (let i = 0, m = deep; i < 40; i++) m = m.n = {};
  r = agent({ op: "set", path: "tasks/deep", data: deep });
  assert.equal(r.body.error.message, "a document is at most 32 levels deep");
  let ok = {};
  for (let i = 0, m = ok; i < 31; i++) m = m.n = {};
  assert.equal(err(agent({ op: "set", path: "tasks/deep", data: ok })), "", "32 deep is taken");
  r = agent({ op: "set", path: "tasks/big", data: { s: "x".repeat(257 << 10) } });
  assert.equal(err(r), "invalid_argument");
  assert.equal(r.body.error.message, `the document is ${(257 << 10) + 8} bytes; the most is 262144 (256 KiB)`);
  // a body at the limit, counted as Go writes it: < is six bytes
  const fill = (256 << 10) - 8;
  assert.equal(err(agent({ op: "set", path: "tasks/edge", data: { s: "x".repeat(fill) } })), "");
  assert.equal(err(agent({ op: "set", path: "tasks/edge", data: { s: "x".repeat(fill - 1) + "<" } })), "invalid_argument");

  assert.deepEqual(agent({ op: "delete", path: "tasks/t1" }), { status: 200, body: {}, changed: ["tasks/t1"] });
  assert.deepEqual(agent({ op: "delete", path: "tasks/t1" }), { status: 200, body: {}, changed: [] });
  assert.equal(agent({ op: "get", path: "tasks/t1" }).body.doc, null);
  // a set again starts over at version 1
  agent({ op: "set", path: "tasks/t1", data: {} });
  assert.equal(agent({ op: "get", path: "tasks/t1" }).body.doc.version, 1);
});

test("merge: nested objects merge, arrays and other values replace, null is a value", () => {
  const { agent } = newStore();
  agent({ op: "set", path: "a/1", data: { o: { p: { q: 1, r: 2 }, s: [1, 2] }, n: 1, keep: true } });
  agent({ op: "update", path: "a/1", data: { o: { p: { r: 3 }, s: [9] }, n: null, x: { y: 1 } } });
  assert.deepEqual(agent({ op: "get", path: "a/1" }).body.doc.data, { keep: true, n: null, o: { p: { q: 1, r: 3 }, s: [9] }, x: { y: 1 } });
  // an object over a scalar replaces it, and a scalar over an object
  agent({ op: "update", path: "a/1", data: { n: { z: 1 }, o: 5 } });
  assert.deepEqual(agent({ op: "get", path: "a/1" }).body.doc.data, { keep: true, n: { z: 1 }, o: 5, x: { y: 1 } });
  // a key named __proto__ is a field like any other
  const p = JSON.parse(`{"__proto__":{"a":1}}`);
  assert.equal(err(agent({ op: "update", path: "a/1", data: p })), "");
  const got = agent({ op: "get", path: "a/1" }).body.doc.data;
  assert.ok(Object.prototype.hasOwnProperty.call(got, "__proto__"));
  assert.equal(Object.getPrototypeOf(got), Object.prototype);
});

// TestDBBatchAllOrNothing
test("batch: written whole or not at all, with or without a transaction", () => {
  for (const noTx of [false, true]) {
    const { store } = newStore({ noTx });
    let r = store.batch(AGENT, null, [{ op: "set", path: "a/1", data: { n: 1 } }, { op: "update", path: "a/missing", data: { n: 2 } }], T0);
    assert.equal(err(r), "invalid_argument");
    assert.equal(store.dump().length, 0, "a refused batch wrote its first set");
    r = store.batch(AGENT, null, [{ op: "set", path: "a/1", data: {} }, { op: "set", path: "a/2", data: {} }, { op: "delete", path: "a/1" }], T0);
    assert.deepEqual(r, { status: 200, body: {}, changed: ["a/1", "a/2", "a/1"] });
    assert.deepEqual(store.stats(), { docs: 1, bytes: 2 });
    r = store.batch(AGENT, null, Array.from({ length: 501 }, (_, i) => ({ op: "set", path: "a/" + i, data: {} })), T0);
    assert.equal(r.body.error.message, "a batch has at most 500 writes");
    r = store.batch(AGENT, null, [{ op: "merge", path: "a/1", data: {} }], T0);
    assert.equal(r.body.error.message, `"merge" isn't a write; they are set, update and delete`);
    // a path written twice in one batch: the second sees the first
    r = store.batch(AGENT, null, [{ op: "set", path: "b/1", data: { a: 1 } }, { op: "update", path: "b/1", data: { b: 2 } }, { op: "update", path: "b/1", data: { a: 3 } }], T0);
    assert.equal(err(r), "");
    assert.deepEqual(store.dump().find((d) => d.path === "b/1"), { path: "b/1", data: { a: 3, b: 2 }, version: 3, updated: "2026-11-25T09:30:00.12Z" });
    // a quota refusal part way writes none of it
    store.maxDocs = 3;
    r = store.batch(AGENT, null, [{ op: "set", path: "c/1", data: {} }, { op: "set", path: "c/2", data: {} }], T0);
    assert.equal(err(r), "quota_exceeded");
    assert.deepEqual(store.dump().map((d) => d.path), ["a/2", "b/1"]);
    assert.deepEqual(store.stats(), { docs: 2, bytes: 2 + `{"a":3,"b":2}`.length });
  }
});

test("batch: a store failure mid-write rolls the transaction back, and answers unavailable", () => {
  const { box, store, agent } = newStore();
  agent({ op: "set", path: "a/1", data: { v: 1 } });
  let inserts = 0;
  box.fail = (q) => /^\s*INSERT INTO docs/.test(q) && ++inserts === 2;
  const r = store.batch(AGENT, null, [{ op: "set", path: "a/1", data: { v: 2 } }, { op: "set", path: "a/2", data: {} }], T0);
  box.fail = null;
  assert.equal(r.status, 400);
  assert.equal(r.body.error.code, "unavailable");
  assert.match(r.body.error.message, /injected/);
  assert.deepEqual(store.dump().map((d) => [d.path, d.data]), [["a/1", { v: 1 }]]);
  assert.deepEqual(store.stats(), { docs: 1, bytes: `{"v":1}`.length });
});

// TestDBQuery, and the ordering of every kind of value
test("query: filters, ordering, limits, and the queries refused", () => {
  const { agent } = newStore();
  const rows = {
    a: { n: 3, tag: "x", tags: ["red"] },
    b: { n: 1, tag: "y", tags: ["red", "blue"] },
    c: { n: 2, tag: "x" },
    d: { tag: "z" },
    e: { n: "2", tag: "x" },
  };
  for (const [id, data] of Object.entries(rows)) assert.equal(err(agent({ op: "set", path: "items/" + id, data })), "");
  agent({ op: "set", path: "other/z", data: { n: 1 } });
  agent({ op: "set", path: "items/a/sub/q", data: { n: 1 } });
  const q = (o) => ids(agent({ op: "query", collection: "items", ...o }));
  for (const [o, want] of [
    [{}, "a,b,c,d,e"],
    [{ orderBy: "n" }, "b,c,a,e,d"],
    [{ orderBy: "n", desc: true }, "e,a,c,b,d"],
    [{ where: [{ field: "tag", op: "==", value: "x" }] }, "a,c,e"],
    [{ where: [{ field: "n", op: ">", value: 1 }] }, "a,c"],
    [{ where: [{ field: "n", op: "<=", value: 2 }, { field: "tag", op: "!=", value: "y" }] }, "c"],
    [{ where: [{ field: "tag", op: "in", value: ["y", "z"] }] }, "b,d"],
    [{ where: [{ field: "tag", op: "not-in", value: ["x"] }] }, "b,d"],
    [{ where: [{ field: "tags", op: "array-contains", value: "red" }] }, "a,b"],
    [{ orderBy: "n", limit: 2 }, "b,c"],
    // a missing field fails != and not-in too
    [{ where: [{ field: "n", op: "!=", value: 9 }] }, "a,b,c,e"],
    [{ where: [{ field: "n", op: "not-in", value: [9] }] }, "a,b,c,e"],
    [{ where: [{ field: "n", op: ">=", value: "2" }] }, "e"],
    [{ where: [{ field: "tag", op: "array-contains", value: "x" }] }, ""],
  ]) assert.equal(q(o), want, JSON.stringify(o));

  for (const [o, msg] of [
    [{ where: Array.from({ length: 11 }, () => ({ field: "n", op: "==", value: 1 })) }, "a query has at most 10 filters"],
    [{ where: [{ field: "n", op: "~", value: 1 }] }, `"~" isn't an operator; they are == != < <= > >= in not-in array-contains`],
    [{ where: [{ field: "n", op: "in", value: 1 }] }, "in takes an array of at most 30 values"],
    [{ where: [{ field: "n", op: "not-in", value: Array(31).fill(1) }] }, "not-in takes an array of at most 30 values"],
    [{ where: [{ field: "", op: "==", value: 1 }] }, "a filter names a field"],
    [{ where: [null] }, "a filter names a field"],
    [{ limit: 1001 }, "a limit is 1 to 1000"],
    [{ limit: -1 }, "a limit is 1 to 1000"],
    [{ collection: "items/a" }, null],
  ]) {
    const r = agent({ op: "query", collection: "items", ...o });
    assert.equal(r.status, 400, JSON.stringify(o));
    assert.equal(r.body.error.code, "invalid_argument");
    if (msg) assert.equal(r.body.error.message, msg);
  }
  assert.deepEqual(agent({ op: "query", collection: "nothing" }).body, { docs: [] });
  assert.equal(ids(agent({ op: "query", collection: "items", limit: 1000 })), "a,b,c,d,e");
});

test("query: values order by kind, then value: strings by code point, objects as Go writes them", () => {
  const { agent } = newStore();
  const vals = {
    k0: null, k1: false, k2: true, k3: -1.5, k4: 0, k5: 1e21, k6: "", k7: "B", k8: "a", k9: "￿", ka: "😀",
    kb: [], kc: [0], kd: [0, 1], ke: [1], kf: {}, kg: { a: 2 }, kh: { b: 1 }, ki: { a: 10 },
  };
  for (const [id, v] of Object.entries(vals)) agent({ op: "set", path: "v/" + id, data: { v } });
  agent({ op: "set", path: "v/zz", data: {} });
  // U+FFFF is below U+1F600 by code point, above it in UTF-16; {} is
  // after {"a":…}, as Go's bytes compare
  assert.equal(ids(agent({ op: "query", collection: "v", orderBy: "v" })), "k0,k1,k2,k3,k4,k5,k6,k7,k8,k9,ka,kb,kc,kd,ke,ki,kg,kh,kf,zz");
  assert.equal(ids(agent({ op: "query", collection: "v", orderBy: "v", desc: true })), "kf,kh,kg,ki,ke,kd,kc,kb,ka,k9,k8,k7,k6,k5,k4,k3,k2,k1,k0,zz");
  // a range compares within its kind alone
  assert.equal(ids(agent({ op: "query", collection: "v", where: [{ field: "v", op: ">", value: "a" }] })), "k9,ka");
  assert.equal(ids(agent({ op: "query", collection: "v", where: [{ field: "v", op: "<", value: [0, 1] }] })), "kb,kc");
  assert.equal(ids(agent({ op: "query", collection: "v", where: [{ field: "v", op: "==", value: { a: 2 } }] })), "kg");
  assert.equal(ids(agent({ op: "query", collection: "v", where: [{ field: "v", op: "==", value: null }] })), "k0");
  assert.equal(ids(agent({ op: "query", collection: "v", where: [{ field: "v", op: "==" }] })), "k0");
  assert.equal(ids(agent({ op: "query", collection: "v", where: [{ field: "v", op: "in", value: [false, 0, ""] }] })), "k1,k4,k6");
});

// TestDBRules: all read the votes and only the owner writes them, each
// writes their own vote, and data/users/<id>/ is the viewer's alone, the
// owner's included.
test("rules: votes, {self}, admin, and data/users/<id> for its viewer alone", () => {
  const decl = { rules: [{ path: "votes", read: "view", write: "owner" }, { path: "votes/{self}", write: "interact" }, { path: "admin", read: "admin", write: "admin" }] };
  const { as, agent } = newStore({ decl });
  const alice = as({ id: "alice", level: LEVEL.interact });
  const bob = as({ id: "bob", level: LEVEL.interact });
  const owner = as({ id: "own", level: LEVEL.owner });
  const viewer = as({ id: "vic", level: LEVEL.view });
  assert.equal(err(alice({ op: "set", path: "votes/alice", data: { v: "yes" } })), "", "alice's own vote");
  const r = alice({ op: "set", path: "votes/bob", data: { v: "no" } });
  assert.deepEqual(r, { status: 400, body: { error: { code: "invalid_argument", message: "this viewer can't write votes/bob" } }, changed: [] });
  assert.equal(err(viewer({ op: "set", path: "votes/vic", data: { v: "no" } })), "invalid_argument", "a viewer wrote");
  assert.equal(err(owner({ op: "set", path: "votes/bob", data: { v: "tidied" } })), "", "the owner tidying");
  assert.ok(bob({ op: "get", path: "votes/alice" }).body.doc, "bob reads alice's vote");
  assert.equal(viewer({ op: "query", collection: "votes" }).body.docs.length, 2);
  agent({ op: "set", path: "admin/x", data: {} });
  assert.equal(alice({ op: "get", path: "admin/x" }).body.doc, null, "a contributor read an admin document");
  assert.ok(owner({ op: "get", path: "admin/x" }).body.doc, "the owner reads an admin document");
  // each viewer's own folder
  assert.equal(err(alice({ op: "set", path: "data/users/alice/prefs", data: { dark: true } })), "");
  assert.equal(owner({ op: "get", path: "data/users/alice/prefs" }).body.doc, null, "the owner read alice's own data");
  assert.equal(bob({ op: "get", path: "data/users/alice/prefs" }).body.doc, null);
  assert.deepEqual(owner({ op: "query", collection: "data/users/alice" }).body.docs, []);
  assert.equal(err(owner({ op: "set", path: "data/users/alice/prefs", data: {} })), "invalid_argument");
  assert.equal(err(owner({ op: "set", path: "data/users", data: {} })), "invalid_argument", "data/users itself is self, the owner's own id included");
  assert.ok(alice({ op: "get", path: "data/users/alice/prefs" }).body.doc);
  assert.equal(alice({ op: "query", collection: "data/users/alice" }).body.docs.length, 1);
  assert.ok(agent({ op: "get", path: "data/users/alice/prefs" }).body.doc, "the agent reaches everything");
  // a viewer with no id has no own folder
  const anon = as({ id: "", level: LEVEL.owner });
  assert.equal(err(anon({ op: "set", path: "data/users/x/y", data: {} })), "invalid_argument");
  // a viewer at view writes nothing by default; interact writes outside data/users
  const plain = newStore();
  assert.equal(err(plain.as({ id: "v", level: LEVEL.view })({ op: "set", path: "a/1", data: {} })), "invalid_argument");
  assert.equal(err(plain.as({ id: "v", level: LEVEL.interact })({ op: "set", path: "a/1", data: {} })), "");
  assert.ok(plain.as({ id: "w", level: LEVEL.view })({ op: "get", path: "a/1" }).body.doc);
  // acquire is a write
  assert.equal(err(viewer({ op: "acquire", path: "votes/vic", holder: "h" })), "invalid_argument");
});

test("rules: self at a {self} prefix, read from above, a deeper rule overriding", () => {
  // signoffs: each signs their own, no one reads another's
  const decl = { rules: [{ path: "signoffs", read: "self", write: "self" }, { path: "signoffs/{self}", read: "view", write: "interact" }, { path: "pub", read: "admin", write: "admin" }, { path: "pub/open", read: "view" }] };
  const { as, rules } = newStore({ decl });
  const ann = as({ id: "ann", level: LEVEL.interact });
  const own = as({ id: "own", level: LEVEL.owner });
  assert.equal(err(ann({ op: "set", path: "signoffs/ann", data: { ok: true } })), "");
  assert.equal(err(ann({ op: "set", path: "signoffs/ann/notes/1", data: {} })), "", "below one's own subtree");
  assert.equal(err(own({ op: "set", path: "signoffs/ann", data: {} })), "invalid_argument");
  assert.equal(own({ op: "get", path: "signoffs/ann" }).body.doc, null);
  assert.deepEqual(own({ op: "query", collection: "signoffs" }).body.docs, []);
  assert.deepEqual(ann({ op: "query", collection: "signoffs" }).body.docs.map((d) => d.id), ["ann"]);
  // write is never below read: pub/open is read at view, written at admin
  assert.equal(err(ann({ op: "set", path: "pub/open", data: {} })), "invalid_argument");
  assert.equal(err(own({ op: "set", path: "pub/open", data: {} })), "");
  assert.ok(ann({ op: "get", path: "pub/open" }).body.doc);
  assert.equal(ann({ op: "get", path: "pub/closed" }).body.doc, null);
  // {self} levels left unset come from above its prefix, the prefix's where
  // they give more
  const r2 = parseRules({ rules: [{ path: "box", read: "owner", write: "owner" }, { path: "box/{self}" }] });
  assert.equal(canRead(r2, { id: "u", level: LEVEL.view }, "box/u"), true);
  assert.equal(canRead(r2, { id: "u", level: LEVEL.admin }, "box/v"), false);
  assert.ok(rules.rules.length >= 6);
});

test("rules: parseRules takes what ParseDBConfig takes, and refuses with its words", () => {
  for (const good of [
    {},
    null,
    undefined,
    { rules: null },
    { rules: [] },
    { rules: [{ path: "signoffs", read: "view", write: "self" }, { path: "signoffs/{self}", read: "view", write: "interact" }] },
    { rules: [{ path: "/votes/", read: "view" }] },
    { rules: [{ path: "", read: "view", write: "admin" }] },
    { rules: [null] },
    { Rules: [{ PATH: "x", Read: "admin" }] },
    { rules: [{ path: "data/users", read: "view", write: "owner" }] },
    { rules: Array.from({ length: 64 }, (_, i) => ({ path: "c" + i })) },
  ]) assert.doesNotThrow(() => parseRules(good), JSON.stringify(good));

  const BAD = `it is {} or {"rules": [{"path": "votes", "read": "view", "write": "owner"}]}, where read and write are levels: view, interact, admin, owner or self`;
  for (const [bad, msg] of [
    [[], BAD],
    [true, BAD],
    ["x", BAD],
    [{ rules: { path: "votes" } }, BAD],
    [{ rules: [{ path: 1 }] }, BAD],
    [{ rules: [{ path: "a", read: 0 }] }, BAD],
    [{ rules: ["a"] }, BAD],
    [{ rules: [{ path: "a", read: "root" }] }, `rule 0: "root" isn't a level; they are view, interact, admin, owner and self`],
    [{ rules: [{ path: "a", read: "admin", write: "view" }] }, "rule 0: writing implies reading, so write is never below read"],
    [{ rules: [{ path: "a", read: "view" }, { path: "a", write: "owner" }] }, `rule 1: a second rule for "a"`],
    [{ rules: [{ path: "a", read: "view" }, { path: "/a/", write: "owner" }] }, `rule 1: a second rule for "/a/"`],
    [{ rules: [{ path: "a", read: "view", write: "self" }] }, "rule 0: self goes only on the rule at the prefix of a {self} rule"],
    [{ rules: [{ path: "a", read: "self", write: "self" }, { path: "a/{self}", read: "self", write: "self" }] }, "rule 1: self goes only on the rule at the prefix of a {self} rule"],
    [{ rules: [{ path: "a", read: "self", write: "owner" }, { path: "a/{self}" }] }, "rule 0: read self is valid only with write self"],
    [{ rules: [{ path: "a", read: "view" }, { path: "a/{self}", write: "interact" }] }, "rule 0: the rule at a {self} rule's prefix sets both read and write"],
    [{ rules: [{ path: "{self}/a" }] }, "rule 0: {self} is the last segment of a path below a prefix"],
    [{ rules: [{ path: "{self}" }] }, "rule 0: {self} is the last segment of a path below a prefix"],
    [{ rules: [{ path: "a/b c" }] }, `rule 0: segment "b c": segments use letters, digits and _ - . ~ : @ + only`],
    [{ rules: [{ path: "a//b" }] }, "rule 0: a path has no empty segment"],
    [{ rules: [{ path: "a/".repeat(16) + "a" }] }, "rule 0: a path has at most 16 segments"],
    [{ rules: Array.from({ length: 65 }, (_, i) => ({ path: "c" + i })) }, "65 rules; the most is 64"],
  ]) assert.throws(() => parseRules(bad), { message: msg }, JSON.stringify(bad));
  // TestDBConfigErrorNamesLevels
  assert.throws(() => parseRules({ rules: { path: "votes" } }), /view, interact, admin, owner or self/);
});

test("canRead: a change is told only to a viewer who may read its path", () => {
  const rules = parseRules({ rules: [{ path: "votes", read: "view", write: "owner" }, { path: "votes/{self}", write: "interact" }, { path: "admin", read: "admin", write: "admin" }, { path: "secret", read: "self", write: "self" }, { path: "secret/{self}", read: "view", write: "interact" }] });
  const alice = { id: "alice", level: LEVEL.interact };
  const bob = { id: "bob", level: LEVEL.interact };
  const owner = { id: "own", level: LEVEL.owner };
  assert.equal(canRead(rules, alice, "data/users/alice/x"), true);
  assert.equal(canRead(rules, bob, "data/users/alice/x"), false, "another viewer's own data");
  assert.equal(canRead(rules, owner, "data/users/alice/x"), false, "the owner, another's own data");
  assert.equal(canRead(rules, bob, "data/users/alice/x/deeper/y"), false);
  assert.equal(canRead(rules, { id: "", level: LEVEL.owner }, "data/users/alice/x"), false);
  assert.equal(canRead(rules, AGENT, "data/users/alice/x"), true);
  assert.equal(canRead(rules, bob, "votes/alice"), true);
  assert.equal(canRead(rules, alice, "admin/x"), false);
  assert.equal(canRead(rules, owner, "admin/x"), true);
  // collections: whether some document in them reads
  assert.equal(canRead(rules, alice, "data/users/alice"), true);
  assert.equal(canRead(rules, bob, "data/users/alice"), false);
  assert.equal(canRead(rules, alice, "secret"), true, "her own secret/alice reads");
  assert.equal(canRead(rules, { id: "", level: LEVEL.owner }, "secret"), false);
  assert.equal(canRead(rules, alice, "admin"), false);
  assert.equal(canRead(rules, alice, "votes"), true);
  // not a path: read by nobody
  for (const p of ["", "a//b", "a/b c", null, 7]) assert.equal(canRead(rules, owner, p), false, String(p));
  // no rules given: the defaults
  assert.equal(canRead(null, bob, "data/users/alice/x"), false);
  assert.equal(canRead(null, { id: "v", level: LEVEL.view }, "a/1"), true);
});

// TestDBQuotas
test("quotas: documents and bytes, kept in totals that follow every write", () => {
  const { store, agent } = newStore();
  store.maxDocs = 2;
  agent({ op: "set", path: "a/1", data: {} });
  agent({ op: "set", path: "a/2", data: {} });
  let r = agent({ op: "set", path: "a/3", data: {} });
  assert.deepEqual(r, { status: 400, body: { error: { code: "quota_exceeded", message: "the artifact's database holds 2 documents, the most it may" } }, changed: [] });
  assert.equal(err(agent({ op: "set", path: "a/2", data: { rewritten: true } })), "", "rewriting one at the count");
  assert.equal(err(agent({ op: "acquire", path: "a/3", holder: "h" })), "quota_exceeded", "a lease making a third");
  agent({ op: "delete", path: "a/1" });
  assert.equal(err(agent({ op: "set", path: "a/3", data: {} })), "", "after a delete");
  store.maxDocs = 100;
  store.maxDataBytes = 100;
  r = agent({ op: "set", path: "a/4", data: { s: "x".repeat(100) } });
  assert.equal(r.body.error.code, "quota_exceeded");
  assert.equal(r.body.error.message, "the artifact's database would hold over 0 MiB, the most it may");
  assert.deepEqual(store.stats(), { docs: 2, bytes: `{"rewritten":true}`.length + `{}`.length });
  // over the bytes already, a write that shrinks a document is taken
  store.maxDataBytes = 5;
  assert.equal(err(agent({ op: "set", path: "a/2", data: { r: 1 } })), "");
  assert.equal(err(agent({ op: "set", path: "a/2", data: { r: 12 } })), "quota_exceeded");
  // bytes are UTF-8, as Go escapes them
  store.maxDataBytes = 1 << 20;
  agent({ op: "delete", path: "a/2" });
  agent({ op: "delete", path: "a/3" });
  agent({ op: "set", path: "u/1", data: { s: "é<\u2028😀" } });
  assert.deepEqual(store.stats(), { docs: 1, bytes: `{"s":"`.length + 2 + 6 + 6 + 4 + `"}`.length });
});

// TestDBAcquire
test("acquire: a lease, refused while another holds it, clamped, renewed and taken after it ends", () => {
  const { agent, store } = newStore();
  let r = agent({ op: "acquire", path: "locks/turn", holder: "alice", ttlMs: 5000, data: { by: "alice" } }, T0);
  const exp = "2026-11-25T09:30:05.12Z";
  assert.deepEqual(r, { status: 200, body: { lease: { acquired: true, version: 1, expiresAt: exp, holder: "alice" } }, changed: ["locks/turn"] });
  r = agent({ op: "acquire", path: "locks/turn", holder: "bob", ttlMs: 1000, data: { by: "bob" } }, T0 + 10);
  assert.deepEqual(r, { status: 200, body: { lease: { acquired: false, expiresAt: exp } }, changed: [] });
  assert.equal(agent({ op: "get", path: "locks/turn" }).body.doc.data.by, "alice", "bob's refused lease wrote");
  // alice again, an hour clamped to ten minutes, the document kept
  r = agent({ op: "acquire", path: "locks/turn", holder: "alice", ttlMs: 3600000 }, T0 + 20);
  assert.deepEqual(r.body.lease, { acquired: true, version: 2, expiresAt: goTimeOf(T0 + 20 + 600000), holder: "alice" });
  assert.deepEqual(agent({ op: "get", path: "locks/turn" }).body.doc.data, { by: "alice" });
  // a set doesn't look at the lease, nor end it
  assert.equal(err(agent({ op: "set", path: "locks/turn", data: { by: "carol" } }, T0 + 30)), "");
  assert.equal(agent({ op: "acquire", path: "locks/turn", holder: "bob" }, T0 + 40).body.lease.acquired, false);
  // once it ends, bob takes it
  r = agent({ op: "acquire", path: "locks/turn", holder: "bob", data: { by: "bob" } }, T0 + 20 + 600000);
  assert.deepEqual(r.body.lease, { acquired: true, version: 4, expiresAt: goTimeOf(T0 + 20 + 600000 + 30000), holder: "bob" });
  assert.deepEqual(agent({ op: "get", path: "locks/turn" }).body.doc.data, { by: "bob" });
  // no ttl is 30s, a tiny one a second, a negative one 30s
  assert.equal(agent({ op: "acquire", path: "locks/a", holder: "h", ttlMs: 1 }, T0).body.lease.expiresAt, goTimeOf(T0 + 1000));
  assert.equal(agent({ op: "acquire", path: "locks/b", holder: "h", ttlMs: -5 }, T0).body.lease.expiresAt, goTimeOf(T0 + 30000));
  // ttlMs * 1ms overflows int64 as Go's Duration does: 2^63/1e6+1 ms wraps negative
  assert.equal(agent({ op: "acquire", path: "locks/c", holder: "h", ttlMs: 9223372036855 }, T0).body.lease.expiresAt, goTimeOf(T0 + 30000));
  for (const [c, msg] of [
    [{ holder: "" }, "acquire names its holder, at most 200 bytes"],
    [{}, "acquire names its holder, at most 200 bytes"],
    [{ holder: "é".repeat(101) }, "acquire names its holder, at most 200 bytes"],
    [{ holder: "h", data: { d: deepObj(40) } }, "a document is at most 32 levels deep"],
  ]) {
    r = agent({ op: "acquire", path: "locks/x", ...c });
    assert.equal(r.status, 400);
    assert.equal(r.body.error.message, msg);
  }
  assert.equal(err(agent({ op: "acquire", path: "locks", holder: "h" })), "invalid_argument");
  assert.equal(store.stats().docs, 4);
});

function deepObj(n) {
  const o = {};
  for (let i = 0, m = o; i < n; i++) m = m.n = {};
  return o;
}

function goTimeOf(ms) {
  return new Date(ms).toISOString().replace(/\.?0+Z$/, "Z");
}

test("the call: unknown ops, values of the wrong type, and a body too big or not JSON", () => {
  const { agent } = newStore();
  assert.deepEqual(agent({ op: "drop" }), { status: 400, body: { error: { code: "capability_removed", message: `"drop" isn't a store call` } }, changed: [] });
  assert.equal(agent({}).body.error.code, "capability_removed");
  assert.equal(agent(null).body.error.code, "capability_removed");
  for (const c of [
    { op: "set", path: "a/1", data: [1] },
    { op: "set", path: "a/1", data: "x" },
    { op: "query", collection: "a", limit: 1.5 },
    { op: "query", collection: "a", limit: "2" },
    { op: "query", collection: "a", where: "n" },
    { op: "query", collection: "a", where: [{ field: 1, op: "==" }] },
    { op: "query", collection: "a", desc: 1 },
    { op: 7 },
    { op: "get", path: ["a", "b"] },
    { op: "acquire", path: "a/1", holder: "h", ttlMs: "5" },
    [],
  ]) {
    const r = agent(c);
    assert.equal(r.status, 400, JSON.stringify(c));
    assert.equal(r.body.error.code, "transform_error", JSON.stringify(c));
    assert.match(r.body.error.message, /^the call isn't JSON: json: cannot unmarshal /);
  }
  assert.equal(agent({ op: "set", path: "a/1", data: [1] }).body.error.message, "the call isn't JSON: json: cannot unmarshal array into Go struct field artifactCall.data of type map[string]interface {}");
  assert.equal(agent({ op: "query", collection: "a", limit: 1.5 }).body.error.message, "the call isn't JSON: json: cannot unmarshal number 1.5 into Go struct field artifactCall.limit of type int");
  // Go matches a call's fields case-insensitively
  assert.equal(err(agent({ OP: "set", Path: "a/1", DATA: { x: 1 } })), "");
  assert.deepEqual(agent({ op: "get", path: "a/1" }).body.doc.data, { x: 1 });

  assert.deepEqual(readCall(`{"op":"get"}`), { c: { op: "get" } });
  assert.deepEqual(readCall(new TextEncoder().encode(`{"op":"get"}`)), { c: { op: "get" } });
  let r = readCall("{nope");
  assert.equal(r.status, 400);
  assert.equal(r.body.error.code, "transform_error");
  assert.match(r.body.error.message, /^the call isn't JSON: /);
  r = readCall(`{"s":"${"x".repeat(1 << 20)}"}`);
  assert.deepEqual(r, { status: 413, body: { error: { code: "invalid_argument", message: "a call is at most 1 MiB" } } });
  // at most 1 MiB in bytes, not characters
  assert.equal(readCall(`"${"é".repeat(1 << 19)}"`).status, 413);
});

test("values as Go decodes them: a lone surrogate is U+FFFD, the time is RFC3339Nano", () => {
  const { agent } = newStore();
  agent(JSON.parse(`{"op":"set","path":"a/1","data":{"s":"x\\ud800y","\\udc00":1}}`), Date.UTC(2026, 0, 2, 3, 4, 5, 0));
  const d = agent({ op: "get", path: "a/1" }).body.doc;
  assert.deepEqual(d.data, { s: "x�y", "�": 1 });
  assert.equal(d.updated, "2026-01-02T03:04:05Z");
  agent({ op: "set", path: "a/2", data: {} }, Date.UTC(2026, 0, 2, 3, 4, 5, 100));
  assert.equal(agent({ op: "get", path: "a/2" }).body.doc.updated, "2026-01-02T03:04:05.1Z");
  agent({ op: "set", path: "a/3", data: {} }, Date.UTC(2026, 0, 2, 3, 4, 5, 7));
  assert.equal(agent({ op: "get", path: "a/3" }).body.doc.updated, "2026-01-02T03:04:05.007Z");
  // a where value is decoded the same way
  assert.equal(ids(agent(JSON.parse(`{"op":"query","collection":"a","where":[{"field":"s","op":"==","value":"x\\udfffy"}]}`))), "1");
});

test("dump and seed: every document out, and back in as the agent, all or none", () => {
  const a = newStore();
  a.agent({ op: "set", path: "tasks/t1", data: { n: 1 } }, T0);
  a.agent({ op: "update", path: "tasks/t1", data: { m: 2 } }, T0 + 5);
  a.agent({ op: "set", path: "data/users/alice/prefs", data: { dark: true } }, T0);
  const out = a.store.dump();
  assert.deepEqual(out, [
    { path: "data/users/alice/prefs", data: { dark: true }, version: 1, updated: "2026-11-25T09:30:00.12Z" },
    { path: "tasks/t1", data: { m: 2, n: 1 }, version: 2, updated: "2026-11-25T09:30:00.125Z" },
  ]);
  const b = newStore();
  assert.deepEqual(b.store.seed(out, T0 + 99), ["data/users/alice/prefs", "tasks/t1"]);
  assert.deepEqual(b.store.dump(), out, "a dump seeds back as it was");
  assert.deepEqual(b.store.stats(), a.store.stats());
  // without versions, they are new documents, written now
  const c = newStore();
  c.store.seed([{ path: "x/1", data: { a: 1 } }], T0);
  assert.deepEqual(c.store.dump(), [{ path: "x/1", data: { a: 1 }, version: 1, updated: "2026-11-25T09:30:00.12Z" }]);
  // the first bad one throws, and nothing is written
  for (const bad of [[{ path: "y/1", data: {} }, { path: "y", data: {} }], [{ path: "y/1", data: {} }, { path: "y/2" }], [{ path: "y/1", data: {} }, { path: "y/2", data: { s: "x".repeat(257 << 10) } }]]) {
    const s = newStore();
    assert.throws(() => s.store.seed(bad, T0), { code: "invalid_argument" });
    assert.deepEqual(s.store.dump(), []);
    assert.deepEqual(s.store.stats(), { docs: 0, bytes: 0 });
  }
  const q = newStore();
  q.store.maxDocs = 1;
  assert.throws(() => q.store.seed([{ path: "y/1", data: {} }, { path: "y/2", data: {} }], T0), { code: "quota_exceeded" });
  assert.deepEqual(q.store.dump(), []);
  // more than a batch's 500 writes seed at once
  const many = newStore();
  many.store.seed(Array.from({ length: 600 }, (_, i) => ({ path: "m/" + i, data: {} })), T0);
  assert.equal(many.store.stats().docs, 600);
});

test("the store opens again over its own tables", () => {
  const box = sqlStorage();
  const s1 = new Store(box.sql, box.transactionSync);
  s1.call(AGENT, null, { op: "set", path: "a/1", data: { v: 1 } }, T0);
  const s2 = new Store(box.sql, box.transactionSync);
  assert.deepEqual(s2.call(AGENT, null, { op: "get", path: "a/1" }, T0).body.doc.data, { v: 1 });
  assert.deepEqual(s2.stats(), { docs: 1, bytes: 7 });
});
