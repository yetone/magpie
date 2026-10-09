// usemagpie.ai. Releases live on GitHub (yetone/magpie-releases); this
// worker turns the newest one into the update feed the app reads and into
// download links that never go stale.
//
//   /api/latest            {version, notes, url, published, assets: {name: {url, size, sha256}}}
//   /api/notes?after=&upto= {releases: [{version, notes, url, published}]}, newest
//                          first: what changed since the version an app last ran
//                          Both take ?lang=zh for the notes in Chinese, where a
//                          release has them (below its <!-- lang:zh --> marker);
//                          any other lang, or none, is the English alone.
//   /api/partners          {partners: [...]}: the add sheet's Partners (partners.js)
//   /download              the Apple Silicon dmg
//   /download/mac-arm64    the same;  /download/mac-intel  the Intel dmg
//   /download/windows      the Windows app (x64);  /download/windows-arm64
//   /download/linux        the Linux app (x86-64); /download/linux-arm64
//   /download/<file>       any file of the newest release, by name
//   /docs, /docs/zh, /docs/ja  the getting-started guide: /docs/start, /docs/<lang>/start
//   /zh/, /ja/, /de/       the home page in Chinese, Japanese, German (i18n.js);
//                          / sends a browser that prefers one of them there,
//                          until a language is picked on the page (the lang
//                          cookie). /de/ has no docs: its links go to English.
//
// Everything else is the static site in public/.

import { LANGS } from "./i18n.js";
import { PARTNERS } from "./partners.js";

const REPO = "yetone/magpie-releases";
const TTL = 300; // seconds the newest release is remembered

const SHORT = {
  "mac-arm64": "magpie-darwin-arm64.dmg",
  "mac-intel": "magpie-darwin-amd64.dmg",
  "mac-amd64": "magpie-darwin-amd64.dmg",
  windows: "magpie-windows-amd64.exe",
  "windows-amd64": "magpie-windows-amd64.exe",
  "windows-arm64": "magpie-windows-arm64.exe",
  linux: "magpie-linux-amd64",
  "linux-amd64": "magpie-linux-amd64",
  "linux-arm64": "magpie-linux-arm64",
};

// The bare docs paths open the getting-started guide.
const DOCS = { "/docs": "/docs/start", "/docs/zh": "/docs/zh/start", "/docs/ja": "/docs/ja/start" };
// the languages the docs are written in besides English
const DOC_LANGS = ["zh", "ja"];

export default {
  async fetch(req, env, ctx) {
    const url = new URL(req.url);
    if (url.hostname.startsWith("www.")) {
      url.hostname = url.hostname.slice(4);
      return Response.redirect(url.toString(), 301);
    }
    if (url.pathname === "/api/latest") {
      const got = await latest(ctx, env);
      if (!got) return json({ error: "no release yet" }, 503, NO_STORE);
      const lang = url.searchParams.get("lang");
      return json({ ...got.rel, notes: inLang(got.rel.notes, lang) }, 200, cacheFor(got));
    }
    if (url.pathname === "/api/partners") {
      return json({ partners: PARTNERS }, 200, { "Cache-Control": "public, max-age=600" });
    }
    if (url.pathname === "/api/notes") {
      const got = await releases(ctx, env);
      if (!got) return json({ error: "no releases" }, 503, NO_STORE);
      const after = url.searchParams.get("after"), upto = url.searchParams.get("upto");
      const lang = url.searchParams.get("lang");
      const pick = got.list
        .filter((r) => (!after || newer(r.version, after)) && (!upto || !newer(r.version, upto)))
        .map((r) => ({ ...r, notes: inLang(r.notes, lang) }));
      return json({ releases: pick }, 200, cacheFor(got));
    }
    if (url.pathname === "/download" || url.pathname.startsWith("/download/")) {
      const want = url.pathname.split("/")[2] || "mac-arm64";
      const got = await latest(ctx, env);
      const asset = got && got.rel.assets && got.rel.assets[SHORT[want] || want];
      if (!asset) return new Response("not found\n", { status: 404 });
      return Response.redirect(asset.url, 302);
    }
    const guide = DOCS[url.pathname.replace(/\/+$/, "")];
    if (guide) return Response.redirect(new URL(guide, url).toString(), 302);
    // an English docs page, to a browser whose language has its own: as /
    // does, until a language is picked
    const doc = url.pathname.match(/^\/docs\/([a-z-]+)(\.html)?$/);
    if (doc && !DOC_LANGS.includes(doc[1]) && (req.method === "GET" || req.method === "HEAD")) {
      const lang = preferred(req);
      const vary = { Vary: "Accept-Language, Cookie", "Cache-Control": "no-cache" };
      const there = `/docs/${lang}/${doc[1]}`;
      // a page not written in that language yet stays English
      if (DOC_LANGS.includes(lang) && (await env.ASSETS.fetch(new Request(new URL(there, url), { method: "HEAD" }))).ok)
        return new Response(null, { status: 302, headers: { Location: there + url.search, ...vary } });
      const res = beacon(await env.ASSETS.fetch(req));
      const out = new Response(res.body, res);
      out.headers.append("Vary", "Accept-Language, Cookie");
      out.headers.set("Cache-Control", "no-cache");
      return out;
    }
    const home = url.pathname.match(/^\/([a-z]{2})(\/(index\.html)?)?$/);
    if (home && LANGS[home[1]]) {
      if (!home[2]) return Response.redirect(new URL(`/${home[1]}/`, url).toString(), 301);
      // the English page, fetched afresh: its ETag would also stand for an
      // older dictionary
      const res = await env.ASSETS.fetch(new Request(new URL("/", url), { method: req.method }));
      return beacon(translate(res, home[1]));
    }
    if (url.pathname === "/" && (req.method === "GET" || req.method === "HEAD")) {
      const lang = preferred(req);
      const vary = { Vary: "Accept-Language, Cookie", "Cache-Control": "no-cache" };
      if (lang !== "en") return new Response(null, { status: 302, headers: { Location: `/${lang}/`, ...vary } });
      const res = beacon(await env.ASSETS.fetch(req));
      const out = new Response(res.body, res);
      out.headers.append("Vary", "Accept-Language, Cookie");
      return out;
    }
    return beacon(await env.ASSETS.fetch(req));
  },
};

// preferred is the home page's language for this browser: the one picked on
// the page (the lang cookie), else the first of its Accept-Language that the
// site has, else English.
export function preferred(req) {
  const picked = (req.headers.get("Cookie") || "").match(/(?:^|;\s*)lang=([a-z]{2})/);
  if (picked) return LANGS[picked[1]] ? picked[1] : "en";
  const wants = (req.headers.get("Accept-Language") || "")
    .split(",")
    .map((p, i) => {
      const [tag, ...rest] = p.trim().toLowerCase().split(";");
      const q = rest.map((x) => x.trim()).find((x) => x.startsWith("q="));
      return { lang: tag.split("-")[0], q: q ? parseFloat(q.slice(2)) || 0 : 1, i };
    })
    .filter((w) => w.lang && w.q > 0)
    .sort((a, b) => b.q - a.q || a.i - b.i);
  for (const w of wants) {
    if (w.lang === "en") return "en";
    if (LANGS[w.lang]) return w.lang;
  }
  return "en";
}

// translate puts a language's strings into the English home page: the
// inner HTML of each data-i18n element, the attributes data-i18n-attr names,
// <html lang>, and links to the docs and home made the language's own.
function translate(res, lang) {
  const { dict, html, docs } = LANGS[lang];
  const out = new Response(res.body, res);
  out.headers.delete("ETag");
  const rw = new HTMLRewriter()
    .on("html", { element: (el) => el.setAttribute("lang", html) })
    .on("[data-i18n]", {
      element: (el) => {
        const v = dict[el.getAttribute("data-i18n")];
        if (v != null) el.setInnerContent(v, { html: true });
      },
    })
    .on("[data-i18n-attr]", {
      element: (el) => {
        for (const pair of el.getAttribute("data-i18n-attr").split(",")) {
          const [attr, key] = pair.split(":");
          if (dict[key] != null) el.setAttribute(attr, dict[key]);
        }
      },
    })
    .on("a.brand", { element: (el) => el.setAttribute("href", `/${lang}/`) });
  // the docs in the language where there are any, else the English ones
  if (docs)
    rw.on('a[href^="/docs/"]', {
      element: (el) => {
        const href = el.getAttribute("href");
        if (!href.startsWith(`/docs/${lang}/`)) el.setAttribute("href", `/docs/${lang}/` + href.slice(6));
      },
    });
  return rw.transform(out);
}

// beacon adds Cloudflare Web Analytics to a page. The dashboard's automatic
// injection skips whatever a worker returns, and every page passes through
// this one.
const BEACON = `<script defer src="https://static.cloudflareinsights.com/beacon.min.js" data-cf-beacon='{"token": "c09e76abf16b40b2aa5571c196efb847"}'></script>`;

function beacon(res) {
  if (!(res.headers.get("Content-Type") || "").startsWith("text/html")) return res;
  return new HTMLRewriter().on("body", { element: (el) => el.append(BEACON, { html: true }) }).transform(res);
}

// How long answers are kept. A whole answer (from GitHub's API) for TTL; a
// degraded one (the API said no, so the release page and the releases feed
// stood in, or the last whole answer did) for BRIEF, so that one bad minute
// at GitHub doesn't leave apps without notes for long (#661). An error is
// never kept: the next ask tries again.
const BRIEF = 60;
const KEEP = 30 * 86400; // the last whole answer, for when GitHub fails
const NO_STORE = { "Cache-Control": "no-store" };

function cacheFor(got) {
  return { "Cache-Control": `public, max-age=${got.degraded ? BRIEF : TTL}` };
}

// cached and remember are the edge's cache (caches.default) by name. The
// names carry a version: an entry an older worker kept in another shape is
// never read as this one's. Anything unreadable is a miss.
const CACHE = "https://usemagpie.ai/__v2/";

async function cached(name) {
  try {
    const hit = await caches.default.match(new Request(CACHE + name));
    return hit ? await hit.json() : null;
  } catch (e) {
    console.log("cache", name, e);
    return null;
  }
}

function remember(ctx, name, v, ttl) {
  ctx.waitUntil(caches.default.put(new Request(CACHE + name), json(v, 200, { "Cache-Control": `max-age=${ttl}` })).catch((e) => console.log("cache put", name, e)));
}

// latest is the newest release, condensed, with each file's SHA-256 taken
// from the release's SHA256SUMS: {rel, degraded}, or null when GitHub can't
// say. GitHub's API gives the notes and sizes but limits anonymous callers
// by IP, and a worker shares its IP with many others (a GITHUB_TOKEN secret
// lifts that: `npx wrangler secret put GITHUB_TOKEN`); when the API says
// no, the release page's redirect gives the version, SHA256SUMS the files
// and the releases feed the notes, which is all an update needs. The last
// whole answer is kept, and stands in for a degraded one of its version.
async function latest(ctx, env) {
  const hit = await cached("latest");
  if (hit && hit.rel && hit.rel.version) return hit;
  const full = await fromAPI(env);
  if (full) {
    const got = { rel: full, degraded: false };
    remember(ctx, "latest", got, TTL);
    remember(ctx, "latest-good", full, KEEP);
    return got;
  }
  let good = await cached("latest-good");
  if (!good || !good.version || !good.assets) good = null;
  const pages = await fromPages();
  let rel = pages;
  if (good && (!pages || !newer(pages.version, good.version))) rel = good;
  else if (pages && !pages.notes && good && good.version === pages.version) rel = good;
  if (!rel) return null;
  const got = { rel, degraded: true };
  remember(ctx, "latest", got, BRIEF);
  return got;
}

const UA = { "User-Agent": "usemagpie.ai" };

// api asks GitHub's API, with the GITHUB_TOKEN secret where one is set.
function api(path, env) {
  const headers = { ...UA, Accept: "application/vnd.github+json" };
  if (env && env.GITHUB_TOKEN) headers.Authorization = `Bearer ${env.GITHUB_TOKEN}`;
  return fetch(`https://api.github.com/repos/${REPO}/${path}`, { headers });
}

async function fromAPI(env) {
  let res;
  try {
    res = await api("releases/latest", env);
  } catch (e) {
    console.log("github api", e);
    return null;
  }
  if (!res.ok) {
    console.log("github api", res.status, await res.text());
    return null;
  }
  const gh = await res.json();
  const tag = gh.tag_name;
  const sums = await checksums(tag);
  if (!sums) return null;
  const rel = { version: tag.replace(/^v/, ""), notes: gh.body || "", url: gh.html_url, published: gh.published_at, assets: {} };
  for (const a of gh.assets) {
    if (a.name === "SHA256SUMS") continue;
    rel.assets[a.name] = { url: a.browser_download_url, size: a.size, sha256: sums[a.name] || "" };
  }
  return rel;
}

async function fromPages() {
  let res;
  try {
    res = await fetch(`https://github.com/${REPO}/releases/latest`, { headers: UA, redirect: "manual" });
  } catch (e) {
    console.log("github latest redirect", e);
    return null;
  }
  const tag = (res.headers.get("Location") || "").split("/tag/")[1];
  if (!tag) {
    console.log("github latest redirect", res.status);
    return null;
  }
  const [sums, feed] = await Promise.all([checksums(tag), fromFeed()]);
  if (!sums) return null;
  const url = `https://github.com/${REPO}/releases/tag/${tag}`;
  const entry = (feed || []).find((r) => r.version === tag.replace(/^v/, ""));
  const rel = { version: tag.replace(/^v/, ""), notes: entry ? entry.notes : "", url, published: entry ? entry.published : null, assets: {} };
  for (const [name, sha256] of Object.entries(sums)) {
    rel.assets[name] = { url: `https://github.com/${REPO}/releases/download/${tag}/${name}`, size: 0, sha256 };
  }
  return rel;
}

// releases is the newest hundred releases' notes, newest first, kept as
// long as the newest release is: {list, degraded}, or null when GitHub
// can't say. Drafts and pre-releases are left out. When the API says no,
// the releases feed (the newest ten, not rate-limited) and the last whole
// list stand in.
async function releases(ctx, env) {
  const hit = await cached("releases");
  if (hit && Array.isArray(hit.list)) return hit;
  let res = null;
  try {
    res = await api("releases?per_page=100", env);
  } catch (e) {
    console.log("github api releases", e);
  }
  if (res && res.ok) {
    const list = (await res.json())
      .filter((r) => !r.draft && !r.prerelease)
      .map((r) => ({ version: r.tag_name.replace(/^v/, ""), notes: r.body || "", url: r.html_url, published: r.published_at }));
    const got = { list, degraded: false };
    remember(ctx, "releases", got, TTL);
    remember(ctx, "releases-good", list, KEEP);
    return got;
  }
  if (res) console.log("github api releases", res.status, await res.text());
  let [good, feed] = await Promise.all([cached("releases-good"), fromFeed()]);
  if (!Array.isArray(good)) good = null;
  if (!good && !feed) return null;
  // the last whole list's notes (markdown, both languages) where it has the
  // release, the feed's for the newer ones
  const have = new Set((good || []).map((r) => r.version));
  const list = [...(feed || []).filter((r) => !have.has(r.version)), ...(good || [])];
  list.sort((a, b) => (newer(a.version, b.version) ? -1 : newer(b.version, a.version) ? 1 : 0));
  const got = { list, degraded: true };
  remember(ctx, "releases", got, BRIEF);
  return got;
}

// fromFeed is the newest releases from GitHub's releases feed (Atom), which
// isn't the API and so isn't limited by it: [{version, notes, url,
// published}], newest first, or null. The feed has each release's notes as
// HTML, which notes() turns back into the markdown the app draws.
async function fromFeed() {
  let res;
  try {
    res = await fetch(`https://github.com/${REPO}/releases.atom`, { headers: UA });
  } catch (e) {
    console.log("github feed", e);
    return null;
  }
  if (!res.ok) {
    console.log("github feed", res.status);
    return null;
  }
  return parseFeed(await res.text());
}

export function parseFeed(xml) {
  const out = [];
  for (const [, entry] of xml.matchAll(/<entry>([\s\S]*?)<\/entry>/g)) {
    const href = (entry.match(/<link[^>]*href="([^"]*\/releases\/tag\/[^"]+)"/) || [])[1];
    if (!href) continue;
    const url = unxml(href);
    const tag = decodeURIComponent(url.split("/tag/")[1]);
    if (/-/.test(tag)) continue; // a pre-release
    const content = (entry.match(/<content[^>]*>([\s\S]*?)<\/content>/) || [])[1] || "";
    const updated = (entry.match(/<updated>([^<]*)<\/updated>/) || [])[1] || null;
    out.push({ version: tag.replace(/^v/, ""), notes: notes(unxml(content)), url, published: updated });
  }
  return out;
}

function unxml(s) {
  return s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, "&");
}

function unhtml(s) {
  return s
    .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCodePoint(parseInt(h, 16)))
    .replace(/&#(\d+);/g, (_, d) => String.fromCodePoint(+d))
    .replace(/&nbsp;/g, " ")
    .replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&#39;|&apos;/g, "'")
    .replace(/&amp;/g, "&");
}

// inline is a piece of HTML as markdown text: bold, code and links kept,
// every other tag dropped.
function inline(html) {
  return unhtml(
    html
      .replace(/<(strong|b)>([\s\S]*?)<\/\1>/g, "**$2**")
      .replace(/<code>([\s\S]*?)<\/code>/g, "`$1`")
      .replace(/<a [^>]*href="([^"]*)"[^>]*>([\s\S]*?)<\/a>/g, (_, h, t) => `[${t}](${h})`)
      .replace(/<br\s*\/?>/g, " ")
      .replace(/<[^>]+>/g, ""),
  ).replace(/\s+/g, " ").trim();
}

const HAN = /\p{Script=Han}/u;

// notes turns a release's HTML (as the feed has it) back into markdown:
// headings, bullets and paragraphs. GitHub drops the HTML comment the
// Chinese notes start under, so it goes back before the first heading in
// Chinese that follows an English one.
export function notes(html) {
  const lines = [];
  let english = false, zh = false, prev = "";
  for (const [, tag, body] of html.matchAll(/<(h[1-6]|li|p|pre)\b[^>]*>([\s\S]*?)<\/\1>/g)) {
    const text = tag === "pre" ? unhtml(body.replace(/<[^>]+>/g, "")).trim() : inline(body);
    if (!text) continue;
    if (tag[0] === "h") {
      const isZh = HAN.test(text);
      if (isZh && english && !zh) {
        lines.push("", ZH);
        zh = true;
      }
      if (!isZh) english = true;
      lines.push("", "#".repeat(+tag[1]) + " " + text);
    } else if (tag === "li") {
      if (prev !== "li") lines.push("");
      lines.push("- " + text);
    } else {
      lines.push("", text);
    }
    prev = tag;
  }
  return lines.join("\n").replace(/\n{3,}/g, "\n\n").trim();
}

// A release's notes are in English, then (since the release workflow
// translates them) in Chinese below this marker. The edge keeps the notes
// whole; each answer is cut to one language, and the browser's and the
// edge's caches tell answers apart by their URL, lang and all.
const ZH = "<!-- lang:zh -->";

// inLang is the notes in lang: zh (zh-CN, zh-Hans, ...) the Chinese when
// there is some, else the English, which is everything above the marker.
// An app from before lang asks with none, and gets the English alone.
function inLang(notes, lang) {
  notes = notes || "";
  const i = notes.indexOf(ZH);
  if (i < 0) return notes;
  if (/^zh($|[-_])/i.test(lang || "")) {
    const zh = notes.slice(i + ZH.length).trim();
    if (zh) return zh;
  }
  return notes.slice(0, i).trim();
}

// newer says whether version a comes after b (x.y.z, a pre-release before
// its release), as the app's update.Newer does.
function newer(a, b) {
  const p = (v) => {
    const [core, pre = ""] = String(v).replace(/^v/, "").split(/-(.*)/s);
    const n = core.split(".").map(Number);
    return n.length === 3 && n.every((x) => Number.isInteger(x) && x >= 0) ? { n, pre } : null;
  };
  const x = p(a), y = p(b);
  if (!x || !y) return false;
  for (let i = 0; i < 3; i++) if (x.n[i] !== y.n[i]) return x.n[i] > y.n[i];
  if (x.pre === y.pre) return false;
  if (!x.pre) return true;
  if (!y.pre) return false;
  return x.pre > y.pre;
}

// checksums reads a release's SHA256SUMS: {file name: hash}.
async function checksums(tag) {
  let r;
  try {
    r = await fetch(`https://github.com/${REPO}/releases/download/${tag}/SHA256SUMS`, { headers: UA });
  } catch (e) {
    console.log("SHA256SUMS", e);
    return null;
  }
  if (!r.ok) return null;
  const sums = {};
  for (const line of (await r.text()).split("\n")) {
    const [hash, name] = line.trim().split(/\s+\*?/);
    if (hash && name) sums[name] = hash;
  }
  return sums;
}

function json(v, status = 200, headers = {}) {
  return new Response(JSON.stringify(v), {
    status,
    headers: { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*", ...headers },
  });
}
