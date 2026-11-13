// node --test site/: a release's files from R2, GitHub behind them
// (files.js). R2 is stood in for by r2stub.mjs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { serve, onR2, mirror, held } from "./files.js";
import { bucket, sha, hold } from "./r2stub.mjs";

const REPO = "yetone/magpie-releases";
const GH = (tag, name) => `https://github.com/${REPO}/releases/download/${tag}/${name}`;
const ask = (env, path, headers = {}, method = "GET") => {
  const [, , tag, name] = path.split("/");
  return serve(new Request("https://usemagpie.ai" + path, { method, headers }), env, REPO, tag, name);
};

test("files: R2's file whole, a Range of it, its HEAD, and a 304 for its own ETag", async () => {
  const env = { RELEASES: bucket() };
  await hold(env.RELEASES, "v0.1.3", "magpie-linux-amd64", "0123456789");
  let res = await ask(env, "/files/v0.1.3/magpie-linux-amd64");
  assert.equal(res.status, 200);
  assert.equal(await res.text(), "0123456789");
  assert.equal(res.headers.get("Content-Length"), "10");
  assert.equal(res.headers.get("Accept-Ranges"), "bytes");
  assert.equal(res.headers.get("Content-Type"), "application/octet-stream");
  assert.match(res.headers.get("Cache-Control"), /immutable/);
  const etag = res.headers.get("ETag");

  // the app asks on from what an earlier try kept
  res = await ask(env, "/files/v0.1.3/magpie-linux-amd64", { Range: "bytes=4-" });
  assert.equal(res.status, 206);
  assert.equal(await res.text(), "456789");
  assert.equal(res.headers.get("Content-Range"), "bytes 4-9/10");
  assert.equal(res.headers.get("Content-Length"), "6");
  res = await ask(env, "/files/v0.1.3/magpie-linux-amd64", { Range: "bytes=2-3" });
  assert.equal(res.headers.get("Content-Range"), "bytes 2-3/10");
  assert.equal(await res.text(), "23");
  res = await ask(env, "/files/v0.1.3/magpie-linux-amd64", { Range: "bytes=-3" });
  assert.equal(res.headers.get("Content-Range"), "bytes 7-9/10");
  assert.equal(await res.text(), "789");

  // R2 names every key of the range it served, the unasked ones undefined:
  // a Content-Range is never NaN (JohnnyN70389377 on X)
  for (const [range, want] of [["bytes=1-3", "bytes 1-3/10"], ["bytes=-100", "bytes 0-9/10"], ["bytes=0-", "bytes 0-9/10"], ["bytes=8-99", "bytes 8-9/10"]]) {
    res = await ask(env, "/files/v0.1.3/magpie-linux-amd64", { Range: range });
    assert.equal(res.status, 206, range);
    assert.equal(res.headers.get("Content-Range"), want, range);
    assert.equal(String((await res.arrayBuffer()).byteLength), res.headers.get("Content-Length"), range);
  }

  res = await ask(env, "/files/v0.1.3/magpie-linux-amd64", {}, "HEAD");
  assert.equal(res.status, 200);
  assert.equal(res.headers.get("Content-Length"), "10");
  res = await ask(env, "/files/v0.1.3/magpie-linux-amd64", { "If-None-Match": etag });
  assert.equal(res.status, 304);
  assert.equal((await ask(env, "/files/v0.1.3/magpie-linux-amd64", {}, "POST")).status, 405);
});

test("files: what R2 hasn't got, can't give, or isn't bound goes to GitHub's own address", async () => {
  const env = { RELEASES: bucket() };
  await hold(env.RELEASES, "v0.1.3", "magpie-linux-amd64", "0123456789");
  const toGitHub = async (env, path, headers) => {
    const res = await ask(env, path, headers);
    assert.equal(res.status, 302, path);
    assert.equal(res.headers.get("Location"), GH(...path.split("/").slice(2)));
    assert.equal(res.headers.get("Cache-Control"), "no-store");
  };
  await toGitHub(env, "/files/v0.1.2/magpie-linux-amd64");
  await toGitHub(env, "/files/v0.1.3/magpie-linux-arm64");
  // a Range past the end: GitHub answers it its own way
  await toGitHub(env, "/files/v0.1.3/magpie-linux-amd64", { Range: "bytes=99-" });
  await toGitHub({}, "/files/v0.1.3/magpie-linux-amd64");
  env.RELEASES.fail = true;
  await toGitHub(env, "/files/v0.1.3/magpie-linux-amd64");
});

test("feed: a file R2 holds with the release's hash is at /files, every file says its github.com address", () => {
  const rel = { version: "0.1.3", assets: {
    a: { url: GH("v0.1.3", "a"), api: "https://api/1", size: 1, sha256: sha("A") },
    b: { url: GH("v0.1.3", "b"), size: 1, sha256: sha("B") },
    c: { url: GH("v0.1.3", "c"), size: 1, sha256: sha("C") },
  } };
  // b holds other bytes than the release says, c isn't there
  const got = onR2(rel, { a: sha("A"), b: sha("other") }, REPO, "https://usemagpie.ai");
  assert.deepEqual(got.assets.a, { url: "https://usemagpie.ai/files/v0.1.3/a", api: "https://api/1", size: 1, sha256: sha("A"), github: GH("v0.1.3", "a") });
  assert.equal(got.assets.b.url, GH("v0.1.3", "b"));
  assert.equal(got.assets.b.github, GH("v0.1.3", "b"));
  assert.equal(got.assets.c.url, GH("v0.1.3", "c"));
  // R2 unknown: GitHub for all
  assert.equal(onR2(rel, null, REPO, "https://usemagpie.ai").assets.a.url, GH("v0.1.3", "a"));
  // rel itself untouched (it is the cached feed)
  assert.equal(rel.assets.a.url, GH("v0.1.3", "a"));
});

test("mirror: a release's files are copied once, and bytes that aren't the release's are not kept", async () => {
  const env = { RELEASES: bucket() };
  const bodies = { a: "AAAA", b: "BBBB", c: "tampered" };
  const opened = [];
  const open = async (name) => {
    opened.push(name);
    return name === "gone" ? new Response("", { status: 504 }) : new Response(bodies[name], { headers: { "Content-Length": String(bodies[name].length) } });
  };
  const rel = { version: "0.1.3", assets: {
    a: { size: 4, sha256: sha("AAAA") },
    b: { size: 4, sha256: sha("BBBB") },
    c: { size: 8, sha256: sha("CCCC") },
    gone: { size: 1, sha256: sha("G") },
    nohash: { size: 1, sha256: "" },
  } };
  let done = await mirror(env, [rel, rel], open);
  assert.deepEqual(done.copied.sort(), ["v0.1.3/a", "v0.1.3/b"]);
  assert.deepEqual(done.failed.sort(), ["v0.1.3/c", "v0.1.3/gone"]);
  assert.deepEqual(Object.keys(await held(env, "v0.1.3")).sort(), ["a", "b"]);
  // the same release again: only what isn't held yet is asked for
  opened.length = 0;
  done = await mirror(env, [rel], open);
  assert.deepEqual(opened.sort(), ["c", "gone"]);
  assert.deepEqual(done.copied, []);
});

test("mirror: stops starting copies after its budget, so the next run doesn't copy beside it", async () => {
  const env = { RELEASES: bucket() };
  let t = 0;
  const open = async (name) => {
    t += 3 * 60e3;
    return new Response(name);
  };
  const rel = { version: "0.1.3", assets: { a: { sha256: sha("a") }, b: { sha256: sha("b") }, c: { sha256: sha("c") } } };
  const done = await mirror(env, [rel], open, () => t);
  assert.deepEqual(done.copied, ["v0.1.3/a", "v0.1.3/b"]);
});

test("mirror: the tags the feeds name and the four newest besides are kept, older ones deleted", async () => {
  const env = { RELEASES: bucket() };
  for (const v of ["0.1.2", "0.1.9", "0.1.10", "0.1.11", "0.1.12", "0.1.13", "0.1.14"]) {
    await hold(env.RELEASES, "v" + v, "x", v);
    await hold(env.RELEASES, "v" + v, "y", v + "y");
  }
  // the newest (0.1.15) and Stable (0.1.2)
  const rels = [
    { version: "0.1.15", assets: { x: { sha256: sha("new") } } },
    { version: "0.1.2", assets: { x: { sha256: sha("0.1.2") } } },
  ];
  await mirror(env, rels, async () => new Response("new"));
  const tags = [...new Set([...env.RELEASES.objs.keys()].map((k) => k.split("/")[0]))].sort();
  assert.deepEqual(tags, ["v0.1.11", "v0.1.12", "v0.1.13", "v0.1.14", "v0.1.15", "v0.1.2"]);
  assert.ok(env.RELEASES.objs.has("v0.1.11/y"));
});

test("mirror: without the bucket, or with R2 failing, nothing is copied and nothing throws", async () => {
  assert.deepEqual(await mirror({}, [{ version: "1", assets: { a: { sha256: "x" } } }], async () => new Response("a")), { copied: [], failed: [] });
  const env = { RELEASES: bucket() };
  env.RELEASES.fail = true;
  assert.deepEqual(await mirror(env, [{ version: "1", assets: { a: { sha256: "x" } } }], async () => new Response("a")), { copied: [], failed: [] });
});

test("mirror: a release's attestation bundles land in R2 as attestations.jsonl, once, and only whole (ArcticLampyrid on X)", async () => {
  const env = { RELEASES: bucket() };
  const rel = { version: "0.1.3891", assets: { a: { sha256: sha("A") }, b: { sha256: sha("B") } } };
  const open = async (name) => new Response(name === "a" ? "A" : "B");
  const asked = [];
  let missing = sha("B");
  const attest = async (h) => {
    asked.push(h);
    return h === missing ? null : [{ mediaType: "bundle", for: h }];
  };
  // one file has none yet: nothing is written, the next run asks again
  let done = await mirror(env, [rel], open, Date.now, attest);
  assert.deepEqual(done.failed, ["v0.1.3891/attestations.jsonl"]);
  assert.equal(env.RELEASES.objs.has("v0.1.3891/attestations.jsonl"), false);
  missing = "";
  done = await mirror(env, [rel], open, Date.now, attest);
  assert.deepEqual(done.copied, ["v0.1.3891/attestations.jsonl"]);
  const res = await ask(env, "/files/v0.1.3891/attestations.jsonl");
  assert.equal(res.status, 200);
  const lines = (await res.text()).trimEnd().split("\n").map((l) => JSON.parse(l));
  assert.deepEqual(lines.map((l) => l.for).sort(), [sha("A"), sha("B")].sort());
  // held: it is not asked for again
  asked.length = 0;
  await mirror(env, [rel], open, Date.now, attest);
  assert.deepEqual(asked, []);
});

test("mirror: a kept release the feeds no longer name still gets its bundles, for the files R2 holds of it (v0.1.3978)", async () => {
  const env = { RELEASES: bucket() };
  // v0.1.3978 was the newest for one run, which copied its files and spent
  // its budget; by the next run v0.1.3979 had taken its place in the feed
  await hold(env.RELEASES, "v0.1.3978", "a", "A");
  await hold(env.RELEASES, "v0.1.3978", "b", "B");
  await hold(env.RELEASES, "v0.1.3890", "a", "old");
  const rel = { version: "0.1.3979", assets: { c: { sha256: sha("C") } } };
  const asked = [];
  const attest = async (h) => (asked.push(h), [{ for: h }]);
  const done = await mirror(env, [rel], async () => new Response("C"), Date.now, attest);
  assert.deepEqual(done.copied.sort(), ["v0.1.3978/attestations.jsonl", "v0.1.3979/attestations.jsonl", "v0.1.3979/c"]);
  const res = await ask(env, "/files/v0.1.3978/attestations.jsonl");
  assert.equal(res.status, 200);
  const lines = (await res.text()).trimEnd().split("\n").map((l) => JSON.parse(l));
  assert.deepEqual(lines.map((l) => l.for).sort(), [sha("A"), sha("B")].sort());
  // a kept release from before the attestations is not asked
  assert.equal(asked.includes(sha("old")), false);
  // written once: the next run asks nothing more
  asked.length = 0;
  await mirror(env, [rel], async () => new Response("C"), Date.now, attest);
  assert.deepEqual(asked, []);
});

test("mirror: one statement signing every file is asked for once a release, not once a file", async () => {
  const env = { RELEASES: bucket() };
  const names = ["a", "b", "c"];
  const rel = { version: "0.1.3982", assets: Object.fromEntries(names.map((n) => [n, { sha256: sha(n) }])) };
  const statement = { subject: names.map((n) => ({ name: n, digest: { sha256: sha(n) } })) };
  const bundle = { dsseEnvelope: { payload: btoa(JSON.stringify(statement)) } };
  const asked = [];
  const attest = async (h) => (asked.push(h), [bundle]);
  const done = await mirror(env, [rel], async (n) => new Response(n), Date.now, attest);
  assert.ok(done.copied.includes("v0.1.3982/attestations.jsonl"));
  assert.equal(asked.length, 1);
  const res = await ask(env, "/files/v0.1.3982/attestations.jsonl");
  assert.deepEqual((await res.text()).trimEnd().split("\n").map((l) => JSON.parse(l)), [bundle]);
});

test("mirror: a release from before the attestations is not asked for any", async () => {
  const env = { RELEASES: bucket() };
  const rel = { version: "0.1.3890", assets: { a: { sha256: sha("A") } } };
  let asked = 0;
  await mirror(env, [rel], async () => new Response("A"), Date.now, async () => (asked++, []));
  assert.equal(asked, 0);
});
