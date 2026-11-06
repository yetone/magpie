const fs = require("node:fs/promises");
const path = require("node:path");
const assets = path.resolve(__dirname, "../../assets");

const sum = (rows) => ({
  calls: rows.length, errors: rows.filter((r) => r.status >= 400).length,
  input: rows.reduce((n, r) => n + r.in, 0), output: rows.reduce((n, r) => n + r.out, 0),
  cache_read: 0, cache_write: 0, reasoning: 0, cost: rows.reduce((n, r) => n + r.cost, 0), unpriced: 0,
});

function fixture(lang, theme, events, options = {}) {
  let keys = [
    { id: "laptop", name: "Laptop", masked: "sk-magpie-key-…111111" },
    { id: "server", name: "Server", masked: "sk-magpie-key-…222222" },
    { id: "work", name: "Work", masked: "sk-magpie-key-…333333" },
  ];
  // options.keys is the test's own list: [] for the empty Gateway keys
  if (options.keys) keys = options.keys;
  for (const k of keys) {
    if (options.limits?.[k.id]) Object.assign(k, options.limits[k.id]);
    if (options.keyHolds?.[k.id]) Object.assign(k, options.keyHolds[k.id]);
  }
  let serial = 0;
  let lan = !!options.lan;
  let lanKeyID = "", rotations = 0, lanSecret = "";
  const secrets = new Map([["laptop", "fixture-laptop"], ["server", "fixture-server"], ["work", "fixture-work"]]);
  // options.lanURLs is the test's own list, so it can take the network away
  const lanURLs = options.lanURLs || ["http://192.168.1.10:3999", "http://10.0.0.10:3999"];
  // options.lanPublic are those of them at an internet address
  const lanPublic = () => lan && options.lanPublic ? { lanPublic: options.lanPublic } : {};
  // options.publicURL is MAGPIE_PUBLIC_URL's, offered while shared (#1551)
  const publicURL = () => lan && options.publicURL ? { publicURL: options.publicURL } : {};
  // options.container: magpie runs in a container, and lanURLs are its
  // own; options.containerHost is where other containers on the host
  // reach the gateway (the host's Docker bridge address)
  const container = () => ({ ...(lan && options.container ? { container: true } : {}),
    ...(lan && options.containerHost ? { containerHost: options.containerHost } : {}) });
  const lanState = () => ({ lang, theme, lan,
    lanURLs: lan ? lanURLs : [], ...lanPublic(), ...(container().container ? { lanContainer: true } : {}),
    ...(container().containerHost ? { lanContainerHost: container().containerHost } : {}), fx: { rate: 7.2, at: new Date().toISOString() },
    ...(options.publicURL ? { gateway: "http://127.0.0.1:3999", ...publicURL() } : {}) });
  const rows = [
    { callerKeyId: "laptop", callerKeyName: "Laptop", in: 100, out: 10, cost: 0.1 },
    { callerKeyId: "server", callerKeyName: "Server", in: 200, out: 20, cost: 0.2 },
    { callerKeyId: "work", callerKeyName: "Work", in: 300, out: 30, cost: 0.3 },
    { in: 40, out: 4, cost: 0.04 },
  ].map((r, i) => ({
    t: new Date(Date.now() - i * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "generic", provider: "relay", providerName: "Relay",
    model: "m", req: "relay/m", ms: 1000, status: 200, priced: true, ...r,
  }));
  const keyName = (r) => keys.find((k) => k.id === r.callerKeyId)?.name || r.callerKeyName;
  const groups = () => ({
    callerKeys: ["laptop", "server", "work"].map((id) => {
      const rs = rows.filter((r) => r.callerKeyId === id);
      return { id, callerKeyId: id, name: keyName(rs[0]), icon: "generic", ...sum(rs) };
    }),
  });
  const ledger = (q) => {
    let rs = rows;
    if (q.get("callerKey")) rs = rs.filter((r) => r.callerKeyId === q.get("callerKey"));
    if (q.get("failed") === "1") rs = rs.filter((r) => r.status >= 400);
    return { ...sum(rs), ...groups(), total: rs.length, offset: 0, agents: [{ id: "codex", name: "Codex" }],
      rows: rs.map((r) => ({ ...r, callerKeyLabel: keyName(r) })) };
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme, web: !!options.web,
      // with options.publicURL or options.bootGateway, where this process's
      // gateway listens, as magpie's boot.js says it, and while shared the
      // addresses other machines use, MAGPIE_PUBLIC_URL with them
      ...(options.publicURL || options.bootGateway ? { sideGateway: { url: "http://127.0.0.1:3999", running: true, boot: true,
        ...(lan ? { lan, lanURLs } : {}), ...container(), ...publicURL() } } : {}) })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/favicon.ico") return route.fulfill({ status: 204 });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/providers") return json({
      providers: [{ id: "relay", name: "Relay", icon: "generic", models: [{ id: "m", name: "Model", on: true }], agents: [] }],
      gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", lan, lanURLs: lan ? lanURLs : [], ...lanPublic(), ...container(), ...publicURL(), calls: [], groups: [] },
    });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/lan") {
      const body = req.postDataJSON();
      events.push({ action: "lan", body });
      lan = body.on;
      let key = keys.find((k) => k.id === lanKeyID);
      if (lan && (!key || body.newKey)) {
        if (!key) {
          key = { id: "lan-key-" + (++serial), name: "Magpie", lan: true };
          lanKeyID = key.id;
          keys.push(key);
        }
        lanSecret = "fixture-lan-" + (++rotations);
        key.masked = "sk-magpie-key-…" + lanSecret.slice(-6);
      }
      return json(lanState());
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        events.push({ action: "settings", body });
        theme = body.theme;
      }
      return json(lanState());
    }
    // options.keysError: the list can't be read
    if (url.pathname === "/api/caller-keys" && options.keysError) return route.fulfill({ status: 500, json: { error: options.keysError } });
    if (url.pathname === "/api/caller-keys") return json({ keys });
    if (url.pathname === "/api/caller-keys/models") return json({ models: [
      ...(options.groups ? [
        { id: "group/coding", name: "Coding", group: true },
        { id: "group/fast", name: "Fast", group: true },
      ] : []),
      { id: "relay/m", name: "Model", provider: "relay", providerName: "Relay" },
      { id: "relay/m-mini", name: "Model mini", provider: "relay", providerName: "Relay" },
      { id: "openai/gpt-5", name: "GPT-5", provider: "openai", providerName: "OpenAI" },
      // options.deciders: decision models, which no agent lists
      ...(options.deciders ? [
        { id: "cf/@cf/cloudflare/clef", name: "Clef", provider: "cf", providerName: "Cloudflare", decides: true },
        { id: "ts/jev-latest", name: "Jev", provider: "ts", providerName: "TypeSafe", decides: true },
      ] : []),
    ], accounts: options.accounts === false ? [] : [
      { id: "relay/me@example.com", name: "me@example.com", provider: "relay", providerName: "Relay", account: true, plan: "Pro" },
      { id: "relay/spare@example.com", name: "spare@example.com", provider: "relay", providerName: "Relay", account: true },
      { id: "openai/k-1a2b3c", name: "k-1a2b3c", provider: "openai", providerName: "OpenAI", key: true },
    ] });
    if (url.pathname.startsWith("/api/caller-keys/")) {
      const action = url.pathname.split("/").at(-1), body = req.postDataJSON();
      events.push({ action, body });
      const k = keys.find((k) => k.id === body.key);
      let secret = "";
      if (action === "add-key") {
        if (!body.name.trim()) return route.fulfill({ status: 400, json: { error: "Use a name between 1 and 120 characters" } });
        // a value of the user's own, checked as access.ownSecret does
        if (body.secret !== undefined && body.secret.trim().length < 8) return route.fulfill({ status: 400, json: { error: "Use a key between 8 and 256 characters" } });
        const id = "new-key-" + (++serial);
        secret = body.secret?.trim() || "fixture-created-" + serial;
        secrets.set(id, secret);
        keys.push({ id, name: body.name, masked: body.secret ? "…" + secret.slice(-4) : "sk-magpie-key-…created" });
      }
      if (action === "rename-key") k.name = body.name;
      if (action === "rotate-key") {
        // a value of the user's own, checked as access.ownSecret does
        if (body.secret !== undefined && body.secret.trim().length < 8) return route.fulfill({ status: 400, json: { error: "Use a key between 8 and 256 characters" } });
        secret = body.secret?.trim() || "fixture-rotated-" + (++rotations);
        secrets.set(k.id, secret);
        if (k.id === lanKeyID) lanSecret = secret;
        k.masked = body.secret ? "…" + secret.slice(-4) : "sk-magpie-key-…" + secret.slice(-6);
      }
      if (action === "remove-key") keys = keys.filter((v) => v !== k);
      if (action === "on-key" || action === "off-key") k.off = action === "off-key";
      if (action === "limit-key") {
        // what a key has used is the fixture's; a new limit starts unused
        const usedOf = (l) => {
          const span = l.period === "hours" ? l.hours * 36e5 : (l.days || 1) * 864e5;
          return { period: l.period, days: l.days, hours: l.hours, start: new Date().toISOString(), reset: new Date(Date.now() + span).toISOString(),
            calls: 0, tokens: 0, cost: 0, tokenLimit: l.tokens, costLimit: l.cost, tokensLeft: l.tokens, costLeft: l.cost, spent: false,
            ...(l.models ? { models: l.models } : {}) };
        };
        // one of several by its index (#2227), as access.Update keeps them;
        // without one, the key's only limit
        const ls = body.index === undefined ? [] : k.limit ? [k.limit, ...(k.moreLimits || [])] : [];
        const us = body.index === undefined ? [] : k.usedAll || (k.used ? [k.used] : []);
        const i = body.index ?? 0;
        if (!body.limit) ls.splice(i, 1), us.splice(i, 1);
        else ls[i] = body.limit, us[i] = usedOf(body.limit);
        k.limit = ls[0];
        k.moreLimits = ls.length > 1 ? ls.slice(1) : undefined;
        k.used = us[0];
        k.usedAll = us.length > 1 ? us : undefined;
      }
      if (action === "reset-limit-key") {
        // what it used goes back to 0, tokens and cost together (#1509);
        // an N-day or N-hour cycle starts again now, a calendar window keeps
        // its end; one limit of several by its index, or every one (#2227)
        const now = Date.now();
        const reset = (u) => ({ ...u, start: new Date(now).toISOString(), calls: 0, tokens: 0, cost: 0, inFlight: 0, unpriced: 0, spent: false,
          tokensLeft: u.tokenLimit || 0, costLeft: u.costLimit || 0,
          reset: u.period === "days" ? new Date(now + u.days * 864e5).toISOString() : u.period === "hours" ? new Date(now + u.hours * 36e5).toISOString() : u.reset });
        if (k.usedAll) k.usedAll = k.usedAll.map((u, i) => (body.index === undefined || body.index === i ? reset(u) : u)), k.used = k.usedAll[0];
        else k.used = reset(k.used);
      }
      if (action === "models-key") k.models = body.models?.length ? body.models : undefined;
      if (action === "accounts-key") k.accounts = body.accounts?.length ? body.accounts : undefined;
      if (action === "quotas-key") k.quotas = body.quotas || undefined;
      if (action === "access-key") k.readOnly = body.readOnly || undefined;
      // a key put at a place in the list, as access.Update keeps it;
      // options.refuseMove: the store can't be written
      if (action === "move-key") {
        if (options.refuseMove) return route.fulfill({ status: 500, json: { error: "There is no such place in the list of keys" } });
        keys.splice(body.index, 0, ...keys.splice(keys.indexOf(k), 1));
      }
      if (action === "copy-key") secret = k.id === lanKeyID ? lanSecret : secrets.get(k.id);
      return json({ keys, secret });
    }
    if (url.pathname === "/api/copy") { events.push({ action: "clipboard", body: req.postDataJSON() }); return json({}); }
    if (url.pathname === "/api/usage") return json({ ...sum(rows), ...groups(), agents: [], models: [], series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/usage/requests") { events.push({ action: "ledger", query: url.search }); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/requests/export") { events.push({ action: "export", query: url.search }); return json({ path: "~/Downloads/keys.csv", rows: ledger(url.searchParams).total }); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}
// keyAction picks Rotate key or Remove from a key's … menu
async function keyAction(page, row, label) {
  await row.locator(".gw-key-more").click();
  await page.locator(".row-menu").getByRole("menuitem", { name: label, exact: true }).click();
}
async function confirmKeyAction(page, row, label) {
  await keyAction(page, row, label);
  await page.locator("#modal").getByRole("button", { name: label, exact: true }).click();
  await page.locator("#modal").waitFor({ state: "hidden" });
}
module.exports = { fixture, keyAction, confirmKeyAction };
