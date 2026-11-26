// Run with Node's test runner and Playwright on the module path; see README.md.
// room from a page (internal/artifact/runtime.js): two tabs of one
// artifact see each other come and go, each one's presence, and each
// one's emits (their own too, as sameTab); a named room is heard by its
// joiners only; what is wrong is refused on the page. A shared copy's
// room goes through the link's page, and a visitor sends only on the
// topics the declaration opens to them. Under the sandbox CSP magpie
// serves pages with, so the socket is from an opaque origin. In both
// engines. The hub is faked here, on the same wire; internal/gateway's
// TestArtifactLive* hold magpie's, and site/live.test.mjs the site's.
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const runtime = fs.readFileSync(path.resolve(__dirname, "../../artifact/runtime.js"), "utf8").replace(/\n+$/, "");
const CSP = "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads";
const ME = "u_0123456789abcdef01234567";

function served(page, cfg) {
  const tag = "<script>" + runtime + "(" + JSON.stringify(cfg).replace(/</g, "\\u003c") + ");</script>";
  return page.replace(/<head>/i, "<head>" + tag);
}

const PAGE = `<!doctype html><html><head><title>Room</title></head><body><script>
(async () => {
  const out = (window.out = { peers: [], heard: [], conn: [] });
  const room = await claude.use("room");
  out.served = !!room;
  if (!room) { out.ready = true; return; }
  out.frozen = Object.isFrozen(room);
  out.same = room === (await claude.use("room"));
  window.room = room;
  room.onPeers((c) => { out.peers.push(c.peers.map((p) => ({ sameTab: p.sameTab, isMe: p.isMe, by: p.by, presence: p.presence }))); });
  room.on("move", (m) => out.heard.push({ data: m.data, sameTab: m.sameTab, by: m.by }));
  room.onConnection((up) => out.conn.push(up));
  const code = (p) => p.then(() => "ok", (e) => e.name + ":" + e.code);
  window.code = code;
  out.badTopic = await code(room.emit("Bad"));
  out.big = await code(room.emit("move", "x".repeat(5000)));
  out.notJSON = await code(room.emit("move", { at: new Date() }));
  out.badPresence = await code(room.presence([1]));
  out.badRoom = await code(room.join("Bad"));
  out.ready = true;
})().catch((e) => { window.out = { crash: String(e && e.stack || e) }; });
</script></body></html>`;

// a WebSocket server on node's own http: text frames, close and ping
function sockets(server, onConn) {
  server.on("upgrade", (req, sock) => {
    const accept = crypto.createHash("sha1").update(req.headers["sec-websocket-key"] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");
    sock.write("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n");
    const frame = (op, body) => {
      const head = body.length < 126 ? Buffer.from([0x80 | op, body.length]) : Buffer.from([0x80 | op, 126, body.length >> 8, body.length & 255]);
      if (!sock.destroyed) sock.write(Buffer.concat([head, body]));
    };
    const c = { req, onmsg: () => {}, onclose: () => {}, send: (m) => frame(1, Buffer.from(JSON.stringify(m))), closed: false };
    let buf = Buffer.alloc(0);
    sock.on("data", (d) => {
      buf = Buffer.concat([buf, d]);
      for (;;) {
        if (buf.length < 2) return;
        const op = buf[0] & 15;
        let len = buf[1] & 127, at = 2;
        if (len === 126) { if (buf.length < 4) return; len = buf.readUInt16BE(2); at = 4; }
        else if (len === 127) { if (buf.length < 10) return; len = Number(buf.readBigUInt64BE(2)); at = 10; }
        if (buf.length < at + 4 + len) return;
        const mask = buf.subarray(at, at + 4), body = Buffer.from(buf.subarray(at + 4, at + 4 + len));
        for (let i = 0; i < body.length; i++) body[i] ^= mask[i & 3];
        buf = buf.subarray(at + 4 + len);
        if (op === 8) { frame(8, body.subarray(0, 2)); sock.end(); return; }
        if (op === 9) { frame(10, body); continue; }
        if (op === 1) { try { c.onmsg(JSON.parse(body.toString())); } catch {} }
      }
    });
    const end = () => { if (c.closed) return; c.closed = true; c.onclose(); };
    sock.on("close", end);
    sock.on("error", end);
    onConn(c);
  });
}

// magpie's hub, as artifacts_room.go answers: every tab is the owner
function fakeMagpie(t) {
  const hub = { conns: [], rooms: new Map(), seen: [] };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    if (req.method === "GET" && url.pathname === "/a/room/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(PAGE, { api: "/a/room/_api", token: "tok", caps: { room: {}, user: {} }, viewer: { id: ME }, version: 1, home: "/a/room/" }));
    }
    if (req.method === "GET" && url.pathname === "/a/none/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(PAGE, { api: "/a/none/_api", token: "tok", caps: { user: {} }, viewer: { id: ME }, version: 1, home: "/a/none/" }));
    }
    res.writeHead(404);
    res.end();
  });
  let n = 0;
  sockets(server, (c) => {
    const url = new URL(c.req.url, "http://127.0.0.1");
    hub.seen.push({ path: url.pathname, t: url.searchParams.get("t"), origin: c.req.headers.origin });
    c.sid = "p_" + ++n;
    hub.conns.push(c);
    const member = (r) => hub.rooms.get(r) || hub.rooms.set(r, new Map()).get(r);
    const peer = (r, x, to) => ({ peer: x.sid, by: ME, isMe: true, sameTab: x === to, kind: "viewer", guest: false, presence: member(r).get(x).presence, updatedAt: member(r).get(x).at });
    const tell = (r, skip, msg) => { for (const x of member(r).keys()) if (x !== skip) x.send(msg(x)); };
    c.send({ t: "hi", sid: c.sid });
    c.onmsg = (m) => {
      const r = m.room;
      if (m.t === "join") {
        if (r !== "" && !/^[a-z0-9][a-z0-9_.-]{0,47}$/.test(r)) return c.send({ t: "error", ref: m.ref, code: "invalid_argument", message: "a bad name" });
        member(r).set(c, { presence: {}, at: Date.now() });
        c.send({ t: "joined", ref: m.ref, room: r, peers: [...member(r).keys()].map((x) => peer(r, x, c)) });
        tell(r, c, (x) => ({ t: "peer", room: r, peer: peer(r, c, x) }));
      } else if (m.t === "presence" && member(r).has(c)) {
        const me = member(r).get(c);
        for (const [k, v] of Object.entries(m.patch || {})) if (v === null) delete me.presence[k]; else me.presence[k] = v;
        me.at = Date.now();
        tell(r, null, (x) => ({ t: "peer", room: r, peer: peer(r, c, x) }));
      } else if (m.t === "emit" && member(r).has(c)) {
        tell(r, null, (x) => ({ t: "msg", room: r, topic: m.topic, data: m.data, from: { peer: c.sid, by: ME, isMe: true, sameTab: x === c, kind: "viewer", guest: false } }));
      } else if (m.t === "leave" && member(r).has(c)) {
        member(r).delete(c);
        tell(r, null, () => ({ t: "gone", room: r, peer: c.sid }));
      }
    };
    c.onclose = () => {
      for (const [r, ms] of hub.rooms) if (ms.has(c)) { ms.delete(c); tell(r, null, () => ({ t: "gone", room: r, peer: c.sid })); }
    };
  });
  return new Promise((ok) => server.listen(0, "127.0.0.1", () => {
    t.after(() => { for (const c of hub.conns) c.closed = true; server.closeAllConnections?.(); server.close(); });
    ok({ base: `http://127.0.0.1:${server.address().port}`, hub });
  }));
}

// a shared copy: the link's page (site/shares.js) hands the frame a
// visitor's session and relays its room; here it records what the frame
// sends and answers joins itself
const WRAPPER = (frameSrc, caps) => `<!doctype html><html><head><title>Link</title></head><body><iframe id=f src="${frameSrc}"></iframe><script>
window.sent = [];
const f = document.getElementById("f");
let port = null;
const to = (m) => port && port.postMessage(Object.assign({ magpie: "artifact-share" }, m));
window.relay = (msg) => to({ type: "live", msg });
addEventListener("message", (e) => {
  if (e.source !== f.contentWindow || !e.data || e.data.magpie !== "artifact-share" || e.data.type !== "hello") return;
  port = e.ports[0];
  port.onmessage = (p) => from(p.data);
  to({ type: "session", api: "https://usemagpie.ai/api/shares/abcdefghijklmnop/rt", token: "s", viewer: { id: "u_guest0000000000000000000", name: "Guest", level: "interact", guest: true }, caps: ${JSON.stringify(caps)} });
  relay({ t: "open" });
});
function from(d) {
  if (d && d.type === "live") {
    const m = d.msg;
    sent.push(m);
    if (m.t === "join") relay({ t: "joined", ref: m.ref, room: m.room, peers: [{ peer: "p_1", by: "u_guest0000000000000000000", isMe: true, sameTab: true, guest: true, presence: {} }] });
  }
}
</script></body></html>`;

const SHARED = `<!doctype html><html><head><title>Board</title></head><body><script>
(async () => {
  const out = (window.out = { heard: [] });
  const room = await claude.use("room");
  out.served = !!room;
  if (!room) return;
  room.on("move", (m) => out.heard.push({ data: m.data, guest: m.guest }));
  const code = (p) => p.then(() => "ok", (e) => e.name + ":" + e.code);
  await new Promise((ok) => room.onConnection((up) => { if (up) ok(); }));
  out.move = await code(room.emit("move", { to: 1 }));
  out.chat = await code(room.emit("chat", "hi"));
  out.ready = true;
})().catch((e) => { window.out = { crash: String(e && e.stack || e) }; });
</script></body></html>`;

function fakeLink(t) {
  const caps = { room: { topics: { move: "interact" } }, user: {} };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    if (url.pathname === "/a/abcdefghijklmnop") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      return res.end(WRAPPER("/c/abcdefghijklmnop/1/", caps));
    }
    if (url.pathname === "/c/abcdefghijklmnop/1/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(SHARED, { caps, shared: true }));
    }
    res.writeHead(404);
    res.end();
  });
  return new Promise((ok) => server.listen(0, "127.0.0.1", () => {
    t.after(() => { server.closeAllConnections?.(); server.close(); });
    ok({ base: `http://127.0.0.1:${server.address().port}` });
  }));
}

// the real link's page script (site/shares.js shell), a page that keeps
// data, and a page of the same share a link in it opens, which says hello
// as the runtime does and reports to /got what it is sent
const KEY = "k".repeat(43);
const LINKED = `<!doctype html><html><head><title>Notes</title></head><body><a id=go href="other.html">other</a><script>
(async () => {
  const out = (window.out = {});
  out.hash = location.hash;
  const user = await claude.use("user");
  out.user = !!user;
  out.ready = true;
})().catch((e) => { window.out = { crash: String(e && e.stack || e) }; });
</script></body></html>`;
const OTHER = `<!doctype html><html><head><title>Other</title></head><body><script>
const got = (m) => fetch("/got?m=" + encodeURIComponent(JSON.stringify(m)), { mode: "no-cors" });
addEventListener("message", (e) => got(e.data));
// listen first: the link's page sends nothing to a document that hasn't
// said its key, socket news included; then ask, with a wrong key and
// with none
setTimeout(() => {
  parent.postMessage({ magpie: "artifact-share", type: "hello", key: "x".repeat(43) }, "*");
  parent.postMessage({ magpie: "artifact-share", type: "hello" }, "*");
}, 2500);
</script></body></html>`;

async function realLink(t) {
  const { shell } = await import(path.resolve(__dirname, "../../../site/shares.js"));
  const caps = { user: {} };
  const got = [];
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    if (url.pathname === "/a/abcdefghijklmnop") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      return res.end(`<!doctype html><html><head><title>Link</title></head><body><header><span class="peers" hidden></span><button class="me" type="button" hidden></button></header>` +
        `<iframe src="/c/abcdefghijklmnop/1/#magpie-share=${KEY}" sandbox="allow-scripts allow-forms allow-popups"></iframe>` +
        `<form class="sheet" hidden><input><button type="submit">Join</button><button class="out" type="button" hidden>Sign out</button></form>` +
        `<script>(${shell.toString()})(${JSON.stringify({ id: "abcdefghijklmnop", key: KEY })})</script></body></html>`);
    }
    if (url.pathname === "/api/shares/abcdefghijklmnop/session" && req.method === "POST") {
      res.writeHead(200, { "Content-Type": "application/json" });
      return res.end(JSON.stringify({ api: "https://usemagpie.ai/api/shares/abcdefghijklmnop/rt", token: "the-session-token", exp: Date.now() + 36e5, viewer: { id: "", name: "", level: "view", guest: false }, caps }));
    }
    if (url.pathname === "/c/abcdefghijklmnop/1/") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(served(LINKED, { caps, shared: true }));
    }
    if (url.pathname === "/c/abcdefghijklmnop/1/other.html") {
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": CSP });
      return res.end(OTHER);
    }
    if (url.pathname === "/got") got.push(url.searchParams.get("m"));
    res.writeHead(404);
    res.end();
  });
  return new Promise((ok) => server.listen(0, "127.0.0.1", () => {
    t.after(() => { server.closeAllConnections?.(); server.close(); });
    ok({ base: `http://127.0.0.1:${server.address().port}`, got });
  }));
}

// until is fn's first truthy answer, polled; it fails after 15s
async function until(fn, what) {
  const end = Date.now() + 15000;
  for (;;) {
    const v = await fn().catch(() => null);
    if (v) return v;
    if (Date.now() > end) throw new Error("waited for " + what);
    await new Promise((ok) => setTimeout(ok, 50));
  }
}

const out = (page) => page.evaluate(() => window.out);
async function ready(page) {
  await page.waitForFunction(() => window.out && (window.out.ready || window.out.crash), null, { timeout: 15000 });
  const o = await out(page);
  assert.equal(o.crash, undefined, o.crash);
  return o;
}
// the newest onPeers delivery
const lastPeers = (page) => page.evaluate(() => window.out.peers.at(-1) || []);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`two tabs of a page are in its room (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const ctx = await browser.newContext({ viewport: { width: 420, height: 700 } });
    const errors = [];
    const tab = async () => {
      const p = await ctx.newPage();
      p.on("pageerror", (e) => errors.push(String(e)));
      await p.goto(site.base + "/a/room/");
      await ready(p);
      await p.waitForFunction(() => window.out.conn.includes(true), null, { timeout: 10000 });
      return p;
    };
    const one = await tab();
    const o = await out(one);
    assert.equal(o.served, true);
    assert.equal(o.frozen, true);
    assert.equal(o.same, true, "use() is memoized");
    assert.equal(o.badTopic, "RoomError:invalid_argument");
    assert.equal(o.big, "RoomError:invalid_argument");
    assert.equal(o.notJSON, "RoomError:invalid_argument");
    assert.equal(o.badPresence, "RoomError:invalid_argument");
    assert.equal(o.badRoom, "RoomError:invalid_argument");
    assert.deepEqual(site.hub.seen[0], { path: "/a/room/_api/live", t: "tok", origin: "null" }, "the socket: its token, from the page's opaque origin");
    await one.waitForFunction(() => (window.out.peers.at(-1) || []).length === 1);
    assert.deepEqual(await lastPeers(one), [{ sameTab: true, isMe: true, by: ME, presence: {} }]);

    // a second tab comes
    const two = await tab();
    await one.waitForFunction(() => (window.out.peers.at(-1) || []).length === 2);
    assert.deepEqual((await lastPeers(one)).map((p) => p.sameTab).sort(), [false, true]);

    // its presence shows in the first; its own shows at once
    // its own shows on the next delivery (onPeers delivers once a frame)
    await two.evaluate(() => room.presence({ x: 3, sel: "a<b" }));
    await two.waitForFunction(() => ((window.out.peers.at(-1) || []).find((p) => p.sameTab) || { presence: {} }).presence.x === 3);
    assert.deepEqual((await lastPeers(two)).find((p) => p.sameTab).presence, { x: 3, sel: "a<b" }, "its own presence");
    await one.waitForFunction(() => (window.out.peers.at(-1) || []).some((p) => !p.sameTab && p.presence.x === 3));
    await two.evaluate(() => room.presence({ x: null }));
    await one.waitForFunction(() => (window.out.peers.at(-1) || []).some((p) => !p.sameTab && !("x" in p.presence) && p.presence.sel === "a<b"));

    // an emit: everyone hears it, the sender as sameTab
    await one.evaluate(() => room.emit("move", { to: 4 }));
    await two.waitForFunction(() => window.out.heard.length === 1);
    await one.waitForFunction(() => window.out.heard.length === 1);
    assert.deepEqual((await out(two)).heard, [{ data: { to: 4 }, sameTab: false, by: ME }]);
    assert.deepEqual((await out(one)).heard, [{ data: { to: 4 }, sameTab: true, by: ME }]);

    // a named room is its joiners' alone
    const named = await two.evaluate(async () => {
      const g = await room.join("game-1");
      window.g = g;
      window.rolls = [];
      g.on("roll", (m) => rolls.push(m.data));
      const again = await room.join("game-1");
      await g.emit("roll", 6);
      return { name: g.name, same: again === g, frozen: Object.isFrozen(g) };
    });
    assert.deepEqual(named, { name: "game-1", same: true, frozen: true });
    await two.waitForFunction(() => window.rolls.length === 1);
    await one.waitForTimeout(300);
    assert.equal((await out(one)).heard.length, 1, "the lobby heard a named room");
    assert.equal(await two.evaluate(async () => { await g.leave(); return code(g.emit("roll", 1)); }), "RoomError:invalid_argument", "a room left");

    // a tab that goes is gone from the others
    await two.close();
    await one.waitForFunction(() => (window.out.peers.at(-1) || []).length === 1);
    assert.deepEqual(errors, []);
  });

  test(`a page that doesn't declare room gets null (${engine})`, async (t) => {
    const site = await fakeMagpie(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    await page.goto(site.base + "/a/none/");
    assert.equal((await ready(page)).served, false);
    await page.waitForTimeout(300);
    assert.equal(site.hub.seen.length, 0, "a socket for a page with no room");
  });

  test(`a page a link in the frame opens is never handed the session (${engine})`, async (t) => {
    const site = await realLink(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(site.base + "/a/abcdefghijklmnop");
    // the page's document in the frame, once it has run
    const notes = () => until(async () => {
      const f = page.frames().find((f) => f !== page.mainFrame() && /\/c\/abcdefghijklmnop\/1\/(#.*)?$/.test(f.url()));
      const o = f && await f.evaluate(() => window.out && (window.out.ready || window.out.crash) && window.out);
      return o;
    }, "the page in the frame");
    let o = await notes();
    assert.equal(o.crash, undefined, o.crash);
    assert.equal(o.user, true, "the page's own document has the session");
    assert.equal(o.hash, "", "the key is off the address before the page reads it");

    const frame = page.frames().find((f) => f !== page.mainFrame());
    await frame.click("#go");
    // the other page listens, then asks: it hears nothing, and the frame
    // is opened again, at a new key, where the page has the session again
    await page.waitForFunction((k) => {
      const src = document.querySelector("iframe").getAttribute("src");
      return /#magpie-share=/.test(src) && !src.endsWith(k);
    }, KEY, { timeout: 15000 });
    o = await notes();
    assert.equal(o.user, true, "the page opened again has the session");
    assert.deepEqual(site.got, [], "the other page was sent nothing");
    assert.deepEqual(errors, []);
  });

  test(`a shared copy's visitor sends on the topics opened to them (${engine})`, async (t) => {
    const site = await fakeLink(t);
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(site.base + "/a/abcdefghijklmnop");
    const frame = page.frame({ url: /\/c\/abcdefghijklmnop\/1\// });
    await frame.waitForFunction(() => window.out && (window.out.ready || window.out.crash), null, { timeout: 15000 });
    const o = await frame.evaluate(() => window.out);
    assert.equal(o.crash, undefined, o.crash);
    assert.equal(o.served, true);
    assert.equal(o.move, "ok", "move is open to interact");
    assert.equal(o.chat, "RoomError:not_permitted", "chat is the owner's and Editors'");
    const sent = await page.evaluate(() => window.sent);
    assert.deepEqual(sent.filter((m) => m.t === "emit"), [{ t: "emit", room: "", topic: "move", data: { to: 1 } }], "only what may go goes");
    assert.ok(sent.some((m) => m.t === "join" && m.room === ""), "the lobby joined through the link");
    // what the link relays reaches the page
    await page.evaluate(() => relay({ t: "msg", room: "", topic: "move", data: { to: 2 }, from: { peer: "p_2", by: "u_x", guest: true } }));
    await frame.waitForFunction(() => window.out.heard.length === 1);
    assert.deepEqual(await frame.evaluate(() => window.out.heard), [{ data: { to: 2 }, guest: true }]);
    assert.deepEqual(errors, []);
  });
}
