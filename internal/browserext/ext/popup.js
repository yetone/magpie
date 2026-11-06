import { GATEWAY, KEY } from "./config.js";

const zh = /^zh/i.test(navigator.language);
const say = zh
  ? { on: "已连接 Magpie", off: "未连接 Magpie", onHint: "通过 Magpie 运行的智能体可以使用这个浏览器里的标签页。它们打开的标签页放在各自的 “Magpie” 分组里。", offHint: "请确认 Magpie 正在运行。扩展会自动重连。", key: "网关密钥", gw: "Magpie 的地址", save: "保存并重连" }
  : { on: "Connected to Magpie", off: "Not connected to Magpie", onHint: "Agents you run through Magpie can use this browser's tabs. Each puts the tabs it opens in a \"Magpie\" group of its own.", offHint: "Check that Magpie is running. The extension reconnects by itself.", key: "Gateway key", gw: "Magpie's address", save: "Save and reconnect" };

async function show() {
  const { status } = await chrome.storage.session.get("status");
  const on = !!(status && status.connected);
  document.getElementById("dot").classList.toggle("on", on);
  document.getElementById("state").textContent = on ? say.on : say.off;
  document.getElementById("hint").textContent = on ? say.onHint : say.offHint;
}

const gw = document.getElementById("gw");
const key = document.getElementById("key");
document.querySelector('label[for="gw"]').textContent = say.gw;
document.getElementById("keyLabel").textContent = say.key;
document.getElementById("save").textContent = say.save;
chrome.storage.local.get(["gateway", "key"]).then(({ gateway, key: k }) => { gw.value = gateway || GATEWAY; key.value = k !== undefined ? k : KEY; });
document.getElementById("save").addEventListener("click", async () => {
  const v = gw.value.trim();
  if (!v || v === GATEWAY) await chrome.storage.local.remove("gateway");
  else await chrome.storage.local.set({ gateway: v.replace(/^http/, "ws") });
  const k = key.value.trim();
  if (k === KEY) await chrome.storage.local.remove("key");
  else await chrome.storage.local.set({ key: k });
  chrome.runtime.sendMessage({ type: "reconnect" }).catch(() => {});
});
chrome.storage.onChanged.addListener((c, area) => { if (area === "session" && c.status) show(); });
show();
