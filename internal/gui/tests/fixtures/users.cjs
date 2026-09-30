const fs = require("node:fs/promises");
const path = require("node:path");
const assets = path.resolve(__dirname, "../../assets");

const sum = (rows) => ({
  calls: rows.length, errors: rows.filter((r) => r.status >= 400).length,
  input: rows.reduce((n, r) => n + r.in, 0), output: rows.reduce((n, r) => n + r.out, 0),
  cache_read: 0, cache_write: 0, reasoning: 0, cost: rows.reduce((n, r) => n + r.cost, 0), unpriced: 0,
});

function fixture(lang, theme, events) {
  let users = [
    { id: "alice", name: "Alice", keys: [{ id: "laptop", name: "Laptop", masked: "sk-magpie-user-…111111" }, { id: "server", name: "Server", masked: "sk-magpie-user-…222222" }] },
    { id: "bob", name: "Bob", keys: [{ id: "work", name: "Work", masked: "sk-magpie-user-…333333" }] },
  ];
  let serial = 0;
  const rows = [
    { userId: "alice", userName: "Alice", callerKeyId: "laptop", callerKeyName: "Laptop", in: 100, out: 10, cost: 0.1 },
    { userId: "alice", userName: "Alice", callerKeyId: "server", callerKeyName: "Server", in: 200, out: 20, cost: 0.2 },
    { userId: "bob", userName: "Bob", callerKeyId: "work", callerKeyName: "Work", in: 300, out: 30, cost: 0.3 },
    { in: 40, out: 4, cost: 0.04 },
  ].map((r, i) => ({
    t: new Date(Date.now() - i * 60e3).toISOString(), agent: "codex", agentName: "Codex", icon: "generic", provider: "relay", providerName: "Relay",
    model: "m", req: "relay/m", ms: 1000, status: 200, priced: true, ...r,
  }));
  const userName = (r) => users.find((u) => u.id === r.userId)?.name || r.userName || "Local / unassigned";
  const keyName = (r) => users.find((u) => u.id === r.userId)?.keys.find((k) => k.id === r.callerKeyId)?.name || r.callerKeyName;
  const groups = () => ({
    users: ["alice", "bob", ""].map((id) => {
      const rs = rows.filter((r) => (r.userId || "") === id);
      return { id: id || "-", userId: id, name: userName(rs[0]), icon: "generic", ...sum(rs) };
    }),
    callerKeys: ["laptop", "server", "work"].map((id) => {
      const rs = rows.filter((r) => r.callerKeyId === id);
      return { id, userId: rs[0].userId, callerKeyId: id, name: keyName(rs[0]), sub: userName(rs[0]), icon: "generic", ...sum(rs) };
    }),
  });
  const ledger = (q) => {
    let rs = rows;
    if (q.get("user")) rs = rs.filter((r) => (r.userId || "-") === q.get("user"));
    if (q.get("callerKey")) rs = rs.filter((r) => r.callerKeyId === q.get("callerKey"));
    if (q.get("failed") === "1") rs = rs.filter((r) => r.status >= 400);
    return { ...sum(rs), ...groups(), total: rs.length, offset: 0, keys: [], agents: [{ id: "codex", name: "Codex" }],
      rows: rs.map((r) => ({ ...r, userLabel: userName(r), callerKeyLabel: keyName(r) })) };
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
      gateway: { running: true, window: true, mine: true, url: "http://127.0.0.1:3999", calls: [], groups: [] },
    });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/users") return json({ users });
    if (url.pathname.startsWith("/api/users/")) {
      const action = url.pathname.split("/").at(-1), body = req.postDataJSON();
      events.push({ action, body });
      let u = users.find((u) => u.id === body.user), k = u?.keys.find((k) => k.id === body.key), secret = "";
      if (action === "add-user") {
        if (!body.name.trim()) return route.fulfill({ status: 400, json: { error: "Use a name between 1 and 120 characters" } });
        users.push({ id: "new-user-" + (++serial), name: body.name, keys: [] });
      }
      if (action === "rename-user") u.name = body.name;
      if (action === "remove-user") users = users.filter((v) => v !== u);
      if (action === "on-user" || action === "off-user") u.off = action === "off-user";
      if (action === "add-key") {
        secret = "sk-magpie-user-test-created";
        u.keys.push({ id: "new-key-" + (++serial), name: body.name, masked: "sk-magpie-user-…created" });
      }
      if (action === "rename-key") k.name = body.name;
      if (action === "remove-key") u.keys = u.keys.filter((v) => v !== k);
      if (action === "on-key" || action === "off-key") k.off = action === "off-key";
      if (action === "copy-key") secret = "sk-magpie-user-test-copy";
      return json({ users, secret });
    }
    if (url.pathname === "/api/copy") { events.push({ action: "clipboard", body: req.postDataJSON() }); return json({}); }
    if (url.pathname === "/api/usage") return json({ ...sum(rows), ...groups(), keys: [], agents: [], models: [], series: [], bucket: "day", path: "~/.config/magpie/usage.jsonl" });
    if (url.pathname === "/api/usage/requests") { events.push({ action: "ledger", query: url.search }); return json(ledger(url.searchParams)); }
    if (url.pathname === "/api/usage/requests/export") { events.push({ action: "export", query: url.search }); return json({ path: "~/Downloads/users.csv", rows: ledger(url.searchParams).total }); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}
module.exports = { fixture };
