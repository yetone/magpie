// Run with Node's test runner and Playwright on the module path; see README.md.
// dictation from a page in magpie's viewer shell (internal/artifact/runtime.js,
// shell.js): the shell asks the viewer once for each artifact, opens the
// microphone itself, cuts the recording at each pause and sends each phrase
// to magpie with its own token; the page gets the words. The question, the
// Permissions panel, the states, the viewer's Stop and Escape, tts being
// busy, and magpie's refusals reach the page. In both engines, at a narrow
// width, in every language. The microphone is a tone the test turns up and
// down (a voice and its pauses); the route itself is held by
// TestArtifactDictation* in internal/gateway.
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

const PAGE = `<!doctype html><html><head><title>Dictate</title></head><body>
<button id=go>Dictate</button><input id=box>
<script>
window.out = {};
window.log = [];
(async () => {
  const d = await claude.use("dictation");
  const p = await claude.use("permissions");
  window.p = p;
  out.served = !!d;
  out.frozen = Object.isFrozen(d);
  out.verbs = d ? Object.keys(d).sort() : [];
  if (!d) return;
  window.d = d;
  window.t = await claude.use("tts");
  d.onState((s) => log.push(["state", s.state, s.reason || ""]));
  d.onPartial((s) => log.push(["partial", s.text]));
  d.onFinal((s) => log.push(["final", s.text]));
  window.ans = (p) => p.then(() => "ok", (e) => JSON.parse(JSON.stringify({ code: e.code, reason: e.reason, name: e.name })));
  out.ready = true;
})().catch((e) => { out.crash = String(e && e.stack || e); });
</script></body></html>`;

// the microphone: a tone the test turns up (a voice) and down (a pause)
const MIC = `(() => {
  if (window.top !== window) return;
  window.__mic = { calls: 0, deny: false, gain: null };
  // on the prototype: WebKit may hand out a new navigator.mediaDevices
  // wrapper later, without a property set on the old one
  const md = window.MediaDevices ? MediaDevices.prototype : {};
  if (!navigator.mediaDevices) Object.defineProperty(navigator, "mediaDevices", { value: md });
  md.getUserMedia = async () => {
    __mic.calls++;
    if (__mic.deny) throw new DOMException("the viewer said no", "NotAllowedError");
    const ctx = new (window.AudioContext || window.webkitAudioContext)();
    const osc = ctx.createOscillator(), g = ctx.createGain(), dst = ctx.createMediaStreamDestination();
    osc.frequency.value = 220;
    g.gain.value = 0;
    osc.connect(g).connect(dst);
    osc.start();
    ctx.resume();
    __mic.gain = g;
    __mic.stream = dst.stream;
    return dst.stream;
  };
  window.__voice = (on) => { __mic.gain.gain.value = on ? 0.5 : 0; };
})();`;

// a fake magpie: the shell, the page, an empty comments store, and
// dictation answering site.say (a status and code when site.fail is set)
async function fakeMagpie(t, lang, caps) {
  const streams = new Set(), pieces = [];
  const site = { say: "Hello there", fail: null, pieces };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    if (req.method === "GET" && url.pathname === "/a/x/") {
      const origin = "http://" + req.headers.host;
      if (req.headers["sec-fetch-dest"] === "document") {
        const { html, nonce } = shellPage({ api: "/a/x/_api", token: "stok", id: "x", title: "Dictate", src: "/a/x/", version: 1, origin, lang });
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
    if (req.method === "POST" && (url.pathname === "/a/x/_api/comments" || url.pathname === "/a/x/_api/dictation")) {
      const chunks = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", () => {
        if (url.pathname.endsWith("/comments")) {
          res.writeHead(200, { "Content-Type": "application/json" });
          return res.end(JSON.stringify({ threads: [], version: 1, agent: { can: "no_session" }, me: "u_0123456789abcdef01234567" }));
        }
        pieces.push({ type: req.headers["content-type"], token: req.headers["x-magpie-token"], version: req.headers["x-magpie-version"], language: req.headers["x-magpie-language"], bytes: Buffer.concat(chunks).length });
        if (site.fail) {
          res.writeHead(site.fail[0], { "Content-Type": "application/json" });
          return res.end(JSON.stringify({ error: { code: site.fail[1], message: "magpie says " + site.fail[1] } }));
        }
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ text: site.say }));
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  t.after(() => { for (const s of streams) s.destroy(); server.close(); });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  site.base = `http://127.0.0.1:${server.address().port}`;
  return site;
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

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium", args: ["--autoplay-policy=no-user-gesture-required"] }));
async function open(t, engine, opts) {
  opts = opts || {};
  const site = await fakeMagpie(t, opts.lang || "en", opts.caps || { dictation: {}, tts: {} });
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: opts.viewport || { width: 1200, height: 800 } });
  await page.addInitScript(MIC);
  if (opts.inert) await page.addInitScript(INERT);
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(site.base + "/a/x/");
  if (opts.grant) await page.evaluate((g) => localStorage.setItem("magpie.artifact.x.dictation", g), opts.grant);
  if (opts.grant) await page.reload();
  const frame = await (await page.waitForSelector("#page")).contentFrame();
  await frame.waitForFunction(() => window.out && (window.out.ready || window.out.crash || window.out.served === false), null, { timeout: 15000 });
  return { page, frame, errors, site };
}
const bar = (page) => page.locator(".reading.listening.in");
const ask = (page) => page.locator(".pop.save.in[role=alertdialog]");
// the page's start, from a click in the page as a page would
async function start(frame, o) {
  await frame.evaluate((o) => {
    document.getElementById("go").onclick = () => { window.res = window.ans(window.d.start(o)); };
  }, o);
  await frame.locator("#go").click();
}
const states = (frame) => frame.evaluate(() => log.filter((l) => l[0] === "state").map((l) => l[1] + (l[2] ? ":" + l[2] : "")));
const finals = (frame) => frame.evaluate(() => log.filter((l) => l[0] === "final").map((l) => l[1]));
// a phrase: the voice for ms, then quiet until its words are in
async function say(page, frame, ms, n) {
  await page.evaluate(() => window.__voice(true));
  await page.waitForTimeout(ms);
  await page.evaluate(() => window.__voice(false));
  await frame.waitForFunction((n) => log.filter((l) => l[0] === "final").length >= n, n, { timeout: 15000 });
}

// a phrase whose words magpie refuses: the session ends with its reason
async function speakUntilIdle(page, frame) {
  await page.evaluate(() => window.__voice(true));
  await page.waitForTimeout(1300);
  await page.evaluate(() => window.__voice(false));
  await frame.waitForFunction(() => !d.active(), null, { timeout: 15000 });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`a page takes the viewer's words, asked once (${engine})`, async (t) => {
    const { page, frame, errors, site } = await open(t, engine);
    const out = await frame.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.deepEqual([out.served, out.frozen, out.verbs], [true, true, ["active", "onFinal", "onPartial", "onState", "start", "stop"]]);
    assert.equal(await frame.evaluate(() => p.state("dictation")), "prompt");
    await start(frame);
    await ask(page).waitFor();
    assert.equal(await ask(page).locator(".ask").textContent(), "Let “Dictate” take dictation?");
    assert.equal(await page.evaluate(() => window.__mic.calls), 0, "no microphone before the viewer says yes");
    await page.waitForTimeout(450);
    await ask(page).getByRole("button", { name: "Allow", exact: true }).click();
    assert.equal(await frame.evaluate(() => window.res), "ok");
    await bar(page).waitFor();
    assert.equal(await bar(page).locator(".what").textContent(), "Listening");
    assert.equal(await frame.evaluate(() => d.active()), true);

    await say(page, frame, 1600, 1);
    site.say = "how are you";
    await say(page, frame, 1300, 2);
    site.say = "，好的";
    await say(page, frame, 1300, 3);
    assert.deepEqual(await finals(frame), ["Hello there", " how are you", "，好的"]);
    // each final clears the guess
    const lastPartial = await frame.evaluate(() => log.filter((l) => l[0] === "partial").pop());
    assert.deepEqual(lastPartial, ["partial", ""]);
    assert.ok(site.pieces.length >= 3);
    for (const p of site.pieces) {
      assert.match(p.type, /^audio\/(webm|mp4|ogg)$/);
      assert.deepEqual([p.token, p.version, p.language], ["stok", "0", undefined]);
      assert.ok(p.bytes > 0);
    }
    assert.equal(await frame.evaluate(() => d.stop().then(() => d.active())), false);
    assert.deepEqual(await states(frame), ["starting", "listening", "finishing", "idle:stopped"]);
    await page.waitForFunction(() => !document.querySelector(".reading.listening"));
    assert.equal(await page.evaluate(() => window.__mic.stream.getTracks().every((k) => k.readyState === "ended")), true, "the microphone is let go");
    assert.equal(await frame.evaluate(() => p.state("dictation")), "granted");

    // asked once: the next start listens at once, with the language named
    await start(frame, { language: "es-MX" });
    assert.equal(await frame.evaluate(() => window.res), "ok");
    assert.equal(await ask(page).count(), 0);
    await say(page, frame, 1300, 4);
    assert.equal(site.pieces.at(-1).language, "es-MX");
    // while it listens, tts is busy and start is a no-op
    assert.equal((await frame.evaluate(() => window.ans(t.speak("hi")))).code, "busy");
    await start(frame);
    assert.equal(await frame.evaluate(() => window.res), "ok");
    await bar(page).getByRole("button", { name: "Stop" }).click();
    await frame.waitForFunction(() => !d.active());
    assert.deepEqual((await states(frame)).slice(-1), ["idle:viewer_stopped"]);
    assert.deepEqual(errors, []);
  });

  test(`Escape stops it, unless a field or the page claims it (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { grant: "granted" });
    await start(frame);
    await bar(page).waitFor();
    await frame.locator("#box").focus();
    await page.keyboard.press("Escape");
    assert.equal(await frame.evaluate(() => d.active()), true);
    await frame.evaluate(() => { window.claimed = (e) => { if (e.key === "Escape") e.preventDefault(); }; window.addEventListener("keydown", window.claimed, true); });
    await frame.locator("#go").focus();
    await page.keyboard.press("Escape");
    assert.equal(await frame.evaluate(() => d.active()), true);
    await frame.evaluate(() => window.removeEventListener("keydown", window.claimed, true));
    await page.keyboard.press("Escape");
    await frame.waitForFunction(() => !d.active());
    assert.deepEqual((await states(frame)).slice(-1), ["idle:viewer_stopped"]);
  });

  test(`Don't allow is kept, and Permissions changes it (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine);
    await start(frame);
    await ask(page).waitFor();
    await page.waitForTimeout(450);
    await ask(page).getByRole("button", { name: "Don't allow" }).click();
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "not_granted", reason: "not_granted", name: "DictationError" });
    assert.deepEqual(await frame.evaluate(() => p.request(["dictation", "db"])), { dictation: "denied", db: "unavailable" });
    await start(frame);
    assert.equal((await frame.evaluate(() => window.res)).code, "not_granted");
    assert.equal(await ask(page).count(), 0, "a no is never asked again");
    // the panel
    await frame.evaluate(() => { document.getElementById("go").onclick = () => { window.managed = p.manage().then(() => "closed"); }; });
    await frame.locator("#go").click();
    const panel = page.locator(".pop.save.perms.in");
    await panel.waitFor();
    assert.equal(await panel.locator("button.on").textContent(), "Don't allow");
    await panel.getByRole("button", { name: "Ask", exact: true }).click();
    await panel.getByRole("button", { name: "Close" }).click();
    assert.equal(await frame.evaluate(() => window.managed), "closed");
    assert.equal(await frame.evaluate(() => p.state("dictation")), "prompt");
    // closed unanswered: dismissed, and asked again on a later click
    await start(frame);
    await ask(page).waitFor();
    await page.keyboard.press("Escape");
    assert.equal((await frame.evaluate(() => window.res)).code, "dismissed");
    // request asks in the same dialog, once
    const asked = frame.evaluate(() => p.request());
    await ask(page).waitFor();
    await page.waitForTimeout(450);
    await ask(page).getByRole("button", { name: "Allow", exact: true }).click();
    assert.deepEqual(await asked, { dictation: "granted", tts: "granted" });
    await start(frame);
    assert.equal(await frame.evaluate(() => window.res), "ok");
    assert.equal(await ask(page).count(), 0);
    await frame.evaluate(() => d.stop());
    assert.equal(await page.evaluate(() => localStorage.getItem("magpie.artifact.x.dictation")), "granted");
  });

  test(`a bad call is refused, and nothing listens before a click (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine, { grant: "granted", inert: true });
    assert.equal((await frame.evaluate(() => window.ans(d.start()))).code, "no_gesture");
    await frame.locator("#box").click();
    const codes = await frame.evaluate(async () => [
      (await window.ans(d.start({ language: "spanish" }))).code,
      (await window.ans(d.start({ language: 7 }))).code,
      (await window.ans(d.start("es"))).code,
    ]);
    assert.deepEqual(codes, Array(3).fill("invalid_argument"));
    assert.equal(await page.evaluate(() => window.__mic.calls), 0);
    assert.equal(site.pieces.length, 0);
    assert.deepEqual(await states(frame), []);
  });

  test(`magpie's refusals and the microphone's reach the page (${engine})`, async (t) => {
    const { page, frame, site } = await open(t, engine, { grant: "granted" });
    site.fail = [503, "unavailable"];
    await start(frame);
    await bar(page).waitFor();
    await speakUntilIdle(page, frame);
    assert.deepEqual((await states(frame)).slice(-1), ["idle:unavailable"]);
    site.fail = [429, "rate_limited"];
    await start(frame);
    await bar(page).waitFor();
    await speakUntilIdle(page, frame);
    assert.deepEqual((await states(frame)).slice(-1), ["idle:rate_limited"]);
    await page.evaluate(() => { window.__mic.deny = true; });
    await start(frame);
    assert.deepEqual(await frame.evaluate(() => window.res), { code: "not_granted", reason: "not_granted", name: "DictationError" });
  });

  test(`a page without dictation gets null (${engine})`, async (t) => {
    // composer_only: nothing for the viewer to allow (full comments asks)
    const { frame } = await open(t, engine, { caps: { comments: { composer_only: true } } });
    assert.equal(await frame.evaluate(() => window.out.served), false);
    assert.equal(await frame.evaluate(() => p.state("dictation")), "unavailable");
    assert.equal(await frame.evaluate(() => p.manage().then(() => "opened", (e) => e.code)), "unavailable");
  });

  test(`the bar and the question fit a narrow window (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { viewport: { width: 320, height: 568 } });
    await start(frame);
    await ask(page).waitFor();
    await page.waitForTimeout(250);
    const q = await ask(page).boundingBox();
    assert.ok(q.x >= 0 && q.x + q.width <= 320 && q.y >= 0 && q.y + q.height <= 568, `the question inside the window: ${JSON.stringify(q)}`);
    await page.waitForTimeout(200);
    await ask(page).getByRole("button", { name: "Allow", exact: true }).click();
    await bar(page).waitFor();
    await page.waitForTimeout(250);
    const b = await bar(page).boundingBox(), c = await page.locator(".bar").boundingBox();
    assert.ok(b.x >= 0 && b.x + b.width <= 320 && b.y + b.height <= 568, `inside the window: ${JSON.stringify(b)}`);
    assert.ok(!c || b.x + b.width <= c.x || c.y + c.height <= b.y, `apart from the comments bar: ${JSON.stringify([b, c])}`);
    await frame.evaluate(() => d.stop());
  });

  const WORDS = {
    zh: ["允许“Dictate”听写？", "允许", "正在听", "停止"],
    "zh-TW": ["允許「Dictate」聽寫？", "允許", "正在聽", "停止"],
    ja: ["「Dictate」に音声入力を許可しますか？", "許可", "聞き取り中", "停止"],
    de: ["„Dictate“ Diktieren erlauben?", "Erlauben", "Hört zu", "Stoppen"],
  };
  for (const [lang, [question, allow, what, stop]] of Object.entries(WORDS)) {
    test(`the question and the bar speak ${lang} (${engine})`, async (t) => {
      const { page, frame } = await open(t, engine, { lang });
      await start(frame);
      await ask(page).waitFor();
      assert.equal(await ask(page).locator(".ask").textContent(), question);
      await page.waitForTimeout(450);
      await ask(page).getByRole("button", { name: allow, exact: true }).click();
      await bar(page).waitFor();
      assert.equal(await bar(page).locator(".what").textContent(), what);
      await bar(page).getByRole("button", { name: stop, exact: true }).click();
      await frame.waitForFunction(() => !d.active());
      assert.deepEqual((await states(frame)).slice(-1), ["idle:viewer_stopped"]);
    });
  }
}
