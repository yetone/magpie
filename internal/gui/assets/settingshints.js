// Settings' explanations, shown on asking (the owner, 11-02: "让整个设计
// 页面看起来很简洁，很有呼吸感"). app.css keeps a row's lines that explain
// it out of sight (--hint: 1 on each); the pointer resting on the row, or a
// key reaching it, shows them here in a card under the row's name, over
// the page, so no row moves. The card takes no pointer: a click goes to
// what is under it. It goes when the pointer or focus leaves the row, the
// page scrolls, a key is pressed or the row is drawn again. Imported by
// app.js when the pointer or focus first comes to Settings, not at start.
const view = document.getElementById("view-settings");
const card = el("div", "set-hint");
card.setAttribute("aria-hidden", "true"); // a screen reader reads the lines in the row
document.body.append(card);
let row = null, wait = 0;

const acts = () => {
  for (const s of view.querySelectorAll(".row.pref > .who > .sub")) s.classList.toggle("acts", !!s.querySelector("a, button, input, select, code, .searcher-failed"));
};
// before the page is painted again: no frame shows such a line hidden
new MutationObserver(acts).observe(view, { childList: true, subtree: true });
acts();

const hints = (r) => [...r.querySelectorAll(":scope > .who > .sub")]
  .filter((s) => !s.hidden && getComputedStyle(s).getPropertyValue("--hint").trim() === "1" && s.textContent.trim());

function hide() {
  clearTimeout(wait);
  row = null;
  card.classList.remove("on");
  card.hidden = true;
}
function show(r) {
  const subs = r.isConnected && !view.hidden ? hints(r) : [];
  if (!subs.length) return hide();
  card.replaceChildren(...subs.map((s) => el("p", "", s.textContent.trim())));
  card.hidden = false;
  const who = r.querySelector(":scope > .who").getBoundingClientRect(), box = r.getBoundingClientRect();
  const w = card.offsetWidth, h = card.offsetHeight;
  const left = Math.max(16, Math.min(who.left, innerWidth - 16 - w));
  // under the row, or over it where the window ends first
  const top = box.bottom + 4 + h <= innerHeight - 8 ? box.bottom + 4 : Math.max(8, box.top - 4 - h);
  card.style.left = left + "px";
  card.style.top = top + "px";
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
const rowOf = (n) => n instanceof Element ? n.closest("#view-settings .row.pref") : null;
view.addEventListener("pointerover", (e) => e.pointerType !== "touch" && point(rowOf(e.target)));
view.addEventListener("pointerleave", () => hide());
view.addEventListener("pointerdown", () => hide(), true);
view.addEventListener("focusin", (e) => {
  let key = true;
  try { key = e.target.matches(":focus-visible"); } catch {} // Safari before 15.4
  if (key) point(rowOf(e.target), true);
});
view.addEventListener("focusout", (e) => {
  if (!rowOf(e.relatedTarget) || rowOf(e.relatedTarget) !== row) hide();
});
// a scroll takes the pointer's card away; focus's follows its row, which
// a key moving focus scrolls into sight
view.addEventListener("scroll", () => row?.contains(document.activeElement) ? show(row) : hide(), { passive: true, capture: true });
addEventListener("keydown", (e) => !/^(Tab|Shift|Alt|Meta|Control)$/.test(e.key) && hide(), true);
addEventListener("resize", hide);
hide();
// the pointer already on a row, or focus, as the import came
{
  const on = view.querySelector(".row.pref:hover") || rowOf(document.activeElement);
  if (on) point(on);
}
