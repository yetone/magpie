// Settings › General's look (CHENGJIAHEGOXIS on X): an accent colour of
// the reader's own and a picture behind the window's pages, beside the
// Colorblind, Reduce motion and Loading bird rows. The stylesheet that
// draws the look is the server's (appearance.go): boot.js puts it in
// before the first paint, and lookPaint takes it again once it changed.
// app.js imports this when Settings draws or the look changed, so it
// weighs nothing on the core.

// the presets: magpie's own first, then a few quiet ones
const ACCENTS = () => [["", t("Default"), "#4f46e5"], ["#2563eb", t("Blue")], ["#0d9488", t("Teal")], ["#16a34a", t("Green")], ["#ea580c", t("Orange")], ["#db2777", t("Pink")], ["#52525b", t("Graphite")]];

window.lookPaint = async function () {
  try {
    const r = await (await fetch("/api/appearance/css")).json();
    let st = document.getElementById("lookCSS");
    if (!st) document.head.append((st = Object.assign(document.createElement("style"), { id: "lookCSS" })));
    st.textContent = r.css || "";
    // the accent is in the panel's tint and the title bar too
    if (typeof tintPanel == "function") tintPanel(0);
  } catch { /* the look stays as drawn */ }
};

window.drawLook = function (s, keep) {
  $("#colorblindSegs").title = t("Colorblind-safe chart colors, and each allowance window's line in a dash of its own");
  $("#colorblindSegs").replaceChildren(segsOO(s.colorblind, (v) => savePrefs({ ...keep, colorblind: v })));
  $("#reduceMotionSegs").title = t("Animations stand still, as with the system's Reduce Motion, so magpie keeps the computer less busy");
  $("#reduceMotionSegs").replaceChildren(segsOO(s.reduceMotion, (v) => savePrefs({ ...keep, reduceMotion: v })));
  $("#birdSegs").title = t("magpie's bird hops on what is still loading; Off keeps it away, and magpie stops looking for it");
  $("#birdSegs").replaceChildren(segsOO(!s.noBird, (v) => savePrefs({ ...keep, noBird: !v })));
  drawAccent(s, keep);
  drawWallpaper(s);
};

function drawAccent(s, keep) {
  const box = $("#accentPick");
  if (!box) return;
  const now = (s.accent || "").toLowerCase();
  const pick = (v) => savePrefs({ ...keep, accent: v });
  const swatch = (v, name, color) => {
    const b = el("button", "swatch" + (v === now ? " on" : ""));
    b.type = "button";
    b.setAttribute("role", "radio");
    b.setAttribute("aria-checked", String(v === now));
    b.title = name;
    b.setAttribute("aria-label", name);
    b.dataset.accent = v;
    b.style.setProperty("--sw", color || v);
    b.onclick = () => v !== now && pick(v);
    return b;
  };
  const presets = ACCENTS();
  const list = presets.map(([v, name, color]) => swatch(v, name, color));
  // a colour of the reader's own, from the system's colour picker
  const mine = now && !presets.some(([v]) => v === now);
  const own = el("label", "swatch own" + (mine ? " on" : ""));
  own.title = t("Custom color");
  own.setAttribute("aria-label", own.title);
  own.setAttribute("role", "radio");
  own.setAttribute("aria-checked", String(!!mine));
  if (mine) own.style.setProperty("--sw", now);
  const input = el("input");
  input.type = "color";
  input.value = mine ? now : "#4f46e5";
  input.tabIndex = -1;
  // the picker reports as the reader drags; the choice is saved when it closes
  input.onchange = () => input.value.toLowerCase() !== now && pick(input.value.toLowerCase());
  own.tabIndex = 0;
  own.onkeydown = (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); input.click(); } };
  own.append(input);
  box.replaceChildren(...list, own);
}

function drawWallpaper(s) {
  const box = $("#wallpaperPick");
  if (!box) return;
  const file = el("input");
  file.type = "file";
  file.accept = "image/png,image/jpeg,image/gif,image/webp";
  file.hidden = true;
  const send = async (body) => {
    try {
      prefs = await api("appearance/wallpaper", body);
      if (state) state.settings = prefs;
      applyPrefs(prefs);
      renderSettings();
    } catch (e) { statusErr(e); }
  };
  file.onchange = async () => {
    const f = file.files[0];
    if (!f) return;
    if (f.size > 16 << 20) { statusErr(new Error(t("A background image is 16 MB at most"))); return; }
    // as base64 in JSON: the app's web view drops a File sent as the body
    const bytes = new Uint8Array(await f.arrayBuffer());
    let bin = "";
    for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    send({ data: btoa(bin) });
  };
  const choose = el("button", "text", s.wallpaper ? t("Change") : t("Choose…"));
  choose.type = "button";
  choose.onclick = () => file.click();
  if (!s.wallpaper) { box.replaceChildren(choose, file); return; }
  const thumb = el("img", "wall-thumb");
  thumb.alt = "";
  thumb.src = "/api/appearance/wallpaper?v=" + s.wallpaper;
  thumb.onerror = () => thumb.remove(); // a stamp whose file is gone (another computer's settings)
  const remove = el("button", "text", t("Remove"));
  remove.type = "button";
  remove.onclick = () => send({ remove: true });
  box.replaceChildren(thumb, choose, remove, file);
}
