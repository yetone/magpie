// Settings that couldn't be read, imported from settingsUnread (app.js) when
// a read fails, so it weighs nothing on the core. A window back after a
// restart found Settings › About blank under its header and sidebar
// (WaiooT on X): every page of Settings is drawn from that read, and only
// the footer said, for 8 s, that it had failed. The view now says so, with
// Try again, and reads the settings again by itself, sooner first.
let timer, tries = 0;

export function unread(e) {
  if (prefs || view !== "settings") return;
  let line = $("#setUnread");
  if (!line) {
    line = el("div", "empty-state");
    line.id = "setUnread";
    $("#view-settings .set-head").after(line);
  }
  const again = el("button", "link", t("Try again"));
  again.onclick = () => {
    clearTimeout(timer);
    if (prefs || view !== "settings") return;
    (settingsLoad = loadSettings()).then(() => { tries = 0; backAfterLoad($("#view-settings")); }, settingsUnread);
  };
  line.replaceChildren(el("b", "", t("Couldn't read the settings")), e.message || "", " ", again);
  clearTimeout(timer);
  timer = setTimeout(() => again.click(), Math.min(30, 2 ** ++tries) * 1e3);
}
