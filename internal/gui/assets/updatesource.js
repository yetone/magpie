// Settings › About's Update source (RootSir and Sakugar_ on X): where an
// update is read and downloaded from, one menu button as the language's is.
// app.js imports this when the page is drawn, so it weighs nothing on the
// core. "" is automatic; site never goes to GitHub, github never to
// usemagpie.ai.
window.updateSourcePick = function updateSourcePick(s, keep) {
  const names = { "": "Automatic", site: "usemagpie.ai only", github: "GitHub only" };
  const b = $("#updateSourcePick");
  const chosen = s.updateSource in names ? s.updateSource : "";
  b.dataset.value = chosen;
  b.setAttribute("aria-label", t("Update source"));
  b.replaceChildren(el("span", "", t(names[chosen])), svg(CHEV, 11, 1.6));
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, Object.entries(names).map(([v, name]) => ({ v, name, note: "" })), chosen, (v) => {
      if (v !== chosen) savePrefs({ ...keep, updateSource: v });
    }, "Update source", "sess-menu", "right");
  };
};

// Settings' Download source, drawn here with Update source so it weighs
// nothing on the core; it says what the source picked does
window.setUpdateMirror = function (mirror) {
  return writingPrefs(api("settings/update-mirror", { mirror }))
    .then((ns) => { prefs = ns; mirrorEditing = false; mirrorDraft = mirrorErr = ""; renderSettings(); status(t("Saved"), "ok", 1500); return ns; });
};
// Use a mirror… and Change open the field from mirroredit.js
const editMirror = (r, s, v) => import("./mirroredit.js").then(() => { mirrorEditing = true; mirrorDraft = v; renderUpdateMirror(r, s); }, statusErr);
window.renderUpdateMirror = function (r, s) {
  r.classList.add("update-mirror-row");
  const who = r.querySelector(".who"), val = r.querySelector(".val");
  who.querySelector(".sub")?.remove();
  val.replaceChildren();
  const sub = el("div", "sub");
  who.append(sub);
  const m = s.updateMirror || "", from = s.updateSource;
  // as Update source says: site never asks GitHub, so no mirror is used
  sub.textContent = mirrorErr || (from === "site" ? t("Update source is usemagpie.ai only: updates never come from GitHub, so no mirror is used")
    : from === "github" ? (m ? t("Update source is GitHub only: updates come through this mirror, each checked against the release's signed checksums") : t("Update source is GitHub only: updates come from GitHub; a mirror can stand in for it where it is slow"))
    : m ? t("Updates are downloaded from usemagpie.ai, and through this mirror when that fails, each checked against usemagpie.ai's checksum")
    : t("Updates are downloaded from usemagpie.ai, and from GitHub when that fails; a mirror can stand in for GitHub where it is slow"));
  // the docs' Mirrors page says which mirror and how (WeltAn_X on X)
  const more = el("button", "sub-more", t("How mirrors work"));
  more.onclick = () => api("open", { url: "https://usemagpie.ai/docs/" + ({ zh: "zh/", "zh-TW": "zh/", ja: "ja/" }[locale] || "") + "mirrors#settings" }).catch(() => {});
  sub.append(" ", more);
  sub.classList.toggle("err", !!mirrorErr);
  sub.title = from === "github" ? t("The checksums come signed with the release; a file from the mirror that doesn't match them is not installed.")
    : t("The update and its checksum always come from usemagpie.ai; a file from the mirror that doesn't match it is not installed.");
  if (mirrorEditing) return mirrorEdit(r, s, sub, val);
  if (m) {
    const official = el("button", "text", t("Remove mirror"));
    official.onclick = () => setUpdateMirror("").catch(statusTErr);
    const change = el("button", "text", t("Change"));
    change.onclick = () => editMirror(r, s, m);
    val.append(el("code", "", m), change, official);
  } else {
    const add = el("button", "text", t("Use a mirror…"));
    add.onclick = () => editMirror(r, s, "");
    val.append(el("code", "", from === "github" ? "github.com" : "usemagpie.ai"), add);
  }
};
