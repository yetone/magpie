// Run with Node's test runner and Playwright on the module path; see README.md.
// A plugin page served to a browser comes as one document whose inline
// scripts are allowed by hash (pageinline.go). The hash is of the exact
// bytes between the tags, the newline before </script> included; a hash of
// the body alone blocks the script and leaves the page blank (zhangly1 on X).
// pluginpage_test.go checks the hashes Go sends against the bytes it serves;
// this checks the browsers agree, with no violation, and that a hash of the
// body without the newline would have been blocked.
const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const tok = "d".repeat(32);
const sha = (s) => `'sha256-${crypto.createHash("sha256").update(s).digest("base64")}'`;
const body = "window.ran = 1;";

function serve(hash) {
  return async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname !== `/_magpie/plugin/${tok}/index.html`) return route.fulfill({ status: 404, body: "" });
    const doc = `<!doctype html><meta charset="utf-8"><script>${body}\n</script><p id=p>page</p>`;
    return route.fulfill({ headers: { "Content-Type": "text/html", "Content-Security-Policy": `sandbox allow-scripts; default-src 'none'; script-src ${hash}` }, body: doc });
  };
}

async function open(browser, hash) {
  const p = await browser.newPage();
  p.setDefaultNavigationTimeout(60000);
  await p.addInitScript(() => { window.violations = []; document.addEventListener("securitypolicyviolation", (e) => window.violations.push(e.violatedDirective)); });
  await p.route("**/*", serve(hash));
  await p.goto(`http://magpie.test/_magpie/plugin/${tok}/index.html`);
  return p;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a hash of the served bytes runs the page's script`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const ok = await open(browser, sha(body + "\n"));
    assert.equal(await ok.evaluate(() => window.ran), 1);
    assert.deepEqual(await ok.evaluate(() => window.violations), []);
    // the old hash, of the body without the newline served after it
    const bad = await open(browser, sha(body));
    assert.equal(await bad.evaluate(() => window.ran), undefined);
  });
}
