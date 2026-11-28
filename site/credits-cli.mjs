#!/usr/bin/env node
// The people magpie's history credits (credits.mjs), from git and GitHub.
//
//   node site/credits-cli.mjs thanks <from>..<to>
//     the thanks line for a release's notes: English on the first line,
//     Chinese on the second, nothing when no one is credited. yetone/
//     magpie-releases' release run calls it for the commits since the last
//     tag.
//   node site/credits-cli.mjs wall [out]
//     public/contributors.json (or out) from every tag's commits, and the
//     contributors page's table in each language beside it
//     (public/contributors/en.html, zh, ja), which worker.js puts into the
//     page. wrangler.jsonc's build runs it at each deploy.
//
// The issue and PR authors a subject's "(#N)" names are asked of GitHub's
// GraphQL API with GH_TOKEN, GITHUB_TOKEN or `gh auth token`. When that
// can't be read, those people are left out (and `wall` keeps the authors
// its last output knew); the rest still goes out. The same API gives each
// credited GitHub user's public X account (twitter_username), so an X
// credit under that handle is that GitHub person; a user it can't read
// joins nobody (and `wall` keeps the accounts its last output knew). Names
// in contributors-exclude.txt are never given.
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync, renameSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { credit, everyone, thanks, wall, refKey, parse, renderWall, logins } from "./credits.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const warn = (m) => process.stderr.write(`credits: ${m}\n`);

const SEP = "\x1f", END = "\x1e";
function log(args) {
  const out = execFileSync("git", ["log", `--format=%H${SEP}%P${SEP}%an${SEP}%ae${SEP}%s${SEP}%(trailers:key=Co-authored-by,valueonly,separator=%x1d)${SEP}%D${END}`, ...args], { encoding: "utf8", maxBuffer: 1 << 30 });
  return out.split(END).map((r) => r.replace(/^\n/, "")).filter(Boolean).map((r) => {
    const [sha, parents, author, email, subject, co, refs] = r.split(SEP);
    return {
      sha, author, email, subject,
      parents: parents.trim() ? parents.trim().split(" ").length : 0,
      // the address only tells an agent from a person; it is never written out
      coauthors: (co || "").split("\x1d").map((s) => s.trim()).filter(Boolean).map((s) => {
        const m = s.match(/^(.*?)\s*<([^>]*)>$/);
        return m ? { name: m[1].trim(), email: m[2].trim() } : { name: s, email: "" };
      }),
      tags: (refs || "").split(", ").map((s) => s.replace(/^tag: /, "")).filter((t) => /^v\d+\.\d+\.\d+$/.test(t)),
    };
  });
}

export function excluded(file = join(here, "contributors-exclude.txt")) {
  try {
    return readFileSync(file, "utf8").split("\n").map((l) => l.replace(/#.*/, "").trim()).filter(Boolean);
  } catch { return []; }
}

function token() {
  if (process.env.GH_TOKEN) return process.env.GH_TOKEN;
  if (process.env.GITHUB_TOKEN) return process.env.GITHUB_TOKEN;
  try { return execFileSync("gh", ["auth", "token"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim(); } catch { return ""; }
}

// "repo#n" → login (a bot's with "[bot]"), "" for no author; refs it
// couldn't read are left out of the map
export async function authors(refs, { known = {}, fetchImpl = globalThis.fetch, tok = token() } = {}) {
  const got = { ...known };
  const want = new Map();
  for (const r of refs) if (!(refKey(r) in got)) want.set(refKey(r), r);
  if (!want.size) return got;
  if (!tok) { warn(`no GitHub token: ${want.size} issue authors left out`); return got; }
  const byRepo = new Map();
  for (const r of want.values()) (byRepo.get(r.repo) || byRepo.set(r.repo, []).get(r.repo)).push(r.n);
  for (const [repo, ns] of byRepo) {
    const [owner, name] = repo.split("/");
    for (let i = 0; i < ns.length; i += 100) {
      const chunk = ns.slice(i, i + 100);
      const who = "author { login __typename }";
      const q = `query { repository(owner: ${JSON.stringify(owner)}, name: ${JSON.stringify(name)}) { ${chunk.map((n) => `i${n}: issueOrPullRequest(number: ${n}) { ... on Issue { ${who} } ... on PullRequest { ${who} } }`).join(" ")} } }`;
      try {
        const res = await fetchImpl("https://api.github.com/graphql", {
          method: "POST",
          headers: { Authorization: `bearer ${tok}`, "Content-Type": "application/json", "User-Agent": "magpie-credits" },
          body: JSON.stringify({ query: q }),
          signal: AbortSignal.timeout(30000),
        });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const body = await res.json();
        const repoData = body && body.data && body.data.repository;
        if (!repoData) throw new Error(JSON.stringify(body.errors || body).slice(0, 300));
        for (const n of chunk) {
          const item = repoData[`i${n}`];
          if (item === undefined) continue; // not answered: unknown, asked again next time
          const a = item && item.author;
          got[`${repo}#${n}`] = a ? (a.__typename === "Bot" ? `${a.login}[bot]` : a.login) : "";
        }
      } catch (e) {
        warn(`${repo}: issue authors not read (${e.message}); those people are left out`);
      }
    }
  }
  return got;
}

// GitHub login → the X handle its public profile names, asked fresh each
// time. Only logins with one are in the map. A login GitHub doesn't answer
// keeps what known had for it, or is left out: unknown joins nobody.
export async function xAccounts(users, { known = {}, fetchImpl = globalThis.fetch, tok = token() } = {}) {
  const got = {};
  const kept = (l) => { if (known[l]) got[l] = known[l]; };
  const list = [...new Set(users.filter((l) => /^[A-Za-z0-9-]{1,39}$/.test(l)))];
  if (!list.length) return got;
  if (!tok) { warn(`no GitHub token: X accounts of ${list.length} people not read`); list.forEach(kept); return got; }
  for (let i = 0; i < list.length; i += 100) {
    const chunk = list.slice(i, i + 100);
    const q = `query { ${chunk.map((l, j) => `u${j}: user(login: ${JSON.stringify(l)}) { login twitterUsername }`).join(" ")} }`;
    try {
      const res = await fetchImpl("https://api.github.com/graphql", {
        method: "POST",
        headers: { Authorization: `bearer ${tok}`, "Content-Type": "application/json", "User-Agent": "magpie-credits" },
        body: JSON.stringify({ query: q }),
        signal: AbortSignal.timeout(30000),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const body = await res.json();
      const data = body && body.data;
      if (!data) throw new Error(JSON.stringify((body && body.errors) || body).slice(0, 300));
      chunk.forEach((l, j) => {
        const u = data[`u${j}`];
        if (u === undefined) return kept(l); // not answered
        // null: no such user any more, so no X account
        if (u && u.twitterUsername && String(u.login).toLowerCase() === l.toLowerCase()) got[l] = String(u.twitterUsername);
      });
    } catch (e) {
      warn(`X accounts not read (${e.message}); those people keep their last or none`);
      chunk.forEach(kept);
    }
  }
  return got;
}

async function cmdThanks(range) {
  if (!range) throw new Error("usage: credits-cli.mjs thanks <from>..<to>");
  const commits = log(["--no-merges", range]);
  const refs = commits.flatMap((c) => parse(c.subject).refs);
  const issueAuthors = await authors(refs);
  const exclude = excluded();
  const x = await xAccounts(logins(credit(commits, { issueAuthors, exclude })));
  const people = everyone(credit(commits, { issueAuthors, exclude, xAccounts: x }));
  const en = thanks(people, "en");
  if (en) process.stdout.write(en + "\n" + thanks(people, "zh") + "\n");
}

async function cmdWall(out = join(here, "public", "contributors.json")) {
  let before = {};
  try { before = JSON.parse(readFileSync(out, "utf8")); } catch {}
  let commits;
  try {
    commits = log(["--topo-order", "--reverse", "--decorate-refs=refs/tags/v*", "HEAD"]);
  } catch (e) {
    warn(`git log failed (${e.message.split("\n")[0]}); ${existsSync(out) ? "the last list is kept" : "no list is written"}`);
    return;
  }
  // a commit goes out in the first tag at or after it
  let pending = [];
  for (const c of commits) {
    pending.push(c);
    if (c.tags.length) {
      const v = c.tags.map((t) => t.slice(1)).sort((a, b) => a.localeCompare(b, "en", { numeric: true }))[0];
      for (const p of pending) p.v = v;
      pending = [];
    }
  }
  const released = commits.filter((c) => c.v && c.parents < 2);
  const refs = released.flatMap((c) => parse(c.subject).refs);
  const issueAuthors = await authors(refs, { known: before.issueAuthors || {} });
  const exclude = excluded();
  const x = await xAccounts(logins(credit(released, { issueAuthors, exclude })), { known: before.xAccounts || {} });
  const people = wall(released, { issueAuthors, exclude, xAccounts: x });
  const data = { generated: new Date().toISOString(), head: commits.length ? commits.at(-1).sha : "", issueAuthors, xAccounts: x, people };
  const put = (file, text) => { writeFileSync(file + ".tmp", text); renameSync(file + ".tmp", file); };
  put(out, JSON.stringify(data) + "\n");
  const dir = join(dirname(out), "contributors");
  mkdirSync(dir, { recursive: true });
  for (const lang of ["en", "zh", "ja"]) put(join(dir, `${lang}.html`), renderWall(data, lang) + "\n");
  warn(`${people.length} people over ${released.length} released commits → ${out}`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const [cmd, arg] = process.argv.slice(2);
  const run = cmd === "thanks" ? cmdThanks(arg) : cmd === "wall" ? cmdWall(arg) : Promise.reject(new Error("usage: credits-cli.mjs thanks <range> | wall [out]"));
  run.catch((e) => { warn(e.message); process.exit(cmd === "wall" ? 0 : 1); });
}
