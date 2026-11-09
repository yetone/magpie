// llms-full.mjs builds public/llms-full.txt: every English docs page's
// article as Markdown, for AI assistants that read a site in one file.
// Run `node llms-full.mjs` after changing a docs page; a test fails while
// the committed file differs from what this builds.
import { readFile, readdir, writeFile } from "node:fs/promises";

const pub = new URL("./public/", import.meta.url);

const ENT = { amp: "&", lt: "<", gt: ">", quot: '"', "#39": "'", nbsp: " ", rarr: "→", larr: "←", hellip: "…", mdash: "—", ndash: "–" };
const unent = (s) => s.replace(/&(#x[0-9a-f]+|#\d+|\w+);/gi, (m, e) => {
  if (e[0] === "#") return String.fromCodePoint(e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
  return ENT[e] ?? m;
});

// inline turns a fragment of inline HTML into Markdown.
function inline(h) {
  return unent(
    h
      .replace(/<code>([\s\S]*?)<\/code>/g, (_, c) => "`" + c.replace(/<[^>]+>/g, "") + "`")
      .replace(/<a [^>]*href="([^"]+)"[^>]*>([\s\S]*?)<\/a>/g, (_, href, t) => {
        const text = t.replace(/<[^>]+>/g, "").trim();
        if (href.startsWith("#")) return text;
        const abs = href.startsWith("/") ? "https://usemagpie.ai" + href : href;
        return `[${text}](${abs})`;
      })
      .replace(/<(b|strong)>([\s\S]*?)<\/\1>/g, "**$2**")
      .replace(/<(i|em)>([\s\S]*?)<\/\1>/g, "*$2*")
      .replace(/<br\s*\/?>/g, " ")
      .replace(/<[^>]+>/g, ""),
  ).replace(/\s+/g, " ").trim();
}

// article converts one docs page's <article> to Markdown.
export function article(html) {
  const m = html.match(/<article>([\s\S]*?)<\/article>/);
  if (!m) return "";
  let a = m[1]
    .replace(/<p class="eyebrow"[\s\S]*?<\/p>/g, "")
    .replace(/<button[\s\S]*?<\/button>/g, "")
    .replace(/<span class="lang">[\s\S]*?<\/span>/g, "")
    .replace(/<script[\s\S]*?<\/script>/g, "");
  const out = [];
  const blocks = /<(h[1-4])[^>]*>([\s\S]*?)<\/\1>|<pre[^>]*>([\s\S]*?)<\/pre>|<(ol|ul)[^>]*>([\s\S]*?)<\/\4>|<table[^>]*>([\s\S]*?)<\/table>|<p[^>]*>([\s\S]*?)<\/p>|<div class="callout"[^>]*>([\s\S]*?)<\/div>|<(?:details)[^>]*>\s*<summary[^>]*>([\s\S]*?)<\/summary>/g;
  let b;
  while ((b = blocks.exec(a))) {
    if (b[1]) out.push("#".repeat(Number(b[1][1])) + " " + inline(b[2]));
    else if (b[3] !== undefined) out.push("```\n" + unent(b[3].replace(/<[^>]+>/g, "")).replace(/\n+$/, "") + "\n```");
    else if (b[4]) {
      const items = [...b[5].matchAll(/<li[^>]*>([\s\S]*?)<\/li>/g)].map((li, i) => {
        const t = li[1].replace(/^\s*<b>([\s\S]*?)<\/b>/, "<b>$1.</b> ");
        return (b[4] === "ol" ? `${i + 1}. ` : "- ") + inline(t);
      });
      out.push(items.join("\n"));
    } else if (b[6]) {
      const rows = [...b[6].matchAll(/<tr[^>]*>([\s\S]*?)<\/tr>/g)].map((r) =>
        [...r[1].matchAll(/<t[hd][^>]*>([\s\S]*?)<\/t[hd]>/g)].map((c) => inline(c[1]).replace(/\|/g, "\\|")));
      if (rows.length) {
        const n = rows[0].length;
        out.push([rows[0], Array(n).fill("---"), ...rows.slice(1)].map((r) => "| " + r.join(" | ") + " |").join("\n"));
      }
    } else if (b[7] !== undefined) { const t = inline(b[7]); if (t) out.push(t); }
    else if (b[8] !== undefined) { const t = inline(b[8]); if (t) out.push("> " + t); }
    else if (b[9] !== undefined) out.push("### " + inline(b[9]));
  }
  return out.join("\n\n");
}

// build returns llms-full.txt's text: llms.txt's header, then each English
// docs page in llms.txt's order.
export async function build() {
  const llms = await readFile(new URL("llms.txt", pub), "utf8");
  const head = llms.split("\n## ")[0].trim();
  const order = [...llms.matchAll(/\]\(https:\/\/usemagpie\.ai\/docs\/([a-z0-9-]+)\)/g)].map((m) => m[1]);
  const files = (await readdir(new URL("docs/", pub))).filter((f) => f.endsWith(".html") && f !== "contributors.html").map((f) => f.slice(0, -5));
  const pages = [...new Set([...order, ...files.sort()])].filter((p) => files.includes(p));
  const parts = [head, "This file holds the full text of every English docs page. The index is https://usemagpie.ai/llms.txt."];
  for (const p of pages) {
    const html = await readFile(new URL(`docs/${p}.html`, pub), "utf8");
    const body = article(html);
    if (body) parts.push(`---\n\nSource: https://usemagpie.ai/docs/${p}\n\n${body}`);
  }
  return parts.join("\n\n") + "\n";
}

if (import.meta.url === `file://${process.argv[1]}`) {
  await writeFile(new URL("llms-full.txt", pub), await build());
}
