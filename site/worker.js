// usemagpie.ai. Releases live on GitHub (yetone/magpie-releases); this
// worker turns the newest one into the update feed the app reads and into
// download links that never go stale, and serves their files from R2,
// copied there from GitHub (files.js).
//
//   /api/latest            {version, notes, url, published, assets: {name: {url, api, size, sha256}}}
//   /api/latest?channel=stable
//                          the same for Settings' Stable channel: a release a
//                          week, an urgent one at once ("The Stable channel"
//                          below), with channel and urgent; 503 while unknown
//   /api/notes?after=&upto=&limit= {releases: [{version, notes, url, published}]}, newest
//                          first: what changed since the version an app last ran
//                          Both take ?lang=zh for the notes in Chinese, where a
//                          release has them (below its <!-- lang:zh --> marker);
//                          any other lang, or none, is the English alone.
//   /api/partners          {partners: [...]}: the add sheet's Partners (partners.js),
//                          their website and key links through /go
//   /go/<id>/keys[/<region>], /go/<id>/site[/<region>]
//                          a partner's key page or website, counted
//   /go/<id>/home, /go/<id>/readme
//                          a sponsor's link from the home page or the
//                          READMEs (sponsors.js), counted
//   /api/icon?url=<https URL>
//                          a picture a plugin, market or import link names,
//                          fetched here so its host sees Cloudflare, not the
//                          user (the app fetches every remote icon this way)
//   /docs/contributors, /docs/zh/contributors, /docs/ja/contributors
//                          the contributors page, its table put in from
//                          public/contributors/<lang>.html (credits-cli.mjs
//                          wall, at each deploy)
//   /download              the Apple Silicon dmg
//   /download/mac-arm64    the same;  /download/mac-intel  the Intel dmg
//   /download/windows      the Windows app (x64);  /download/windows-arm64
//   /download/linux        the Linux app (x86-64); /download/linux-arm64
//   /download/<file>       any file of the newest release, by name
//   /download/stable, /download/stable/<platform or file>
//                          the same from the Stable channel's release
//   /files/<tag>/<file>    a release's file from R2 (files.js), the feed's
//                          url and /download's for the files R2 holds;
//                          GitHub's own address for any it doesn't
//   /dl/<tag>/<file>       a release's file through GitHub's API, the feed's
//                          url while github.com's download address fails
//   /r/...                 the pages' PostHog snippet, passed on to PostHog
//                          (relay, below) so a blocker that knows its host
//                          doesn't drop the count
//   /docs, /docs/zh, /docs/ja  the getting-started guide: /docs/start, /docs/<lang>/start
//   /zh/, /ja/, /de/       the home page in Chinese, Japanese, German (i18n.js);
//                          / sends a browser that prefers one of them there,
//                          until a language is picked on the page (the lang
//                          cookie). /de/ has no docs: its links go to English.
//
// Everything else is the static site in public/.

import { LANGS } from "./i18n.js";
import { PARTNERS } from "./partners.js";
import { SPONSORS } from "./sponsors.js";
import { serve as serveFile, held, onR2, mirror, githubFile } from "./files.js";
import { handle as pluginStats, prune as prunePlugins } from "./pluginstats.js";

const REPO = "yetone/magpie-releases";
const SITE = "https://usemagpie.ai";
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
// pages that moved, from the old path (no .html) to the new one: the MCP
// hub was renamed the MCP router
const MOVED = { "/docs/mcp-hub": "/docs/mcp-router", "/docs/zh/mcp-hub": "/docs/zh/mcp-router", "/docs/ja/mcp-hub": "/docs/ja/mcp-router" };
// the languages the docs are written in besides English
const DOC_LANGS = ["zh", "ja"];

export default {
  async fetch(req, env, ctx) {
    const url = new URL(req.url);
    if (url.hostname.startsWith("www.")) {
      url.hostname = url.hostname.slice(4);
      return Response.redirect(url.toString(), 301);
    }
    if (url.pathname === "/api/plugins/tagged") return taggedPlugins(ctx);
    if (url.pathname === "/api/plugins/readme") return pluginReadme(ctx, url.searchParams.get("name"));
    if (url.pathname.startsWith("/api/plugins/")) {
      const got = await pluginStats(req, env, ctx);
      if (got) return got;
    }
    if (url.pathname === "/api/latest") {
      const lang = url.searchParams.get("lang");
      // Settings' Stable channel; any other channel, or none, is every
      // version as it comes out
      if (url.searchParams.get("channel") === "stable") {
        const got = await stable(ctx, env, Date.now());
        if (!got) return json({ error: "the stable version isn't known yet" }, 503, NO_STORE);
        return json({ ...(await fromR2(ctx, env, got.rel)), notes: inLang(got.rel.notes, lang) }, 200, cacheFor(got));
      }
      const got = await latest(ctx, env);
      if (!got) return json({ error: "no release yet" }, 503, NO_STORE);
      return json({ ...(await fromR2(ctx, env, got.rel)), notes: inLang(got.rel.notes, lang) }, 200, cacheFor(got));
    }
    if (url.pathname === "/api/partners") {
      const now = Date.now();
      if (PARTNERS.some((p) => live(p, now)) && Math.random() < 1 / FETCH_SAMPLE)
        ctx.waitUntil(capture("magpie partners fetch", { weight: FETCH_SAMPLE, country: country(req) }));
      return json({ partners: served(PARTNERS, now) }, 200, { "Cache-Control": "public, max-age=600" });
    }
    if (url.pathname.startsWith("/go/")) {
      let id, what, region, to, counted;
      try {
        [, , id, what, region] = url.pathname.split("/").map(decodeURIComponent);
        if ((to = goTarget(PARTNERS, id, what, region))) counted = ["magpie partner go", { id, what, region: region || "" }];
        else if (region === undefined && (to = sponsorTarget(SPONSORS, id, what))) counted = ["magpie sponsor go", { id, where: what }];
      } catch {}
      if (!to) return new Response("not found\n", { status: 404 });
      if (req.method === "GET") ctx.waitUntil(capture(counted[0], { ...counted[1], country: country(req) }));
      return new Response(null, { status: 302, headers: { Location: to, "Cache-Control": "no-store" } });
    }
    if (url.pathname === "/api/notes") {
      const got = await releases(ctx, env);
      if (!got) return json({ error: "no releases" }, 503, NO_STORE);
      const after = url.searchParams.get("after"), upto = url.searchParams.get("upto");
      const lang = url.searchParams.get("lang");
      // limit: the newest that many alone (Settings' "this version and the
      // 10 before it", Linka), so an app asking for 11 isn't sent 100
      const limit = parseInt(url.searchParams.get("limit"), 10);
      const within = (r) => (!after || newer(r.version, after)) && (!upto || !newer(r.version, upto));
      let pick = got.list.filter(within);
      // the hundred newest are a day of releases: a range reaching further
      // back (a Stable app's week, an app long unopened) is read on from
      // the ledger
      const oldest = got.list.at(-1);
      const short = after ? !oldest || newer(oldest.version, after) : limit > 0 ? pick.length < limit : !pick.some((r) => r.version === upto);
      if (short && upto) pick = [...pick, ...(await fromLedger(env, oldest && oldest.version, after, upto, limit > 0 ? limit - pick.length : LEDGER_MOST))];
      pick = pick.slice(0, limit > 0 ? limit : undefined).map((r) => ({ ...r, notes: inLang(r.notes, lang) }));
      return json({ releases: pick }, 200, cacheFor(got));
    }
    if (url.pathname === "/api/icon") return icon(url.searchParams.get("url"));
    const file = url.pathname.match(/^\/files\/(v[0-9][0-9A-Za-z.+-]*)\/([0-9A-Za-z._+-]+)$/);
    if (file) return serveFile(req, env, REPO, file[1], file[2]);
    const dl = url.pathname.match(/^\/dl\/(v[0-9][0-9A-Za-z.+-]*)\/([0-9A-Za-z._+-]+)$/);
    if (dl) return detour(ctx, env, dl[1], dl[2]);
    if (url.pathname.startsWith(RELAY)) return relay(req, url);
    if (url.pathname === "/download" || url.pathname.startsWith("/download/")) {
      // /download/stable/...: the same from the Stable channel's release
      // (the home page's Stable choice, fishmagic80 on X)
      const parts = url.pathname.split("/");
      const onStable = parts[2] === "stable";
      const want = parts[onStable ? 3 : 2] || "mac-arm64";
      const got = onStable ? await stable(ctx, env, Date.now()).catch(() => null) : await latest(ctx, env);
      const rel = got && (await fromR2(ctx, env, got.rel));
      const asset = rel && rel.assets && rel.assets[SHORT[want] || want];
      if (!asset) return new Response(onStable && !got ? "the stable version isn't known yet\n" : "not found\n", { status: onStable && !got ? 503 : 404, headers: NO_STORE });
      if (req.method === "GET") ctx.waitUntil(capture("magpie site download", { link: onStable ? "stable/" + want : want, file: SHORT[want] || want, client: client(req), country: country(req) }));
      return Response.redirect(asset.url, 302);
    }
    // crawlers and old browsers ask for /favicon.ico whatever the page links
    if (url.pathname === "/favicon.ico") return Response.redirect(new URL("/favicon.png", url).toString(), 301);
    const guide = DOCS[url.pathname.replace(/\/+$/, "")];
    if (guide) return Response.redirect(new URL(guide, url).toString(), 302);
    const moved = MOVED[url.pathname.replace(/(\.html)?\/*$/, "")];
    if (moved) return Response.redirect(new URL(moved + url.search, url).toString(), 301);
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
      const res = beacon(await withWall(await env.ASSETS.fetch(req), env, url));
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
    if (url.pathname === "/install.sh" && req.method === "GET")
      ctx.waitUntil(capture("magpie site install script", { client: client(req), country: country(req) }));
    return beacon(await withWall(await env.ASSETS.fetch(req), env, url));
  },
  // the crons (wrangler.jsonc's triggers): every quarter hour the ledger
  // the Stable channel is chosen from and Discover's old counts pruned, every five minutes the newest and
  // the Stable release's files into R2
  async scheduled(event, env, ctx) {
    if (event.cron === MIRROR_CRON) ctx.waitUntil(keepFiles(ctx, env));
    else {
      ctx.waitUntil(keepLedger(env, Date.now()));
      ctx.waitUntil(prunePlugins(env));
    }
  },
};

// A partner's reach and clicks are counted here, not in the app: the app's
// own counts (provider.CountPartner) go only from users who let the stats
// go, and these are what the server sees anyway. /api/partners hands out
// each partner's website and key page as a /go link, which counts the
// click (event "magpie partner go") and sends the browser on. Each app
// fetches the list every six hours, so the fetches tell how many apps a
// partner can be seen in: one in FETCH_SAMPLE is sent, as "magpie partners
// fetch" with weight FETCH_SAMPLE (sum it), and only while someone is
// listed. No id, address or account goes: a country, at most.
const FETCH_SAMPLE = 10;
const GO = "https://usemagpie.ai/go/";
const POSTHOG = "https://us.i.posthog.com/i/v0/e/";
// the Magpie project's PostHog key, as the app's (internal/stats): it can
// send events and read nothing
const POSTHOG_KEY = "phc_nyDyYyrUTRGvNq5U7FBesCyM2j4GdKeqGp8ko7DBerTC";

// live reports whether a partner's term includes now.
function live(p, now) {
  return (!p.from || Date.parse(p.from) <= now) && (!p.until || now < Date.parse(p.until));
}

// served is the list the app gets: those whose term hasn't ended (an
// ended one stays in partners.js, so the /go links of providers added from
// it keep working), with their website and key page as /go links. A
// website's own host goes beside it as siteHost, for the editor's link to
// read (www.kimi.com, not usemagpie.ai).
export function served(list, now) {
  const go = (p, what, r) => GO + [p.id, what, r && r.id].filter(Boolean).map(encodeURIComponent).join("/");
  const host = (u) => { try { return new URL(u).host; } catch { return ""; } };
  const linked = (o, p, r) => ({ ...o, ...(o.website && { website: go(p, "site", r), siteHost: host(o.website) }), ...(o.keysUrl && { keysUrl: go(p, "keys", r) }) });
  return list
    .filter((p) => !p.until || now < Date.parse(p.until))
    .map((p) => ({ ...linked(p, p), ...(p.regions && { regions: p.regions.map((r) => linked(r, p, r)) }) }));
}

// goTarget is where a /go link sends the browser: the partner's own key
// page or website, the region's where it has one. Only partners.js's own
// addresses, never one the link names. It says it came from magpie, as
// ref=magpie, unless the address names a ref of its own (a referral code).
export function goTarget(list, id, what, region) {
  const p = list.find((x) => x.id === id);
  const field = what === "keys" ? "keysUrl" : what === "site" ? "website" : "";
  if (!p || !field) return "";
  if (!region) return fromMagpie(p[field] || "");
  const r = (p.regions || []).find((x) => x.id === region);
  return r ? fromMagpie(r[field] || p[field] || "") : "";
}

// sponsorTarget is where a sponsor's /go link sends the browser: the link
// sponsors.js has for that place, as it is (its own ref is how the sponsor
// counts us).
export function sponsorTarget(list, id, where) {
  const s = list.find((x) => x.id === id);
  if (!s || (where !== "home" && where !== "readme")) return "";
  return s[where] || "";
}

function fromMagpie(to) {
  if (!to) return "";
  try {
    const u = new URL(to);
    if (!u.searchParams.has("ref")) u.searchParams.set("ref", "magpie");
    return u.toString();
  } catch {
    return to;
  }
}

// client says whether a browser asked, or a command (curl, wget, a script).
function client(req) {
  return /^Mozilla\//.test(req.headers.get("User-Agent") || "") ? "browser" : "command";
}

function country(req) {
  return (req.cf && req.cf.country) || "";
}

// capture sends one event to PostHog, under an id of its own: a count, not
// a person.
async function capture(event, props) {
  try {
    await fetch(POSTHOG, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ api_key: POSTHOG_KEY, event, distinct_id: crypto.randomUUID(), properties: { ...props, $process_person_profile: false } }),
    });
  } catch {}
}

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

// relay passes a request of the pages' PostHog snippet (under RELAY) on to
// PostHog: the library and its config from the assets host, the rest from
// the ingestion host. The visitor's address goes as X-Forwarded-For, which
// PostHog reads for the country, and the site's cookies stay here.
const RELAY = "/r/";
export async function relay(req, url) {
  const path = url.pathname.slice(RELAY.length - 1);
  const host = /^\/(static|array)\//.test(path) ? "us-assets.i.posthog.com" : "us.i.posthog.com";
  const headers = new Headers(req.headers);
  for (const h of ["Cookie", "Host"]) headers.delete(h);
  const ip = req.headers.get("CF-Connecting-IP");
  if (ip) headers.set("X-Forwarded-For", ip);
  const body = req.method === "GET" || req.method === "HEAD" ? null : req.body;
  try {
    return await fetch(`https://${host}${path}${url.search}`, { method: req.method, headers, body, redirect: "manual" });
  } catch {
    return new Response("", { status: 502 });
  }
}

// beacon adds PostHog's Web Analytics to a page; every page passes through
// this one. Cloudflare Web Analytics is not added here: Cloudflare injects
// its own beacon into worker responses now, and that one reports to this
// site's /cdn-cgi/rum. A second, manual beacon loaded first and won, and
// reported to cloudflareinsights.com, which answers 404 for this site's
// token, so Web Analytics counted no visits at all. PostHog's requests go through RELAY
// on this site. PostHog keeps its visitor id in
// localStorage, never a cookie: these pages vary on Cookie (the lang
// cookie), and a per-visitor cookie would ride on every request. It isn't
// cookieless, since cookieless drops the address before GeoIP and the
// country with it. It sends page views and leaves, clicks (autocapture,
// for heatmaps, dead clicks too), Web Vitals and network timings, uncaught
// errors, and a session recording with every input masked; the recording
// and the rest of the project's settings come from its remote config, so
// flags aren't turned off. Nothing is loaded under Do Not Track or Global
// Privacy Control. Its project is the daily count's.
const WEB_ANALYTICS = `<script>if(!(navigator.doNotTrack=="1"||window.doNotTrack=="1"||navigator.globalPrivacyControl)){!function(t,e){var o,n,p,r;e.__SV||(window.posthog&&window.posthog.__loaded)||(window.posthog=e,e._i=[],e.init=function(i,s,a){function g(t,e){var o=e.split(".");2==o.length&&(t=t[o[0]],e=o[1]),t[e]=function(){t.push([e].concat(Array.prototype.slice.call(arguments,0)))}}(p=t.createElement("script")).type="text/javascript",p.crossOrigin="anonymous",p.async=!0,p.src=s.api_host.replace(".i.posthog.com","-assets.i.posthog.com")+"/static/array.js",(r=t.getElementsByTagName("script")[0]).parentNode.insertBefore(p,r);var u=e;for(void 0!==a?u=e[a]=[]:a="posthog",u.people=u.people||[],u.toString=function(t){var e="posthog";return"posthog"!==a&&(e+="."+a),t||(e+=" (stub)"),e},u.people.toString=function(){return u.toString(1)+" (stub)"},o="capture register unregister opt_out_capturing opt_in_capturing has_opted_out_capturing set_config".split(" "),n=0;n<o.length;n++)g(u,o[n]);e._i.push([i,s,a])},e.__SV=1)}(document,window.posthog||[]);posthog.init("${POSTHOG_KEY}",{api_host:location.origin+"/r",ui_host:"https://us.posthog.com",persistence:"localStorage",person_profiles:"identified_only",capture_pageview:true,capture_pageleave:true,autocapture:true,enable_heatmaps:true,capture_dead_clicks:true,capture_exceptions:true,capture_performance:{web_vitals:true,network_timing:true},session_recording:{maskAllInputs:true},disable_surveys:true,respect_dnt:true})}</script>`;
const BEACON = WEB_ANALYTICS;

// The contributors page: its #wall gets the table credits-cli.mjs rendered
// for that language at deploy. Without it (a deploy that couldn't build
// it) the page keeps the line it is written with.
const WALL_PAGE = /^\/docs\/(?:(zh|ja)\/)?contributors(?:\.html)?$/;
export async function withWall(res, env, url) {
  const m = url.pathname.match(WALL_PAGE);
  if (!m || !res.ok || !(res.headers.get("Content-Type") || "").startsWith("text/html")) return res;
  let table = "";
  try {
    const got = await env.ASSETS.fetch(new Request(new URL(`/contributors/${m[1] || "en"}.html`, url)));
    if (got.ok) table = await got.text();
  } catch {}
  if (!table.includes("<table")) return res;
  return new HTMLRewriter().on("#wall", { element: (el) => el.setInnerContent(table, { html: true }) }).transform(res);
}

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
    // a feed sending the files by /dl is kept briefly: github.com's own
    // address is given again as soon as it answers
    const got = { rel: full, degraded: detoured(full) };
    remember(ctx, "latest", got, got.degraded ? BRIEF : TTL);
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

const MIRROR_CRON = "*/5 * * * *";

// fromR2 is rel with the files R2 holds at /files (onR2); what R2 holds
// of a tag is asked once a minute.
async function fromR2(ctx, env, rel) {
  if (!rel || !env.RELEASES) return rel;
  const tag = "v" + rel.version;
  let have = await cached("r2/" + tag);
  if (!have || typeof have !== "object") {
    have = await held(env, tag);
    if (have) remember(ctx, "r2/" + tag, have, BRIEF);
  }
  return onR2(rel, have, REPO, SITE);
}

// keepFiles is the five-minute cron's run: the newest release's files and
// the Stable one's into R2, each from github.com's address or, when that
// fails, by way of GitHub's API (located).
async function keepFiles(ctx, env) {
  if (!env.RELEASES) return;
  const rels = [];
  const got = await latest(ctx, env);
  if (got) rels.push(got.rel);
  const st = await stable(ctx, env, Date.now()).catch((e) => console.log("r2: stable", String(e)));
  if (st) rels.push(st.rel);
  const open = async (name, a, tag) => {
    let res = null;
    try {
      res = await fetch(githubFile(REPO, tag, name), { headers: UA });
    } catch (e) {
      console.log("r2: github.com", name, String(e));
    }
    if (res && res.ok) return res;
    const to = a.api && (await located(a.api, env));
    return to ? fetch(to, { headers: UA }) : res;
  };
  // the attestation bundles GitHub holds for a digest, in R2 beside the files
  const attest = async (sha256) => {
    const res = await api(`attestations/sha256:${sha256}`, env);
    if (!res.ok) return null;
    const j = await res.json();
    return (j.attestations || []).map((x) => x.bundle).filter(Boolean);
  };
  const done = await mirror(env, rels, open, Date.now, attest);
  if (done.copied.length || done.failed.length) console.log("r2", JSON.stringify(done));
}

// api asks GitHub's API, with the GITHUB_TOKEN secret where one is set.
function api(path, env) {
  const headers = { ...UA, Accept: "application/vnd.github+json" };
  if (env && env.GITHUB_TOKEN) headers.Authorization = `Bearer ${env.GITHUB_TOKEN}`;
  return fetch(`https://api.github.com/repos/${REPO}/${path}`, { headers });
}

// fromAPI is a release from GitHub's API: the newest, or the one path names
// (releases/tags/v0.1.2), or null.
async function fromAPI(env, path = "releases/latest") {
  let res;
  try {
    res = await api(path, env);
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
  const listed = gh.assets.find((a) => a.name === "SHA256SUMS");
  const listedSig = gh.assets.find((a) => a.name === SIG);
  const got = await checksums(tag, env, listed && listed.url, listedSig && listedSig.url);
  if (!got) return null;
  const rel = { version: tag.replace(/^v/, ""), notes: gh.body || "", url: gh.html_url, published: gh.published_at, assets: {} };
  if (got.signed) rel.signed = got.signed;
  for (const a of gh.assets) {
    if (a.name === "SHA256SUMS" || a.name === SIG) continue;
    // api: the file's address in GitHub's API, which an app asks when
    // url is busy. With github.com's download address failing (504 for
    // hours on 2026-11-08), url is the site's /dl, which redirects
    // through the API: an app too old to know api follows it, and still
    // checks the file against sha256.
    const url = got.direct ? a.browser_download_url : `${SITE}/dl/${tag}/${encodeURIComponent(a.name)}`;
    rel.assets[a.name] = { url, api: a.url, size: a.size, sha256: got.sums[a.name] || "" };
  }
  return rel;
}

// detoured: whether rel's files go by the site's /dl
function detoured(rel) {
  return Object.values(rel.assets || {}).some((a) => a.url && a.url.startsWith(SITE + "/dl/"));
}

// located is where GitHub's API sends an ask for a release file's bytes
// (Accept: application/octet-stream): a signed address on GitHub's asset
// host, good for some minutes, or null. The redirect is read, not
// followed, so no Authorization goes to the asset host.
async function located(apiURL, env) {
  const headers = { ...UA, Accept: "application/octet-stream" };
  if (env && env.GITHUB_TOKEN) headers.Authorization = `Bearer ${env.GITHUB_TOKEN}`;
  let res;
  try {
    res = await fetch(apiURL, { headers, redirect: "manual" });
  } catch (e) {
    console.log("github asset", e);
    return null;
  }
  const to = res.headers.get("Location") || "";
  let host = "";
  try {
    host = new URL(to).hostname;
  } catch {}
  if (res.status < 300 || res.status > 399 || !to.startsWith("https://") || !(host.endsWith(".githubusercontent.com") || host === "github.com")) {
    console.log("github asset", res.status, to.slice(0, 80));
    return null;
  }
  return to;
}

// detour is /dl/<tag>/<file>: a redirect to the release's file by way of
// GitHub's API (located), for when github.com's download address fails.
// Where GitHub's API sends is kept five minutes, and each release's list
// of files an hour, so a worker IP's 60 anonymous asks an hour go far.
// The file is checked by whoever downloads it, against the feed's hash.
async function detour(ctx, env, tag, name) {
  const key = `dl/${tag}/${name}`;
  let to = await cached(key);
  if (typeof to !== "string" || !to) {
    let files = await cached(`dl-files/${tag}`);
    if (!files || typeof files !== "object") {
      let res = null;
      try {
        res = await api(`releases/tags/${tag}`, env);
      } catch (e) {
        console.log("github api", tag, e);
      }
      if (!res || !res.ok) return new Response("GitHub didn't say where the file is; try again in a minute\n", { status: 502, headers: NO_STORE });
      files = {};
      for (const a of (await res.json()).assets || []) files[a.name] = a.url;
      remember(ctx, `dl-files/${tag}`, files, 3600);
    }
    if (!files[name]) return new Response("not found\n", { status: 404 });
    to = await located(files[name], env);
    if (!to) return new Response("GitHub didn't hand out the file; try again in a minute\n", { status: 502, headers: NO_STORE });
    remember(ctx, key, to, 300);
  }
  return new Response(null, { status: 302, headers: { Location: to, ...NO_STORE } });
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
  const feed = await fromFeed();
  return fromSums(tag, (feed || []).find((r) => r.version === tag.replace(/^v/, "")));
}

// fromSums is tag's release from its SHA256SUMS alone, with its notes and
// date from entry ({notes, published}) where there is one: every file, its
// size unknown (0). null when SHA256SUMS can't be read.
async function fromSums(tag, entry) {
  const got = await checksums(tag);
  if (!got) return null;
  const sums = got.sums;
  const url = `https://github.com/${REPO}/releases/tag/${tag}`;
  const rel = { version: tag.replace(/^v/, ""), notes: entry ? entry.notes : "", url, published: entry ? entry.published : null, assets: {} };
  if (got.signed) rel.signed = got.signed;
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

// The Stable channel (Settings' Update channel, ssharpcc on X): magpie
// releases many times a day, and someone who reads every changelog before
// updating wants fewer, older ones. /api/latest?channel=stable is the
// update feed for them, chosen here from the release list and never by
// hand:
//
//   - once a week, as the week turns (Monday 00:00 UTC), Stable becomes
//     the newest release that was out SOAK (three days) by then;
//   - an urgent release (a security hole, a ban risk, lost data) is Stable
//     as soon as it is out, until a weekly pick passes it.
//
// A release is urgent when its notes have a line that starts with
// URGENT_MARK: "**Urgent release:** why". magpie-releases' release run
// writes it from an `Urgent:` trailer on any commit since the version
// before, and it can be added to a published release's notes by hand
// (gh release edit) within a day, while the release is on the ledger's
// first page. GitHub releases are never marked pre-release for this:
// /download and the Latest mark go by them.
//
// A hundred releases are a day or so of magpie, so the list GitHub's API
// gives in one ask doesn't reach a week back. The ledger does: a KV
// namespace (LEDGER) the cron fills every quarter hour, its "index" every
// release of the last LEDGER_DAYS ({v, p: published, u: urgent}, newest
// first) and "notes:<UTC day>" each day's notes by version, which
// /api/notes reads for a range older than the hundred newest. Without the
// ledger, or one that doesn't reach back to the week's cut, Stable isn't
// known and the feed says so (503) rather than guess. The Docker image's
// stable tag follows this feed (.github/workflows/docker-stable.yml).
const DAY = 86400e3;
const SOAK = 3 * DAY;
const LEDGER_DAYS = 45;
const LEDGER_REACH = 21 * DAY; // how far back the cron fills the ledger
const LEDGER_PAGES = 4; // GitHub API asks one cron run makes at most
const LEDGER_MOST = 1500; // releases /api/notes reads from the ledger at most
export const URGENT_MARK = "**Urgent release:**";

// urgentIn says whether a release's notes mark it urgent: a line (or a
// quoted one) that starts with URGENT_MARK, in any case.
export function urgentIn(notes) {
  return /^[ \t]*(?:>[ \t]*)?\*\*urgent release:\*\*/im.test(notes || "");
}

// weekStart is the Monday 00:00 UTC on or before now, in ms.
export function weekStart(now) {
  const d = new Date(now);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()) - ((d.getUTCDay() + 6) % 7) * DAY;
}

// stableOf is the Stable release at now among list ([{version, published,
// urgent}], any order): the newer of the newest release out SOAK before
// this week began and the newest urgent one. null when the list doesn't
// reach back to that cut and has no urgent release either: unknown.
export function stableOf(list, now) {
  const cut = weekStart(now) - SOAK;
  let weekly = null, rush = null, reaches = false;
  for (const r of list) {
    const at = Date.parse(r.published);
    if (!(at <= now)) continue; // a date not known, or not yet
    if (at <= cut) {
      reaches = true;
      if (!weekly || newer(r.version, weekly.version)) weekly = r;
    }
    if (r.urgent && (!rush || newer(r.version, rush.version))) rush = r;
  }
  if (!reaches) return rush;
  return rush && newer(rush.version, weekly.version) ? rush : weekly;
}

// readIndex is the ledger's index, null when there is no ledger, and
// undefined when it can't be read: unknown, never an empty one.
async function readIndex(env, cacheTtl) {
  if (!env || !env.LEDGER) return null;
  try {
    const ix = await env.LEDGER.get("index", cacheTtl ? { type: "json", cacheTtl } : { type: "json" });
    if (ix === null) return { list: [] };
    return Array.isArray(ix && ix.list) ? ix : undefined;
  } catch (e) {
    console.log("ledger index", e);
    return undefined;
  }
}

// stable is the Stable release, condensed as latest's is: {rel, degraded},
// or null when it isn't known. rel also says it is Stable's (channel) and
// whether it went there as urgent.
async function stable(ctx, env, now) {
  const hit = await cached("stable");
  if (hit && hit.rel && hit.rel.version) return hit;
  const ix = await readIndex(env, 60);
  if (!ix) return null;
  const pick = stableOf(ix.list.map((r) => ({ version: r.v, published: r.p, urgent: !!r.u })), now);
  if (!pick) return null;
  const tag = "v" + pick.version;
  let rel = await fromAPI(env, "releases/tags/" + tag);
  let degraded = !!rel && detoured(rel);
  if (!rel) {
    // the API said no: the files from SHA256SUMS, the notes from the ledger
    const [entry] = await fromLedger(env, null, null, pick.version, 1);
    rel = await fromSums(tag, entry && entry.version === pick.version ? entry : { notes: "", published: pick.published });
    degraded = true;
  }
  if (!rel) return null;
  const got = { rel: { ...rel, channel: "stable", urgent: !!pick.urgent }, degraded };
  remember(ctx, "stable", got, degraded ? BRIEF : TTL);
  return got;
}

// fromLedger is the notes of the releases the ledger has older than below
// (every one when null), after after (exclusive, "" for any) up to upto,
// newest first, at most most: [{version, notes, url, published}].
async function fromLedger(env, below, after, upto, most) {
  const ix = await readIndex(env, 300);
  if (!ix || !(most > 0)) return [];
  const want = ix.list
    .filter((r) => (!below || newer(below, r.v)) && (!after || newer(r.v, after)) && !newer(r.v, upto))
    .sort((a, b) => (newer(a.v, b.v) ? -1 : newer(b.v, a.v) ? 1 : 0))
    .slice(0, most);
  const days = [...new Set(want.map((r) => dayOf(r.p)))];
  const books = await Promise.all(days.map((d) => env.LEDGER.get("notes:" + d, { type: "json", cacheTtl: 300 }).catch(() => null)));
  const by = Object.fromEntries(days.map((d, i) => [d, books[i] || {}]));
  return want.map((r) => ({ version: r.v, notes: by[dayOf(r.p)][r.v] || "", url: `https://github.com/${REPO}/releases/tag/v${r.v}`, published: r.p }));
}

function dayOf(iso) {
  return String(iso || "").slice(0, 10);
}

// keepLedger is the cron's run: GitHub's newest hundred releases into the
// ledger (or the releases feed's ten, when the API says no), the pages
// after them while the ledger has a hole or doesn't yet reach
// LEDGER_REACH back, at most LEDGER_PAGES asks. A ledger that can't be
// read is left alone, never written over as an empty one.
export async function keepLedger(env, now) {
  const ix = await readIndex(env);
  if (!ix) return;
  const kv = env.LEDGER;
  const had = new Map(ix.list.map((r) => [r.v, r]));
  const got = new Map(); // version: {version, notes, published}
  let asks = 0, bottom = !!ix.bottom;
  const page = async (n) => {
    asks++;
    let res;
    try {
      res = await api(`releases?per_page=100&page=${n}`, env);
    } catch (e) {
      console.log("ledger page", n, e);
      return null;
    }
    if (!res.ok) {
      console.log("ledger page", n, res.status);
      return null;
    }
    const rels = (await res.json())
      .filter((r) => !r.draft && !r.prerelease)
      .map((r) => ({ version: r.tag_name.replace(/^v/, ""), notes: r.body || "", published: r.published_at }));
    if (rels.length < 100) bottom = true;
    for (const r of rels) got.set(r.version, r);
    return rels;
  };
  const first = await page(1);
  if (!first) for (const r of (await fromFeed()) || []) got.set(r.version, r);
  // a hole (the cron stopped for a day): the pages after the first until
  // one meets what the ledger had
  let n = 1;
  for (let last = first; last && last.length === 100 && had.size && !last.some((r) => had.has(r.version)) && asks < LEDGER_PAGES; ) last = await page(++n);
  // reaching back: the page where the ledger's oldest is, then on
  const oldest = () => {
    let o = null;
    for (const r of [...had.values(), ...[...got.values()].map((g) => ({ v: g.version, p: g.published }))]) if (!o || newer(o.v, r.v)) o = r;
    return o;
  };
  let deep = Math.max(n + 1, Math.floor((had.size || got.size) / 100) + 1);
  while (first && !bottom && asks < LEDGER_PAGES) {
    const o = oldest();
    if (o && Date.parse(o.p) <= now - LEDGER_REACH) break;
    if (!(await page(deep++))) break;
  }
  // the index: what was had, what came, nothing older than LEDGER_DAYS
  const keep = now - LEDGER_DAYS * DAY;
  const merged = new Map(had);
  for (const r of got.values()) {
    // the feed's date is when the release was last edited: the API's, or
    // the one the ledger has, is when it came out
    const was = merged.get(r.version);
    merged.set(r.version, { v: r.version, p: !first && was ? was.p : r.published, u: urgentIn(r.notes) });
  }
  const list = [...merged.values()]
    .filter((r) => r.v && Date.parse(r.p) >= keep)
    .sort((a, b) => (newer(a.v, b.v) ? -1 : newer(b.v, a.v) ? 1 : 0));
  const next = { list, bottom };
  if (JSON.stringify(next) !== JSON.stringify({ list: ix.list, bottom: !!ix.bottom })) await kv.put("index", JSON.stringify(next));
  // each day's notes, written where they changed
  const days = new Map();
  for (const r of got.values()) {
    // by the day the index has it out (the feed's date is its last edit)
    const p = merged.get(r.version).p;
    if (!p || Date.parse(p) < keep) continue;
    const d = dayOf(p);
    if (!days.has(d)) days.set(d, []);
    days.get(d).push(r);
  }
  for (const [d, rels] of days) {
    const key = "notes:" + d;
    let book;
    try {
      book = (await kv.get(key, { type: "json" })) || {};
    } catch (e) {
      console.log("ledger notes", d, e);
      continue; // unreadable: left as it is
    }
    let changed = false;
    for (const r of rels) {
      // the feed's notes (HTML made markdown, the Chinese marker guessed)
      // never replace the API's
      if (!first && book[r.version]) continue;
      if (book[r.version] !== r.notes) {
        book[r.version] = r.notes;
        changed = true;
      }
    }
    if (changed) await kv.put(key, JSON.stringify(book), { expirationTtl: (LEDGER_DAYS + 15) * 86400 });
  }
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

// SIG is the signature of SHA256SUMS, a release asset (see signed below).
const SIG = "SHA256SUMS.sig";

// checksums reads a release's SHA256SUMS: {sums: {file name: hash},
// direct, signed}, or null. direct says github.com's download address gave
// it; when that fails, viaAPI (the file's address in GitHub's API) is asked.
// signed is {sums, sig}: SHA256SUMS as published and its ed25519 signature
// (base64, the release's SHA256SUMS.sig, viaAPISig in the API), or absent
// for a release with none. The site only carries them; the app checks the
// signature against the key it holds, so nothing here is trusted, this
// worker included.
async function checksums(tag, env, viaAPI, viaAPISig) {
  let r = null;
  try {
    r = await fetch(`https://github.com/${REPO}/releases/download/${tag}/SHA256SUMS`, { headers: UA });
  } catch (e) {
    console.log("SHA256SUMS", e);
  }
  let direct = !!r && r.ok;
  if (!direct) {
    if (r) console.log("SHA256SUMS", r.status);
    const to = viaAPI && (await located(viaAPI, env));
    if (!to) return null;
    try {
      r = await fetch(to, { headers: UA });
    } catch (e) {
      console.log("SHA256SUMS through the API", e);
      return null;
    }
    if (!r.ok) return null;
  }
  const text = await r.text();
  const sums = {};
  for (const line of text.split("\n")) {
    const [hash, name] = line.trim().split(/\s+\*?/);
    if (hash && name) sums[name] = hash;
  }
  const out = { sums, direct };
  const sig = await signature(tag, env, direct, viaAPISig);
  if (sig) out.signed = { sums: text, sig };
  return out;
}

// signature is the release's SHA256SUMS.sig as the base64 line it holds, or
// "" when the release has none (every release before it was signed).
async function signature(tag, env, direct, viaAPI) {
  let r = null;
  if (direct) {
    try {
      r = await fetch(`https://github.com/${REPO}/releases/download/${tag}/${SIG}`, { headers: UA });
    } catch (e) {
      console.log(SIG, e);
    }
  }
  if (!r || !r.ok) {
    const to = viaAPI && (await located(viaAPI, env));
    if (!to) return "";
    try {
      r = await fetch(to, { headers: UA });
    } catch (e) {
      console.log(SIG, "through the API", e);
      return "";
    }
  }
  if (!r.ok) return "";
  const t = (await r.text()).trim();
  return /^[A-Za-z0-9+/=]{80,100}$/.test(t) ? t : "";
}

// ---- icons ------------------------------------------------------------------

// An icon a plugin's package.json, a market listing or an import link names
// is fetched here, not by the app, so its host learns nothing about who
// looks: a plugin author's https picture would otherwise see the IP of
// everyone who opened Discover. Nothing of the asker's request is passed
// on, only the picture comes back, and Cloudflare keeps it a day.
export const ICON_MAX = 1 << 20; // the app's MaxIcon

export async function icon(raw) {
  let u;
  try {
    u = new URL(raw);
  } catch {
    return new Response("not a URL\n", { status: 400 });
  }
  if (u.protocol !== "https:" || u.username || u.password || (u.port && u.port !== "443"))
    return new Response("only a public https picture\n", { status: 400 });
  // the app asks for usemagpie.ai's own pictures straight; asking here for
  // one would only send the worker round to itself
  if (u.hostname === "usemagpie.ai" || u.hostname.endsWith(".usemagpie.ai"))
    return new Response("a usemagpie.ai picture is fetched as it is\n", { status: 400 });
  let res;
  try {
    res = await fetch(u.toString(), {
      headers: { Accept: "image/*", "User-Agent": "magpie-icon/1 (+https://usemagpie.ai)" },
      redirect: "follow",
      signal: AbortSignal.timeout(10000),
      // a missing or failing picture is asked for again soon, not in a day
      cf: { cacheEverything: true, cacheTtlByStatus: { "200-299": 86400, "404": 300, "500-599": 0 } },
    });
  } catch (e) {
    return new Response("could not fetch the picture\n", { status: 502 });
  }
  if (!res.ok) return new Response(`the picture's host answered ${res.status}\n`, { status: 502 });
  const b = await readUpTo(res, ICON_MAX + 1);
  if (b.byteLength > ICON_MAX) return new Response("the picture is over 1 MB\n", { status: 413 });
  const type = imageType(new Uint8Array(b));
  if (!type) return new Response("not a picture\n", { status: 415 });
  return new Response(b, {
    headers: {
      "Content-Type": type,
      "Cache-Control": "public, max-age=86400",
      "X-Content-Type-Options": "nosniff",
      "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; sandbox",
    },
  });
}

async function readUpTo(res, max) {
  const r = res.body.getReader();
  const parts = [];
  let n = 0;
  while (n < max) {
    const { done, value } = await r.read();
    if (done) break;
    parts.push(value);
    n += value.byteLength;
  }
  r.cancel().catch(() => {});
  const out = new Uint8Array(Math.min(n, max));
  let at = 0;
  for (const p of parts) {
    const take = p.subarray(0, out.length - at);
    out.set(take, at);
    at += take.length;
    if (at >= out.length) break;
  }
  return out.buffer;
}

// imageType is the picture's type by its bytes, as the app's StoreIcon
// takes them: PNG, JPEG, GIF, WebP, ICO or SVG; "" for anything else.
export function imageType(b) {
  const at = (i, ...xs) => xs.every((x, k) => b[i + k] === x);
  if (at(0, 0x89, 0x50, 0x4e, 0x47)) return "image/png";
  if (at(0, 0xff, 0xd8, 0xff)) return "image/jpeg";
  if (at(0, 0x47, 0x49, 0x46, 0x38)) return "image/gif";
  if (at(0, 0x52, 0x49, 0x46, 0x46) && at(8, 0x57, 0x45, 0x42, 0x50)) return "image/webp";
  if (at(0, 0, 0, 1, 0)) return "image/x-icon";
  if (new TextDecoder().decode(b.subarray(0, 1024)).toLowerCase().includes("<svg")) return "image/svg+xml";
  return "";
}

function json(v, status = 200, headers = {}) {
  return new Response(JSON.stringify(v), {
    status,
    headers: { "Content-Type": "application/json", "Access-Control-Allow-Origin": "*", ...headers },
  });
}

// taggedPlugins is the Plugins page's Discover list, built every half hour
// by the plugins repo's `tagged` workflow onto its dist branch, passed on
// for the desktops that can't reach GitHub (River_WLan on X). The edge
// keeps it ten minutes, and the last whole copy stands in for a failed ask.
const TAGGED = [
  "https://raw.githubusercontent.com/magpie-community/plugins/dist/tagged.json",
  "https://cdn.jsdelivr.net/gh/magpie-community/plugins@dist/tagged.json",
];
export async function taggedPlugins(ctx) {
  const hit = await cached("tagged");
  if (hit && Array.isArray(hit.plugins)) return json(hit, 200, { "Cache-Control": "public, max-age=600" });
  for (const u of TAGGED) {
    try {
      const res = await fetch(u, { headers: UA });
      if (!res.ok) continue;
      const got = await res.json();
      if (got.version !== 1 || !Array.isArray(got.plugins) || !got.plugins.length) continue;
      remember(ctx, "tagged", got, 600);
      remember(ctx, "tagged-good", got, KEEP);
      return json(got, 200, { "Cache-Control": "public, max-age=600" });
    } catch (e) {
      console.log("tagged", u, e);
    }
  }
  const good = await cached("tagged-good");
  if (good && Array.isArray(good.plugins)) return json(good, 200, { "Cache-Control": "public, max-age=60" });
  return json({ error: "no list yet" }, 503, NO_STORE);
}

// pluginReadme is a Discover plugin's README, as the `tagged` workflow keeps
// it on the dist branch (readmes/<key>.md), passed on for the desktops npm's
// human check or GitHub's block holds back (River_WLan on X). name is the
// package's npm name or github:owner/repo, as magpie installs it. The edge
// keeps a README ten minutes; one the branch doesn't have is a 404, which
// magpie answers by asking npm or GitHub itself.
const PKG_NAME = /^(@[a-z0-9][a-z0-9._~-]*\/)?[a-z0-9][a-z0-9._~-]*$/;
export function readmeKey(name) {
  name = String(name ?? "");
  if (name.startsWith("github:")) {
    const repo = name.slice(7).split("#")[0];
    return /^[\w.-]+\/[\w.-]+$/.test(repo) && !repo.includes("..") ? "github/" + repo : "";
  }
  return PKG_NAME.test(name) ? name : "";
}
export async function pluginReadme(ctx, name) {
  const key = readmeKey(name);
  if (!key) return json({ error: "not a plugin name" }, 400, NO_STORE);
  const hit = await cached("readme/" + key);
  if (hit && typeof hit.readme === "string") return readmeText(hit.readme, 600);
  for (const base of [
    "https://raw.githubusercontent.com/magpie-community/plugins/dist/readmes/",
    "https://cdn.jsdelivr.net/gh/magpie-community/plugins@dist/readmes/",
  ]) {
    try {
      const res = await fetch(base + key.split("/").map(encodeURIComponent).join("/") + ".md", { headers: UA });
      if (!res.ok) continue;
      const readme = await res.text();
      if (!readme.trim()) continue;
      remember(ctx, "readme/" + key, { readme }, 600);
      return readmeText(readme, 600);
    } catch (e) {
      console.log("readme", key, e);
    }
  }
  return json({ error: "no README kept" }, 404, NO_STORE);
}
function readmeText(readme, age) {
  return new Response(readme, { status: 200, headers: { "Content-Type": "text/markdown; charset=utf-8", "Cache-Control": `public, max-age=${age}` } });
}
