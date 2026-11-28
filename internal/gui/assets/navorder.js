// The pages in the reader's order (chet030714 on X), kept in localStorage
// magpie.navOrder by Settings › General › Sidebar pages (navpages.js).
// app.js navHides imports this once an order is kept, so a reader who never
// reorders loads none of it.

// the order of the pages vs; one it doesn't name, new in an update, goes
// after the page it follows at home
window.navOrder = function (vs) {
  const o = [...new Set(navList("Order"))].filter((v) => vs.includes(v));
  vs.forEach((v, i) => { if (!o.includes(v)) o.splice(i && o.indexOf(vs[i - 1]) + 1, 0, v); });
  return o;
};

// the built-in pages and their groups' names, in the order they come at home
const nav = $("#nav");
window.navHome = [...nav.querySelectorAll(":scope > .nav-gl:not([data-plugin-pages]), :scope > button[data-view]:not([data-plugin-page])")];

// the built-in pages put in that order, the phone's tab bar's too
// (phonetabs.js reads #nav); a group's name goes while the order is the
// reader's own, and comes back with the home order
window.navArrange = function () {
  const home = navHome, vs = home.flatMap((e) => e.dataset.view || []), o = navOrder(vs), own = o.join() !== vs.join();
  let prev = null;
  for (const e of own ? o.map((v) => home.find((b) => b.dataset.view === v)) : home) {
    let nx = prev ? prev.nextElementSibling : nav.firstElementChild;
    while (nx?.matches(".nav-sub, .thumb")) nx = nx.nextElementSibling;
    if (nx !== e) nav.insertBefore(e, nx);
    prev = e;
    // a plugin's page in this built-in's place (pluginpages.js) goes with
    // it, when the order is put on after that tab is there (eisen0419, #7335)
    const repl = e.dataset?.replaced && nav.querySelector(`button[data-plugin-page][data-view="${CSS.escape(e.dataset.replaced)}"]`);
    if (repl) { if (e.nextElementSibling !== repl) e.after(repl); prev = repl; }
  }
  for (const e of home) if (!e.dataset.view) own ? e.remove() : e.dataset.en && (e.textContent = t(e.dataset.en));
};
