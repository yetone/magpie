// A contributor's card: usemagpie.ai/c/<id>, <id> being their row's id on
// the contributors page (credits.mjs anchors). credits-cli.mjs writes each
// person's card to public/contributors/c/<id>.json at deploy (credits.mjs
// cards); this serves the page around it, in the reader's language
// (?lang=, else Accept-Language), with the card drawn by public/card.js
// and everything it credits listed under it, so the page reads the same
// with script off and to a crawler. A link preview gets the person's own
// picture, name, number and first lines.

import { esc } from "./groups.js";

const SITE = "https://usemagpie.ai";
const ROUTE = /^\/c\/([^/]+)\/?$/;

export const WORDS = {
  en: {
    html: "en", wall: "/docs/contributors",
    contributor: "Contributor", back: "Contributors",
    title: (n, no) => `${n} · magpie contributor No. ${no}`,
    desc: (k, v) => `Credited in ${k === 1 ? "1 release" : `${k} releases`} of magpie${v ? `, since ${v}` : ""}.`,
    built: "What I built for magpie", since: "Since", releases: "Releases", more: (n) => `+ ${n} more`,
    flip: "Turn over", save: "Save image", copy: "Copy link", copied: "Copied", share: "Share on X",
    post: (no) => `I'm magpie contributor No. ${no}.`,
    gone: "There's no card here. Find yours on the contributors page.",
  },
  zh: {
    html: "zh-CN", wall: "/docs/zh/contributors",
    contributor: "贡献者", back: "贡献者",
    title: (n, no) => `${n} · magpie 第 ${no} 号贡献者`,
    desc: (k, v) => `在 magpie 的 ${k} 个版本中被致谢${v ? `，始于 ${v}` : ""}。`,
    built: "我为 magpie 做的", since: "始于", releases: "版本", more: (n) => `还有 ${n} 项`,
    flip: "翻面", save: "保存图片", copy: "复制链接", copied: "已复制", share: "分享到 X",
    post: (no) => `我是 magpie 的第 ${no} 号贡献者。`,
    gone: "这里没有贡献卡。去贡献者页面找到你自己的吧。",
  },
  ja: {
    html: "ja", wall: "/docs/ja/contributors",
    contributor: "コントリビューター", back: "コントリビューター",
    title: (n, no) => `${n} · magpie コントリビューター No. ${no}`,
    desc: (k, v) => `magpie の ${k} リリースに名前が載っています${v ? `（${v} から）` : ""}。`,
    built: "magpie で作ったもの", since: "Since", releases: "リリース", more: (n) => `ほか ${n} 件`,
    flip: "裏返す", save: "画像を保存", copy: "リンクをコピー", copied: "コピーしました", share: "X で共有",
    post: (no) => `magpie のコントリビューター No. ${no} です。`,
    gone: "このカードはありません。コントリビューターのページで探してください。",
  },
};

export function langOf(req) {
  const url = new URL(req.url);
  const asked = (url.searchParams.get("lang") || "").toLowerCase().slice(0, 2);
  if (WORDS[asked]) return asked;
  for (const part of (req.headers.get("Accept-Language") || "").split(",")) {
    const tag = part.split(";")[0].trim().toLowerCase().slice(0, 2);
    if (WORDS[tag]) return tag;
  }
  return "en";
}

// No. 0042: four digits at least, as a card's number
export const number = (no) => String(no).padStart(4, "0");

const PICS = "https://avatars.githubusercontent.com https://pbs.twimg.com";

function headers(nonce, cache) {
  return {
    "Content-Type": "text/html; charset=utf-8",
    "Cache-Control": cache,
    Vary: "Accept-Language",
    "Content-Security-Policy": `default-src 'none'; style-src 'self' 'unsafe-inline'; script-src 'self' 'nonce-${nonce}'; img-src 'self' data: blob: ${PICS}; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`,
    "X-Content-Type-Options": "nosniff",
    "Referrer-Policy": "strict-origin-when-cross-origin",
  };
}

const THEME = `(()=>{let m="system";try{m=localStorage.getItem("theme")||"system"}catch{}const q=matchMedia("(prefers-color-scheme: dark)");const a=()=>{const t=m==="system"?(q.matches?"dark":"light"):m;document.documentElement.dataset.theme=t;document.querySelector('meta[name="theme-color"]').content=t==="dark"?"#0b0b0c":"#fafafa"};q.addEventListener("change",()=>m==="system"&&a());a()})();`;

const nonceOf = () => btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16))));

// a card's fields, each checked: the file is ours, but a page puts nothing
// in it unchecked
export function clean(c) {
  if (!c || typeof c !== "object" || typeof c.id !== "string" || typeof c.name !== "string" || !Number.isInteger(c.no) || c.no < 1) return null;
  const str = (s) => (typeof s === "string" ? s : "");
  const pic = /^https:\/\/(avatars\.githubusercontent\.com|pbs\.twimg\.com)\//.test(str(c.pic)) ? c.pic : "";
  const lines = (Array.isArray(c.lines) ? c.lines : [])
    .filter((l) => l && typeof l.t === "string" && typeof l.v === "string")
    .map((l) => ({ v: l.v, t: l.t, href: /^https:\/\//.test(str(l.href)) ? l.href : "" }));
  const since = c.since && typeof c.since.v === "string" ? { v: c.since.v, ...(Number.isFinite(c.since.t) ? { t: c.since.t } : {}) } : null;
  return { id: c.id, name: c.name, no: c.no, n: Number.isInteger(c.n) ? c.n : 0, github: str(c.github), x: str(c.x), pic, since, lines };
}

export async function page(req, env) {
  const url = new URL(req.url);
  const m = url.pathname.match(ROUTE);
  if (!m || (req.method !== "GET" && req.method !== "HEAD")) return null;
  let id;
  try { id = decodeURIComponent(m[1]); } catch { id = ""; }
  const lang = langOf(req);
  const w = WORDS[lang];
  // ids are lowercase (credits.mjs slug): a link typed in capitals lands
  if (id && id !== id.toLowerCase()) return Response.redirect(`${SITE}/c/${encodeURIComponent(id.toLowerCase())}${url.search}`, 301);
  let card = null;
  if (id && !/[/\\]/.test(id) && id !== "." && id !== "..") {
    try {
      const got = await env.ASSETS.fetch(new Request(new URL(`/contributors/c/${encodeURIComponent(id)}.json`, url)));
      if (got.ok) card = clean(await got.json());
    } catch {}
  }
  const nonce = nonceOf();
  if (!card) {
    const body = `<main class="cc-gone"><p>${esc(w.gone)}</p><p><a href="${w.wall}">${esc(w.back)} →</a></p></main>`;
    return new Response(req.method === "HEAD" ? null : shell({ w, nonce, title: esc(w.back) + " · magpie", desc: esc(w.gone), robots: true, body }), { status: 404, headers: headers(nonce, "public, max-age=60") });
  }
  const link = `${SITE}/c/${encodeURIComponent(card.id)}`;
  const title = w.title(card.name, number(card.no));
  const desc = w.desc(card.n, card.since && card.since.v) + (card.lines.length ? " " + card.lines.slice(0, 2).map((l) => l.t).join(" · ") : "");
  const list = card.lines.map((l) => `<li>${l.href ? `<a href="${esc(l.href)}"><code>${esc(l.v)}</code></a>` : `<code>${esc(l.v)}</code>`}<span>${esc(l.t)}</span></li>`).join("");
  const handle = card.github ? `https://github.com/${encodeURIComponent(card.github)}` : card.x ? `https://x.com/${encodeURIComponent(card.x)}` : "";
  const intent = `https://x.com/intent/post?text=${encodeURIComponent(w.post(number(card.no)))}&url=${encodeURIComponent(link)}`;
  const body = `<header class="cc-top"><a href="${w.wall}#${encodeURIComponent(card.id)}"><img class="bird" src="/img/magpie-small.svg" alt="" width="22" height="22">${esc(w.back)}</a></header>
<main class="cc">
<div class="cc-stage" hidden><button class="cc-card" type="button" aria-label="${esc(w.flip)}" title="${esc(w.flip)}"><span class="cc-flip"><span class="cc-face cc-front"><canvas aria-hidden="true"></canvas></span><span class="cc-face cc-back"><canvas aria-hidden="true"></canvas></span></span><span class="cc-sheen" aria-hidden="true"></span></button></div>
<div class="cc-acts"><button class="btn cc-save" type="button" hidden>${esc(w.save)}</button><button class="btn ghost cc-copy" type="button" hidden data-done="${esc(w.copied)}">${esc(w.copy)}</button><a class="btn ghost" href="${esc(intent)}" target="_blank" rel="noopener">${esc(w.share)}</a></div>
<section class="cc-list"><h1>${handle ? `<a href="${esc(handle)}">${esc(card.name)}</a>` : esc(card.name)} <span>No. ${number(card.no)}</span></h1><p class="cc-sub">${esc(w.desc(card.n, card.since && card.since.v))}</p><ul>${list}</ul></section>
</main>
<script type="application/json" id="cc-data">${JSON.stringify({ card, link, w: { contributor: w.contributor, built: w.built, since: w.since, releases: w.releases, more: w.more(0).replace("0", "{n}"), lang: w.html } }).replace(/</g, "\\u003c")}</script>
<script src="/card.js" defer></script>`;
  const og = card.pic
    ? `<meta property="og:image" content="${esc(card.pic)}">\n<meta name="twitter:card" content="summary">`
    : `<meta property="og:image" content="${SITE}/img/og.png">\n<meta name="twitter:card" content="summary_large_image">`;
  const html = shell({ w, nonce, title: esc(title), desc: esc(desc), url: link, og, body });
  return new Response(req.method === "HEAD" ? null : html, { headers: headers(nonce, "public, max-age=300") });
}

function shell({ w, nonce, title, desc, url, og = "", robots, body }) {
  return `<!doctype html>
<html lang="${w.html}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${title}</title>
<meta name="description" content="${desc}">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#fafafa">
${robots ? '<meta name="robots" content="noindex">\n' : ""}<script nonce="${nonce}">${THEME}</script>
<link rel="icon" href="/favicon.png" type="image/png">
<link rel="stylesheet" href="/docs.css">
<link rel="stylesheet" href="/card.css">
<meta property="og:title" content="${title}">
<meta property="og:description" content="${desc}">
${url ? `<meta property="og:url" content="${url}">\n<link rel="canonical" href="${url}">\n` : ""}${og}
</head>
<body>
${body}
</body>
</html>
`;
}
