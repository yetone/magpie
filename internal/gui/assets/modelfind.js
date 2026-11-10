// Model settings' search and its Expand, for a provider with many models, a
// relay's hundreds (soilzhu80 on X). app.js imports this only for such a list,
// so it weighs nothing on the core. Each redraw of the list calls it again.
const findQs = {};
let findOpen = false;
window.modelFind = function (names, head) {
  if (!head.isConnected) return;
  const pid = names.dataset.provider;
  const q = input(findQs[pid] || "", t("Search models"), "search");
  q.classList.add("mfind");
  q.setAttribute("aria-label", t("Search models"));
  const none = el("span", "hint mnone");
  const big = el("button", "text action mbig");
  big.type = "button";
  const drawBig = () => {
    names.classList.toggle("open", findOpen);
    big.textContent = findOpen ? t("Collapse the list") : t("Expand the list");
    big.setAttribute("aria-expanded", findOpen);
  };
  big.onclick = (e) => { e.stopPropagation(); findOpen = !findOpen; drawBig(); };
  // a row matches on its ids, its own name and the ones typed
  const sift = () => {
    const words = (findQs[pid] || "").trim().toLowerCase().split(/\s+/).filter(Boolean);
    let n = 0;
    for (const r of names.querySelectorAll(":scope > .mname")) {
      const hay = [r.querySelector(".mwho code")?.textContent, ...[...r.querySelectorAll(".mwho input, .mlocal input")].map((i) => i.value + " " + i.placeholder)].join(" ").toLowerCase();
      r.hidden = !words.every((w) => hay.includes(w));
      if (!r.hidden) n++;
    }
    none.hidden = !words.length || n > 0;
    none.textContent = t("No model matches “{q}”", { q: (findQs[pid] || "").trim() });
  };
  q.oninput = () => { findQs[pid] = q.value; sift(); };
  // Escape clears what is typed first, then does what it does in any field
  const own = q.onkeydown;
  q.onkeydown = (e) => {
    if (e.key === "Enter") e.preventDefault();
    if (e.key !== "Escape" || !q.value) return own(e);
    e.stopPropagation();
    e.preventDefault();
    q.value = findQs[pid] = "";
    sift();
  };
  head.classList.add("finds");
  head.querySelector("b").after(q, big);
  (names.querySelector(":scope > .msuffix") || head).after(none);
  drawBig();
  sift();
};
