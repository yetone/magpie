// magpie's tap on Claude Code's stream (internal/claudetui/tap.go). Claude
// Code, a Bun program, loads this before itself (BUN_OPTIONS --preload).
// Its requests to Anthropic go through Bun.FetchSession: this makes that
// a FetchSession whose fetch is the real one's, the request, connection
// and TLS untouched, and copies the reply of each of the main turn's
// requests (x-claude-code-request-class: main), as it streams, to the
// file MAGPIE_CLAUDE_TAP names: a JSON line an event, and the reply's
// anthropic-ratelimit-unified-* headers as it opens. Claude Code reads
// its own copy of the stream as before.
//
// MAGPIE_CLAUDE_GUARD names a file of the bridge's holding the
// CLAUDE_CODE_EXTRA_BODY a turn of the client's gave (its safeguards).
// Claude Code reads that variable as it builds each request, but only
// from its own environment, which nothing outside can change once it
// runs: each request to /v1/messages is given the file's safeguards, as
// Claude Code would have put them there had the variable said so, and
// the variable is set to it for the requests after. The file says it was
// seen (<file>.live) at the first request it could be put in.
//
// The file's effort, beside the extra body, is the level the run was told
// since it started (#5584): each main request asks it in
// output_config.effort, in place of the --effort Claude Code started
// with. The interactive mode takes another level only from a person
// typing /effort, which saves it as the user's default for new sessions;
// a --settings file is read once, as Claude Code starts.
(() => {
  const file = process.env.MAGPIE_CLAUDE_TAP;
  const guard = process.env.MAGPIE_CLAUDE_GUARD;
  delete process.env.MAGPIE_CLAUDE_GUARD;
  if (!file) return;
  // none of it is handed on to what Claude Code runs, nor says it is here
  delete process.env.MAGPIE_CLAUDE_TAP;
  const ours = /(^|\s)--preload(=|\s+)\S*magpie-claude-tap-\S*/;
  const opts = (process.env.BUN_OPTIONS || "").replace(ours, "").trim();
  if (opts) process.env.BUN_OPTIONS = opts;
  else delete process.env.BUN_OPTIONS;
  const argv = process.execArgv;
  for (let i = argv.length - 1; i >= 0; i--) {
    if (!/magpie-claude-tap-/.test(argv[i])) continue;
    if (argv[i].startsWith("--preload=")) argv.splice(i, 1);
    else if (argv[i - 1] === "--preload") argv.splice(--i, 2);
  }

  const fs = require("fs");
  let fd;
  try {
    fd = fs.openSync(file, "a");
  } catch {
    return;
  }
  const put = (v) => {
    try {
      fs.writeSync(fd, JSON.stringify(v) + "\n");
    } catch {}
  };
  const Real = globalThis.Bun && Bun.FetchSession;
  if (typeof Real !== "function") {
    put({ tap: "none" });
    return;
  }
  let streams = 0;
  let live = false;
  // body is a request's body with the guard file's safeguards and effort:
  // the same bytes when they are what it has, or when it has none to
  // replace (a request Claude Code builds without the extra body). The
  // effort goes in the main turn's requests (main) alone, never in what
  // Claude Code asks beside them (titles, summaries, its classifiers).
  const guarded = (body, main) => {
    if (!guard || typeof body !== "string") return body;
    const carries = body.indexOf('"safeguards"') >= 0;
    if (!carries && !main) return body;
    let text, now;
    try {
      text = fs.readFileSync(guard, "utf8");
      now = JSON.parse(text);
    } catch {
      return body;
    }
    if (!now || typeof now !== "object") return body;
    // the file is the extra body Claude Code is to send, and the effort
    // beside it, which is no field of a request
    const { effort, ...extra } = now;
    const level = main && typeof effort === "string" ? effort : "";
    const safeguards = carries && "safeguards" in extra;
    if (!safeguards && !main) return body;
    let req;
    try {
      req = JSON.parse(body);
    } catch {
      return body;
    }
    if (!req || typeof req !== "object") return body;
    if (safeguards && !("safeguards" in req)) return body;
    if (!live) {
      live = true;
      try {
        fs.writeFileSync(guard + ".live", "");
      } catch {}
    }
    let changed = false;
    if (safeguards) {
      const env = effort === undefined ? text : JSON.stringify(extra);
      if (process.env.CLAUDE_CODE_EXTRA_BODY !== env) process.env.CLAUDE_CODE_EXTRA_BODY = env;
      if (JSON.stringify(req.safeguards) !== JSON.stringify(extra.safeguards)) {
        req.safeguards = extra.safeguards;
        changed = true;
        put({ guard: "set" });
      }
    }
    if (level && !(req.output_config && req.output_config.effort === level)) {
      req.output_config = { ...(req.output_config || {}), effort: level };
      changed = true;
    }
    return changed ? JSON.stringify(req) : body;
  };
  // read tells each event of stream s, then its end: cut when it broke
  // off, as a dropped connection or Claude Code's own Esc ends it
  const read = async (s, body) => {
    const dec = new TextDecoder();
    let buf = "";
    const event = (text) => {
      const data = text
        .split("\n")
        .filter((l) => l.startsWith("data:"))
        .map((l) => l.slice(5).replace(/^ /, ""))
        .join("\n");
      if (!data) return;
      let e;
      try {
        e = JSON.parse(data);
      } catch {
        return;
      }
      if (e && e.type !== "ping") put({ s, e });
    };
    try {
      for await (const chunk of body) {
        buf = (buf + dec.decode(chunk, { stream: true })).replace(/\r\n?/g, "\n");
        let i;
        while ((i = buf.indexOf("\n\n")) >= 0) {
          event(buf.slice(0, i));
          buf = buf.slice(i + 2);
        }
      }
      if (buf.trim()) event(buf);
      put({ s, end: "done" });
    } catch (err) {
      put({ s, end: "cut" });
    }
  };
  class FetchSession extends Real {
    async fetch(input, init) {
      try {
        const url = String((input && input.url) || input);
        if (init && /\/v1\/messages(\?|$)/.test(url)) {
          const headers = new Headers(init.headers || (input && input.headers) || undefined);
          const body = guarded(init.body, headers.get("x-claude-code-request-class") === "main");
          if (body !== init.body) init = { ...init, body };
        }
      } catch {}
      const res = await super.fetch(input, init);
      try {
        const url = String((input && input.url) || input);
        const headers = new Headers((init && init.headers) || (input && input.headers) || undefined);
        if (
          headers.get("x-claude-code-request-class") === "main" &&
          /\/v1\/messages(\?|$)/.test(url) &&
          res.body &&
          /^text\/event-stream/.test(res.headers.get("content-type") || "")
        ) {
          const [theirs, mine] = res.body.tee();
          Object.defineProperty(res, "body", { value: theirs, configurable: true });
          // told before Claude Code has the reply, so the helper knows a
          // stream is coming before any of its messages
          const s = ++streams;
          // with the account's allowance as Anthropic tells it on the
          // reply, which -p tells in its rate_limit_event
          const h = {};
          res.headers.forEach((v, k) => {
            if (/^anthropic-ratelimit-unified-/i.test(k)) h[k.toLowerCase()] = v;
          });
          put(Object.keys(h).length ? { s, open: true, h } : { s, open: true });
          read(s, mine);
        }
      } catch {}
      return res;
    }
  }
  try {
    Bun.FetchSession = FetchSession;
  } catch {}
  put({ tap: Bun.FetchSession === FetchSession ? "on" : "none" });
})();
