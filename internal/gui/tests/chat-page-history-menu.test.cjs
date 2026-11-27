// Run with Node's test runner and Playwright on the module path; see README.md.
// The gateway's chat page (DeepSeek's chat-demo release, served by
// internal/gateway/chatpage.go with magpie.js): a past conversation's "…"
// menu stays beside its "…" while the pointer goes to Delete, and Delete
// asks in the middle of the window (Zaneyunlive on X: the menu went to the
// top-left corner). The page is served here as the gateway serves it: the
// release's files from the embedded zip, its API origin replaced and
// magpie.js loaded before its script.
const assert = require("node:assert/strict");
const fs = require("node:fs");
const fsp = require("node:fs/promises");
const path = require("node:path");
const zlib = require("node:zlib");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const dir = path.join(__dirname, "..", "..", "gateway", "chatpage");
const MAIN = "static/js/main.9820cd3a.js";

// unzip is the zip's files by name (stored or deflated entries).
function unzip(buf) {
  let end = buf.length - 22;
  while (end >= 0 && buf.readUInt32LE(end) !== 0x06054b50) end--;
  assert(end >= 0, "the chat page's zip has no end record");
  const count = buf.readUInt16LE(end + 10);
  let at = buf.readUInt32LE(end + 16);
  const files = new Map();
  for (let i = 0; i < count; i++) {
    assert.equal(buf.readUInt32LE(at), 0x02014b50);
    const method = buf.readUInt16LE(at + 10), size = buf.readUInt32LE(at + 20);
    const nameLen = buf.readUInt16LE(at + 28), extraLen = buf.readUInt16LE(at + 30), commentLen = buf.readUInt16LE(at + 32);
    const local = buf.readUInt32LE(at + 42);
    const name = buf.toString("utf8", at + 46, at + 46 + nameLen);
    at += 46 + nameLen + extraLen + commentLen;
    if (name.endsWith("/")) continue;
    const start = local + 30 + buf.readUInt16LE(local + 26) + buf.readUInt16LE(local + 28);
    const data = buf.subarray(start, start + size);
    files.set(path.posix.normalize(name), method === 8 ? zlib.inflateRawSync(data) : Buffer.from(data));
  }
  return files;
}

// chatPageFiles as internal/gateway/chatpage.go makes them.
function pageFiles() {
  const zip = fs.readdirSync(dir).find((n) => /^chat-demo-v.*\.zip$/.test(n));
  const files = unzip(fs.readFileSync(path.join(dir, zip)));
  const main = files.get(MAIN).toString("utf8");
  const origin = `let m="https://api.deepseek.com"`;
  assert.equal(main.split(origin).length, 2, "the page's script names its API origin once");
  files.set(MAIN, Buffer.from(main.replace(origin, `let m=location.origin+"/deepseek-chat"`)));
  const index = files.get("index.html").toString("utf8");
  const tag = `<script defer src="${MAIN}`;
  assert(index.includes(tag));
  files.set("index.html", Buffer.from(index.replace(tag, `<script defer src="magpie.js"></script>${tag}`)));
  files.set("magpie.js", fs.readFileSync(path.join(dir, "magpie.js")));
  return files;
}

const TYPES = { ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
  ".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf", ".svg": "image/svg+xml", ".png": "image/png" };

function reply(text) {
  const ev = (type, data) => `event: ${type}\ndata: ${JSON.stringify({ type, ...data })}\n\n`;
  return ev("message_start", { message: { id: "msg_1", type: "message", role: "assistant", model: "deep/deepseek-flash", content: [], stop_reason: null, usage: { input_tokens: 3, output_tokens: 0 } } }) +
    ev("content_block_start", { index: 0, content_block: { type: "text", text: "" } }) +
    ev("content_block_delta", { index: 0, delta: { type: "text_delta", text } }) +
    ev("content_block_stop", { index: 0 }) +
    ev("message_delta", { delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 2 } }) +
    ev("message_stop", {});
}

const files = pageFiles();
const ORIGIN = "http://127.0.0.1:3999";

async function serve(route) {
  const req = route.request(), url = new URL(req.url());
  if (url.origin !== ORIGIN) return route.abort();
  const rest = url.pathname.replace(/^\/deepseek-chat\//, "");
  if (rest === "models") {
    return route.fulfill({ json: { object: "list", data: [{ id: "deep/deepseek-flash", object: "model", display_name: "DeepSeek Flash" }], default: "deep/deepseek-flash" } });
  }
  if (rest === "anthropic/v1/messages" && req.method() === "POST") {
    return route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: reply("Hello there.") });
  }
  const name = rest === "" ? "index.html" : rest;
  if (req.method() === "GET" && files.has(name)) {
    return route.fulfill({ status: 200, headers: { "Content-Type": TYPES[path.extname(name)] || "application/octet-stream" }, body: files.get(name) });
  }
  return route.fulfill({ status: 404, json: { type: "error", error: { type: "not_found_error", message: "not found" } } });
}

const box = (loc) => loc.evaluate((e) => { const r = e.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height }; });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const locale of ["en-US", "zh-CN"]) {
    test(`${engine} ${locale}: a past conversation's menu stays by its "…" and Delete asks in the middle`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 760 }, locale, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fsp.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${locale}-chat-page-history-menu.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve);
      await page.goto(`${ORIGIN}/deepseek-chat/`);

      // a conversation, then a new one, so the first is a past one: its
      // "…" shows only on hover
      const input = page.locator("textarea").first();
      await input.waitFor();
      await input.fill("Say hello");
      await input.press("Enter");
      await page.getByText("Hello there.").waitFor();
      await page.locator(".newChat-ac14ac").click();
      const row = page.locator(".item-ff6fc9").first();
      await row.waitFor();
      await page.waitForFunction(() => !document.querySelector(".item-ff6fc9.active-ff6fc9"));
      assert.equal(await row.locator(".action-ff6fc9").evaluate((e) => getComputedStyle(e).display), "none", "a past conversation's … waits for hover");

      await row.hover();
      const more = row.locator(".action-ff6fc9 button");
      await more.waitFor();
      const at = await box(more);
      await more.click();
      const menu = page.locator(".menu-_5f384");
      await menu.waitFor();
      const opened = await box(menu);
      assert(Math.abs(opened.y - (at.y + at.h + 4)) < 2 && Math.abs(opened.x - (at.x - 5)) < 2, `the menu opens under its …: ${JSON.stringify({ at, opened })}`);

      // the pointer goes from the … to Delete, off the row on its way
      const del = menu.locator('[role="menuitem"]').last();
      const target = await box(del);
      await page.mouse.move(target.x + target.w / 2, target.y + target.h / 2, { steps: 12 });
      // a ResizeObserver moved it, before the next frame
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
      const moved = await box(menu);
      assert.deepEqual(moved, opened, "the menu stays where it opened, not in the top-left corner");
      assert.equal(await row.locator(".action-ff6fc9").evaluate((e) => getComputedStyle(e).display), "flex", "its … stays shown while the menu is open");

      await del.click();
      const dialog = page.locator('[role="dialog"]');
      await dialog.waitFor();
      const d = await box(dialog);
      assert(Math.abs(d.x + d.w / 2 - 550) < 2 && Math.abs(d.y + d.h / 2 - 380) < 2, `Delete asks in the middle: ${JSON.stringify(d)}`);
      assert.equal(await menu.count(), 0, "the menu closed");
      assert.equal(await page.locator("[data-magpie-menu]").count(), 0, "the row lets go of its … once the menu closes");

      await dialog.locator("button").last().click();
      await page.waitForFunction(() => !document.querySelector(".item-ff6fc9"));
      assert.deepEqual(errors, []);
    });
  }
}
