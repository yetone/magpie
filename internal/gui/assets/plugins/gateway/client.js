// The Gateway page (plugins/gateway): the gateway's state and its keys
// (keys.js), how to connect a client, its models and its recent calls. It
// is a page the plugin adds to the window (kernel.js): its markup is the
// html below, and the shell draws it through its hooks — loading before
// the state is in, draft from the state kept, render with the new one —
// behind the kernel's error boundary.
//
// Classic script, loaded before app.js: what runs as it loads touches
// neither the page nor app.js; mount, at magpie.start, wires the page.

// the gateway tab's choices, kept per machine
const gwParams = new URLSearchParams(location.search);
let flavor = gwParams.get("flavor") || localStorage.getItem("magpie.flavor") || "openai"; // which API the snippets speak
let lang = gwParams.get("lang") || localStorage.getItem("magpie.lang") || "shell";        // which snippet
let snipOS = localStorage.getItem("magpie.snipOS") || "";                               // the shell the Shell and curl snippets are for (#1514)
let exampleModel = localStorage.getItem("magpie.model") || "";  // the model in the snippets
let decideModel = localStorage.getItem("magpie.decideModel") || ""; // the System One snippets' decision model
// Connect folded away under its heading: folded until it is opened, as it
// is set up once and seldom read again (the owner)
let connectFolded = true;
try { connectFolded = localStorage.getItem("magpie.gwConnectFolded") !== "0"; } catch {}
const expandedCalls = new Set(); // recent-call ids whose wire bodies are open
const callBodies = new Map(); // callRow → its bodies, read when it is opened (#1521)
const wireOpen = new Set(); // recent-call ids whose HTTP headers are open
// recent calls drawn a page at a time, as many as the plugin's setting
// says (callsPage)
let callsPage = 20;
let callsShown = callsPage;

function renderGatewayLoading() {
  const page = $("#view-gateway");
  page.classList.add("loading");
  page.setAttribute("aria-busy", "true");
  $("#connectNote").textContent = "";
  $("#callsNote").textContent = "";
  $("#copyModels").hidden = true;

  const gateway = $("#gateway");
  gateway.replaceChildren();
  const mark = el("span", "skeleton gw-sk-dot");
  const who = el("div", "who gw-sk-who");
  who.append(el("span", "skeleton gw-sk-title"), el("span", "skeleton gw-sk-sub"));
  gateway.append(mark, who, el("span", "skeleton gw-sk-url"));

  const connect = $("#connect");
  connect.replaceChildren();
  connect.hidden = connectFolded;
  for (let i = 0; i < 4; i++) {
    connect.append(el("span", "skeleton gw-sk-label"));
    const value = el("div", "gw-sk-field");
    value.append(el("span", "skeleton"), el("span", "skeleton short"));
    connect.append(value);
  }

  const models = $("#gwModels");
  models.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const row = el("div", "row model gw-sk-row");
    row.append(el("span", "skeleton gw-sk-model"), el("span", "grow"), el("span", "skeleton gw-sk-provider"));
    models.append(row);
  }

  const activity = $("#activity");
  activity.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const row = el("div", "call gw-sk-call");
    row.append(el("span", "skeleton time"), el("span", "skeleton agent"), el("span", "skeleton model"), el("span", "grow"), el("span", "skeleton result"));
    activity.append(row);
  }
}

function renderGatewayView() {
  const page = $("#view-gateway");
  page.classList.remove("loading");
  page.removeAttribute("aria-busy");
  renderGateway();
  renderGatewayLan();
  $("#gatewayKeysBlock").hidden = !providers.gateway.lan;
  // not shared, the keys are left out; this line says where they come from,
  // as a tunnel to loopback is refused until sharing is on (DD on Discord)
  $("#gatewayKeysOff").hidden = !!providers.gateway.lan;
  if (!providers.gateway.lan) gatewayKeyDraft = null;
  if (gatewayKeyDraft === null && gatewayLimit === null && !$("#gatewayKeys .rename-in")) renderGatewayKeys();
  renderConnect();
  renderGatewayModels();
  renderActivity();
}

// The status card's button (leslie_luo on Discord: told to quit the older
// magpie serving the gateway, with no way to tell which one it was): with
// an older magpie on the port it quits that one, checked to be magpie by
// its process, and this one serves the gateway at once; otherwise it
// restarts the gateway this one serves, or starts it. A magpie of this
// version or newer serving it gets none: that one is not to be quit here.
// What stood in the way stays under it until the gateway's state changes.
let gatewayFix = { busy: false, err: "", when: "" };
const gatewayFixWhen = (g) => [g.running, g.mine, g.version || ""].join("|");
function gatewayFixButton(g, older) {
  if (g.running && !g.mine && !older) return null;
  // bordered: a plain text button read as words on the card in dark mode
  const b = el("button", "text action gw-fix");
  b.textContent = gatewayFix.busy
    ? (older ? t("Quitting magpie {v}…", { v: g.version }) : t(g.running ? "Restarting the gateway…" : "Starting the gateway…"))
    : (older ? t("Quit magpie {v} and take over", { v: g.version }) : t(g.running ? "Restart gateway" : "Start gateway"));
  b.disabled = gatewayFix.busy;
  b.onclick = async () => {
    const when = gatewayFixWhen(g);
    gatewayFix = { busy: true, err: "", when };
    renderGateway();
    let out;
    try { out = await api(older ? "gateway/take-over" : "gateway/restart", {}); } catch (e) { out = { reason: "error", error: e.message }; }
    gatewayFix = { busy: false, err: out?.ok ? "" : gatewayFixSays(out || {}), when };
    try { await loadProviders(); } catch {}
    renderGateway();
  };
  return b;
}
// gatewayFixSays is why a take-over or a restart didn't go, in the page's
// language, naming the process it was
function gatewayFixSays(o) {
  const p = { port: o.port || "", pid: o.pid || "", path: o.path || "", v: o.version || "", error: o.error || "" };
  switch (o.reason) {
    case "not-magpie": return t("Port {port} is held by {path} (pid {pid}), which isn't magpie, so magpie leaves it alone. Quit it yourself, or move the gateway to another port in Settings.", p);
    // leslie_luo on Discord: an old magpie in OrbStack, its helper named
    // as the program to quit
    case "container": return t("Port {port} is forwarded by {path} (pid {pid}) to a container or a VM: the magpie {v} answering there runs inside it, out of this magpie's reach. Stop that container (docker ps lists it, docker stop <name> stops it, or stop it in OrbStack or Docker Desktop), or move this magpie's gateway to another port in Settings.", p);
    case "unseen": return t("magpie can't see which process holds port {port}: it runs as another user or as administrator. Quit it there.", p);
    case "denied": return t("magpie {v} (pid {pid}, {path}) runs as another user or as administrator, so this magpie can't quit it. Quit it there.", p);
    case "stuck": return t("magpie {v} (pid {pid}) didn't quit. End it in your system's process list (Activity Monitor, Task Manager).", p);
    case "respawned": return t("Something started magpie on port {port} again at once ({path}, pid {pid}): a login item or a service keeps it running (a LaunchAgent, a systemd unit, a scheduled task). Remove that, then try again.", p);
    case "taken": return p.pid
      ? t("Port {port} is in use by {path} (pid {pid}): quit it, or move the gateway to another port in Settings.", p)
      : t("Port {port} is in use by another program: quit it, or move the gateway to another port in Settings.", p);
    case "other": return t("magpie {v} serves the gateway, and it isn't older than this one: quit it there to serve the gateway here.", p);
  }
  return t("The gateway couldn't be served here: {error}", p);
}

// gatewaySkewLine: a magpie of another version serves the gateway in this
// one's place (gui/gateway_older.go). matrix0487 on Discord: the app wrote pi
// a model its older gateway didn't know, pi said 404 "add a provider in
// magpie first", and only the Gateway page told of the older magpie. Said
// over the Agents list, in the panel and the window, until it is gone: an
// older copy of the app is taken over by itself once idle; any other is
// the user's to update or quit, here with the Gateway page's own button.
let gwSkewFix = { busy: false, err: "", pid: 0 };
function gatewaySkewLine(list) {
  let box = $("#gwSkew");
  const o = state.gatewayOther;
  if (!o || !o.version) {
    if (box) box.hidden = true;
    return;
  }
  if (!box) {
    box = el("div", "gw-skew");
    box.id = "gwSkew";
    ($("#agentsLead") || list).before(box);
  }
  box.hidden = false;
  if (gwSkewFix.pid !== (o.pid || 0)) gwSkewFix = { busy: false, err: "", pid: o.pid || 0 };
  const p = { v: o.version, mine: o.mine, n: o.busy || 0 };
  let head, why, quit = false;
  if (o.replacing) {
    head = t("magpie {v}, an older copy of this app, still serves the gateway", p);
    why = o.busy > 0
      ? t(o.busy === 1 ? "This magpie ({mine}) takes it over once its {n} request in flight finishes; until then agents' requests go through magpie {v}." : "This magpie ({mine}) takes it over once its {n} requests in flight finish; until then agents' requests go through magpie {v}.", p)
      : o.busy < 0
        ? t("This magpie ({mine}) takes it over once it has no request in flight; until then agents' requests go through magpie {v}.", p)
        : t("This magpie ({mine}) takes it over in a moment.", p);
  } else if (o.older) {
    head = t("The gateway is served by magpie {v}, older than this app ({mine})", p);
    why = o.serves === "serve"
      ? t("It runs as magpie serve, in a terminal or as the service. Agents' requests go through it without this version's fixes, and models set up here may not reach them. Update that magpie and start it again (magpie daemon restart, for the service), or quit it here and this app serves the gateway.", p)
      : t("Agents' requests go through it without this version's fixes, and models set up here may not reach them. Quit it here and this app serves the gateway at once.", p);
    quit = true;
  } else if (o.newer) {
    head = t("The gateway is served by magpie {v}, newer than this app ({mine})", p);
    why = t("Agents' requests go through it, and what this app writes for them may not match it. Update this magpie, or quit the newer one there.", p);
  } else {
    head = t("The gateway is served by magpie {v}, a different build from this app ({mine})", p);
    why = t("Agents' requests go through it, and what this app writes for them may not match it. Quit it there for this app to serve the gateway.", p);
  }
  const card = el("div", "deprecation");
  card.setAttribute("role", "note");
  const body = el("div", "dep-body");
  body.append(el("div", "dep-head", head), el("p", "dep-why", why));
  if (gwSkewFix.err) body.append(el("p", "dep-why gw-skew-err", gwSkewFix.err));
  card.append(el("span", "dep-dot"), body);
  if (quit) {
    const acts = el("div", "dep-acts");
    const b = el("button", "text action", gwSkewFix.busy ? t("Quitting magpie {v}…", p) : t("Quit magpie {v} and take over", p));
    b.disabled = b.classList.toggle("busy", gwSkewFix.busy);
    b.onclick = async () => {
      gwSkewFix = { busy: true, err: "", pid: o.pid || 0 };
      gatewaySkewLine(list);
      let out;
      try { out = await api("gateway/take-over", {}); } catch (e) { out = { reason: "error", error: e.message }; }
      gwSkewFix = { busy: false, err: out?.ok ? "" : gatewayFixSays(out || {}), pid: o.pid || 0 };
      try { state = await api("state"); } catch {}
      renderAgents();
    };
    acts.append(b);
    card.append(acts);
  }
  box.replaceChildren(card);
}

// The status card: the gateway's mark, its state and what it does, and the
// button that fixes what stands in its way.
const GW_GLYPH = '<svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><g fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="14" height="5" rx="1.5"/><rect x="3" y="11" width="14" height="5" rx="1.5"/></g><circle class="led" cx="6.2" cy="6.5" r="1.1"/><circle class="led" cx="6.2" cy="13.5" r="1.1"/></svg>';
function renderGateway() {
  const g = providers.gateway;
  const box = $("#gateway");
  box.replaceChildren();
  // an older magpie keeping the port sends every agent's request its own
  // way, without this version's fixes (#506): said in place of the counts
  const older = g.running && !g.mine && g.older;
  // the sidebar's mark, its lights the state: green running, amber not
  const mark = el("span", "gw-mark " + (older ? "old" : g.running ? "on" : ""));
  mark.innerHTML = GW_GLYPH;
  const who = el("div", "who");
  const name = el("div", "name", t("Gateway"));
  name.append(el("span", "state", older
    ? t("running · served by magpie {v}, older than this one", { v: g.version })
    : t(g.running ? (g.mine ? "running" : "running · served by another magpie") : "not running")));
  const routed = new Set();
  for (const p of providers.providers) for (const a of p.agents) if (a.current) routed.add(a.id);
  const n = routed.size;
  if (older) {
    who.append(name, el("div", "sub old", t("Agents' requests go through magpie {v} and are sent as it sends them, without this version's fixes. Quit it here and this one serves the gateway at once.", { v: g.version })));
  } else who.append(name, el("div", "sub", g.running
    ? [t(g.models === 1 ? "{n} model" : "{n} models", { n: g.models }), n ? t(n === 1 ? "{n} agent routed through it" : "{n} agents routed through it", { n }) : t("no agent routed through it yet"), t("five APIs, one URL")].join(" · ")
    : t("start it with magpie serve, or open magpie at login")));
  if (gatewayFix.err && gatewayFix.when === gatewayFixWhen(g)) who.append(el("div", "sub old err", gatewayFix.err));
  box.append(mark, who);
  const fix = gatewayFixButton(g, older);
  if (fix) box.append(fix);
}

// Share on local network, set on the page that hands out its keys (the
// owner): the same setting as Settings › Network and sharing, and every
// address the gateway answers on, each to copy.
let gatewayLanBusy = false;
function renderGatewayLan() {
  const g = providers.gateway;
  const box = $("#gatewayLan");
  box.replaceChildren();
  const head = el("div", "row gw-share");
  const choice = segs([["off", t("This computer")], ["on", t("Local network")]], g.lan ? "on" : "off", (v) => {
    if (gatewayLanBusy || (v === "on") === !!g.lan) return;
    gatewayLanBusy = true;
    writingPrefs(api("settings/lan", { on: v === "on" }))
      .then((ns) => { prefs = ns; if (ns.rekeyed) status(ns.rekeyed, "err"); })
      .catch((e) => status(t(e.message), "err"))
      .then(() => loadProviders().catch(() => {}))
      .finally(() => {
        gatewayLanBusy = false;
        if (view !== "gateway" || !providers) return;
        renderGatewayView();
        if (providers.gateway.lan) loadGatewayKeys();
      });
  });
  choice.setAttribute("role", "group");
  choice.setAttribute("aria-label", t("Share on local network"));
  head.append(el("div", "name grow", t("Share on local network")), choice);
  box.append(head);
  const list = el("div", "gw-urls");
  const line = (label, url) => {
    const r = el("div", "gw-url");
    const val = el("span", "gw-url-val");
    val.append(el("code", "", url), copyBtn(url, t("Gateway URL")));
    r.append(el("span", "gw-url-label", label), val);
    list.append(r);
  };
  // served to other machines' browsers (magpie web), the address shown is
  // the one they reach, MAGPIE_PUBLIC_URL's, not this computer's loopback
  // (#1551)
  const shown = web && g.publicURL || g.url;
  line(t(shown === g.url ? "This computer" : "Public address"), shown);
  if (g.lan) {
    const { urls, sub } = lanShown(g.lanURLs || [], prefs?.lanContainer);
    // an internet address on an interface (an ISP's static one) is said
    // to be one, not listed as this network's
    const far = new Set(g.lanPublic || []), rest = urls.filter((u) => u !== shown);
    rest.filter((u) => !far.has(u)).forEach((u, i) => line(i ? "" : t("Local network"), u));
    rest.filter((u) => far.has(u)).forEach((u, i) => line(i ? "" : t("On the internet"), u));
    if (!urls.length) list.append(el("div", "gw-url-none", t("This computer has no local network address right now")));
    if (sub) list.append(el("div", "gw-url-none", sub));
  }
  box.append(list);
}

// One entry per API the gateway serves: where each SDK's base URL points,
// the env vars the usual tools read, and a request in four dialects.
const FLAVORS = {
  openai: {
    name: "OpenAI", base: (u) => u + "/v1", baseEnv: "OPENAI_BASE_URL", keyEnv: "OPENAI_API_KEY",
    note: "Chat Completions, the API most tools speak. Anything with an OpenAI base-URL setting works.",
    curl: (b, m, k = "magpie") => ({ url: `${b}/chat/completions`, headers: [`Authorization: Bearer ${k}`],
      body: `{"model": "${m}",\n "messages": [{"role": "user", "content": "hi"}]}` }),
    python: (b, m, k = "magpie") => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="${k}")
r = client.chat.completions.create(
    model="${m}",
    messages=[{"role": "user", "content": "hi"}],
)
print(r.choices[0].message.content)`,
    node: (b, m, k = "magpie") => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "${k}" });
const r = await client.chat.completions.create({
  model: "${m}",
  messages: [{ role: "user", content: "hi" }],
});
console.log(r.choices[0].message.content);`,
  },
  responses: {
    name: "Responses", base: (u) => u + "/v1", baseEnv: "OPENAI_BASE_URL", keyEnv: "OPENAI_API_KEY",
    note: "OpenAI's newer API: reasoning, built-in tool items, encrypted reasoning. Codex speaks this.",
    curl: (b, m, k = "magpie") => ({ url: `${b}/responses`, headers: [`Authorization: Bearer ${k}`],
      body: `{"model": "${m}", "input": "hi"}` }),
    python: (b, m, k = "magpie") => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="${k}")
r = client.responses.create(model="${m}", input="hi")
print(r.output_text)`,
    node: (b, m, k = "magpie") => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "${k}" });
const r = await client.responses.create({ model: "${m}", input: "hi" });
console.log(r.output_text);`,
  },
  anthropic: {
    name: "Anthropic", base: (u) => u, baseEnv: "ANTHROPIC_BASE_URL", keyEnv: "ANTHROPIC_API_KEY",
    note: "Messages API. Claude Code reads ANTHROPIC_AUTH_TOKEN instead of the key; the Agents tab sets that for you.",
    curl: (b, m, k = "magpie") => ({ url: `${b}/v1/messages`, headers: [`x-api-key: ${k}`, "anthropic-version: 2023-06-01"],
      body: `{"model": "${m}", "max_tokens": 1024,\n "messages": [{"role": "user", "content": "hi"}]}` }),
    python: (b, m, k = "magpie") => `import anthropic

client = anthropic.Anthropic(
    base_url="${b}", api_key="${k}",
)
m = client.messages.create(
    model="${m}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "hi"}],
)
print(m.content[0].text)`,
    node: (b, m, k = "magpie") => `import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({ baseURL: "${b}", apiKey: "${k}" });
const m = await client.messages.create({
  model: "${m}",
  max_tokens: 1024,
  messages: [{ role: "user", content: "hi" }],
});
console.log(m.content[0].text);`,
  },
  gemini: {
    name: "Gemini", base: (u) => u, baseEnv: "GOOGLE_GEMINI_BASE_URL", keyEnv: "GEMINI_API_KEY",
    note: "Google's generateContent API, v1beta. Gemini CLI and the google-genai SDKs speak this.",
    curl: (b, m, k = "magpie") => ({ url: `${b}/v1beta/models/${m}:generateContent`, headers: [`x-goog-api-key: ${k}`],
      body: `{"contents": [{"parts": [{"text": "hi"}]}]}` }),
    python: (b, m, k = "magpie") => `from google import genai

client = genai.Client(api_key="${k}", http_options={"base_url": "${b}"})
r = client.models.generate_content(model="${m}", contents="hi")
print(r.text)`,
    node: (b, m, k = "magpie") => `import { GoogleGenAI } from "@google/genai";

const ai = new GoogleGenAI({
  apiKey: "${k}",
  httpOptions: { baseUrl: "${b}" },
});
const r = await ai.models.generateContent({ model: "${m}", contents: "hi" });
console.log(r.text);`,
  },
  // magpie's own System One API (ARNO on Discord: gateway添加system one
  // api 有想法吗): a decision model — Jev, Clef, Bailian's — answers typed
  // questions about a state; no SDK speaks it, so no shell variables
  systemone: {
    name: "System One", base: (u) => u + "/v1", decide: true,
    note: "TypeSafe's decision API: a Jev or Clef model answers typed questions (choice, noul, score) about a state, with how sure it is. Clef also reads images: add \"images\": [\"data:image/png;base64,…\"].",
    curl: (b, m, k = "magpie") => ({ url: `${b}/systemone`, headers: [`Authorization: Bearer ${k}`],
      body: `{"model": "${m}",\n "state": {"message": "My order never arrived, refund it"},\n "questions": {"team": {"type": "choice",\n   "instructions": "Which team should answer the message?",\n   "criteria": {"billing": "Payments and refunds", "shipping": "Deliveries", "other": "Anything else"}}}}` }),
    python: (b, m, k = "magpie") => `import requests

r = requests.post("${b}/systemone",
    headers={"Authorization": "Bearer ${k}"},
    json={
        "model": "${m}",
        "state": {"message": "My order never arrived, refund it"},
        "questions": {"team": {
            "type": "choice",
            "instructions": "Which team should answer the message?",
            "criteria": {"billing": "Payments and refunds", "shipping": "Deliveries", "other": "Anything else"},
        }},
    })
a = r.json()["answers"]["team"]
print(a["choice"], a["confidence"])`,
    node: (b, m, k = "magpie") => `const r = await fetch("${b}/systemone", {
  method: "POST",
  headers: { Authorization: "Bearer ${k}", "Content-Type": "application/json" },
  body: JSON.stringify({
    model: "${m}",
    state: { message: "My order never arrived, refund it" },
    questions: { team: {
      type: "choice",
      instructions: "Which team should answer the message?",
      criteria: { billing: "Payments and refunds", shipping: "Deliveries", other: "Anything else" },
    } },
  }),
});
const a = (await r.json()).answers.team;
console.log(a.choice, a.confidence);`,
  },
};
// The Shell and curl snippets are written for one shell: PowerShell on
// Windows ($env: in place of export, and the curl.exe that ships with it,
// as plain curl there is Invoke-WebRequest; the body goes in on stdin,
// since Windows PowerShell drops the quotes inside an argument), or a POSIX
// shell (bash, zsh) on macOS and Linux. They start as the shell of the
// machine magpie runs on, and can be switched to the other: a Windows
// desktop's user often runs them on a Linux server over ssh (FourLeafTec,
// #1514). The choice is kept with the snippet language.
const WIN = /^Win/.test(navigator.platform);
const SNIP_OSES = [["windows", "Windows"], ["unix", "macOS · Linux"]];
const snipWin = () => (SNIP_OSES.some(([id]) => id === snipOS) ? snipOS : WIN ? "windows" : "unix") === "windows";
const langs4 = () => [["shell", snipWin() ? "PowerShell" : "Shell"], ["curl", "curl"], ["python", "Python"], ["node", "Node"]];

// A word each shell takes as it is, quoted only where it has to be: a key
// of the user's own can hold any ASCII symbol (", $, `, ', !), and a model
// id brackets ([1m]) that zsh would glob; a comma would make PowerShell
// pass a list.
const plainWord = /^[\w%+=:./-]+$/;
const shQuote = (s) => (plainWord.test(s) ? s : `'${s.replaceAll("'", `'\\''`)}'`);
// in "…" for a header's spaces, unless it holds what bash expands there
const shHeader = (s) => (/[$`"\\!]/.test(s) ? `'${s.replaceAll("'", `'\\''`)}'` : `"${s}"`);
const psQuote = (s) => (plainWord.test(s) ? s : `'${s.replace(/['‘’‚‛]/g, "$&$&")}'`);
// "…" as the C runtime splits a command line: \" for a quote, and the
// backslashes before one doubled
const winArg = (s) => (plainWord.test(s) ? s : `"${s.replace(/(\\*)"/g, '$1$1\\"').replace(/(\\+)$/, "$1$1")}"`);
const psHeader = (s) => (/[$`"“”„]/.test(s) ? `'${s.replace(/['‘’‚‛]/g, "$&$&")}'` : `"${s}"`);

function curlSnippet({ url, headers, body }) {
  headers = [...headers, "Content-Type: application/json"];
  // curl reads [ ] { } in a URL as a range unless told not to
  const g = /[[\]{}]/.test(url) ? " -g" : "";
  // A " inside an argument reaches curl.exe differently from Windows
  // PowerShell 5.1 and from PowerShell 7, and whole from neither as
  // written; after --% the line goes to curl.exe as it stands, on one line,
  // quoted as the C runtime reads a command line. (A %NAME% of a variable
  // the shell has is still put in there; a key would have to hold both.)
  if (snipWin() && headers.some((h) => h.includes('"'))) return `@'\n${body}\n'@ | curl.exe --%${g} ${winArg(url)} ${headers.map((h) => `-H ${winArg(h)}`).join(" ")} --data-binary "@-"`;
  if (snipWin()) return `@'\n${body}\n'@ | curl.exe${g} ${psQuote(url)} \`\n${headers.map((h) => `  -H ${psHeader(h)} \``).join("\n")}\n  --data-binary "@-"`;
  return `curl${g} ${shQuote(url)} \\\n${headers.map((h) => `  -H ${shHeader(h)} \\`).join("\n")}\n  -d '${body.replaceAll("'", `'\\''`).replaceAll("\n", "\n      ")}'`;
}

function envSnippet(vars) {
  return vars.map(([k, v]) => (snipWin() ? `$env:${k}=${psHeader(v)}` : `export ${k}=${shQuote(v)}`)).join("\n");
}

// every exposed model, as the ids agents use
function gatewayModels() {
  // the routing groups first, as the agents' pickers list them
  // a group's lists may come as null (#1585): one such group mustn't take
  // every model off the list
  const out = (providers.gateway.groups || []).filter((g) => g && typeof g.id === "string").map((g) => ({ id: g.id, name: g.name || g.id, icons: g.icons || [], group: true,
    provider: { name: [t("routing group"), (g.providers || []).join(", ")].filter(Boolean).join(" · ") },
    efforts: g.efforts || [], images: !!g.images, context: g.context, output: g.output, members: g.members || [] }));
  // a provider switched off is skipped: the gateway refuses its models, so
  // they are not ids an agent can use, and listing them here would show a
  // model the picker's own list, the Gateway count and the Connect example
  // all say works but a request to it turns away (the same off gate the
  // backend's providerEntries and the fallback picker already honour).
  // the levels agents are offered: those kept, else all it has; one whose
  // levels aren't known (given) offers only those it was given; one sent
  // no reasoning field (reasonless) offers none
  const offered = (m) => m.reasonless ? [] : m.kept?.length || m.given ? m.kept || [] : m.efforts || [];
  for (const p of providers.providers) { if (p.off) continue; for (const m of p.models) if (m.on) out.push({ id: `${p.id}/${m.id}`, name: m.name, provider: p, context: m.context, output: m.output, efforts: offered(m), images: !!m.images }); }
  return out;
}

// Connect is set up once and seldom looked at again, so it folds away under
// its heading, the base URL left beside it; the fold is remembered
// (connectFolded, with the tab's other choices at the top).

function renderConnect() {
  const g = providers.gateway;
  const box = $("#connect");
  box.replaceChildren();
  const f = FLAVORS[flavor] || FLAVORS.openai;
  // System One's snippets take a decision model, the others any other
  const models = gatewayModels().filter((m) => decideEntry(m) === !!f.decide);
  let model;
  if (f.decide) {
    if (!models.some((m) => m.id === decideModel)) decideModel = models[0]?.id || "";
    model = decideModel || "provider/jev-latest";
  } else {
    if (!models.some((m) => m.id === exampleModel)) exampleModel = models[0]?.id || "";
    model = exampleModel || "provider/model";
  }
  // no SDK speaks System One, so it has no shell variables to set
  const langs = f.baseEnv ? langs4() : langs4().filter(([id]) => id !== "shell");
  const snipLang = langs.some(([id]) => id === lang) ? lang : "curl";
  const urls = [g.url, ...(g.lanURLs || [])];
  // the address picked is kept (coeo91): an address not offered right now
  // (the network not up yet after a start) shows this computer's, and the
  // pick comes back with it. With none picked, MAGPIE_PUBLIC_URL is where
  // it starts: it says the address other machines use (#1551)
  const first = g.publicURL && urls.includes(g.publicURL) ? g.publicURL : g.url;
  connectURL = urls.includes(connectWant) ? connectWant : first;
  // a gateway MAGPIE_ADDR puts on the network (a Docker image's, whose
  // host is another machine to it) is shown with a gateway key everywhere
  const remote = connectURL !== g.url || !!g.onNetwork;
  const keys = g.lan ? (gatewayKeys || []).filter((k) => !k.off) : [];
  if (!keys.some((k) => k.id === connectKeyID)) {
    connectKeyID = "";
    connectSecret = null;
  }
  if (!connectKeyID && connectKeyWant && keys.some((k) => k.id === connectKeyWant)) {
    queueMicrotask(() => selectConnectKey(connectKeyWant));
  } else if (remote && !connectKeyID && keys.length) {
    queueMicrotask(() => selectConnectKey(keys[0].id));
  }
  const key = keys.find((k) => k.id === connectKeyID);
  const secret = key ? (connectSecret?.id === key.id ? connectSecret.secret : "") : remote ? "" : "magpie";
  const base = f.base(connectURL);
  const fold = $("#foldConnect");
  fold.setAttribute("aria-expanded", String(!connectFolded));
  fold.title = t(connectFolded ? "Show how to connect" : "Fold Connect away");
  box.hidden = connectFolded;
  // folded, the head keeps the one thing reached for: the address, to copy
  const note = $("#connectNote");
  note.replaceChildren();
  note.classList.toggle("brief", connectFolded);
  if (connectFolded) note.append(el("code", "", base), copyBtn(base, "Base URL"));
  else note.textContent = t(remote ? "Local network · an enabled gateway key is required" : g.lan && gatewayKeys?.length ? "Use a gateway key to track usage" : "Loopback only · the key can be anything");

  box.append(...field("API", segs(Object.entries(FLAVORS).map(([k, v]) => [k, v.name]), flavor, (id) => { flavor = id; localStorage.setItem("magpie.flavor", id); renderConnect(); }), t(f.note)));

  const b = el("div", "val");
  if (urls.length > 1) b.append(connectPick("connectAddress", "Address", base,
    urls.map((u) => ({ v: u, name: f.base(u), note: t(u === g.url ? "This computer" : u === g.publicURL ? "Public address" : "Local network") })), connectURL,
    (u) => { connectWant = u; keepPick("magpie.connectAddress", u); renderConnect(); }));
  else b.append(el("code", "", base));
  b.append(copyBtn(base, "Base URL"));
  box.append(...field("Base URL", b, f.baseEnv ? t("What {env} takes.", { env: f.baseEnv }) : t("Requests go to POST {url}.", { url: base + "/systemone" })));

  const k = el("div", "val");
  const options = keys.map((k) => ({ v: k.id, name: k.name, literalName: true, note: k.masked }));
  if (!remote) options.unshift({ v: "", name: "This computer", note: t("Any key · no key attribution") });
  const keyLabel = remote || key ? "Gateway key" : "API key";
  if (!g.lan) k.append(el("code", "", "magpie"));
  else if (options.length) k.append(connectPick("connectKey", keyLabel, key ? key.name : remote ? t("Choose a gateway key") : t("This computer"),
    options, connectKeyID, (id) => { connectKeyWant = id; keepPick("magpie.connectKey", id); selectConnectKey(id); }));
  else k.append(el("code", "", t("Create a gateway key above to connect")));
  if (key) k.append(copyCallerKeyBtn(key));
  else if (!remote) k.append(copyBtn("magpie", t("Key")));
  box.append(...field(t(keyLabel), k, !f.keyEnv ? t(g.lan && (remote || gatewayKeys?.length)
    ? "Choose a gateway key to send as Authorization: Bearer; usage is tracked by key."
    : "Sent as Authorization: Bearer. The gateway trusts everything on loopback, so any value works.") : t(g.lan && (remote || gatewayKeys?.length)
    ? "Choose a gateway key to use as {env}; usage is tracked by key."
    : "{env}=magpie. The gateway trusts everything on loopback, so any value works.", { env: f.keyEnv })));

  const m = el("div", "val");
  m.append(el("code", "", model), copyBtn(model, t("Model id")));
  box.append(...field(t("Model"), m, f.decide
    ? t(models.length ? "A decision model (Jev, Clef), as listed below. Click one there to put it in the snippets." : "No decision models yet. Add a System One, Jev or Workers AI provider.")
    : t(models.length ? "provider/model, as listed below. Click a model there to put it in the snippets." : "No models yet. Add a provider, or sign in to Codex or Copilot.")));

  const ex = el("div", "stack");
  const tabs = el("div", "snip-tabs");
  tabs.append(segs(langs, snipLang, (id) => { lang = id; localStorage.setItem("magpie.lang", id); renderConnect(); }));
  // the shell the Shell and curl snippets are for; Python and Node read the same everywhere
  if (snipLang === "shell" || snipLang === "curl") {
    const os = segs(SNIP_OSES.map(([id, name]) => [id, t(name)]), snipWin() ? "windows" : "unix", (id) => {
      snipOS = id;
      try { localStorage.setItem("magpie.snipOS", id); } catch {}
      renderConnect();
    });
    os.classList.add("snip-os");
    os.setAttribute("role", "group");
    os.setAttribute("aria-label", t("Where the snippet runs"));
    os.title = t("Where the snippet runs");
    tabs.append(os);
  }
  ex.append(tabs);
  const code = !secret ? t(key ? "Loading gateway key…" : "Create a gateway key above to connect")
    : snipLang === "shell" ? envSnippet([[f.baseEnv, base], [f.keyEnv, secret]])
    : snipLang === "curl" ? curlSnippet(f.curl(base, model, secret))
    : f[snipLang](base, model, secret);
  const pre = el("pre", "snip");
  const c = el("code");
  c.append(highlight(code, snipLang));
  pre.append(c);
  // the button sits outside the scrolling box, so a long line doesn't carry it off
  const wrap = el("div", "snip-wrap");
  wrap.append(pre);
  if (secret) wrap.append(copyBtn(code, t("Snippet")));
  ex.append(wrap);
  box.append(...field(t("Example"), ex, snipLang === "shell" ? t("Put these in the shell (or the tool's settings) and the tool talks to magpie instead of the vendor.") : ""));
}

let modelQuery = "";
// The model list can be folded away, so Recent calls sits under Connect;
// the fold is remembered.
let modelsFolded = false;
try { modelsFolded = localStorage.getItem("magpie.gwModelsFolded") === "1"; } catch {}

function renderGatewayModels() {
  const list = $("#gwModels");
  list.replaceChildren();
  const all = gatewayModels();
  const fold = $("#foldModels");
  fold.setAttribute("aria-expanded", String(!modelsFolded));
  fold.title = t(modelsFolded ? "Show the models" : "Fold the models away");
  $("#modelsCount").textContent = all.length ? String(all.length) : "";
  list.hidden = modelsFolded && all.length > 0;
  // the search sits in the section head, beside Copy all ids; typing
  // redraws only the list, so it keeps its focus
  let q = $("#findModel");
  if (!q) {
    q = input(modelQuery, t("Find a model…"));
    q.id = "findModel";
    q.className = "find";
    q.oninput = () => { modelQuery = q.value; renderGatewayModels(); };
    q.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape" && q.value) { q.value = modelQuery = ""; renderGatewayModels(); } };
    $("#copyModels").before(q);
  }
  q.hidden = all.length < 5 || list.hidden;
  q.title = t("Words match a model's id, name, provider or a group's models");
  const words = modelQuery.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const models = q.hidden ? all : all.filter((m) => {
    const hay = `${m.id} ${m.name || ""} ${m.provider.name} ${(m.members || []).join(" ")}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
  $("#copyModels").hidden = !models.length || list.hidden;
  $("#copyModels").onclick = () => copy(models.map((m) => m.id).join("\n"), t("Model ids"));
  if (!all.length) {
    const empty = el("div", "empty-state", "");
    empty.append(el("b", "", t("No models exposed yet")), t("Add a provider, or sign in to Codex or Copilot; their models show up here for every agent."));
    list.append(empty);
    return;
  }
  if (!models.length) {
    list.append(el("div", "none", t("No models match “{q}”", { q: modelQuery.trim() })));
    return;
  }
  for (const m of models) {
    const row = el("div", "row model" + (m.id === (FLAVORS[flavor]?.decide ? decideModel : exampleModel) ? " selected" : ""));
    const who = el("div", "who");
    const name = el("div", "name", m.id);
    if (namedFree(m.id, m.name)) name.append(freeBadge(false));
    who.append(name, el("div", "sub", m.name && m.name !== m.id.split("/")[1] ? `${m.name} · ${m.provider.name}` : m.provider.name));
    row.append(m.group ? stackIcon(m.icons) : icon(m.provider.icon || "generic"), who, modelInfo(m), copyBtn(m.id, t("Model id")));
    row.title = t("Use this model in the snippets");
    // a decision model is put in System One's snippets, any other in the
    // other APIs'; the API follows the model clicked
    row.onclick = () => {
      const selection = window.getSelection();
      if (selection && !selection.isCollapsed && row.contains(selection.anchorNode)) return;
      const so = decideEntry(m);
      if (so) { decideModel = m.id; localStorage.setItem("magpie.decideModel", m.id); }
      else { exampleModel = m.id; localStorage.setItem("magpie.model", m.id); }
      if (so !== !!FLAVORS[flavor]?.decide) { flavor = so ? "systemone" : "openai"; localStorage.setItem("magpie.flavor", flavor); }
      renderConnect(); renderGatewayModels();
    };
    list.append(row);
  }
}

// A recent call's bodies, which /api/providers leaves out: forty calls'
// bodies made every refresh of it 20 MB (#1521).
function loadCallBodies(c, id) {
  const pending = { busy: true };
  callBodies.set(id, pending);
  api(`gateway/call?id=${encodeURIComponent(c.id)}`)
    .then((body) => { if (callBodies.get(id) === pending) callBodies.set(id, body); })
    .catch((e) => { if (callBodies.get(id) === pending) callBodies.set(id, { error: t(e.message) }); })
    .finally(() => { if (view === "gateway" && providers) renderActivity(); });
}

// a recent call's row: by the gateway's number for it, as two calls of
// one agent to one model can come in the same instant
const callRow = (c) => (c.id ? `#${c.id}` : `${c.time}|${c.agent}|${c.model}`);

function renderActivity() {
  const g = providers.gateway;
  // what calls left the ring keep, they let go of
  const ids = new Set(g.calls.map(callRow));
  for (const id of callBodies.keys()) if (!ids.has(id)) callBodies.delete(id);
  for (const id of expandedCalls) if (!ids.has(id)) expandedCalls.delete(id);
  for (const id of wireOpen) if (!ids.has(id)) wireOpen.delete(id);
  const box = $("#activity");
  // a body being read keeps its place as new calls come in
  const kept = new Map();
  for (const item of box.querySelectorAll(".call-item[data-id]")) kept.set(item.dataset.id, [...item.querySelectorAll("pre")].map((p) => p.scrollTop));
  box.replaceChildren();
  $("#callsNote").textContent = g.running && !g.mine ? t("shown by the magpie that serves the gateway") : "";
  const calls = g.calls.slice(0, callsShown);
  if (!calls.length) { box.append(el("div", "none", t("No requests yet. Point an agent at a model, or run the example above; every call shows up here as it happens."))); return; }
  for (const c of calls) {
    const id = callRow(c);
    const open = expandedCalls.has(id);
    const item = el("div", "call-item" + (open ? " open" : "") + (c.status >= 400 ? " bad" : ""));
    const r = el("div", "call");
    r.setAttribute("role", "button");
    r.setAttribute("tabindex", "0");
    r.setAttribute("aria-expanded", String(open));
    const chev = el("span", "call-chev");
    chev.append(svg(CHEV_R, 11, 1.6));
    r.append(chev);
    r.append(el("span", "when", new Date(c.time).toLocaleTimeString(intlLang(), { hour: "2-digit", minute: "2-digit", second: "2-digit" })));
    r.append(el("span", "a", c.agent || "—"));
    r.append(el("span", "m", c.model));
    r.append(el("span", "p", c.from === c.to ? c.from : `${c.from} → ${c.to}`));
    // what masking kept from the vendor, its record a click away
    if (c.masked?.n || c.masked?.warned) r.append(maskedBadge(c.masked));
    r.append(el("span", "grow"));
    r.append(el("span", "st", c.error ? `${c.status} ${c.error}` : `${c.status} · ${ledTook(c.ms)}` + (c.ttft ? " · " + t("TTFT {ms}", { ms: ledTook(c.ttft) }) : "")));
    r.title = open ? t("Hide request and response bodies") : t("Show request and response bodies");
    const toggle = () => {
      if (expandedCalls.has(id)) expandedCalls.delete(id); else expandedCalls.add(id);
      renderActivity();
    };
    r.onclick = toggle;
    r.onkeydown = (ev) => {
      if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); toggle(); }
    };
    item.append(r);
    if (open) {
      if (!callBodies.has(id)) loadCallBodies(c, id);
      const body = callBodies.get(id);
      const details = el("div", body.busy || body.error ? "" : "call-details");
      if (body.busy) details.append(el("div", "none", t("Fetching…")));
      else if (body.error) {
        details.append(el("div", "call-archive-err", body.error));
        const retry = el("button", "text", t("Try again"));
        retry.onclick = () => { loadCallBodies(c, id); renderActivity(); };
        details.append(retry);
      } else details.append(
        callBodyPanel("Request Body", body.requestBody, body.requestTruncated, undefined, id + "|req"),
        callBodyPanel("Response Body", body.responseBody, body.responseTruncated, id),
      );
      item.dataset.id = id;
      item.append(details);
      // the headers both ways, folded (wireHeadersView, usage/page.js)
      if (body.headers) item.append(wireHeadersView(body.headers, wireOpen.has(id), () => {
        if (wireOpen.has(id)) wireOpen.delete(id); else wireOpen.add(id);
        renderActivity();
      }));
    }
    box.append(item);
  }
  if (g.calls.length > calls.length) {
    const more = el("button", "text activity-more");
    more.textContent = t("Show {n} more", { n: Math.min(callsPage, g.calls.length - calls.length) });
    more.onclick = () => {
      callsShown = Math.min(g.calls.length, callsShown + callsPage);
      renderActivity();
    };
    box.append(more);
  }
  for (const item of box.querySelectorAll(".call-item[data-id]")) {
    const tops = kept.get(item.dataset.id) || [];
    item.querySelectorAll("pre").forEach((p, i) => { if (tops[i]) p.scrollTop = tops[i]; });
  }
}


magpie.plugin("gateway", (ctx) => {
  const configured = () => {
    callsPage = ctx.config.callsPage || 20;
    callsShown = callsPage;
  };
  configured();
  ctx.onConfig(() => {
    configured();
    magpie.call("gateway", "calls");
  });
  ctx.slots.add("page", {
    key: "gateway",
    title: "Gateway",
    // what the page draws from: the shell reads /api/providers for it
    loads: "providers",
    html: `
<div class="list gwcard">
  <div class="row gw" id="gateway"></div>
  <div id="gatewayLan"></div>
</div>
<section id="gatewayKeysBlock" hidden>
  <div class="row-head"><span class="label" data-t>Gateway keys</span><span class="grow"></span><button class="text" id="addGatewayKey" data-t>Add gateway key</button></div>
  <p class="usage-note" data-t>Create a gateway key for each client. Usage is counted separately for each key.</p>
  <section id="gatewayKeys"></section>
</section>
<p class="usage-note gw-keys-off" id="gatewayKeysOff" hidden data-t>Gateway keys are for other computers, and for a tunnel to this one such as Cloudflare Tunnel: they are added here once Share on local network is on. Agents on this computer need none.</p>
<div class="row-head"><button class="fold" id="foldConnect" aria-expanded="true" aria-controls="connect"><span class="label" data-t>Connect</span></button><span class="grow"></span><span class="note" id="connectNote"></span></div>
<section class="list connect" id="connect"></section>
<div class="row-head"><button class="fold" id="foldModels" aria-expanded="true" aria-controls="gwModels"><span class="label" data-t>Models</span><span class="note" id="modelsCount"></span></button><span class="grow"></span><button class="text" id="copyModels" data-t>Copy all ids</button></div>
<div class="list" id="gwModels"></div>
<div class="row-head"><span class="label" data-t>Recent calls</span><span class="grow"></span><span class="note" id="callsNote"></span></div>
<div class="list activity" id="activity"></div>
`,
    mount() {
      $("#foldConnect").prepend(svg(CHEV_R, 11, 1.6));
      $("#foldConnect").onclick = () => {
        connectFolded = !connectFolded;
        try { localStorage.setItem("magpie.gwConnectFolded", connectFolded ? "1" : "0"); } catch {}
        renderConnect();
        backToReader($("#view-gateway"));
      };
      $("#foldModels").prepend(svg(CHEV_R, 11, 1.6));
      $("#foldModels").onclick = () => {
        modelsFolded = !modelsFolded;
        try { localStorage.setItem("magpie.gwModelsFolded", modelsFolded ? "1" : "0"); } catch {}
        renderGatewayModels();
        backToReader($("#view-gateway"));
      };
      new MutationObserver(gatewayKeysTicks).observe($("#view-gateway"), { attributes: true, attributeFilter: ["hidden"] });
      document.addEventListener("visibilitychange", gatewayKeysTicks);
      gatewayKeysTicks();
    },
    // nothing read yet: the page's skeleton (#123)
    loading: renderGatewayLoading,
    // opened from another page that had the list: drawn from it at once,
    // not left as empty cards until the new one is in (#123)
    draft() {
      if (!$("#gateway").childElementCount) renderGatewayView();
    },
    // The page is looked at and left open: coming back to the window
    // redraws it only when something on it changed, and when only the calls
    // did, only them (an account's last sign-in check isn't on it).
    render(was) {
      const json = (a) => JSON.stringify(a, (k, v) => (k === "seen" ? undefined : v));
      const same = (a, b) => json(a) === json(b);
      if (was && !$("#view-gateway").classList.contains("loading") && same({ ...was, gateway: { ...was.gateway, calls: 0 } }, { ...providers, gateway: { ...providers.gateway, calls: 0 } })) {
        if (!same(was.gateway.calls, providers.gateway.calls)) renderActivity();
      } else renderGatewayView();
      // and what each key has used of its limits, read on open only before
      refreshGatewayKeys();
    },
    // the recent calls again, a page of them (the setting changed)
    calls() {
      if (view === "gateway" && providers && !$("#view-gateway").classList.contains("loading")) renderActivity();
    },
    open: loadGatewayKeys,
  });
});

// A recorded call's request and response bodies, drawn here for the
// recent calls and shared with Usage's ledger and Routing's trace, which
// read the same calls (callBodyPanel); moved out of app.js, the core.
// a streamed reply's body is read as its reply, its events or as it came;
// the pick is one for every call and kept, the events drawn per call
let sseView = "events";
try { sseView = localStorage.getItem("magpie.sseView") || "events"; } catch {}
const sseShown = new Map(); // call id → how many events are drawn

function formatWireBody(raw) {
  if (!raw) return "";
  try { return JSON.stringify(JSON.parse(raw), null, 2); } catch { return raw; }
}

// A JSON body as a tree: each object and array folds at its bracket, its
// parts in the snippets' colours; the copy button a line shows on hover
// copies its value, and a click on a key copies the key. The text is the
// JSON pretty-printed, as before, so what is selected and copied by hand
// still reads as JSON. What is folded is kept by the body's key and the
// node's path, across the redraws a new call brings.
const jsonFolds = new Set();
function jsonTree(v, key) {
  const code = el("code", "jt");
  code.append(jtNode(v, undefined, "", key + "|", ""));
  return code;
}
function jtNode(v, k, indent, path, comma) {
  const f = document.createDocumentFragment();
  const head = el("span", "jt-head");
  f.append(indent, head);
  if (k !== undefined) {
    const ks = el("span", "jt-key", JSON.stringify(k));
    ks.title = t("Click to copy the key");
    // a click copies; a drag selects, as in any text
    ks.onclick = (ev) => {
      ev.stopPropagation();
      if (!String(getSelection())) copy(String(k), String(k), null);
    };
    head.append(ks, ": ");
  }
  const copyV = el("button", "jt-copy");
  copyV.type = "button";
  copyV.title = t("Copy value");
  copyV.append(svg(COPY_ICON, 11, 1.5));
  // a string's value is its text, not the JSON that quotes it
  copyV.onclick = (ev) => {
    ev.stopPropagation();
    copy(typeof v === "string" ? v : JSON.stringify(v, null, 2), k !== undefined ? String(k) : t("Value"), copyV);
  };
  if (v === null || typeof v !== "object") {
    head.append(el("span", typeof v === "string" ? "tk-s" : typeof v === "number" ? "tk-n" : "tk-k", JSON.stringify(v)), copyV);
    f.append(comma);
    return f;
  }
  const arr = Array.isArray(v);
  const keys = arr ? v.map((_, i) => i) : Object.keys(v);
  const [o, c] = arr ? ["[", "]"] : ["{", "}"];
  if (!keys.length) {
    head.append(o + c, copyV);
    f.append(comma);
    return f;
  }
  const tog = el("button", "jt-fold");
  tog.type = "button";
  tog.append(svg(CHEV_R, 10, 1.6));
  const kids = el("span", "jt-kids");
  const sum = el("button", "jt-sum", "…");
  sum.type = "button";
  sum.title = t("Unfold");
  sum.dataset.n = t(keys.length === 1 ? "1 item" : "{n} items", { n: keys.length });
  // the summary is on the head's line: what a click on it holds in place is
  // then its neighbours there, not the line after, which the unfolding pushes down
  head.prepend(tog);
  head.append(o, sum, copyV);
  f.append(kids, c, comma);
  let built = false;
  const set = (folded) => {
    if (!folded && !built) { // its insides are drawn the first time it opens
      built = true;
      keys.forEach((kk, i) => kids.append("\n", jtNode(v[kk], arr ? undefined : kk, indent + "  ", path + "/" + kk, i < keys.length - 1 ? "," : "")));
      kids.append("\n" + indent);
    }
    kids.hidden = folded;
    sum.hidden = !folded;
    tog.classList.toggle("open", !folded);
    tog.setAttribute("aria-expanded", String(!folded));
    tog.title = t(folded ? "Unfold" : "Fold");
    if (folded) jsonFolds.add(path); else jsonFolds.delete(path);
  };
  // the box keeps its height across a fold, so what is below it, and the
  // page, stay where they are
  const keep = () => { const pre = tog.closest("pre"); if (pre) pre.style.minHeight = pre.offsetHeight + "px"; };
  tog.onclick = (ev) => { ev.stopPropagation(); keep(); set(!kids.hidden); };
  sum.onclick = (ev) => { ev.stopPropagation(); keep(); set(false); };
  set(jsonFolds.has(path));
  return f;
}

// A streamed body (server-sent events) as its events: each one's name and
// its data, the data lines joined. Anything else is not one: null.
function parseSSE(raw) {
  if (!raw || !/^\s*(?::[^\n]*\n\s*)*(event|data|id|retry):/.test(raw)) return null;
  const events = [];
  let ev = null;
  const done = () => { if (ev && ev.data.length) events.push({ event: ev.event, data: ev.data.join("\n") }); ev = null; };
  for (const line of raw.split(/\r?\n/)) {
    if (line === "") { done(); continue; }
    if (line[0] === ":") continue; // a comment, a keep-alive
    const i = line.indexOf(":");
    const field = i < 0 ? line : line.slice(0, i);
    let value = i < 0 ? "" : line.slice(i + 1);
    if (value[0] === " ") value = value.slice(1);
    ev = ev || { event: "", data: [] };
    if (field === "event") ev.event = value;
    else if (field === "data") ev.data.push(value);
  }
  done();
  return events.length ? events : null;
}

const jsonOr = (s) => { try { return JSON.parse(s); } catch { return undefined; } };

// What a stream adds up to — its text, reasoning and tool calls, how it
// stopped and what it used — for the Anthropic Messages, OpenAI Responses,
// Chat Completions and Gemini streams; null for one it can't read.
function sseReply(events) {
  const out = {};
  const blocks = [];      // Anthropic content blocks, Chat tool calls, Responses items, in order
  const byKey = new Map();
  const block = (key, init) => { let b = byKey.get(key); if (!b) { b = { ...init }; byKey.set(key, b); blocks.push(b); } return b; };
  let kind = "";
  for (const e of events) {
    const d = jsonOr(e.data);
    if (!d || typeof d !== "object") continue;
    const type = d.type || e.event || "";
    if (d.error) { out.error = d.error; continue; }
    if (type === "message_start" && d.message) {
      kind = "anthropic";
      out.id = d.message.id; out.model = d.message.model;
      if (d.message.usage) out.usage = { ...d.message.usage };
    } else if (type === "content_block_start" && d.content_block) {
      kind = "anthropic";
      const cb = d.content_block;
      const b = block("a" + d.index, { type: cb.type });
      if (cb.type === "tool_use" || cb.type === "server_tool_use") { b.id = cb.id; b.name = cb.name; b.input = ""; }
      else if (cb.type === "text") b.text = cb.text || "";
      else if (cb.type === "thinking") b.thinking = cb.thinking || "";
      else Object.assign(b, cb);
    } else if (type === "content_block_delta" && d.delta) {
      const b = block("a" + d.index, { type: "text" });
      const x = d.delta;
      if (x.type === "text_delta") b.text = (b.text || "") + x.text;
      else if (x.type === "thinking_delta") b.thinking = (b.thinking || "") + x.thinking;
      else if (x.type === "input_json_delta") b.input = (b.input || "") + x.partial_json;
    } else if (type === "message_delta") {
      if (d.delta?.stop_reason) out.stop_reason = d.delta.stop_reason;
      if (d.usage) out.usage = { ...out.usage, ...d.usage };
    } else if (type.startsWith("response.")) {
      kind = "responses";
      if (d.response) {
        out.id = d.response.id; out.model = d.response.model; out.status = d.response.status;
        if (d.response.usage) out.usage = d.response.usage;
        if (d.response.error) out.error = d.response.error;
        if (d.response.incomplete_details) out.incomplete = d.response.incomplete_details;
        if (type === "response.completed" && d.response.output?.length) out.output = d.response.output;
      }
      const key = "r" + (d.item_id || d.item?.id || d.output_index);
      if (type === "response.output_item.added" || type === "response.output_item.done") {
        const it = d.item || {};
        const b = block(key, { type: it.type });
        if (it.type === "function_call" || it.type === "custom_tool_call") { b.call_id = it.call_id; b.name = it.name; }
        if (type === "response.output_item.done") {
          if (it.arguments !== undefined) b.arguments = it.arguments;
          if (it.input !== undefined) b.input = it.input;
          const text = (it.content || []).map((c) => c.text || c.refusal || "").join("");
          if (text) b.text = text;
          const sum = (it.summary || []).map((c) => c.text || "").join("\n\n");
          if (sum) b.summary = sum;
        }
      } else if (type === "response.output_text.delta" || type === "response.refusal.delta") {
        const b = block(key, { type: "message" }); b.text = (b.text || "") + d.delta;
      } else if (type === "response.reasoning_summary_text.delta") {
        const b = block(key, { type: "reasoning" }); b.summary = (b.summary || "") + d.delta;
      } else if (type === "response.reasoning_text.delta") {
        const b = block(key, { type: "reasoning" }); b.text = (b.text || "") + d.delta;
      } else if (type === "response.function_call_arguments.delta") {
        const b = block(key, { type: "function_call" }); b.arguments = (b.arguments || "") + d.delta;
      } else if (type === "response.custom_tool_call_input.delta") {
        const b = block(key, { type: "custom_tool_call" }); b.input = (b.input || "") + d.delta;
      }
    } else if (Array.isArray(d.choices)) {
      kind = "chat";
      if (d.id) out.id = d.id;
      if (d.model) out.model = d.model;
      if (d.usage) out.usage = d.usage;
      for (const c of d.choices) {
        const x = c.delta || c.message || {};
        const msg = block("c" + (c.index || 0), { type: "message" });
        if (typeof x.content === "string") msg.content = (msg.content || "") + x.content;
        const r = x.reasoning_content ?? x.reasoning;
        if (typeof r === "string") msg.reasoning = (msg.reasoning || "") + r;
        for (const tc of x.tool_calls || []) {
          const b = block("c" + (c.index || 0) + ":" + (tc.index ?? tc.id), { type: "tool_call" });
          if (tc.id) b.id = tc.id;
          if (tc.function?.name) b.name = (b.name || "") + tc.function.name;
          if (tc.function?.arguments) b.arguments = (b.arguments || "") + tc.function.arguments;
        }
        if (c.finish_reason) out.finish_reason = c.finish_reason;
      }
    } else if (Array.isArray(d.candidates)) {
      kind = "gemini";
      if (d.modelVersion) out.model = d.modelVersion;
      if (d.usageMetadata) out.usage = d.usageMetadata;
      for (const c of d.candidates) {
        for (const p of c.content?.parts || []) {
          if (typeof p.text === "string") {
            const b = block("g" + (c.index || 0) + (p.thought ? "t" : ""), { type: p.thought ? "thought" : "text" });
            b.text = (b.text || "") + p.text;
          } else if (p.functionCall) blocks.push({ type: "functionCall", ...p.functionCall });
        }
        if (c.finishReason) out.finishReason = c.finishReason;
      }
    }
  }
  if (!kind && !out.error) return null;
  // the Responses API's own finished output is the reply, when it came
  const content = (out.output || blocks).filter((b) => !(kind === "chat" && b.type === "message" && !b.content && !b.reasoning));
  // a tool's arguments read as the object they spell, when they are whole
  for (const b of content) {
    for (const k of ["input", "arguments"]) {
      if (typeof b[k] !== "string") continue;
      const v = b[k] === "" && k === "input" ? {} : jsonOr(b[k]);
      if (v !== undefined) b[k] = v;
    }
  }
  const reply = {};
  for (const k of ["id", "model", "status"]) if (out[k] !== undefined) reply[k] = out[k];
  if (content.length) reply[{ anthropic: "content", responses: "output", gemini: "parts" }[kind] || "choices"] = content;
  for (const k of ["stop_reason", "finish_reason", "finishReason", "incomplete", "usage", "error"]) if (out[k] !== undefined) reply[k] = out[k];
  return reply;
}

// The data of each event, as JSON where it is JSON, under its name.
function sseEventNode(e, key) {
  const box = el("span", "sse-ev");
  if (e.event) box.append(el("span", "sse-name", "event: " + e.event + "\n"));
  const d = jsonOr(e.data);
  box.append(d !== null && typeof d === "object" ? jtNode(d, undefined, "", key + "|", "") : e.data); // [DONE] as it is
  return box;
}
function sseData(data) {
  const d = jsonOr(data);
  return d !== null && typeof d === "object" ? JSON.stringify(d, null, 2) : data; // [DONE] as it is
}
const SSE_PAGE = 200; // events drawn at a time: a long stream stays quick to open

function sseBodyPanel(label, raw, truncated, id) {
  const events = parseSSE(raw);
  if (!events) return null;
  const reply = sseReply(events);
  const panel = el("section", "call-body sse");
  const head = el("div", "call-body-head");
  head.append(el("span", "call-body-label", t(label)));
  if (truncated) head.append(el("span", "call-body-truncated", typeof truncated === "string" ? truncated : t("first 256 KB")));
  const views = [["reply", t("Reply")], ["events", t("Events")], ["raw", t("Raw")]].filter(([v]) => v !== "reply" || reply);
  let view = views.some(([v]) => v === sseView) ? sseView : "events";
  const count = el("span", "call-body-count", t(events.length === 1 ? "1 event" : "{n} events", { n: events.length }));
  const pick = segs(views, view, (v) => { view = sseView = v; try { localStorage.setItem("magpie.sseView", v); } catch {} draw(); });
  pick.classList.add("sse-views");
  const copyB = copyBtn("", t(label)); // what it copies is set below
  head.append(count, el("span", "grow"), pick, copyB);
  panel.append(head);
  const pre = el("pre");
  // a long stream's events come a page at a time; what draws more sits
  // under the box, where it stays as they come in rather than at the end
  // of what is drawn
  const foot = el("div", "call-body-foot");
  const shownNote = el("span");
  const moreB = el("button", "link", "");
  foot.append(shownNote, el("span", "grow"), moreB);
  panel.append(pre, foot);
  let code = null, shown = 0;
  const more = () => {
    const upto = Math.min(events.length, Math.max(sseShown.get(id) || 0, shown + SSE_PAGE));
    for (; shown < upto; shown++) code.append(sseEventNode(events[shown], id + "|ev" + shown));
    if (shown > SSE_PAGE) sseShown.set(id, shown);
    foot.hidden = view !== "events" || shown >= events.length;
    shownNote.textContent = t("{n} of {total} events shown", { n: shown, total: events.length });
    moreB.textContent = t("Show {n} more events", { n: Math.min(SSE_PAGE, events.length - shown) });
  };
  moreB.onclick = (ev) => { ev.stopPropagation(); more(); };
  const draw = () => {
    // the box keeps its height across a switch, so what is below it, and
    // the page, stay where they are
    if (pre.isConnected) pre.style.minHeight = pre.offsetHeight + "px";
    code = view === "reply" ? jsonTree(reply, id + "|reply") : el("code", view === "events" ? "jt" : "");
    pre.replaceChildren(code);
    pre.scrollTop = 0;
    shown = 0;
    if (view === "raw") code.textContent = raw;
    if (view === "events") more(); else foot.hidden = true;
  };
  // what is copied is what is shown: the reply, every event, or the body
  copyB.onclick = (ev) => {
    ev.stopPropagation();
    const text = view === "reply" ? JSON.stringify(reply, null, 2) : view === "raw" ? raw
      : events.map((e) => (e.event ? "event: " + e.event + "\n" : "") + sseData(e.data)).join("\n\n");
    copy(text, t(label), copyB);
  };
  draw();
  return panel;
}

// id, given, is a response's, whose stream reads as its events; key names
// the body for what is folded in it
function callBodyPanel(label, raw, truncated, id, key = id) {
  if (id) {
    const sse = sseBodyPanel(label, raw, truncated, id);
    if (sse) return sse;
  }
  const panel = el("section", "call-body");
  const head = el("div", "call-body-head");
  head.append(el("span", "call-body-label", t(label)));
  if (truncated) head.append(el("span", "call-body-truncated", typeof truncated === "string" ? truncated : t("first 256 KB")));
  const formatted = formatWireBody(raw);
  if (formatted) head.append(el("span", "grow"), copyBtn(raw, t(label)));
  panel.append(head);
  const pre = el("pre");
  const v = formatted ? jsonOr(raw) : undefined;
  if (v !== null && typeof v === "object") pre.append(jsonTree(v, key));
  else {
    const code = el("code", "", formatted || t("No body captured"));
    if (!formatted) code.classList.add("empty");
    pre.append(code);
  }
  panel.append(pre);
  return panel;
}
