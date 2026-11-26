// Draws the share pictures (public/img/og.png and og-<lang>.png, 2400x1260):
// the home page's headline in each language beside the app's model picker,
// in the site's dark colours and fonts. The headline comes from index.html
// and i18n.js, so a new headline needs only a run of this:
//
//   NODE_PATH=<playwright's node_modules> node og.mjs
//
// It wants the fonts the site names (SF Pro, PingFang, Hiragino), so run it
// on a Mac.
import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { LANGS } from "./i18n.js";

const { chromium } = createRequire(import.meta.url)("playwright");
const here = path.dirname(fileURLToPath(import.meta.url));
const pub = path.join(here, "public");
const url = (p) => pathToFileURL(path.join(pub, p)).href;

// The line under the headline: an agent on a plan bought for another one,
// a sentence a line where two don't fit on one.
const SUB = {
  en: "Claude Code on your ChatGPT Pro.<br>Codex on your Claude Max.",
  zh: "Claude Code 用 ChatGPT Pro，Codex 用 Claude Max。",
  "zh-tw": "Claude Code 用 ChatGPT Pro，Codex 用 Claude Max。",
  ja: "Claude Code で ChatGPT Pro、Codex で Claude Max。",
  de: "Claude Code mit Ihrem ChatGPT Pro.<br>Codex mit Ihrem Claude Max.",
};
// The plans that pool: subscriptions first, then coding plans sold as keys.
const ICONS = ["claude-color", "openai", "githubcopilot", "gemini-color", "zhipu-color", "kimi", "volcengine-color", "qwen-color", "minimax-color", "deepseek-color"];
const INVERT = new Set(["openai", "githubcopilot", "kimi"]); // drawn black

const en = fs.readFileSync(path.join(pub, "index.html"), "utf8").match(/<h1 data-i18n="hero.h1">(.*?)<\/h1>/)[1];
const heads = { en, ...Object.fromEntries(Object.entries(LANGS).map(([k, v]) => [k, v.dict["hero.h1"]])) };
const html = { en: "en", ...Object.fromEntries(Object.entries(LANGS).map(([k, v]) => [k, v.html])) };

const page = (lang) => {
  const [first, second] = heads[lang].replace(/<br>|<wbr>/g, "").split(/<span>|<\/span>/);
  return `<!doctype html><html lang="${html[lang]}"><meta charset="utf-8"><style>
* { box-sizing: border-box; margin: 0; }
html, body { width: 1200px; height: 630px; overflow: hidden; }
body { background: #0b0b0c; color: #f2f2f3; position: relative; -webkit-font-smoothing: antialiased;
  font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Inter", "PingFang SC", sans-serif;
  background-image: radial-gradient(rgba(255,255,255,.05) 1px, transparent 1px); background-size: 22px 22px; }
:lang(zh-TW) body { font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang TC", sans-serif; }
:lang(ja) body { font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", "Hiragino Sans", sans-serif; }
.left { position: absolute; left: 60px; top: 52px; width: 500px; height: 526px; display: flex; flex-direction: column; }
.top { display: flex; align-items: center; justify-content: space-between; }
.brand { display: flex; align-items: center; gap: 14px; font-size: 25px; font-weight: 650; letter-spacing: -.01em; }
.brand img { width: 40px; height: 40px; filter: invert(1); }
.site { font-size: 17px; color: #7a7a82; }
h1 { margin-top: auto; font-weight: 700; letter-spacing: -.035em; line-height: 1.04; font-size: var(--h, 60px); }
h1 span { display: block; color: #7a7a82; }
:lang(zh-CN) h1, :lang(zh-TW) h1, :lang(ja) h1 { letter-spacing: 0; line-height: 1.12; }
p { margin-top: 24px; font-size: 20px; color: #b3b3ba; }
.icons { display: flex; gap: 10px; margin-top: 32px; }
.icons i { width: 40px; height: 40px; border-radius: 10px; border: 1px solid #26262a; background: #151517; display: grid; place-items: center; }
.icons img { width: 22px; height: 22px; }
.icons img.inv { filter: invert(1); }
.shot { position: absolute; left: 640px; top: 60px; width: 600px; border-radius: 18px 0 0 0; overflow: hidden; border: 1px solid #26262a; border-right: 0;
  box-shadow: 0 20px 60px -20px rgba(0,0,0,.8); }
.shot img { display: block; width: 600px; }
.fade { position: absolute; left: 600px; right: 0; bottom: 0; height: 120px; background: linear-gradient(to bottom, transparent, #0b0b0c); }
</style><body>
<div class="left">
  <div class="top"><div class="brand"><img src="${url("img/magpie.svg")}" alt="">magpie</div><div class="site">usemagpie.ai</div></div>
  <h1>${first.trim()}<span>${(second || "").trim()}</span></h1>
  <p>${SUB[lang]}</p>
  <div class="icons">${ICONS.map((n) => `<i><img${INVERT.has(n) ? ' class="inv"' : ""} src="${url(`icons/${n}.svg`)}" alt=""></i>`).join("")}</div>
</div>
<div class="shot"><img src="${url("img/picker-dark.png")}" alt=""></div>
<div class="fade"></div>
</body></html>`;
};

const browser = await chromium.launch();
const tab = await browser.newPage({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 2 });
for (const lang of Object.keys(SUB)) {
  const file = path.join(here, `.og-${lang}.html`);
  fs.writeFileSync(file, page(lang));
  await tab.goto(pathToFileURL(file).href);
  await tab.evaluate(() => document.fonts.ready);
  // The largest headline that keeps each half on one line in the column.
  const size = await tab.evaluate(() => {
    const h = document.querySelector("h1"), w = h.parentElement.clientWidth;
    let s = 58;
    for (; s > 36; s -= 1) {
      h.style.setProperty("--h", `${s}px`);
      h.style.whiteSpace = "nowrap";
      if (h.scrollWidth <= w) break;
    }
    return s;
  });
  const out = path.join(pub, "img", lang === "en" ? "og.png" : `og-${lang}.png`);
  await tab.screenshot({ path: out });
  fs.rmSync(file);
  console.log(lang, `${size}px`, path.relative(here, out));
}
await browser.close();
