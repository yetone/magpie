// search-index.mjs builds public/search/<lang>.json, what the docs' search
// (public/docs-search.js) looks through: each docs page in that language,
// its title and its sections, as plain text. Run `node search-index.mjs`
// after changing a docs page; a test fails while a committed index differs
// from what this builds.
import { readFile, readdir } from "node:fs/promises";

const pub = new URL("./public/", import.meta.url);
export const LANGS = { en: "docs/", zh: "docs/zh/", ja: "docs/ja/" };

const ENT = { amp: "&", lt: "<", gt: ">", quot: '"', "#39": "'", nbsp: " ", rarr: "→", larr: "←", hellip: "…", mdash: "—", ndash: "–", middot: "·" };
const unent = (s) => s.replace(/&(#x[0-9a-f]+|#\d+|\w+);/gi, (m, e) => {
  if (e[0] === "#") return String.fromCodePoint(e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
  return ENT[e] ?? m;
});
// text is a fragment of HTML as one line of plain text; a step's bold
// first words (ol.steps) keep a space from the words after them
const text = (h) => unent(h.replace(/(<li[^>]*>\s*<b>[\s\S]*?<\/b>)/g, "$1 ").replace(/<br\s*\/?>/g, " ").replace(/<\/(p|li|h[1-6]|tr|td|th|pre|div)>/g, " ").replace(/<[^>]+>/g, "")).replace(/\s+/g, " ").trim();

// page reads one docs page: its title (the contents' name for it, else the
// h1) and its sections, each the
// id of its h2 ("" for what comes before the first), the h2's words and
// the text under it. An h3 stays in its h2's section.
const GENERIC = new Set(["On this page", "本页内容", "このページ"]);
export function page(html, path) {
  const m = html.match(/<article>([\s\S]*?)<\/article>/);
  if (!m) return null;
  const a = m[1]
    .replace(/<p class="eyebrow"[\s\S]*?<\/p>/g, "")
    .replace(/<button[\s\S]*?<\/button>/g, "")
    .replace(/<span class="lang">[\s\S]*?<\/span>/g, "")
    .replace(/<script[\s\S]*?<\/script>/g, "")
    .replace(/<style[\s\S]*?<\/style>/g, "");
  // the page's short name: the nav's link to it (what the contents list),
  // else its contents' heading, else its h1
  const h1 = a.match(/<h1[^>]*>([\s\S]*?)<\/h1>/);
  const links = (html.match(/<div class="links">([\s\S]*?)<\/div>/) || [])[1] || "";
  const link = path && [...links.matchAll(/<a\b([^>]*)>([\s\S]*?)<\/a>/g)].find((m) => m[1].includes(`href="${path}"`));
  const short = html.match(/<aside class="toc">\s*<p>([\s\S]*?)<\/p>/);
  const title = link ? text(link[2]) : short && !GENERIC.has(text(short[1])) ? text(short[1]) : h1 ? text(h1[1]) : "";
  const parts = a.split(/(?=<h2\b)/);
  const sections = [];
  for (const [i, p] of parts.entries()) {
    let id = "", head = "", body = p;
    if (i > 0 || /^<h2\b/.test(p)) {
      const h = p.match(/^<h2([^>]*)>([\s\S]*?)<\/h2>/);
      if (!h) continue;
      id = (h[1].match(/\bid="([^"]+)"/) || [])[1] || "";
      head = text(h[2]);
      body = p.slice(h[0].length);
    } else body = body.replace(/<h1[^>]*>[\s\S]*?<\/h1>/, "");
    const t = text(body);
    if (!head && !t) continue;
    sections.push([id, head, t]);
  }
  return { title, sections };
}

// build returns each language's index, by language: [[path, title,
// [[id, heading, text], …]], …], the pages in name order.
export async function build() {
  const out = {};
  for (const [lang, dir] of Object.entries(LANGS)) {
    const files = (await readdir(new URL(dir, pub))).filter((f) => f.endsWith(".html") && f !== "contributors.html").sort();
    const pages = [];
    for (const f of files) {
      const path = "/" + dir + f.slice(0, -5);
      const p = page(await readFile(new URL(dir + f, pub), "utf8"), path);
      if (p && p.title) pages.push([path, p.title, p.sections]);
    }
    out[lang] = JSON.stringify(pages) + "\n";
  }
  return out;
}

// run as a script, it writes every file built from the docs (docs-index.mjs)
if (import.meta.url === `file://${process.argv[1]}`) {
  await (await import("./docs-index.mjs")).writeAll();
}
