// The lines that explain a setting, shown on asking (the owner, 11-02:
// "让整个设计页面看起来很简洁，很有呼吸感"; yujezhang on X, 11-10: "界面只
// 保留完成操作必需的信息"). app.css keeps a row's lines that explain it out
// of sight (--hint: 1 on each); the pointer resting on the row, or a key
// reaching it, shows them here in a card under the row's name, over the
// page, so no row moves. The rows: Settings', a provider editor's fields
// and the Settings rows it holds, and any .hint-row (Routing groups'
// settings). The card takes no pointer: a click goes to what is under it.
// It goes when the pointer or focus leaves the row, the page scrolls, a key
// is pressed or the row is drawn again. Imported by app.js at start.
const ROWS = "#view-settings .row.pref, .editor.roomy .row.pref, .editor.roomy .fld, .hint-row";
const LINES = ":scope > .who > .sub, :scope > .hint, :scope > .txt > .expl";
const card = el("div", "set-hint");
card.setAttribute("aria-hidden", "true"); // a screen reader reads the lines in the row
document.body.append(card);
let row = null, wait = 0;

// a line holding a link or a control is one to use, so it stays in sight
const acts = (root) => {
  for (const s of root.querySelectorAll(".row.pref > .who > .sub, .fld > .hint.expl")) s.classList.toggle("acts", !!s.querySelector("a, button, input, select, code, .searcher-failed"));
};
// before the page is painted again: no frame shows such a line hidden
// (Settings and the editor's dialog; Routing groups' lines hold none, and
// its page is drawn again as each request plays)
for (const root of [document.getElementById("view-settings"), document.getElementById("modal")]) {
  if (!root) continue;
  new MutationObserver(() => acts(root)).observe(root, { childList: true, subtree: true });
  acts(root);
}

const hints = (r) => [...r.querySelectorAll(LINES)]
  .filter((s) => !s.hidden && getComputedStyle(s).getPropertyValue("--hint").trim() === "1" && s.textContent.trim());

function hide() {
  clearTimeout(wait);
  row = null;
  card.classList.remove("on");
  card.hidden = true;
}
function show(r) {
  const subs = r.isConnected && !r.closest("[hidden]") ? hints(r) : [];
  if (!subs.length) return hide();
  card.replaceChildren(...subs.map((s) => el("p", "", s.textContent.trim())));
  card.hidden = false;
  const who = (r.querySelector(":scope > .who, :scope > .txt") || r).getBoundingClientRect(), box = r.getBoundingClientRect();
  const w = card.offsetWidth, h = card.offsetHeight;
  const left = Math.max(16, Math.min(who.left, innerWidth - 16 - w));
  // under the row, or over it where the window ends first
  const top = box.bottom + 4 + h <= innerHeight - 8 ? box.bottom + 4 : Math.max(8, box.top - 4 - h);
  card.style.left = left + "px";
  card.style.top = top + "px";
  card.style.transformOrigin = (top < box.top ? "bottom " : "top ") + Math.round(Math.min(Math.max(who.left - left, 0), w)) + "px";
  requestAnimationFrame(() => row === r && card.classList.add("on"));
}
function point(r, now) {
  if (r === row) return;
  hide();
  if (!r) return;
  row = r;
  // a pointer passing over rows on its way shows nothing; resting does
  wait = setTimeout(() => show(r), now ? 0 : 450);
}
// rowOf: the row a node is in; an editor field's name, beside it in the
// grid, is the field's too
function rowOf(n) {
  if (!(n instanceof Element)) return null;
  const l = n.closest(".editor.roomy label");
  if (l?.nextElementSibling?.matches(".fld")) return l.nextElementSibling;
  return n.closest(ROWS);
}
document.addEventListener("pointerover", (e) => e.pointerType !== "touch" && point(rowOf(e.target)));
// the pointer gone from the window
document.addEventListener("pointerout", (e) => !e.relatedTarget && hide());
document.addEventListener("pointerdown", () => hide(), true);
document.addEventListener("focusin", (e) => {
  let key = true;
  try { key = e.target.matches(":focus-visible"); } catch {} // Safari before 15.4
  if (key) point(rowOf(e.target), true);
});
document.addEventListener("focusout", (e) => {
  if (!rowOf(e.relatedTarget) || rowOf(e.relatedTarget) !== row) hide();
});
// a scroll takes the pointer's card away; focus's follows its row, which
// a key moving focus scrolls into sight
document.addEventListener("scroll", () => row?.contains(document.activeElement) ? show(row) : hide(), { passive: true, capture: true });
addEventListener("keydown", (e) => !/^(Tab|Shift|Alt|Meta|Control)$/.test(e.key) && hide(), true);
addEventListener("resize", hide);
hide();
// the pointer already on a row, or focus, as the import came
{
  const on = [...document.querySelectorAll(ROWS)].findLast?.((r) => r.matches(":hover")) || rowOf(document.activeElement);
  if (on) point(on);
}
