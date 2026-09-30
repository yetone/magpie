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
  let serial = 0;
  let lan = !!options.lan;
  let lanKeyID = "", rotations = 0, lanSecret = "";
  const secrets = new Map([["laptop", "fixture-laptop"], ["server", "fixture-server"], ["work", "fixture-work"]]);
  const lanURLs = ["http://192.168.1.10:3999", "http://10.0.0.10:3999"];
  const lanState = () => ({ lang, theme, lan,
    lanURLs: lan ? lanURLs : [], fx: { rate: 7.2, at: new Date().toISOString() } });
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
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme, web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/favicon.ico") return route.fulfill({ status: 204 });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme } });
    if (url.pathname === "/api/providers") return json({
      providers: [{ id: "relay", name: "Relay", icon: "generic", models: [{ id: "m", name: "Model", on: true }], agents: [] }],
      gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", lan, lanURLs: lan ? lanURLs : [], calls: [], groups: [] },
    });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/settings/lan") {
      const body = req.postDataJSON();
      events.push({ action: "lan", body });
      lan = body.on;
      let key = keys.find((k) => k.id === lanKeyID);
      if (lan && (!key || body.newKey)) {
        if (!key) {
          key = { id: "lan-key-" + (++serial), name: "Magpie" };
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
    if (url.pathname === "/api/caller-keys") return json({ keys });
    if (url.pathname.startsWith("/api/caller-keys/")) {
      const action = url.pathname.split("/").at(-1), body = req.postDataJSON();
      events.push({ action, body });
      const k = keys.find((k) => k.id === body.key);
      let secret = "";
      if (action === "add-key") {
        if (!body.name.trim()) return route.fulfill({ status: 400, json: { error: "Use a name between 1 and 120 characters" } });
        const id = "new-key-" + (++serial);
        secret = "fixture-created-" + serial;
        secrets.set(id, secret);
        keys.push({ id, name: body.name, masked: "sk-magpie-key-…created" });
      }
      if (action === "rename-key") k.name = body.name;
      if (action === "rotate-key") {
        secret = "fixture-rotated-" + (++rotations);
        secrets.set(k.id, secret);
        if (k.id === lanKeyID) lanSecret = secret;
        k.masked = "sk-magpie-key-…" + secret.slice(-6);
      }
      if (action === "remove-key") keys = keys.filter((v) => v !== k);
      if (action === "on-key" || action === "off-key") k.off = action === "off-key";
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
async function confirmKeyAction(page, row, label) {
  await row.getByRole("button", { name: label, exact: true }).click();
  await page.locator("#modal").getByRole("button", { name: label, exact: true }).click();
  await page.locator("#modal").waitFor({ state: "hidden" });
}
module.exports = { fixture, confirmKeyAction };
