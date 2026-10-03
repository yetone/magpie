const http = require("node:http");
const fs = require("node:fs/promises");
const path = require("node:path");
const { spawn } = require("node:child_process");
const demo = {
  key: "sk-validation-api-key",
  newapi: { port: 18080, token: "demo-newapi-access-token", user: "42" },
  sub2api: { port: 18081, token: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln" },
};
async function startVendors() {
  const requests = [], servers = [];
  for (const kind of ["newapi", "sub2api"]) {
    const server = http.createServer((req, res) => {
      const pathname = new URL(req.url, "http://localhost").pathname;
      const reply = (status, data) => { res.writeHead(status, { "Content-Type": "application/json" }); res.end(JSON.stringify(data)); };
      if (pathname === "/v1/models") return reply(200, { object: "list", data: [{ id: "demo-model", object: "model", owned_by: "validation" }] });
      requests.push({ kind, path: pathname, authorization: req.headers.authorization, user: req.headers["new-api-user"], userHeaderPresent: Object.hasOwn(req.headers, "new-api-user") });
      if (kind === "newapi" && pathname === "/api/user/self") {
        if (req.headers.authorization !== demo.newapi.token) return reply(401, { success: false, message: "invalid demo access token" });
        if (req.headers["new-api-user"] !== demo.newapi.user) return reply(401, { success: false, message: "Unauthorized, New-Api-User header not provided or mismatched" });
        return reply(200, { success: true, data: { id: 42, quota: 1500000 } });
      }
      if (kind === "sub2api" && pathname === "/api/v1/user/profile") {
        if (req.headers.authorization !== "Bearer " + demo.sub2api.token) return reply(401, { code: 401, message: "invalid demo JWT" });
        return reply(200, { code: 0, data: { id: 1, balance: 12.5 } });
      }
      reply(404, { message: "unknown demo route" });
    });
    await new Promise((resolve, reject) => { server.once("error", reject); server.listen(demo[kind].port, "127.0.0.1", resolve); });
    servers.push(server);
  }
  return { requests, close: () => Promise.all(servers.map((s) => new Promise((resolve) => s.close(resolve)))) };
}
async function startApp(config, { host = "127.0.0.1", port = 13430, key = "validation-web-key-0123456789" } = {}) {
  const env = { ...process.env, XDG_CONFIG_HOME: config, XDG_CACHE_HOME: path.join(config, "cache"), XDG_DATA_HOME: path.join(config, "data"), XDG_STATE_HOME: path.join(config, "state"), MAGPIE_WEB_KEY: key, MAGPIE_ADDR: "127.0.0.1:13425" };
  await fs.mkdir(path.join(config, "magpie"), { recursive: true });
  await fs.writeFile(path.join(config, "magpie", "settings.json"), JSON.stringify({ lang: "en", theme: "light", noAutoUpdate: true }));
  const child = spawn("/out/magpie", ["web", "--addr", `${host}:${port}`, "--no-open"], { env, stdio: ["ignore", "pipe", "pipe"] });
  let log = "", exit = null;
  child.stdout.on("data", (b) => { log += b; });
  child.stderr.on("data", (b) => { log += b; });
  child.on("exit", (code) => { exit = code; });
  child.on("error", (e) => { log += e.message; exit = -1; });
  const url = `http://127.0.0.1:${port}/?k=${key}&view=providers`;
  for (let i = 0; i < 120; i++) {
    if (exit !== null) throw new Error(`Magpie exited ${exit}: ${log}`);
    try {
      if ((await fetch(url, { redirect: "manual" })).status === 303) return {
        url, config, log: () => log, process: child,
        close: () => new Promise((resolve) => {
          if (child.exitCode !== null || child.signalCode !== null) return resolve();
          child.once("exit", resolve); child.kill("SIGTERM");
        }),
      };
    } catch {}
    await new Promise((r) => setTimeout(r, 250));
  }
  child.kill("SIGTERM");
  throw new Error(`Magpie did not become ready: ${log}`);
}
module.exports = { demo, startVendors, startApp };
