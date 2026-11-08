// A release's files on R2 (the RELEASES bucket), GitHub behind them.
//
// github.com's download address can fail for hours (504 on every file,
// 2026-11-08), so the files the update feed and /download hand out are
// served from R2 here, at /files/<tag>/<file>. GitHub stays the source:
// the cron (mirror) copies the newest release and the Stable one from it
// into R2, and R2 takes a file only when its SHA-256 is the one the
// release's SHA256SUMS gives (put's sha256). A file R2 doesn't have, or
// can't give, is sent on to GitHub's own address (302), and the feed names
// R2 only for files it has with that hash, so nothing is ever only on R2.
// The app checks every file against the feed's hash wherever it came from.
//
// Each release is about 1 GB; the tags the feeds name and the KEEP newest
// besides are kept, the rest deleted (prune), and an older tag's files go
// to GitHub.

const KEEP = 4;
// a cron run starts no new copy after this long, so the next run (five
// minutes on) doesn't copy the same file beside it
const BUDGET = 4 * 60e3;

export function githubFile(repo, tag, name) {
  return `https://github.com/${repo}/releases/download/${tag}/${name}`;
}

// serve answers GET and HEAD for /files/<tag>/<name> from R2, a Range
// included, and sends anything R2 can't give to GitHub.
export async function serve(req, env, repo, tag, name) {
  const toGitHub = () => new Response(null, { status: 302, headers: { Location: githubFile(repo, tag, name), "Cache-Control": "no-store" } });
  if (req.method !== "GET" && req.method !== "HEAD") return new Response("method not allowed\n", { status: 405, headers: { Allow: "GET, HEAD" } });
  if (!env.RELEASES) return toGitHub();
  const key = `${tag}/${name}`;
  let obj = null;
  try {
    obj = req.method === "HEAD" ? await env.RELEASES.head(key) : await env.RELEASES.get(key, { range: req.headers, onlyIf: req.headers });
  } catch (e) {
    // a Range R2 refuses, or R2 failing: GitHub answers it its own way
    console.log("r2 get", key, String(e));
    return toGitHub();
  }
  if (!obj) return toGitHub();
  const headers = new Headers();
  obj.writeHttpMetadata(headers);
  headers.set("ETag", obj.httpEtag);
  headers.set("Accept-Ranges", "bytes");
  // a tag's file never changes
  headers.set("Cache-Control", "public, max-age=31536000, immutable");
  if (req.method === "HEAD") {
    headers.set("Content-Length", String(obj.size));
    return new Response(null, { status: 200, headers });
  }
  // onlyIf not met: the object comes without its body
  if (!("body" in obj) || !obj.body) return new Response(null, { status: req.headers.has("If-None-Match") ? 304 : 412, headers });
  const r = obj.range;
  if (req.headers.has("Range") && r) {
    const start = "suffix" in r ? Math.max(0, obj.size - r.suffix) : r.offset || 0;
    const length = "suffix" in r ? obj.size - start : r.length ?? obj.size - start;
    headers.set("Content-Range", `bytes ${start}-${start + length - 1}/${obj.size}`);
    headers.set("Content-Length", String(length));
    return new Response(obj.body, { status: 206, headers });
  }
  headers.set("Content-Length", String(obj.size));
  return new Response(obj.body, { status: 200, headers });
}

// held is what R2 has of tag: {name: sha256}, the hash each was put with.
// null when R2 can't say.
export async function held(env, tag) {
  if (!env.RELEASES) return null;
  const out = {};
  let cursor;
  try {
    do {
      const page = await env.RELEASES.list({ prefix: tag + "/", cursor, include: ["customMetadata"] });
      for (const o of page.objects) out[o.key.slice(tag.length + 1)] = (o.customMetadata && o.customMetadata.sha256) || "";
      cursor = page.truncated ? page.cursor : undefined;
    } while (cursor);
  } catch (e) {
    console.log("r2 list", tag, String(e));
    return null;
  }
  return out;
}

// onR2 is rel with each file R2 holds, with the hash rel gives, at
// site/files/<tag>/<file>; every file also says its github.com address
// (github), which the app takes when the site's fails. have is held's.
export function onR2(rel, have, repo, site) {
  if (!rel || !rel.assets) return rel;
  const tag = "v" + rel.version;
  const assets = {};
  for (const [name, a] of Object.entries(rel.assets)) {
    const github = githubFile(repo, tag, name);
    const there = have && a.sha256 && have[name] === a.sha256;
    assets[name] = there ? { ...a, url: `${site}/files/${tag}/${encodeURIComponent(name)}`, github } : { ...a, github };
  }
  return { ...rel, assets };
}

// mirror copies into R2 each file of rels (the feeds' releases, with
// their hashes) it doesn't hold yet, then prunes. open(name, asset, tag)
// is the file's bytes from GitHub (a Response), null when GitHub won't
// give them. Returns {copied, failed}.
export async function mirror(env, rels, open, now = Date.now) {
  const done = { copied: [], failed: [] };
  if (!env.RELEASES) return done;
  const until = now() + BUDGET;
  const tags = [];
  for (const rel of rels) {
    if (!rel || !rel.version || !rel.assets) continue;
    const tag = "v" + rel.version;
    if (tags.includes(tag)) continue;
    tags.push(tag);
    const have = await held(env, tag);
    if (!have) continue;
    for (const [name, a] of Object.entries(rel.assets)) {
      if (!a.sha256 || have[name] === a.sha256) continue;
      if (now() > until) return done;
      if (await copy(env, tag, name, a, open)) done.copied.push(`${tag}/${name}`);
      else done.failed.push(`${tag}/${name}`);
    }
  }
  await prune(env, tags);
  return done;
}

async function copy(env, tag, name, a, open) {
  let res = null;
  try {
    res = await open(name, a, tag);
  } catch (e) {
    console.log("r2 copy: github", tag, name, String(e));
  }
  if (!res || !res.ok || !res.body) {
    console.log("r2 copy: github", tag, name, res && res.status);
    return false;
  }
  // R2 takes a stream of a known length: GitHub's own, or the feed's size
  const length = Number(res.headers.get("Content-Length")) || a.size || 0;
  let body = res.body;
  if (length > 0 && typeof FixedLengthStream === "function") {
    const fixed = new FixedLengthStream(length);
    res.body.pipeTo(fixed.writable).catch(() => {});
    body = fixed.readable;
  }
  try {
    await env.RELEASES.put(`${tag}/${name}`, body, {
      sha256: a.sha256,
      httpMetadata: { contentType: "application/octet-stream", contentDisposition: `attachment; filename="${name}"` },
      customMetadata: { sha256: a.sha256 },
    });
    return true;
  } catch (e) {
    // a hash that isn't the release's is refused here, and nothing kept
    console.log("r2 copy: put", tag, name, String(e));
    return false;
  }
}

// prune deletes every tag but keep and the KEEP newest besides.
async function prune(env, keep) {
  let tags = [], cursor;
  try {
    do {
      const page = await env.RELEASES.list({ delimiter: "/", cursor });
      tags.push(...page.delimitedPrefixes.map((p) => p.replace(/\/$/, "")));
      cursor = page.truncated ? page.cursor : undefined;
    } while (cursor);
  } catch (e) {
    console.log("r2 prune: list", String(e));
    return;
  }
  const rest = tags.filter((t) => !keep.includes(t)).sort((a, b) => byVersion(b, a));
  for (const tag of rest.slice(KEEP)) {
    const have = await held(env, tag);
    if (!have) continue;
    const keys = Object.keys(have).map((n) => `${tag}/${n}`);
    try {
      if (keys.length) await env.RELEASES.delete(keys);
    } catch (e) {
      console.log("r2 prune: delete", tag, String(e));
    }
  }
}

function byVersion(a, b) {
  const pa = a.replace(/^v/, "").split(".").map(Number), pb = b.replace(/^v/, "").split(".").map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] || 0) - (pb[i] || 0);
    if (d) return d;
  }
  return 0;
}
