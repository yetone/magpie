// Settings' Download source while a mirror is typed (#893): the field, its
// Cancel and Save, and the line saying what a mirror's address is
// (WeltAn_X on X: nothing said which mirror or how). app.js imports this
// when Use a mirror… or Change is clicked (editMirror), so it weighs
// nothing on the core.
window.mirrorEdit = function (r, s, sub, val) {
  if (!mirrorErr) sub.firstChild.data = t("Its address goes before the release's github.com link, as a GitHub download proxy's does");
  const i = input(mirrorDraft, "https://mirror.example/");
  i.className = "words update-mirror-input";
  i.setAttribute("aria-label", t("Mirror address"));
  i.oninput = () => { mirrorDraft = i.value; };
  const save = el("button", "text", t("Save"));
  save.onclick = () => {
    const v = i.value.trim();
    if (!v) return i.focus(NO_SCROLL);
    setUpdateMirror(v).catch((e) => { mirrorErr = t(e.message); statusTErr(e); renderSettings(); });
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = () => { mirrorEditing = false; mirrorDraft = mirrorErr = ""; renderUpdateMirror(r, s); };
  i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save.onclick(); else if (e.key === "Escape") cancel.onclick(); };
  val.append(i, cancel, save);
  queueMicrotask(() => { if (i.isConnected && document.activeElement !== i) i.focus(NO_SCROLL); });
};
