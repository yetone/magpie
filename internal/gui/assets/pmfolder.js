// Settings › Privacy's Models folder (MetalHqa on X: on Windows both local
// models went onto the C: drive): where they are downloaded, under an
// Advanced fold since it is set once. Move… picks a folder in the system's
// picker and the models are moved there in the background, copied and
// checked before the old ones go; Move back is magpie's cache. app.js
// imports this the first time the model row draws, so it weighs nothing on
// the core.
let pmFolderOpen = false;
window.pmFolder = function (row, score, m, act) {
  // last in the card, under Rules too: Advanced closes its card rather than
  // sitting between two of its rows (the owner, 11-21: 这个高级没有左对齐)
  let box = document.getElementById("pmFolderRow");
  // a draw the page has since replaced leaves the new one's fold alone
  if (!row.isConnected) return;
  if (!m?.folder || (!m.supported && !m.installed && !m.folderSet)) return void box?.remove();
  if (!box) (box = el("div", "row-fold")).id = "pmFolderRow";
  if (row.parentNode.lastElementChild !== box) row.parentNode.append(box);
  let d = box.querySelector("details.pm-adv");
  if (!d) {
    d = el("details", "prefs-more pm-adv");
    d.ontoggle = () => { pmFolderOpen = d.open; };
    d.append(el("summary", "", t("Advanced")));
  }
  d.open = pmFolderOpen || !!m.folderSet || !!m.moving || !!m.moveError;
  const r = el("div", "row pref pm-folder");
  const who = el("div", "who"), val = el("div", "val");
  const say = m.moving ? t("Moving the models: {done} of {total}", { done: fmtBytes(m.moveDone || 0), total: fmtBytes(m.moveTotal || 0) }) : m.folder;
  who.append(el("div", "name", t("Models folder")), el("div", "sub wraps pm-folder-dir" + (m.moving ? " say" : ""), say));
  if (m.moveError && !m.moving) who.append(el("div", "sub wraps bad pm-folder-said", t("The move stopped: {error}", { error: m.moveError })));
  else if (m.moveLeft && !m.moving) who.append(el("div", "sub wraps pm-folder-said", t("Moved, but the old copy couldn't be deleted: {paths}", { paths: m.moveLeft })));
  const btn = (label, fn) => { const b = el("button", "text", label); b.type = "button"; b.disabled = !!m.moving; b.onclick = fn; return b; };
  val.append(btn(t("Move…"), () => act("folder", "", 0, "")));
  if (m.folderSet) val.append(btn(t("Move back"), () => act("folder", "", 0, "default")));
  r.append(who, val);
  d.replaceChildren(d.firstChild, r);
  if (d.parentNode !== box) box.replaceChildren(d);
};
