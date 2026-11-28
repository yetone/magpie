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

// A page's own tabs, past the five an iPhone's segmented control holds, are
// one menu at the page's top on a phone, "Instructions ⌄", as Usage's days
// are (plugins/usage/page.js): the Library's dozen and Settings' eight
// wrapped into a second bar of two or three lines over the tab bar at the
// foot (gandli on X: 同时存在底部和顶部二级导航). The strip stays, hidden
// (app.css .sub-as-menu), and a pick clicks its tab, so every tab keeps its
// one handler; Usage's four and Plugins' three stay as they are.
const SUB_STRIPS = [["#view-library .lib-tabs", "Library"], ["#setTabs", "Settings"]];
const subTabs = (strip) => [...strip.querySelectorAll(":scope > button")].filter((b) => !b.hidden);
function subMenus() {
  const phone = phoneWeb() && !document.body.classList.contains("panel");
  for (const [q, name] of SUB_STRIPS) {
    const strip = document.querySelector(q);
    if (!strip) continue;
    const tabs = subTabs(strip), menu = phone && tabs.length > 5;
    let b = strip.previousElementSibling?.classList.contains("sub-menu") ? strip.previousElementSibling : null;
    if (strip.classList.contains("sub-as-menu") !== menu) strip.classList.toggle("sub-as-menu", menu);
    if (!menu) { b?.remove(); continue; }
    if (!b) {
      b = el("button", "sub-menu");
      b.type = "button";
      b.setAttribute("aria-haspopup", "menu");
      b.onclick = (e) => {
        e.stopPropagation();
        if (b.classList.contains("open")) return closeProtoMenu();
        const all = subTabs(strip);
        const on = all.findIndex((x) => x.classList.contains("on"));
        openProtoMenu(b, all.map((x, i) => ({ v: String(i), name: x.textContent.trim(), literalName: true, note: "" })), String(on), (v) => all[+v]?.click(), name, "sub-menu-pop");
      };
      strip.before(b);
    }
    const on = tabs.find((x) => x.classList.contains("on")) || tabs[0];
    const said = on.textContent.trim();
    if (b.firstChild?.textContent !== said) b.replaceChildren(el("span", "", said), svg(CHEV, 11, 1.8));
    const label = t(name) + ": " + said;
    if (b.getAttribute("aria-label") !== label) b.setAttribute("aria-label", label);
  }
  subWatch.takeRecords(); // its own changes aren't news
}
const subWatch = new MutationObserver(subMenus);
// the Library draws its head again with each render; Settings' tabs come
// and go with plugins and features, and either's words with a language.
// The Library's page is a plugin's, added to the window when it loads.
const subSeen = new WeakSet();
function subPages() {
  for (const page of document.querySelectorAll(":is(#view-library, #view-settings)")) {
    if (subSeen.has(page)) continue;
    subSeen.add(page);
    subWatch.observe(page, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ["class", "hidden"] });
  }
  subMenus();
}
const pageWatch = new MutationObserver(() => {
  const lib = document.querySelector("#view-library");
  if (!lib) return;
  pageWatch.disconnect();
  subPages();
});
if (!document.querySelector("#view-library")) pageWatch.observe(document.body, { childList: true });
matchMedia("(max-width: 760px), (pointer: coarse) and (max-width: 1024px)").addEventListener?.("change", subMenus);
subPages();
