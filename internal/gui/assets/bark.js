// Settings › Usage's Bark rows (EV_John_Liu on X): the server and device key
// every alert also goes to, and a daily summary. app.js imports this when
// renderSettings draws them (renderBark), so it weighs nothing on the core.
// The page never has the key back: it is written here, and settings tell
// only that one is set.

let barkDraft = { server: "", key: "" };

window.barkPaint = function (s) {
  const box = $("#barkBox");
  const set = !!s.barkKeySet;
  const save = (body, done) => writingPrefs(api("settings/bark", body))
    .then((ns) => { prefs = ns; if (state) state.settings = ns; if (done) barkDraft = { server: "", key: "" }; renderSettings(); status(t("Saved"), "ok", 1500); })
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
    x.onclick = () => save({ key: "", daily: "" });
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
  // the daily summary, with its time of day, once there is a key
  $("#barkDailyRow").hidden = !set;
  const daily = $("#barkDailySegs");
  daily.replaceChildren();
  if (!set) return;
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
