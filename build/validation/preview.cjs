const fs = require("node:fs/promises");
const { demo, startVendors, startApp } = require("./fixtures.cjs");
(async () => {
  const vendors = await startVendors();
  const config = "/preview/config";
  await fs.mkdir(config + "/magpie", { recursive: true });
  await fs.writeFile(config + "/magpie/providers.json", JSON.stringify({ providers: [
    { id: "new-api-demo", name: "New API Demo", chat: `http://127.0.0.1:${demo.newapi.port}/v1`, key: demo.key, models: ["demo-model"], balanceToken: demo.newapi.token, balanceURL: `http://127.0.0.1:${demo.newapi.port}/api/user/self`, balancePath: "$data.quota / 500000" },
    { id: "sub2api-demo", name: "Sub2API Demo", chat: `http://127.0.0.1:${demo.sub2api.port}/v1`, key: demo.key, models: ["demo-model"], balanceToken: demo.sub2api.token, balanceURL: `http://127.0.0.1:${demo.sub2api.port}/api/v1/user/profile`, balancePath: "$data.balance" },
  ] }));
  const app = await startApp(config, { host: "0.0.0.0", port: 3430, key: process.env.MAGPIE_WEB_KEY });
  console.log("Balance preview ready: New API ID=42, quota=$3.00; Sub2API balance=$12.50.");
  const stop = async () => { await app.close(); await vendors.close(); process.exit(0); };
  process.on("SIGTERM", stop); process.on("SIGINT", stop);
  app.process.on("exit", (code) => { if (code) { console.error(app.log()); process.exit(code); } });
})().catch((e) => { console.error(e); process.exit(1); });
