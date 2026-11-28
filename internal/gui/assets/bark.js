// Settings › Usage's Bark rows (EV_John_Liu on X): the server and device key
// every alert also goes to, a Telegram bot and a Discord webhook beside it
// (gandli on X), and a daily summary to each. app.js imports this when
// renderSettings draws them (renderBark), so it weighs nothing on the core.
// The page never has a key, token or webhook back: each is written here,
// and settings tell only that one is set.

let barkDraft = { server: "", key: "" };
let chatDraft = { telegramToken: "", telegramChat: "", discordWebhook: "" };

const barkSaved = (ns) => { prefs = ns; if (state) state.settings = ns; renderSettings(); status(t("Saved"), "ok", 1500); };

// a field of a chat channel's draft, kept across a repaint
function chatField(k, placeholder, type, label) {
  const i = input(chatDraft[k], placeholder, type);
  i.className = "words chat-" + k;
  i.setAttribute("aria-label", label);
  i.oninput = () => { chatDraft[k] = i.value; };
  return i;
}

// one chat channel's row: its fields and Save while it isn't set; once it
// is, what can be shown of it, Test and Remove
function chatPaint(to, sent, set, shown, fields, keys) {
  const box = $("#" + to + "Box");
  box.replaceChildren();
  const save = (body, done) => writingPrefs(api("settings/alert-channels", body))
    .then((ns) => { if (done) for (const k of keys) chatDraft[k] = ""; barkSaved(ns); })
    .catch((e) => statusTErr(e));
  if (set) {
    const test = el("button", "text bark-test", t("Test"));
    test.onclick = () => {
      test.disabled = true;
      api("alert-channels/test", { to }).then(() => status(sent, "ok", 1500), statusTErr).finally(() => { test.disabled = false; });
    };
    const x = el("button", "text bark-remove", t("Remove"));
    x.onclick = () => save(Object.fromEntries(keys.map((k) => [k, ""])));
    if (shown) box.append(el("code", "bark-server", shown));
    box.append(el("code", "bark-key", "••••••••"), test, x);
    return;
  }
  const go = el("button", "text bark-save", t("Save"));
  go.onclick = () => {
    const empty = fields.find((i) => !i.value.trim());
    if (empty) return empty.focus();
    save(Object.fromEntries(keys.map((k, n) => [k, fields[n].value.trim()])), true);
  };
  for (const i of fields) i.addEventListener("keydown", (e) => { if (e.key === "Enter") go.onclick(); });
  box.append(...fields, go);
}

window.barkPaint = function (s) {
  const box = $("#barkBox");
  const set = !!s.barkKeySet;
  const save = (body, done) => writingPrefs(api("settings/bark", body))
    .then((ns) => { if (done) barkDraft = { server: "", key: "" }; barkSaved(ns); })
    .catch((e) => statusTErr(e));
  box.replaceChildren();
  if (set) {
    const server = el("code", "bark-server", (s.barkServer || "https://api.day.app").replace(/^https?:\/\//, ""));
    server.title = s.barkServer || "https://api.day.app";
    const test = el("button", "text bark-test", t("Test"));
    test.onclick = () => {
      test.disabled = true;
      api("bark/test", {}).then(() => status(t("Sent to Bark"), "ok", 1500), statusTErr).finally(() => { test.disabled = false; });
    };
    const x = el("button", "text bark-remove", t("Remove"));
    x.onclick = () => save({ key: "" });
    box.append(server, el("code", "bark-key", "••••••••"), test, x);
  } else {
    const srv = input(barkDraft.server, "https://api.day.app", "url");
    srv.className = "words bark-server-input";
    srv.setAttribute("aria-label", t("Server"));
    srv.oninput = () => { barkDraft.server = srv.value; };
    const key = input(barkDraft.key, t("Device key"), "password");
    key.className = "words bark-key-input";
    key.setAttribute("aria-label", t("Device key"));
    key.oninput = () => { barkDraft.key = key.value; };
    const go = el("button", "text bark-save", t("Save"));
    go.onclick = () => {
      if (!key.value.trim()) return key.focus();
      save({ server: srv.value.trim(), key: key.value.trim() }, true);
    };
    for (const i of [srv, key]) i.addEventListener("keydown", (e) => { if (e.key === "Enter") go.onclick(); });
    box.append(srv, key, go);
  }
  chatPaint("telegram", t("Sent to Telegram"), !!s.telegramSet, s.telegramChat || "", [
    chatField("telegramToken", t("Bot token"), "password", t("Bot token")),
    chatField("telegramChat", t("Chat ID"), "text", t("Chat ID")),
  ], ["telegramToken", "telegramChat"]);
  chatPaint("discord", t("Sent to Discord"), !!s.discordSet, "", [
    chatField("discordWebhook", t("Webhook URL"), "password", t("Webhook URL")),
  ], ["discordWebhook"]);
  // the daily summary, with its time of day, once there is somewhere for it
  // to go; taking the last one away turns it off
  const any = set || !!s.telegramSet || !!s.discordSet;
  $("#barkDailyRow").hidden = !any;
  const daily = $("#barkDailySegs");
  daily.replaceChildren();
  if (!any) return;
  const at = s.barkDaily || "";
  if (at) {
    const i = input(at, "21:00", "time");
    i.className = "at";
    i.setAttribute("aria-label", t("Time of day"));
    blurOnEnter(i);
    i.onchange = () => { if (i.value && i.value !== at) save({ daily: i.value }); };
    daily.append(i);
  }
  daily.append(segs([["off", t("Off")], ["on", t("On")]], at ? "on" : "off", (v) => save({ daily: v === "on" ? "21:00" : "" })));
};
