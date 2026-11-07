// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner: Settings' Search API menu scrolled under a thick dark bar on a
// Mac. A menu (.pop.proto-menu, .pop.row-menu) and a picker's list (.pop ul)
// were left to the system's scrollbar there, which with a mouse, or "Show
// scroll bars: Always", is the classic wide one with a dark track. They now
// scroll under the page's thin thumb, on a Mac and off one, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const playwright = require("playwright");

const css = path.resolve(__dirname, "../assets/app.css");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];

async function gutters(engine, mac) {
  // headless Chromium hides scrollbars unless asked not to
  const browser = await playwright[engine].launch(engine === "chromium" ? { ignoreDefaultArgs: ["--hide-scrollbars"] } : {});
  try {
    const page = await browser.newPage({ viewport: { width: 700, height: 500 } });
    const items = Array.from({ length: 30 }, (_, i) => `<button class="pm-item"><span class="pm-words"><span class="pm-name">Item ${i}</span></span></button>`).join("");
    const lis = Array.from({ length: 30 }, (_, i) => `<li>Model ${i}</li>`).join("");
    await page.setContent(`<style>${await fs.readFile(css, "utf8")}</style>
      <body class="window${mac ? " mac" : ""}">
        <div class="view" id="v" style="height:300px;overflow:auto;display:block"><div style="height:900px"></div></div>
        <div class="pop proto-menu" id="m" style="position:fixed;left:10px;top:10px">${items}</div>
        <div class="pop row-menu" id="r" style="position:fixed;left:300px;top:10px;max-height:200px">${items}</div>
        <div class="pop" id="p" style="position:fixed;left:500px;top:10px;display:flex;flex-direction:column;height:200px"><ul id="u">${lis}</ul></div>
      </body>`);
    return await page.evaluate(() => {
      const bar = (el) => {
        const cs = getComputedStyle(el);
        return el.offsetWidth - el.clientWidth - parseFloat(cs.borderLeftWidth) - parseFloat(cs.borderRightWidth);
      };
      const out = { view: bar(document.querySelector("#v")) };
      for (const id of ["m", "r", "u"]) {
        const e = document.querySelector("#" + id);
        out[id] = { bar: bar(e), scrolls: e.scrollHeight > e.clientHeight };
      }
      return out;
    });
  } finally {
    await browser.close();
  }
}

for (const engine of engines) {
  for (const mac of [false, true]) {
    test(`${engine}${mac ? ", on a Mac" : ", off a Mac"}: a long menu scrolls under the page's thin thumb`, async () => {
      const g = await gutters(engine, mac);
      assert.ok(g.view > 0 && g.view <= 10, "the page draws its thin thumb: " + JSON.stringify(g));
      for (const id of ["m", "r", "u"]) {
        assert.ok(g[id].scrolls, `#${id} runs longer than its box: ` + JSON.stringify(g));
        assert.equal(g[id].bar, g.view, `#${id}'s bar is the page's: ` + JSON.stringify(g));
      }
    });
  }
}
