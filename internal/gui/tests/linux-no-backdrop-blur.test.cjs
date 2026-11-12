// Run with Node's test runner and Playwright on the module path; see README.md.
// The Linux app's WebKitGTK draws without accelerated compositing, where a
// backdrop blur is repainted by the CPU on every frame (a full-window modal
// cost 25ms a frame against 16ms with none). On Linux the app's modals,
// pickers and sticky Settings head draw without the blur; elsewhere they keep it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const css = path.resolve(__dirname, "../assets/app.css");
const probe = `<div class="modal" id=m></div><div class="pop" id=p></div>
<div id="view-settings"><div class="set-head" id=h>x</div></div>`;

for (const [name, engine] of [["chromium", chromium], ["webkit", webkit]]) {
  test(`${name}: backdrop blur is off on Linux only`, async () => {
    const b = await engine.launch();
    try {
      const out = {};
      for (const cls of ["linux", ""]) {
        const page = await b.newPage();
        await page.setContent(`<body class="${cls}">${probe}</body>`);
        await page.addStyleTag({ content: await fs.readFile(css, "utf8") });
        out[cls || "other"] = await page.evaluate(() => {
          const bf = (el, pseudo) => { const s = getComputedStyle(el, pseudo); return s.backdropFilter || s.webkitBackdropFilter || "none"; };
          return [bf(m), bf(p), bf(h, "::before")];
        });
        await page.close();
      }
      assert.deepEqual(out.linux, ["none", "none", "none"]);
      for (const v of out.other) assert.notEqual(v, "none");
    } finally { await b.close(); }
  });
}
