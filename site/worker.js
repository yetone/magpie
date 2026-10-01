// usemagpie.ai. Releases live on GitHub (yetone/magpie-releases); this
// worker turns the newest one into the update feed the app reads and into
// download links that never go stale.
//
//   /api/latest            {version, notes, url, published, assets: {name: {url, size, sha256}}}
//   /api/notes?after=&upto= {releases: [{version, notes, url, published}]}, newest
//                          first: what changed since the version an app last ran
//   /download              the Apple Silicon dmg
//   /download/mac-arm64    the same;  /download/mac-intel  the Intel dmg
//   /download/windows      the Windows app (x64);  /download/windows-arm64
//   /download/linux        the Linux app (x86-64); /download/linux-arm64
//   /download/<file>       any file of the newest release, by name
//   /docs, /docs/zh        the getting-started guide: /docs/start, /docs/zh/start
//
// Everything else is the static site in public/.

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
const DOCS = { "/docs": "/docs/start", "/docs/zh": "/docs/zh/start" };

export default {
  async fetch(req, env, ctx) {
    const url = new URL(req.url);
    if (url.hostname.startsWith("www.")) {
      url.hostname = url.hostname.slice(4);
      return Response.redirect(url.toString(), 301);
    }
    if (url.pathname === "/api/latest") {
      const rel = await latest(ctx);
      if (!rel) return json({ error: "no release yet" }, 503);
      return json(rel, 200, { "Cache-Control": `public, max-age=${TTL}` });
    }
    if (url.pathname === "/api/notes") {
      const list = await releases(ctx);
      if (!list) return json({ error: "no releases" }, 503);
      const after = url.searchParams.get("after"), upto = url.searchParams.get("upto");
      const pick = list.filter((r) => (!after || newer(r.version, after)) && (!upto || !newer(r.version, upto)));
      return json({ releases: pick }, 200, { "Cache-Control": `public, max-age=${TTL}` });
    }
    if (url.pathname === "/download" || url.pathname.startsWith("/download/")) {
      const want = url.pathname.split("/")[2] || "mac-arm64";
      const rel = await latest(ctx);
      const asset = rel && rel.assets[SHORT[want] || want];
      if (!asset) return new Response("not found\n", { status: 404 });
      return Response.redirect(asset.url, 302);
    }
    const guide = DOCS[url.pathname.replace(/\/+$/, "")];
    if (guide) return Response.redirect(new URL(guide, url).toString(), 302);
    return beacon(await env.ASSETS.fetch(req));
  },
};

// beacon adds Cloudflare Web Analytics to a page. The dashboard's automatic
// injection skips whatever a worker returns, and every page passes through
// this one.
const BEACON = `<script defer src="https://static.cloudflareinsights.com/beacon.min.js" data-cf-beacon='{"token": "c09e76abf16b40b2aa5571c196efb847"}'></script>`;

function beacon(res) {
  if (!(res.headers.get("Content-Type") || "").startsWith("text/html")) return res;
  return new HTMLRewriter().on("body", { element: (el) => el.append(BEACON, { html: true }) }).transform(res);
}

// latest is the newest release, condensed, with each file's SHA-256 taken
// from the release's SHA256SUMS. GitHub's API gives the notes and sizes but
// limits anonymous callers by IP, and a worker shares its IP with many
// others; when the API says no, the release page's redirect gives the
// version and SHA256SUMS the files, which is all an update needs.
async function latest(ctx) {
  const cache = caches.default;
  const key = new Request("https://usemagpie.ai/__latest");
  const hit = await cache.match(key);
  if (hit) return hit.json();

  const rel = (await fromAPI()) || (await fromPages());
  if (!rel) return null;
  ctx.waitUntil(cache.put(key, json(rel, 200, { "Cache-Control": `max-age=${TTL}` })));
  return rel;
}

const UA = { "User-Agent": "usemagpie.ai" };

async function fromAPI() {
  const res = await fetch(`https://api.github.com/repos/${REPO}/releases/latest`, {
    headers: { ...UA, Accept: "application/vnd.github+json" },
  });
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
  const res = await fetch(`https://github.com/${REPO}/releases/latest`, { headers: UA, redirect: "manual" });
  const tag = (res.headers.get("Location") || "").split("/tag/")[1];
  if (!tag) {
    console.log("github latest redirect", res.status);
    return null;
  }
  const sums = await checksums(tag);
  if (!sums) return null;
  const url = `https://github.com/${REPO}/releases/tag/${tag}`;
  const rel = { version: tag.replace(/^v/, ""), notes: "", url, published: null, assets: {} };
  for (const [name, sha256] of Object.entries(sums)) {
    rel.assets[name] = { url: `https://github.com/${REPO}/releases/download/${tag}/${name}`, size: 0, sha256 };
  }
  return rel;
}

// releases is the newest hundred releases' notes, newest first, kept as
// long as the newest release is. Drafts and pre-releases are left out.
async function releases(ctx) {
  const cache = caches.default;
  const key = new Request("https://usemagpie.ai/__releases");
  const hit = await cache.match(key);
  if (hit) return hit.json();
  const res = await fetch(`https://api.github.com/repos/${REPO}/releases?per_page=100`, {
    headers: { ...UA, Accept: "application/vnd.github+json" },
  });
  if (!res.ok) {
    console.log("github api releases", res.status, await res.text());
    return null;
  }
  const list = (await res.json())
    .filter((r) => !r.draft && !r.prerelease)
    .map((r) => ({ version: r.tag_name.replace(/^v/, ""), notes: r.body || "", url: r.html_url, published: r.published_at }));
  ctx.waitUntil(cache.put(key, json(list, 200, { "Cache-Control": `max-age=${TTL}` })));
  return list;
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
  const r = await fetch(`https://github.com/${REPO}/releases/download/${tag}/SHA256SUMS`, { headers: UA });
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
