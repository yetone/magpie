// Run with Node's test runner and Playwright on the module path; see README.md.
// ARNO on Discord: the Sessions page's agent tabs had grown a thick Windows
// scrollbar, arrows and all, under them. The strip (and a provider's region
// picker, which shares its class) set scrollbar-width, and in Chromium — so
// WebView2 — that drops every ::-webkit-scrollbar rule, the app's thin thumb
// with them. Off a Mac the strip now scrolls under a 6px thumb; on a Mac it
// is left to the system's overlay bar.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium } = require("playwright");

const css = path.resolve(__dirname, "../assets/app.css");

async function gutter(mac) {
  // headless Chromium hides scrollbars unless asked not to
  const browser = await chromium.launch({ ignoreDefaultArgs: ["--hide-scrollbars"] });
  try {
    const page = await browser.newPage({ viewport: { width: 400, height: 300 } });
    const tabs = Array.from({ length: 12 }, (_, i) => `<button class="opt">Agent ${i}</button>`).join("");
    await page.setContent(`<style>${await fs.readFile(css, "utf8")}</style>
      <body class="window${mac ? " mac" : ""}"><div class="segs regions sm-agents" id="s">${tabs}</div></body>`);
    return await page.$eval("#s", (s) => ({ scrolls: s.scrollWidth > s.clientWidth, bar: s.offsetHeight - s.clientHeight - parseFloat(getComputedStyle(s).borderTopWidth) - parseFloat(getComputedStyle(s).borderBottomWidth), width: getComputedStyle(s).scrollbarWidth }));
  } finally {
    await browser.close();
  }
}

test("off a Mac the overflowing strip scrolls under the app's 6px thumb, not the classic bar", async () => {
  const g = await gutter(false);
  assert.ok(g.scrolls, "the strip overflows");
  assert.equal(g.width, "auto");
  assert.equal(g.bar, 6);
});

test("on a Mac the strip keeps the system's bar", async () => {
  const g = await gutter(true);
  assert.ok(g.scrolls, "the strip overflows");
  assert.notEqual(g.bar, 6);
});
