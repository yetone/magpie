// A provider of a key's Errors menu in its editor's Advanced (ne9ko9 on
// Discord): magpie handles them (asked again, the next provider tried,
// said as the agent's API says it), or they pass through as the vendor
// sent them, asked once. app.js imports this when the editor draws the
// Errors row, so it weighs nothing on the core.
window.passErrorsFill = function fill(b, d) {
  const ERR = [["", "magpie handles"], ["pass", "Pass through"]];
  const now = () => ERR[d.passErrors ? 1 : 0];
  b.type = "button";
  b.setAttribute("aria-haspopup", "menu");
  b.setAttribute("aria-expanded", "false");
  b.replaceChildren(el("span", "", t(now()[1])), svg(CHEV, 11, 1.6));
  b.onclick = (e) => {
    e.stopPropagation();
    if (b.classList.contains("open")) return closeProtoMenu();
    openProtoMenu(b, ERR.map(([v, name]) => ({ v, name })), now()[0], (v) => {
      d.passErrors = v === "pass";
      b.firstChild.textContent = t(now()[1]);
    }, "Errors", "sess-menu");
  };
};
