// What a paste of a curl command or environment lines is read as (curlOf,
// envOf): loaded when the Providers page is first drawn rather than with the
// core, as the editor and the Add sheet need them only at a paste. They are
// on window, for the Providers page and its tests.
// curlOf reads a pasted curl command (@mark_li21) into the editor's API,
// base URL, key and model; a stand-in key ($KEY, <API_KEY>) is the
// placeholder; null if it isn't curl
window.curlOf = function curlOf(text) {
  let s = pasteClean(text);
  const head = /^curl(?:\.exe)?(?=\s)/i.exec(s);
  if (!head) return null;
  const cmd = /\^[ \t]*\r?\n/.test(s), ps = /`[ \t]*\r?\n/.test(s) || /^curl\.exe/i.test(s);
  s = s.slice(head[0].length).replace(/(?:\\|\^|`)[ \t]*\r?\n/g, " ");
  // the words as the shell hands them to curl: quotes taken off, escapes read
  const words = [];
  let w = null;
  for (let i = 0; i < s.length;) {
    const c = s[i];
    if (/\s/.test(c)) { if (w !== null) words.push(w); w = null; i++; continue; }
    w ??= "";
    if (c === "'") {
      // PowerShell doubles a quote inside single quotes
      let j = i + 1;
      for (;;) {
        const k = s.indexOf("'", j);
        if (k < 0) { w += s.slice(j); i = s.length; break; }
        if (ps && s[k + 1] === "'") { w += s.slice(j, k) + "'"; j = k + 2; continue; }
        w += s.slice(j, k); i = k + 1; break;
      }
    } else if (c === '"') {
      for (i++; i < s.length && s[i] !== '"'; i++) {
        const esc = (s[i] === "\\" && /["\\$`]/.test(s[i + 1] || "")) || (ps && s[i] === "`");
        if (esc) i++;
        w += s[i] ?? "";
      }
      i++;
    } else if ((c === "\\" && !cmd && !ps) || (c === "^" && cmd)) { w += s[i + 1] ?? ""; i += 2; }
    else { w += c; i++; }
  }
  if (w !== null) words.push(w);
  // the options curl reads a value after; the others stand alone
  const valued = /^(-[HdXuoAeFbcmT]|--(header|data|data-raw|data-binary|data-ascii|data-urlencode|json|request|url|user|output|user-agent|referer|form|cookie|max-time|connect-timeout|proxy|upload-file|retry|cert|key|cacert))$/;
  let url = "", body = "";
  const headers = [];
  for (let i = 0; i < words.length; i++) {
    const x = words[i];
    let opt = x, val = null;
    if (/^-[Hd]./.test(x)) { opt = x.slice(0, 2); val = x.slice(2); }
    else if (valued.test(x)) val = words[++i] ?? "";
    else if (x.startsWith("-")) continue;
    else { if (!url) url = x; continue; }
    if (opt === "-H" || opt === "--header") headers.push(val);
    else if (opt === "--url") url = val;
    else if (/^(-d|--data(-raw|-binary|-ascii)?|--json)$/.test(opt) && !val.startsWith("@")) body = val;
  }
  const out = { url, api: "", base: "", urlHole: "", key: "", placeholder: "", model: "" };
  const at = /^(https?:\/\/[^?#]*?)\/*(\/chat\/completions|\/messages|\/responses)\/*(?:[?#].*)?$/i.exec(url);
  if (at) {
    out.api = { "/chat/completions": "openai", "/messages": "anthropic", "/responses": "responses" }[at[2].toLowerCase()];
    out.base = respellURL(at[1], out.api);
    out.urlHole = (/\{[^}]*\}|<[^>]*>|\$\{?\w+\}?|%\w+%/.exec(out.base) || [""])[0];
  }
  for (const h of headers) {
    const m = /^\s*(authorization|x-api-key|api-key)\s*:\s*(.*?)\s*$/i.exec(h);
    if (!m) continue;
    const v = m[1].toLowerCase() === "authorization" ? m[2].replace(/^bearer\s+/i, "") : m[2];
    if (!v) continue;
    if (/^\$|^%\w+%$|^<.*>$|^\{\{.*\}\}$|^\[.*\]$|your[_-]?|api[_-]?key|x{6,}|\.\.\.|…/i.test(v)) out.placeholder = v;
    else out.key = v;
    break;
  }
  // a body for PowerShell 5 often escapes its quotes for curl.exe: '{\"model\": …}'
  for (const b of [body, body.replace(/\\"/g, '"')]) {
    try { const j = JSON.parse(b); if (typeof j?.model === "string") out.model = j.model.trim(); break; } catch {}
  }
  return out;
};

// envOf reads the environment lines a vendor's page or a README gives for
// its API (WJaunita45604 on X): export ANTHROPIC_BASE_URL="…", set and
// setx, $env:NAME = "…", Docker's ENV, NAME=value in a .env file, or a
// JSON object of them (Claude Code's settings.json "env"). The names say
// the API: ANTHROPIC_* Anthropic's, OPENAI_* OpenAI's chat, GEMINI_* Gemini's.
// It gives what curlOf does (api, base, urlHole, key, placeholder, model),
// with bases, every API the text names a URL for, and name, the site's
// name taken from the first. A key that only stands for one is placeholder.
// null for text with none of those names.
window.envOf = function envOf(text) {
  const vars = {};
  const put = (k, v) => { if (typeof v === "string" && v.trim() && !(k in vars)) vars[k] = v.trim(); };
  const body = pasteClean(text);
  // JSON whole, or a piece of one: "env": {…} or its lines, a comma after the last
  let json = null;
  for (const b of [body, "{" + body + "}"]) {
    try { json = JSON.parse(b.replace(/,\s*([}\]])/g, "$1").replace(/,\s*$/, "")); break; } catch {}
  }
  const walk = (o, depth) => {
    if (!o || typeof o !== "object" || depth > 3) return;
    for (const [k, v] of Object.entries(o)) { if (typeof v === "string") put(k, v); else walk(v, depth + 1); }
  };
  if (json) walk(json, 0);
  else {
    for (let line of body.split(/\r\n?|\n/)) {
      // cmd's set "NAME=value"
      line = line.replace(/^(\s*set\s+)"([^"]*)"\s*$/i, "$1$2");
      // two or more on one line: export A="…" export B="…", A=… B=… claude,
      // or the lines a one-line box ran together
      const many = [...line.matchAll(/(?:\b(?:export|set|setx)\s+|\$env:)?\b([A-Za-z_][A-Za-z0-9_]*)\s*=\s*("[^"]*"|'[^']*'|[^\s"';&]+)/gi)];
      if (many.length > 1) { for (const [, k, v] of many) put(k, v.replace(/^(["'])(.*)\1$/, "$2")); continue; }
      const m = /^\s*(?:export\s+|set\s+|setx\s+|env\s+|\$env:)?["']?([A-Za-z_][A-Za-z0-9_]*)["']?\s*(?:=|:|\s)\s*(.*?)\s*[;,]?\s*$/i.exec(line);
      if (!m) continue;
      const q = /^(["'])(.*)\1$/.exec(m[2]);
      put(m[1], q ? q[2] : m[2].replace(/\s+#.*$/, ""));
    }
  }
  // a vendor's own names say no API (DEEPSEEK_BASE_URL, MY_RELAY_API_KEY,
  // a bare BASE_URL): the URL says it, else it is OpenAI's chat
  const base = /^(?:(\w+?)_)?(?:BASE_URL|API_BASE|API_URL|API_BASE_URL|BASE|ENDPOINT|URL|HOST|API_HOST)$/i;
  const keyOf = /^(?:(\w+?)_)?(?:AUTH_TOKEN|API_KEY|API_TOKEN|ACCESS_TOKEN|KEY|TOKEN|SECRET_KEY|APIKEY)$/i;
  const modelOf = /^(?:(\w+?)_)?MODEL$/i;
  const named = /^(ANTHROPIC|CLAUDE|OPENAI|GEMINI|GOOGLE_GEMINI|GOOGLE)$/i;
  const apiOf = (prefix, u) => /^(ANTHROPIC|CLAUDE)$/i.test(prefix) ? "anthropic" : /^OPENAI$/i.test(prefix) ? "openai" : /^(GEMINI|GOOGLE_GEMINI|GOOGLE)$/i.test(prefix) ? "gemini" : urlAPI(u);
  const out = { url: "", api: "", base: "", bases: [], urlHole: "", key: "", placeholder: "", model: "", name: "" };
  // the names that say their API first, so OPENAI_BASE_URL wins over a URL
  const prefix = (k) => (base.exec(k) || keyOf.exec(k) || [])[1] || "";
  const order = Object.entries(vars).sort(([a], [b]) => named.test(prefix(b)) - named.test(prefix(a)));
  let seen = 0;
  for (const [k, v] of order) {
    let m;
    if ((m = base.exec(k)) && !/PROXY$/i.test(k)) {
      const known = named.test(m[1] || "");
      // a vendor's own name is read only for a web address: DATABASE_URL isn't one
      if (!known && !/^https?:\/\//i.test(v)) continue;
      seen++;
      // a host written without its scheme is https
      const u = /^[a-z][a-z0-9+.-]*:\/\//i.test(v) ? v : /^[^\s/]+\.[^\s/]+|^localhost\b/i.test(v) ? "https://" + v : "";
      const api = apiOf(m[1] || "", u);
      if (!u || out.bases.some(([a]) => a === api)) continue;
      // …/v1/chat/completions given whole is cut back to the base
      const cut = u.replace(/\/+$/, "").replace(/\/(?:chat\/completions|messages|responses)$/i, "");
      out.bases.push([api, respellURL(cut, api)]);
    } else if ((m = keyOf.exec(k)) && !/^(GITHUB|GH|NPM|AWS|HF|HUGGINGFACE|SLACK|DISCORD)$/i.test(m[1] || "")) {
      seen++;
      if (out.key || out.placeholder) continue;
      if (standIn(v)) out.placeholder = v;
      else out.key = v;
    } else if (modelOf.test(k) && !out.model) out.model = v;
  }
  // no URL in the names: the lines may still give one plainly
  if (!out.bases.length) { const p = plainOf(body); if (p) return p; }
  if (!seen) return null;
  if (!out.bases.length && !out.key && !out.placeholder) return null;
  if (out.bases.length) {
    [out.api, out.base] = out.bases[0];
    out.url = out.base;
    out.urlHole = (/\{[^}]*\}|<[^>]*>|\$\{?\w+\}?|%\w+%/.exec(out.base) || [""])[0];
    out.name = siteName(out.base);
  }
  return out;
};

// pasteClean undoes what a web page or a chat does to text copied from it:
// zero-width characters and a BOM inside the names, no-break and ideographic
// spaces, full-width = and :, and curly quotes
function pasteClean(text) {
  return String(text || "").replace(/[\u200b-\u200d\u2060\ufeff]/g, "").replace(/[\u00a0\u2000-\u200a\u202f\u3000]/g, " ")
    .replace(/＝/g, "=").replace(/：/g, ":").replace(/[“”„]/g, '"').replace(/[‘’]/g, "'").trim();
}

// pastedProvider opens a new custom provider from a vendor's setup pasted
// into a search box: the Add sheet's, and Providers' own, the one in sight
// once there are providers (WJaunita45604 on X: v0.1.4044 read only the
// first). Env lines, JSON, plain "address + key" lines or a curl command
// give the name, URL, API and key; the site's icon is asked for with the URL
// and name only, and the key goes nowhere but the draft. A setup with no
// address still opens, saying so. False for text that is no setup.
window.pastedProvider = function pastedProvider(text) {
  const c = window.curlOf(text) || window.envOf(text);
  const bases = c?.bases || (c?.api && c.base ? [[c.api, c.base]] : []);
  // a word after KEY or TOKEN ("token plan") is no key
  if (!c || !bases.length && !(/^\S{8,}$/.test(c.key) || c.placeholder)) return false;
  const name = c.name || siteName(c.base) || t("Custom provider");
  const d = { id: slug(name), name, preset: "", chat: "", responses: "", anthropic: "", decide: "", catalog: "", key: c.key, api: c.api || "openai", chosen: [], extra: c.model ? [c.model] : [], headers: [], icon: "" };
  for (const [a, base] of bases) d[apiField[a]] = base;
  editing = { custom: true }; draft = d; presetQuery = "";
  renderProviders();
  const took = bases.map(([a]) => t("Base URL") + " (" + t(API_NAMES[a]) + ")");
  if (c.key) took.push(t("API key"));
  if (c.model) took.push(t("model {model}", { model: c.model }));
  const notes = took.length ? [t("Filled in from the pasted text: {what}.", { what: took.join(" · ") })] : [];
  if (!bases.length) notes.push(t("The pasted text gives no web address: type the base URL."));
  if (c.urlHole) notes.push(t("Replace {part} in the base URL.", { part: c.urlHole }));
  if (c.placeholder) notes.push(t("Its key, {key}, only stands for one: paste your own API key.", { key: c.placeholder }));
  const note = notes.join(" ");
  editorError(note, "note");
  // the site's own icon, when it has one; the draft may be gone by then, and
  // the note stays while it is still in sight
  if (bases.length) api("icons/favicon", { url: bases[0][1], name }).then((r) => {
    if (draft !== d || !r?.icon) return;
    const shown = document.querySelector(".editor .editor-error.note")?.textContent === note;
    d.icon = r.icon;
    renderProviders(true);
    if (shown) editorError(note, "note");
  }).catch(() => {});
  return true;
};

// standIn: a key a page gives only to stand for the reader's own
const standIn = (v) => /^\$|^%\w+%$|^<.*>$|^\{\{.*\}\}$|^\[.*\]$|your[_-]?|api[_-]?key|x{6,}|\.\.\.|…/i.test(v);

// urlAPI: the API a base URL with nothing else to say it serves —
// Anthropic's for a path naming it (…/anthropic, /claude), else OpenAI's chat
const urlAPI = (u) => /\/(anthropic|claude)(\/|$)/i.test(String(u).replace(/^\w+:\/\/[^/]+/, "")) ? "anthropic" : "openai";

// plainOf reads a base URL and a key given as plain lines, as a relay's
// page or a chat hands them over: "Base URL: https://…" over "Key: sk-…",
// or the two on one line. Both are needed, or it is no setup: a URL alone
// is a search, a key alone is a key. It gives what envOf does.
function plainOf(text) {
  const s = String(text || "");
  if (s.length > 4000 || /^\s*curl\b/i.test(s)) return null;
  const um = /https?:\/\/[^\s"'<>，。；、]+/i.exec(s);
  if (!um) return null;
  const u = um[0].replace(/[),.;:]+$/, "");
  const rest = s.slice(0, um.index) + " " + s.slice(um.index + um[0].length);
  // a labelled key first (key, token, 密钥, 令牌), then a word that looks like one
  const labelled = /(?:key|token|secret|密钥|令牌|秘钥)\s*[:：=]?\s*["'`]?([A-Za-z0-9][\w.\-~+/=]{11,})/i.exec(rest);
  let key = labelled?.[1] || "";
  if (!key) {
    for (const w of rest.split(/[\s"'`,，;；|]+/)) {
      const x = w.replace(/^[^\w]+|[^\w=]+$/g, "");
      if (/^(sk|ak|pk|rk|cr|ms|tp|fk|key)[-_]/i.test(x) && x.length >= 12 || /^[A-Za-z0-9][\w.\-]{31,}$/.test(x) && /\d/.test(x) && /[a-z]/i.test(x)) { key = x; break; }
    }
  }
  if (!key) return null;
  const cut = u.replace(/\/+$/, "").replace(/\/(?:chat\/completions|messages|responses)$/i, "");
  const api = urlAPI(cut);
  const base = respellURL(cut, api);
  const out = { url: base, api, base, bases: [[api, base]], urlHole: "", key: "", placeholder: "", model: "", name: siteName(base) };
  if (standIn(key)) out.placeholder = key; else out.key = key;
  return out;
}

// siteName is what to call the provider at a URL: the name in its host,
// api.moonshot.cn → Moonshot, open.bigmodel.cn → Bigmodel; an address
// stays as it is.
window.siteName = function siteName(u) {
  let h = "";
  try { h = new URL(u).hostname; } catch { return ""; }
  if (/^[\d.]+$|:|^localhost$/.test(h)) return h;
  let l = h.split(".");
  if (l.length > 1) l = l.slice(0, -1); // the top-level domain
  if (l.length > 1 && /^(co|com|org|net|ac|gov|edu)$/.test(l[l.length - 1])) l = l.slice(0, -1);
  const w = l.filter((x) => !/^(www|api|open|openapi|gateway|llm)$/.test(x)).pop() || l.pop() || h;
  return w.charAt(0).toUpperCase() + w.slice(1);
};
