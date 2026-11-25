// Run with Node's test runner and Playwright on the module path; see README.md.
// tts from a page in magpie's viewer shell (internal/artifact/runtime.js,
// shell.js): the runtime checks a call as contract 0.2.99 has it, the
// shell asks magpie for each line's audio with its own token, and the
// page plays it. The reading's states, lines, pause, replacing, the
// viewer's Stop and Escape, and magpie's refusals reach the page. In
// both engines, at a narrow width, in every language. The route itself
// is held by TestArtifactTTS* in internal/gateway.
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const dir = path.resolve(__dirname, "../../artifact");
const runtime = fs.readFileSync(path.join(dir, "runtime.js"), "utf8").replace(/\n+$/, "");
const shellJS = fs.readFileSync(path.join(dir, "shell.js"), "utf8").replace(/\n+$/, "");
const shellCSS = fs.readFileSync(path.join(dir, "shell.css"), "utf8");
const PAGE_CSP = "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads";
assert.ok(fs.readFileSync(path.join(dir, "shell.go"), "utf8").includes('allow="autoplay; clipboard-write; fullscreen"'), "the shell's frame as shell.go has it");
const shellCSPSource = /func ShellCSP\(nonce string\) string \{\n\treturn (".*")\n\}/.exec(fs.readFileSync(path.join(dir, "shell.go"), "utf8"));
assert.ok(shellCSPSource, "ShellCSP in shell.go");
function shellCSP(nonce) {
  return shellCSPSource[1].split(/" \+ nonce \+ "/).map((s) => s.replace(/^"|"$/g, "")).join(nonce);
}
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&#34;");
function shellPage(cfg) {
  const nonce = crypto.randomBytes(18).toString("base64url");
  const json = JSON.stringify(cfg).replace(/</g, "\\u003c").replace(/>/g, "\\u003e").replace(/&/g, "\\u0026");
  const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="referrer" content="no-referrer"><title>${esc(cfg.title)}</title><style nonce="${nonce}">${shellCSS}</style></head><body><iframe id="page" title="${esc(cfg.title)}" allow="autoplay; clipboard-write; fullscreen"></iframe><div id="ui"></div><script nonce="${nonce}">${shellJS}(${json});</script></body></html>`;
  return { html, nonce };
}
function served(page, cfg) {
  const tag = "<script>" + runtime + "(" + JSON.stringify(cfg).replace(/</g, "\\u003c") + ");</script>";
  return page.replace(/<head>/i, "<head>" + tag);
}


const PAGE = `<!doctype html><html><head><title>Read</title></head><body>
<button id=go>Read</button><input id=box>
<script>
window.out = {};
window.log = [];
(async () => {
  const t = await claude.use("tts");
  out.served = !!t;
  out.frozen = Object.isFrozen(t);
  out.verbs = t ? Object.keys(t).sort() : [];
  if (!t) return;
  window.t = t;
  t.onState((s) => log.push(["state", s.state, s.reason || ""]));
  t.onLine((l) => log.push(["line", l.line, l.id || ""]));
  const ans = (p) => p.then((r) => ({ ms: r.playedMs }), (e) => JSON.parse(JSON.stringify({ code: e.code, reason: e.reason, after: e.retryAfterMs, name: e.name })));
  window.speak = (w, o) => ans(t.speak(w, o));
  window.add = (w, o) => ans(t.add(w, o));
  out.ready = true;
})().catch((e) => { out.crash = String(e && e.stack || e); });
</script></body></html>`;

// a short silent WAV: 8 kHz, 16-bit, mono
function wav(ms) {
  const n = Math.round(8 * ms) * 2, b = Buffer.alloc(44 + n);
  b.write("RIFF", 0); b.writeUInt32LE(36 + n, 4); b.write("WAVE", 8); b.write("fmt ", 12);
  b.writeUInt32LE(16, 16); b.writeUInt16LE(1, 20); b.writeUInt16LE(1, 22); b.writeUInt32LE(8000, 24);
  b.writeUInt32LE(16000, 28); b.writeUInt16LE(2, 32); b.writeUInt16LE(16, 34); b.write("data", 36); b.writeUInt32LE(n, 40);
  return b;
}

// a fake magpie: the shell, the page, an empty comments store, and tts:
// "limit" is rate limited, "none" has no model, "long" is 4 seconds
async function fakeMagpie(t, lang, caps) {
  const streams = new Set(), lines = [];
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    if (req.method === "GET" && url.pathname === "/a/x/") {
      const origin = "http://" + req.headers.host;
      if (req.headers["sec-fetch-dest"] === "document") {
        const { html, nonce } = shellPage({ api: "/a/x/_api", token: "stok", id: "x", title: "Read", src: "/a/x/", version: 1, origin, lang });
        res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": shellCSP(nonce), "Cache-Control": "no-store" });
        return res.end(html);
      }
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": PAGE_CSP });
      return res.end(served(PAGE, { api: "/a/x/_api", token: "ptok", caps, viewer: { id: "u_0123456789abcdef01234567" }, version: 1, home: "/a/x/", origin }));
    }
    if (req.method === "GET" && url.pathname === "/a/x/_api/shell") {
      res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
      res.write('retry: 2000\n\ndata: {"comments":true}\n\n');
      streams.add(res);
      req.on("close", () => streams.delete(res));
      return;
    }
    if (req.method === "POST" && (url.pathname === "/a/x/_api/comments" || url.pathname === "/a/x/_api/tts")) {
      let b = "";
      req.on("data", (c) => (b += c));
      req.on("end", () => {
        if (url.pathname.endsWith("/comments")) {
          res.writeHead(200, { "Content-Type": "application/json" });
          return res.end(JSON.stringify({ threads: [], version: 1, agent: { can: "no_session" }, me: "u_0123456789abcdef01234567" }));
        }
        const j = JSON.parse(b);
        lines.push(j);
        if (j.text === "limit") {
          res.writeHead(429, { "Content-Type": "application/json", "Retry-After": "3" });
          return res.end(JSON.stringify({ error: { code: "rate_limited", message: "a page reads at most 60 lines a minute" } }));
        }
        if (j.text === "none") {
          res.writeHead(503, { "Content-Type": "application/json" });
          return res.end(JSON.stringify({ error: { code: "unavailable", message: "reading aloud needs a text-to-speech model" } }));
        }
        res.writeHead(200, { "Content-Type": "audio/wav" });
        res.end(wav(j.text.startsWith("long") ? 4000 : 400));
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  t.after(() => { for (const s of streams) s.destroy(); server.close(); });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return { base: `http://127.0.0.1:${server.address().port}`, lines };
}

// Playwright's evaluate counts as the viewer's gesture in the frame, so a
// test of what a page may do before one stands in a userActivation that
// only a real click or key press turns on
const INERT = `(() => {
  let on = false, at = 0;
  const ua = { get hasBeenActive() { return on; }, get isActive() { return on && Date.now() - at < 5000; } };
  Object.defineProperty(Navigator.prototype, "userActivation", { get: () => ua, configurable: true });
  for (const k of ["pointerdown", "keydown"]) addEventListener(k, (e) => { if (e.isTrusted) { on = true; at = Date.now(); } }, true);
})();`;

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
async function open(t, engine, opts) {
  opts = opts || {};
  const site = await fakeMagpie(t, opts.lang || "en", opts.caps || { tts: {} });
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: opts.viewport || { width: 1200, height: 800 } });
  if (opts.inert) await page.addInitScript(INERT);
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(site.base + "/a/x/");
  const frame = await (await page.waitForSelector("#page")).contentFrame();
  await frame.waitForFunction(() => window.out && (window.out.ready || window.out.crash || window.out.served === false), null, { timeout: 15000 });
  return { page, frame, errors, lines: site.lines };
}
const bar = (page) => page.locator(".reading.in");
// the page's speak, started from a click in the page as a page would
async function start(frame, what, o) {
  await frame.evaluate(([what, o]) => {
    document.getElementById("go").onclick = () => { window.res = window.speak(what, o); };
  }, [what, o]);
  await frame.locator("#go").click();
}
const states = (frame) => frame.evaluate(() => log.filter((l) => l[0] === "state").map((l) => l[1] + (l[2] ? ":" + l[2] : "")));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`a page reads its lines aloud (${engine})`, async (t) => {
    const { page, frame, errors, lines } = await open(t, engine);
    const out = await frame.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.deepEqual([out.served, out.frozen, out.verbs], [true, true, ["active", "add", "onLine", "onPosition", "onState", "pause", "resume", "setRate", "speak", "stop", "voices"]]);
    await start(frame, [{ text: "One", id: "a" }, { text: "Two", voice: "airy" }]);
    await bar(page).waitFor();
    assert.equal(await bar(page).locator(".what").textContent(), "Reading aloud");
    const extra = frame.evaluate(() => window.add("Three", { voice: "rounded" }));
    const r = await frame.evaluate(() => window.res);
    assert.ok(r.ms > 0, JSON.stringify(r));
    assert.ok((await extra).ms > 0);
    await page.waitForFunction(() => !document.querySelector(".reading"));
    assert.deepEqual(await states(frame), ["starting", "speaking", "idle:completed"]);
    assert.deepEqual(await frame.evaluate(() => log.filter((l) => l[0] === "line")), [["line", 0, "a"], ["line", 1, ""], ["line", 2, ""]]);
    assert.deepEqual(lines.map((l) => [l.t, l.text, l.voice, l.version]), [["stok", "One", "buttery", 0], ["stok", "Two", "airy", 0], ["stok", "Three", "rounded", 0]]);
    assert.equal(await frame.evaluate(() => t.active()), false);
    assert.deepEqual(await frame.evaluate(() => t.voices().then((v) => v.map((x) => x.id))), ["buttery", "airy", "glassy", "rounded"]);
    assert.deepEqual(errors, []);
  });

  test(`the viewer stops it, from the bar or with Escape (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine);
    await start(frame, "long one");
    await bar(page).waitFor();
    await bar(page).getByRole("button", { name: "Stop" }).click();
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "stopped", reason: "viewer_stopped", name: "TTSError" });
    await page.waitForFunction(() => !document.querySelector(".reading"));
    // a new speak waits a few seconds, a click or not
    await start(frame, "again");
    const again = await frame.evaluate(() => window.res);
    assert.equal(again.code, "no_gesture");
    assert.ok(again.after > 0 && again.after <= 6000, JSON.stringify(again));
    await frame.evaluate(() => new Promise((r) => setTimeout(r, 6100)));
    // Escape in a text field is the field's; elsewhere in the page it stops
    await start(frame, "long two");
    await bar(page).waitFor();
    await frame.locator("#box").focus();
    await page.keyboard.press("Escape");
    assert.equal(await frame.evaluate(() => t.active()), true);
    await frame.locator("#go").focus();
    await page.keyboard.press("Escape");
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "stopped", reason: "viewer_stopped", name: "TTSError" });
    assert.deepEqual((await states(frame)).slice(-1), ["idle:viewer_stopped"]);
  });

  test(`pause, resume, a new speak replaces, stop (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine);
    await start(frame, "long three");
    await bar(page).waitFor();
    await frame.waitForFunction(() => log.some((l) => l[1] === "speaking"));
    await frame.evaluate(() => t.pause());
    await page.locator(".reading.paused").waitFor();
    assert.deepEqual((await states(frame)).slice(-1), ["paused"]);
    await frame.evaluate(() => t.resume());
    await page.waitForFunction(() => !document.querySelector(".reading.paused"));
    assert.deepEqual((await states(frame)).slice(-1), ["speaking"]);
    assert.equal(await frame.evaluate(() => t.setRate(9)), 1.5);
    // a new speak ends the first
    await frame.evaluate(() => { document.getElementById("go").onclick = () => { window.res2 = window.speak("Short"); }; });
    await frame.locator("#go").click();
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "stopped", reason: "replaced", name: "TTSError" });
    assert.ok((await frame.evaluate(() => window.res2)).ms > 0);
    await start(frame, "long four");
    await bar(page).waitFor();
    await frame.evaluate(() => t.stop());
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "stopped", reason: "stopped", name: "TTSError" });
    await page.waitForFunction(() => !document.querySelector(".reading"));
  });

  test(`magpie's refusals reach the page (${engine})`, async (t) => {
    const { frame } = await open(t, engine);
    await start(frame, "limit");
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "rate_limited", after: 3000, name: "TTSError" });
    await start(frame, "none");
    assert.equal((await frame.evaluate(() => window.res)).code, "unavailable");
    assert.deepEqual((await states(frame)).slice(-1), ["idle:unavailable"]);
  });

  test(`a bad call is refused, and nothing speaks before a click (${engine})`, async (t) => {
    const { frame, lines } = await open(t, engine, { inert: true });
    assert.equal((await frame.evaluate(() => window.speak("Hello"))).code, "no_gesture");
    await frame.locator("#box").click();
    const codes = await frame.evaluate(async () => {
      const r = async (w, o) => (await window.speak(w, o)).code;
      return [
        await r(""), await r("  "), await r("x".repeat(2001)), await r(Array(101).fill("a")), await r([]),
        await r("a", { voice: "loud" }), await r([{ text: "a", id: "x".repeat(65) }]), await r("a", { rate: "fast" }),
        await r("a", { language: "!!" }), await r(7), await r([{ txt: "a" }]),
      ];
    });
    assert.deepEqual(codes, Array(11).fill("invalid_argument"));
    assert.equal(lines.length, 0, "magpie was asked nothing");
  });

  test(`a page without tts gets null (${engine})`, async (t) => {
    const { frame } = await open(t, engine, { caps: { comments: {} } });
    assert.equal(await frame.evaluate(() => window.out.served), false);
  });

  test(`the bar fits a narrow window beside the comments bar (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { viewport: { width: 320, height: 568 } });
    await start(frame, "long five");
    await bar(page).waitFor();
    await page.waitForTimeout(250);
    const b = await bar(page).boundingBox(), c = await page.locator(".bar").boundingBox();
    assert.ok(b.x >= 0 && b.x + b.width <= 320 && b.y + b.height <= 568, `inside the window: ${JSON.stringify(b)}`);
    assert.ok(!c || b.x + b.width <= c.x || c.y + c.height <= b.y, `apart from the comments bar: ${JSON.stringify([b, c])}`);
    await frame.evaluate(() => t.stop());
  });

  const WORDS = { zh: ["正在朗读", "停止"], "zh-TW": ["正在朗讀", "停止"], ja: ["読み上げ中", "停止"], de: ["Wird vorgelesen", "Stoppen"] };
  for (const [lang, [what, stop]] of Object.entries(WORDS)) {
    test(`the bar speaks ${lang} (${engine})`, async (t) => {
      const { page, frame } = await open(t, engine, { lang });
      await start(frame, "long six");
      await bar(page).waitFor();
      assert.equal(await bar(page).locator(".what").textContent(), what);
      await bar(page).getByRole("button", { name: stop, exact: true }).click();
      assert.equal((await frame.evaluate(() => window.res)).reason, "viewer_stopped");
    });
  }
}
