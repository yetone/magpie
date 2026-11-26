// Run with Node's test runner and Playwright on the module path; see README.md.
// The viewer shell (internal/artifact/shell.js) over a page with the
// runtime (internal/artifact/runtime.js), as a tab opening an artifact
// gets them: the shell from a top-level navigation, under its own CSP,
// framing the page, which is served under the sandbox CSP at an opaque
// origin. Comment mode turns the page's clicks into a new thread's
// composer (the page's own handlers never hear them) and anchors the
// thread where it was clicked; pins follow the page as it scrolls; a
// card replies, resolves and deletes; a comment sent to the agent shows
// its run and then the agent's answer; the page's openComposer opens the
// composer, its onSendToClaude handles a send first, and its
// sendToClaude fills the composer and waits for the viewer, who sends it
// or doesn't (consent_required). A frame inside the page is not heard.
// In both engines, at a narrow width, in every language. The store is
// faked here; internal/gateway's TestArtifactComment* hold the real one.
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
// the shell's CSP as shell.go writes it
const shellCSPSource = /func ShellCSP\(nonce string\) string \{\n\treturn (".*")\n\}/.exec(fs.readFileSync(path.join(dir, "shell.go"), "utf8"));
assert.ok(shellCSPSource, "ShellCSP in shell.go");
function shellCSP(nonce) {
  return shellCSPSource[1].split(/" \+ nonce \+ "/).map((s) => s.replace(/^"|"$/g, "")).join(nonce);
}
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&#34;");
// ShellPage's markup
function shellPage(cfg) {
  const nonce = crypto.randomBytes(18).toString("base64url");
  const json = JSON.stringify(cfg).replace(/</g, "\\u003c").replace(/>/g, "\\u003e").replace(/&/g, "\\u0026");
  const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><meta name="referrer" content="no-referrer"><title>${esc(cfg.title)}</title><style nonce="${nonce}">${shellCSS}</style></head><body><iframe id="page" title="${esc(cfg.title)}" allow="clipboard-write; fullscreen"></iframe><div id="ui"></div><script nonce="${nonce}">${shellJS}(${json});</script></body></html>`;
  return { html, nonce };
}
function served(page, cfg) {
  const tag = "<script>" + runtime + "(" + JSON.stringify(cfg).replace(/</g, "\\u003c") + ");</script>";
  return page.replace(/<head>/i, "<head>" + tag);
}

const PAGE = `<!doctype html><html><head><title>Plan</title><style>
body{margin:0;font:16px/1.4 sans-serif}section{height:700px;padding:24px}#nope{height:300px}
</style></head><body>
<h1 id=top>Plan</h1>
<section id=s1><p class=lead>First part</p><button id=own>Own</button></section>
<section id=s2 data-comment-target><h2>Second</h2><p>Body two</p></section>
<div id=nope data-uncommentable>No comments here</div>
<script>
window.out = { clicks: 0, asked: [] };
document.getElementById("own").addEventListener("click", () => out.clicks++);
(async () => {
  const c = await claude.use("comments");
  out.served = !!c;
  out.frozen = Object.isFrozen(c);
  out.verbs = c ? Object.keys(c).sort() : [];
  window.cm = c;
  c.onSendToClaude(async (req) => {
    out.asked.push(req.text);
    return req.text.startsWith("page:") ? { handled: true, reply: "Handled here" } : { handled: false };
  });
  out.ready = true;
})().catch((e) => { out.crash = String(e && e.stack || e); });
</script></body></html>`;

// a fake magpie: the shell, the page, the comments API and the shell's stream
async function fakeMagpie(t, lang) {
  const threads = [], calls = [], streams = new Set(), dests = [];
  // slow.ms holds each list's answer back, as a busy magpie does
  const slow = { ms: 0 };
  let n = 0;
  const now = () => new Date().toISOString();
  const find = (id) => threads.find((th) => th.id === id);
  function tell() { for (const s of streams) s.write('data: {"comments":true}\n\n'); }
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    const err = (code, status) => { res.writeHead(status || 403, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" }); res.end(JSON.stringify({ error: { code, message: code } })); };
    if (req.method === "GET" && url.pathname === "/a/x/") {
      dests.push(req.headers["sec-fetch-dest"] || "");
      if (req.headers["sec-fetch-dest"] === "document") {
        const origin = "http://" + req.headers.host;
        const { html, nonce } = shellPage({ api: "/a/x/_api", token: "stok", id: "x", title: "Plan", src: "/a/x/" + url.search, version: 1, origin, lang });
        res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": shellCSP(nonce), "Cache-Control": "no-store", "Vary": "Sec-Fetch-Mode, Sec-Fetch-Dest" });
        return res.end(html);
      }
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Content-Security-Policy": PAGE_CSP, "Vary": "Sec-Fetch-Mode, Sec-Fetch-Dest" });
      return res.end(served(PAGE, { api: "/a/x/_api", token: "ptok", caps: { comments: {} }, viewer: { id: "u_0123456789abcdef01234567" }, version: 1, home: "/a/x/", origin: "http://" + req.headers.host }));
    }
    if (req.method === "GET" && url.pathname === "/a/x/_api/shell") {
      if (url.searchParams.get("t") !== "stok") return err("not_granted");
      res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store" });
      res.write('retry: 2000\n\ndata: {"comments":true}\n\n');
      streams.add(res);
      req.on("close", () => streams.delete(res));
      return;
    }
    if (req.method === "POST" && url.pathname === "/a/x/_api/comments") {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        const c = JSON.parse(body);
        const shell = c.t === "stok", pg = c.t === "ptok";
        calls.push({ c, by: shell ? "shell" : pg ? "page" : "?", origin: req.headers.origin, type: req.headers["content-type"] });
        if (!shell && !pg) return err("not_granted");
        // as magpie: the page's token asks "can"; its writes go through the shell
        if (pg && ["create", "reply", "resolve", "delete"].includes(c.op)) return err("consent_required");
        if (pg && c.op !== "can") return err("not_granted");
        const ok = (v) => { res.writeHead(200, { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*" }); res.end(JSON.stringify(v || {})); };
        const author = "u_0123456789abcdef01234567";
        const agent = { id: "claude", name: "Claude Code", can: "available" };
        const comment = (text, sent) => ({ id: "c" + ++n, author, text, at: now(), sent: !!sent });
        const th = find(c.thread);
        switch (c.op) {
          case "list": {
            const v = { threads, version: 1, agent, me: author };
            return slow.ms ? setTimeout(() => ok(v), slow.ms) : ok(v);
          }
          case "can": return ok({ can: "available" });
          case "create": {
            const t2 = { id: "t" + ++n, anchor: c.anchor || undefined, label: c.label, quote: c.quote, version: c.version, author, created: now(), comments: [comment(c.text, c.send)] };
            if (c.send) t2.run = { agent: "claude", state: "working", at: now() };
            threads.push(t2);
            tell();
            return ok({ threadId: t2.id, commentId: t2.comments[0].id });
          }
          case "reply": {
            if (!th) return err("not_found", 404);
            const cm = comment(c.text, c.send);
            th.comments.push(cm);
            th.resolved = false;
            if (c.send) th.run = { agent: "claude", state: "working", at: now() };
            tell();
            return ok({ commentId: cm.id });
          }
          case "resolve": if (!th) return err("not_found", 404); th.resolved = !!c.resolved; tell(); return ok();
          case "stop": if (!th) return err("not_found", 404); delete th.run; tell(); return ok();
          case "delete":
            if (!th) return err("not_found", 404);
            if (c.comment) th.comments = th.comments.filter((x) => x.id !== c.comment);
            if (!c.comment || !th.comments.length) threads.splice(threads.indexOf(th), 1);
            tell();
            return ok();
        }
        err("invalid", 400);
      });
      return;
    }
    res.writeHead(404);
    res.end();
  });
  t.after(() => { for (const s of streams) s.destroy(); server.close(); });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return {
    base: `http://127.0.0.1:${server.address().port}`,
    threads, calls, dests, slow,
    // the agent's answer, as artifacts_send posts it
    finish(id, text) {
      const th = find(id);
      th.comments.push({ id: "c" + ++n, author: "agent:claude", agent: "claude", text, at: now() });
      th.run = { agent: "claude", state: "done", at: now() };
      tell();
    },
  };
}

const launch = (engine) => (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
async function open(t, engine, opts) {
  const site = await fakeMagpie(t, opts && opts.lang || "en");
  const browser = await launch(engine);
  t.after(() => browser.close());
  const page = await browser.newPage({ viewport: opts && opts.viewport || { width: 1200, height: 800 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(site.base + "/a/x/" + (opts && opts.at || ""));
  const frame = await (await page.waitForSelector("#page")).contentFrame();
  await frame.waitForFunction(() => window.out && (window.out.ready || window.out.crash), null, { timeout: 15000 });
  await page.waitForFunction(() => document.querySelector(".bar"), null, { timeout: 10000 });
  return { site, page, frame, errors };
}
const pop = (page) => page.locator(".pop.in").last();
const asked = (page) => page.locator(".pop.save.in[role=alertdialog]");
// what the shell wrote for the page (the viewer's own posts have send set)
const writes = (site) => site.calls.filter((c) => c.by === "shell" && c.c.send === undefined && ["create", "reply", "resolve", "delete"].includes(c.c.op));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`the viewer comments on the page in comment mode (${engine})`, async (t) => {
    const { site, page, frame, errors } = await open(t, engine, { at: "?tab=2#s2" });
    assert.deepEqual(site.dests, ["document", "iframe"], "the tab got the shell and its frame the page");
    assert.equal(await page.evaluate(() => document.getElementById("page").getAttribute("src")), "/a/x/?tab=2#s2", "the frame keeps the query and the fragment");
    assert.equal(await frame.evaluate(() => origin), "null", "the page runs at an opaque origin");
    const out = await frame.evaluate(() => window.out);
    assert.equal(out.crash, undefined);
    assert.equal(out.served, true);
    assert.equal(out.frozen, true);
    assert.deepEqual(out.verbs, ["anchorFor", "canSendToClaude", "create", "delete", "onSendToClaude", "openComposer", "reply", "resolve", "sendToClaude"]);
    await frame.evaluate(() => scrollTo(0, 0));

    // comment mode: a click on the page's button opens the composer at it,
    // and the button's own handler never hears it
    const mode = page.getByRole("button", { name: "Add comments" });
    await mode.click();
    assert.equal(await mode.getAttribute("aria-pressed"), "true");
    await frame.locator("#own").click();
    assert.equal(await frame.evaluate(() => window.out.clicks), 0, "the page's handler didn't run");
    await pop(page).waitFor();
    assert.equal(await pop(page).locator(".where").textContent(), "Own");
    // a part marked data-uncommentable takes no comment
    await page.keyboard.press("Escape");
    await page.waitForFunction(() => !document.querySelector(".pop.in"));
    await frame.locator("#nope").click();
    await page.waitForTimeout(300);
    assert.equal(await page.locator(".pop.in").count(), 0, "no composer on an uncommentable part");
    await frame.locator("#own").click();
    await pop(page).locator("textarea").fill("Bigger");
    await pop(page).getByRole("button", { name: "Comment", exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll(".pin").length === 1);
    const made = site.calls.find((c) => c.c.op === "create");
    assert.equal(made.by, "shell");
    assert.equal(made.c.anchor.path, "#own");
    assert.ok(made.c.anchor.x >= 0 && made.c.anchor.x <= 1 && made.c.anchor.y >= 0 && made.c.anchor.y <= 1);
    assert.equal(made.c.label, "Own");
    assert.equal(made.c.send, false);
    assert.equal(made.origin, site.base, "the shell calls from magpie's own origin");
    assert.equal(await page.locator(".bar .count").textContent(), "1");

    // a button at the frame's top edge keeps its pin, dropped to fit inside
    await frame.evaluate(() => scrollBy(0, document.getElementById("own").getBoundingClientRect().top - 2));
    await page.waitForFunction(() => { const p = document.querySelector(".pin"); return p.style.display !== "none" && p.getBoundingClientRect().top >= 0 && p.getBoundingClientRect().top < 10; });

    // the pin sits at the button and follows the page as it scrolls
    await frame.evaluate(() => scrollTo(0, 0));
    await page.waitForFunction(() => document.querySelector(".pin").getBoundingClientRect().top > 60);
    const ownBox = await frame.locator("#own").boundingBox();
    const at = async () => (await page.locator(".pin").boundingBox());
    let pin = await at();
    assert.ok(Math.abs(pin.y - (ownBox.y + ownBox.height * made.c.anchor.y) + 26 - 4) < 30, `the pin is by the button: ${pin.y} vs ${ownBox.y}`);
    await frame.evaluate(() => scrollTo(0, 40));
    await page.waitForFunction((y) => Math.abs(document.querySelector(".pin").getBoundingClientRect().top - (y - 40)) < 2, pin.y);
    await frame.evaluate(() => scrollTo(0, 0));
    await page.waitForFunction((y) => Math.abs(document.querySelector(".pin").getBoundingClientRect().top - y) < 2, pin.y);

    // Escape leaves comment mode, and the page's clicks are its own again
    await page.keyboard.press("Escape");
    // an Escape in the frame reaches the shell by postMessage
    await page.waitForFunction(() => document.querySelector('button[aria-label="Add comments"]').getAttribute("aria-pressed") === "false");
    // (its corner: the pin's tip marks the middle, where the comment was made)
    await frame.locator("#own").click({ position: { x: 3, y: 3 } });
    assert.equal(await frame.evaluate(() => window.out.clicks), 1);

    // the card: reply, resolve (the pin goes), then from the list, delete
    await page.locator(".pin").click();
    await pop(page).waitFor();
    assert.match(await pop(page).locator(".msgs").textContent(), /Bigger/);
    await pop(page).locator("textarea").fill("And bolder");
    await pop(page).getByRole("button", { name: "Reply", exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll(".pop.in .msg").length === 2);
    await pop(page).getByRole("button", { name: "Resolve" }).click();
    await page.waitForFunction(() => !document.querySelector(".pin") && !document.querySelector(".pop.in"));
    assert.equal(site.threads[0].resolved, true);
    assert.equal(await page.locator(".bar .count").isVisible(), false);
    await page.getByRole("button", { name: "Comments", exact: true }).click();
    const drawer = page.locator(".drawer");
    await drawer.waitFor();
    assert.equal(await drawer.locator(".empty").textContent(), "No comments yet");
    await drawer.getByRole("button", { name: "Resolved 1" }).click();
    await page.waitForFunction(() => document.querySelectorAll(".pin.resolved").length === 1, null, { timeout: 5000 });
    await drawer.locator(".item").click();
    await pop(page).waitFor();
    const del = pop(page).getByRole("button", { name: "Delete" }).first();
    await del.click();
    assert.equal(await pop(page).locator(".icon.danger").textContent(), "Delete?");
    await pop(page).locator(".icon.danger").click();
    await page.waitForFunction(() => !document.querySelector(".pop.in") && !document.querySelector(".drawer .item"));
    assert.equal(site.threads.length, 0);
    assert.deepEqual(errors, []);
  });

  test(`a comment goes to the agent, and the page takes part (${engine})`, async (t) => {
    const { site, page, frame, errors } = await open(t, engine);
    const send = () => pop(page).getByRole("button", { name: "Send to Claude Code" });

    // the page's openComposer: the composer at the element, no mode needed
    assert.deepEqual(await frame.evaluate(() => window.cm.openComposer({ element: document.querySelector("#s2 p") })), { opened: true });
    assert.equal(await pop(page).locator(".where").textContent(), "Second Body two", "data-comment-target took the click to its section");
    // the paragraph was below the fold: like a click, it came into view, and the composer points at it
    const para = await frame.evaluate(() => { const r = document.querySelector("#s2 p").getBoundingClientRect(); return { top: r.top, bottom: r.bottom, h: innerHeight }; });
    assert.ok(para.top >= 0 && para.bottom <= para.h + 1, `in view: ${JSON.stringify(para)}`);
    await pop(page).locator("textarea").fill("Make it blue");
    await send().click();
    await page.waitForFunction(() => document.querySelector(".pin.working"));
    const made = site.calls.filter((c) => c.c.op === "create").at(-1);
    assert.equal(made.by, "shell");
    assert.equal(made.c.send, true);
    assert.equal(made.c.anchor.path, "#s2");
    assert.deepEqual(await frame.evaluate(() => window.out.asked), ["Make it blue"], "the page's onSendToClaude was asked first");
    await page.locator(".pin").click();
    await pop(page).locator(".run").waitFor();
    assert.match(await pop(page).locator(".run").textContent(), /Claude Code is working…/);
    site.finish(site.threads[0].id, "Made it blue.");
    await page.waitForFunction(() => document.querySelectorAll(".pop.in .msg").length === 2);
    assert.match(await pop(page).locator(".msg").last().textContent(), /^Claude Code.*Made it blue\./);
    assert.equal(await pop(page).locator(".run").count(), 0);
    assert.equal(await page.locator(".pin.working").count(), 0);
    await page.keyboard.press("Escape");

    // the page's handler takes a send: a plain comment, and its line
    await page.getByRole("button", { name: "Comments", exact: true }).click();
    const dc = page.locator(".drawer .compose");
    await dc.locator("textarea").fill("page: tidy up");
    await dc.getByRole("button", { name: "Send to Claude Code" }).click();
    await page.getByText("Handled here").waitFor();
    const handled = site.calls.filter((c) => c.c.op === "create").at(-1);
    assert.equal(handled.c.send, false);
    assert.equal(handled.c.anchor, null);
    await page.getByRole("button", { name: "Close" }).first().click();

    // the page's sendToClaude fills the composer; nothing goes until the
    // viewer sends it
    const creates = () => site.calls.filter((c) => c.c.op === "create").length;
    const before = creates();
    await frame.evaluate(() => {
      window.sent = window.cm.anchorFor(document.getElementById("top")).then((anchor) => window.cm.sendToClaude({ anchor, text: "From the page" })).then((r) => r, (e) => ({ code: e.code }));
    });
    await pop(page).waitFor();
    assert.equal(await pop(page).locator("textarea").inputValue(), "From the page");
    await page.waitForTimeout(300);
    assert.equal(creates(), before, "nothing was posted before the viewer pressed Send");
    await send().click();
    const r = await frame.evaluate(() => window.sent);
    assert.match(r.threadId, /^t/);
    assert.equal(site.calls.filter((c) => c.c.op === "create").at(-1).c.send, true);
    assert.equal(site.calls.filter((c) => c.c.op === "create").at(-1).by, "shell");

    // dismissed, or posted as a plain comment: consent_required
    await frame.evaluate(() => { window.sent = window.cm.sendToClaude({ threadId: "nope", text: "x" }).then((r) => r, (e) => ({ code: e.code })); });
    assert.deepEqual(await frame.evaluate(() => window.sent), { code: "not_found" });
    await frame.evaluate(() => {
      window.sent = window.cm.anchorFor(document.getElementById("top")).then((anchor) => window.cm.sendToClaude({ anchor, text: "Dismiss me" })).then((r) => r, (e) => ({ code: e.code }));
    });
    await pop(page).waitFor();
    await pop(page).getByRole("button", { name: "Close" }).click();
    assert.deepEqual(await frame.evaluate(() => window.sent), { code: "consent_required" });
    const n2 = creates();
    await frame.evaluate(() => {
      window.sent = window.cm.anchorFor(document.getElementById("top")).then((anchor) => window.cm.sendToClaude({ anchor, text: "Just a note" })).then((r) => r, (e) => ({ code: e.code }));
    });
    await pop(page).waitFor();
    await pop(page).getByRole("button", { name: "Comment", exact: true }).click();
    assert.deepEqual(await frame.evaluate(() => window.sent), { code: "consent_required" });
    assert.equal(creates(), n2 + 1);
    assert.equal(site.calls.filter((c) => c.c.op === "create").at(-1).c.send, false);

    // the page's own writes: the viewer is asked once, and they go with
    // the shell's token, never to the agent; the page's token asks "can"
    await page.waitForFunction(() => !document.querySelector(".pop.in"));
    await frame.evaluate(() => {
      window.own = (async () => {
        const anchor = await window.cm.anchorFor(document.getElementById("own"));
        const t1 = await window.cm.create({ anchor, text: "By the page" });
        await window.cm.reply(t1.threadId, "Again");
        return { t1, can: await window.cm.canSendToClaude(), bad: await window.cm.create({ anchor, text: "" }).catch((e) => e.code) };
      })();
    });
    await asked(page).waitFor();
    assert.equal(await asked(page).locator(".ask").textContent(), "Let “Plan” comment as you?");
    assert.equal(writes(site).length, 0, "nothing written before the viewer allowed it");
    await page.waitForTimeout(450);
    await asked(page).getByRole("button", { name: "Allow", exact: true }).click();
    const own = await frame.evaluate(() => window.own);
    assert.match(own.t1.threadId, /^t/);
    assert.equal(own.can, "available");
    assert.equal(own.bad, "invalid");
    assert.equal(await asked(page).count(), 0, "asked once");
    assert.deepEqual(writes(site).map((c) => [c.c.op, c.c.send, c.c.text]), [["create", undefined, "By the page"], ["reply", undefined, "Again"]]);
    const pageCalls = site.calls.filter((c) => c.by === "page");
    assert.deepEqual(pageCalls.map((c) => c.c.op), ["can"]);
    for (const c of pageCalls) {
      assert.equal(c.origin, "null");
      assert.match(c.type, /^text\/plain/);
    }
    // a frame inside the page is not the page: its word is not heard
    await frame.evaluate(() => {
      const f = document.createElement("iframe");
      f.srcdoc = `<script>parent.parent.postMessage({magpie: "artifact-shell", type: "compose", req: 9, anchor: {path: "#top", x: 0.5, y: 0.5}, at: {x: 10, y: 10}, label: "Inner"}, "*")<\/script>`;
      document.body.append(f);
    });
    await page.waitForTimeout(500);
    assert.equal(await page.locator(".pop.in").count(), 0, "a nested frame opened no composer");
    assert.deepEqual(errors, []);
  });

  test(`the page's next sendToClaude opens at once, while the list is still loading (${engine})`, async (t) => {
    const { site, page, frame, errors } = await open(t, engine);
    const send = () => pop(page).getByRole("button", { name: "Send to Claude Code" });
    const ask = (text, threadId) => frame.evaluate(([text, threadId]) => {
      window.sent = (threadId ? Promise.resolve({ threadId }) : window.cm.anchorFor(document.getElementById("top")).then((anchor) => ({ anchor })))
        .then((o) => window.cm.sendToClaude(Object.assign(o, { text }))).then((r) => r, (e) => ({ code: e.code }));
    }, [text, threadId]);
    // a composer holding text, open now
    const holds = (text, timeout) => page.waitForFunction((text) => [...document.querySelectorAll(".pop.in textarea")].some((ta) => ta.value === text), text, { timeout: timeout || 1500 });
    site.slow.ms = 4000;

    await ask("First");
    await holds("First");
    await send().click();
    const first = await frame.evaluate(() => window.sent);
    assert.match(first.threadId, /^t/);
    // sent, its list not back yet: the next one is asked, not refused
    await ask("Second");
    await holds("Second");
    await pop(page).getByRole("button", { name: "Close" }).click();
    assert.deepEqual(await frame.evaluate(() => window.sent), { code: "consent_required" });

    // a reply in the thread the shell doesn't know yet: it waits for the
    // list, then asks; then a new one, the reply's list not back
    await ask("Again", first.threadId);
    await holds("Again", 10000);
    await send().click();
    assert.ok((await frame.evaluate(() => window.sent)).commentId);
    await ask("Third");
    await holds("Third");
    await pop(page).getByRole("button", { name: "Close" }).click();
    assert.deepEqual(await frame.evaluate(() => window.sent), { code: "consent_required" });
    site.slow.ms = 0;
    assert.deepEqual(errors, []);
  });

  test(`a page's comment the viewer doesn't allow is not written, and Permissions changes it (${engine})`, async (t) => {
    const { site, page, frame, errors } = await open(t, engine);
    const create = () => frame.evaluate(() => {
      window.r = undefined;
      window.cm.anchorFor(document.getElementById("own")).then((anchor) => window.cm.create({ anchor, text: "Mine" })).then((r) => { window.r = { ok: !!r.threadId }; }, (e) => { window.r = { code: e.code, name: e.name }; });
    });
    const result = async () => (await frame.waitForFunction(() => window.r)).jsonValue();
    await create();
    await asked(page).waitFor();
    await page.waitForTimeout(450);
    await asked(page).getByRole("button", { name: "Don't allow", exact: true }).click();
    assert.deepEqual(await result(), { code: "consent_required", name: "CommentsError" });
    assert.equal(await frame.evaluate(() => claude.use("permissions").then((p) => p.state("comments"))), "denied");
    await create();
    assert.deepEqual(await result(), { code: "consent_required", name: "CommentsError" });
    assert.equal(await asked(page).count(), 0, "a no is never asked again");
    assert.deepEqual(writes(site), []);
    // the panel has a Comments row
    await frame.evaluate(() => {
      const b = document.createElement("button");
      b.id = "go";
      b.textContent = "Permissions";
      b.onclick = () => { window.managed = claude.use("permissions").then((p) => p.manage()).then(() => "closed"); };
      document.body.prepend(b);
    });
    await frame.locator("#go").click();
    const panel = page.locator(".pop.save.perms.in");
    await panel.waitFor();
    assert.deepEqual(await panel.locator(".perm > span").allTextContents(), ["Comments"]);
    assert.equal(await panel.locator(".perm button.on").textContent(), "Don't allow");
    await panel.getByRole("button", { name: "Allow", exact: true }).click();
    await panel.getByRole("button", { name: "Close" }).click();
    assert.equal(await frame.evaluate(() => window.managed), "closed");
    assert.equal(await frame.evaluate(() => claude.use("permissions").then((p) => p.state("comments"))), "granted");
    await create();
    assert.deepEqual(await result(), { ok: true });
    assert.equal(await asked(page).count(), 0);
    assert.deepEqual(writes(site).map((c) => c.c.text), ["Mine"]);
    assert.deepEqual(errors, []);
  });

  test(`Escape in the viewer closes the question unanswered (${engine})`, async (t) => {
    const { site, page, frame } = await open(t, engine);
    await frame.evaluate(() => {
      window.r = undefined;
      window.cm.anchorFor(document.getElementById("own")).then((anchor) => window.cm.create({ anchor, text: "Mine" })).then(() => { window.r = "ok"; }, (e) => { window.r = e.code; });
    });
    await asked(page).waitFor();
    await page.locator(".bar").first().focus().catch(() => {});
    await page.evaluate(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    assert.equal(await (await frame.waitForFunction(() => window.r)).jsonValue(), "consent_required");
    assert.equal(await asked(page).count(), 0);
    assert.equal(await frame.evaluate(() => claude.use("permissions").then((p) => p.state("comments"))), "prompt", "unanswered is not a no");
    assert.deepEqual(writes(site), []);
  });

  test(`the composer fits a narrow window (${engine})`, async (t) => {
    const { page, frame } = await open(t, engine, { viewport: { width: 360, height: 640 } });
    await page.getByRole("button", { name: "Add comments" }).click();
    await frame.locator("#own").click();
    await pop(page).waitFor();
    const b = await pop(page).boundingBox();
    assert.ok(b.x >= 0 && b.x + b.width <= 360 && b.y >= 0 && b.y + b.height <= 640, `inside the window: ${JSON.stringify(b)}`);
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Comments", exact: true }).click();
    const d = await page.locator(".drawer").boundingBox();
    assert.ok(d.width <= 360, "the list fits");
  });

  const WORDS = { zh: ["添加评论", "还没有评论"], "zh-TW": ["新增留言", "還沒有留言"], ja: ["コメントを追加", "コメントはまだありません"], de: ["Kommentare hinzufügen", "Noch keine Kommentare"] };
  for (const [lang, [add, none]] of Object.entries(WORDS)) {
    test(`the shell speaks ${lang} (${engine})`, async (t) => {
      const { page } = await open(t, engine, { lang });
      await page.getByRole("button", { name: add }).waitFor();
      await page.locator(".bar button").nth(1).click();
      assert.equal(await page.locator(".drawer .empty").textContent(), none);
      assert.ok(await page.evaluate(() => document.documentElement.lang !== "en"));
    });
  }
}
