// magpie web on a phone (app.js phoneWeb): the pages are a tab bar at the
// window's foot, as an iPhone app's (app.css). Five fit; with more, the
// fifth is More, a menu of the rest, marked while one of them is open
// (geraldpppppp on X: Agents and the Library were past the edge of a
// strip that scrolled sideways, and nothing said it did). Imported by
// layoutNav the first time the page is a phone's.
const nav = $("#nav");
const more = el("button", "nav-more");
more.type = "button";
more.setAttribute("aria-haspopup", "menu");
more.setAttribute("aria-expanded", "false");
more.innerHTML = '<svg class="nav-ic" viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><g fill="currentColor"><circle cx="4.5" cy="10" r="1.5"/><circle cx="10" cy="10" r="1.5"/><circle cx="15.5" cy="10" r="1.5"/></g></svg>';
const word = el("span");
more.append(word);
let over = [];

function fit() {
  for (const b of nav.querySelectorAll(".tb-over")) b.classList.remove("tb-over");
  // what the bar has: the pages shown, gateway mode's, a plugin's and
  // the switched-off ones left out (app.js navHides, app.css)
  const pages = phoneWeb() ? [...nav.querySelectorAll(":scope > button[data-view]")].filter((b) => getComputedStyle(b).display !== "none") : [];
  over = pages.length > 5 ? pages.slice(4) : [];
  for (const b of over) b.classList.add("tb-over");
  if (!over.length) more.remove();
  else if (nav.lastElementChild !== more) nav.append(more);
  // "More" alone is the heatmap's 多; the tab's is its own word
  const said = t("More pages");
  word.textContent = said === "More pages" ? "More" : said;
  more.setAttribute("aria-label", said);
  const open = over.find((b) => b.classList.contains("on"));
  more.classList.toggle("on", !!open);
  if (open) more.setAttribute("aria-current", "page"); else more.removeAttribute("aria-current");
  more.classList.toggle("has-dot", over.some((b) => b.classList.contains("has-dot")));
  watch.takeRecords(); // its own changes aren't news
}
const watch = new MutationObserver(fit);
// a page shown or left out, the one open, a dot, a language's words
watch.observe(nav, { childList: true, attributes: true, subtree: true, attributeFilter: ["class", "hidden", "data-replaced"] });
matchMedia("(max-width: 760px), (pointer: coarse) and (max-width: 1024px)").addEventListener?.("change", fit);

more.onclick = () => {
  if (more.classList.contains("open")) return closeProtoMenu();
  const open = over.find((b) => b.classList.contains("on"));
  openProtoMenu(more, over.map((b) => ({ v: b.dataset.view, name: b.textContent.trim(), literalName: true })), open?.dataset.view ?? "",
    (v) => over.find((b) => b.dataset.view === v)?.click(), "More pages", "nav-more-menu", "right");
  // each page with its tab's mark, as an iPhone's More list has
  const box = document.querySelector(".nav-more-menu");
  box?.querySelectorAll(".pm-item").forEach((item, i) => {
    const ic = over[i]?.querySelector(".nav-ic");
    if (ic) item.querySelector(".pm-words").before(ic.cloneNode(true));
  });
};
fit();
