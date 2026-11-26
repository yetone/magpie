// Run with Node's test runner and Playwright on the module path; see README.md.
// A tooltip drawn again and again in one box, whose text may wrap (the
// Usage chart's speed tooltip, Settings' hint card), is as wide as what it
// says wherever it last stood. Each is measured, then put beside what it
// tells of; read at the edge where the last one left it, it was squeezed
// into the room left there and wrapped (lwjef on X: the speed chart's names
// wrapped when the pointer came onto the latest column from the right).
// Tooltips whose lines never wrap (the chart's other ones, the context
// map's) can't be squeezed below them. Both engines, with the real
// stylesheets; no backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets");

// the page's own stylesheets, and nothing that draws
async function page(browser) {
  const ctx = await browser.newContext({ viewport: { width: 1100, height: 800 } });
  const p = await ctx.newPage();
  const html = await fs.readFile(path.join(assets, "index.html"), "utf8");
  const sheets = [...html.matchAll(/<link rel="stylesheet" href="([^"]+)">/g)].map((m) => m[1]);
  assert(sheets.includes("app.css"), "index.html's stylesheets: " + sheets);
  await p.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/") return route.fulfill({ contentType: "text/html", body: `<!doctype html><html><head>${sheets.map((s) => `<link rel="stylesheet" href="${s}">`).join("")}</head><body></body></html>` });
    const file = path.join(assets, url.pathname);
    try { await route.fulfill({ body: await fs.readFile(file), contentType: url.pathname.endsWith(".css") ? "text/css" : "application/octet-stream" }); } catch { await route.fulfill({ status: 404, body: "" }); }
  });
  await p.goto("http://magpie.test/");
  return { p, ctx };
}

const CASES = {
  // the Usage chart's speed tooltip: names whole, wrapped only past the chart
  "usage chart, speed": () => {
    const box = document.createElement("div");
    box.className = "led-chart";
    box.innerHTML = `<div class="plot" style="width:800px"><div class="tip speed"><b>2026/11/26 16:00</b>
      <div><i></i><span>claude-sonnet-5-20260929-thinking</span><em>50.0 tok/s</em><em class="ttft">TTFT 1.83 s</em></div>
      <div><i></i><span>gpt-6.1-sol-codex-max-high</span><em>10.0 tok/s</em><em class="ttft">TTFT 640 ms</em></div>
      <div class="sum"><span>All · 67 requests</span><em>36.1 tok/s</em><em class="ttft">TTFT 1.36 s</em></div></div></div>`;
    document.body.append(box);
    return box.querySelector(".tip");
  },
  // Settings' and the editor's hint card
  "settings hint": () => {
    const card = document.createElement("div");
    card.className = "set-hint on";
    card.innerHTML = "<p>magpie opens Claude Code as a person does, in a terminal of its own that you don't see, and reads the reply from its transcript.</p>";
    document.body.append(card);
    return card;
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a tooltip is as wide wherever it last stood", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const { p, ctx } = await page(browser);
    for (const [name, make] of Object.entries(CASES)) {
      await t.test(name, async () => {
        const got = await p.evaluate(`(() => {
          const tip = (${make})();
          tip.hidden = false;
          tip.style.top = "0px";
          const room = tip.offsetParent ? tip.offsetParent.clientWidth : innerWidth;
          tip.style.left = "0px";
          const free = tip.getBoundingClientRect();
          // where the last one was put: near the far edge
          tip.style.left = room - 120 + "px";
          const edge = tip.getBoundingClientRect();
          (tip.closest(".led-chart") || tip.parentElement === document.body && tip || tip.parentElement).remove();
          return { room, free: [free.width, free.height], edge: [edge.width, edge.height] };
        })()`);
        const where = engine + " " + name + " " + JSON.stringify(got);
        assert(got.free[0] > 160, "laid out at all: " + where);
        assert(Math.abs(got.edge[0] - got.free[0]) <= 1, "as wide at the edge: " + where);
        assert(Math.abs(got.edge[1] - got.free[1]) <= 1, "as tall at the edge: " + where);
      });
    }
    await ctx.close();
  });
}
