// Windows' caption buttons (258a14 on X): the main window there is
// frameless (mainwindow.go mainFrameless), its header the title bar, and
// these minimise, maximise or restore, and close: hide, the tray stays.
// The app says whether the window is maximised (mainwindow.go captionJS),
// and maximise turns into restore. A double click on what drags the window
// maximises or restores it, as on a title bar. app.js imports this in the
// Windows window alone, so it weighs nothing on the core.
const cap = $("#wincap");
// over a dialog's backdrop too, so the window can always be closed or
// moved aside: what modalBackground makes inert, these undo
new MutationObserver(() => cap.inert = false).observe(cap, { attributeFilter: ["inert"] });
const act = { min: (w) => w.Minimise(), max: (w) => w.ToggleMaximise(), close: (w) => w.Close() };
cap.onclick = (e) => {
  const b = e.target.closest("button[data-cap]");
  if (b) winRuntime.then((w) => w && act[b.dataset.cap](w.Window));
};
window.magpieCaption = (m) => {
  const b = cap.querySelector('[data-cap="max"]');
  b.textContent = m ? "\uE923" : "\uE922";
  b.dataset.enTitle = m ? "Restore Down" : "Maximize";
  b.title = t(b.dataset.enTitle);
};
if (window.magpieMaximised !== undefined) window.magpieCaption(window.magpieMaximised);
document.addEventListener("dblclick", (e) => {
  if (e.button !== 0 || !(e.target instanceof Element) || e.target.closest("button, a, input, textarea, select, nav, [contenteditable]")) return;
  if (getComputedStyle(e.target).getPropertyValue("--wails-draggable").trim() === "drag") winRuntime.then((w) => w?.Window.ToggleMaximise());
});
