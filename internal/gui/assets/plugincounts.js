// Discover's counts (plugins.js): how many use each plugin and how many
// like it, from usemagpie.ai through /api/plugins/counts, and the like on
// a plugin's page. Loaded when the Plugins page loads, not with the core.
// A like comes only from a computer that has the plugin installed.
(() => {
  const HEART = "M8 13.4S2.6 10.2 2.6 6.3A2.8 2.8 0 0 1 8 5a2.8 2.8 0 0 1 5.4 1.3c0 3.9-5.4 7.1-5.4 7.1z";
  const USER = "M8 7.8a2.4 2.4 0 1 0 0-4.8 2.4 2.4 0 0 0 0 4.8z M3.2 13.4c.6-2.3 2.5-3.6 4.8-3.6s4.2 1.3 4.8 3.6";
  const ENOUGH = 20; // likes before the order goes by them, not by GitHub's stars
  let got = {}, liked = new Set();
  const n = (k) => got[k] || {};
  const num = (v) => v >= 1e3 ? (v / 1e3).toFixed(v >= 1e4 ? 0 : 1) + "k" : String(v);
  const chipOf = (d, v, title) => {
    const c = el("span", "pm-dl");
    c.append(svg(d, 11, 1.5), el("span", "", num(v)));
    c.title = title;
    return c;
  };
  window.pmCounts = {
    async load(redraw) {
      let r;
      try { r = await api("plugins/counts"); } catch { return; }
      const was = JSON.stringify([got, [...liked]]);
      got = r.plugins || {};
      liked = new Set(r.liked || []);
      // the cards are drawn already: draw them again only for a change
      if (JSON.stringify([got, [...liked]]) !== was) redraw();
    },
    users: (k) => n(k).users || 0,
    // a card's facts: who uses it, who likes it
    chips(meta, k) {
      const { users, likes } = n(k);
      if (users) meta.append(chipOf(USER, users, t("{n} people use it, counted from the last week", { n: users.toLocaleString(intlLang()) })));
      if (likes) meta.append(chipOf(HEART, likes, t("{n} people like it", { n: likes.toLocaleString(intlLang()) })));
    },
    // Most liked: by likes once a plugin has ENOUGH, the rest by stars
    order(a, b, then) {
      const la = n(a.package).likes || 0, lb = n(b.package).likes || 0;
      const ea = la >= ENOUGH, eb = lb >= ENOUGH;
      if (ea !== eb) return ea ? -1 : 1;
      if (ea) return lb - la || then(a, b);
      return (b.github?.stars || 0) - (a.github?.stars || 0) || lb - la || then(a, b);
    },
    // the like on a plugin's page; installed says whether it may be given
    like(k, installed) {
      const b = el("button", "pm-like");
      b.type = "button";
      let busy = false;
      const paint = () => {
        if (busy) return;
        const on = liked.has(k), may = on || installed();
        b.classList.toggle("on", on);
        b.disabled = !may;
        b.setAttribute("aria-pressed", String(on));
        b.title = may ? (on ? t("Take back your like") : t("Like it")) : t("Install it to like it");
        b.replaceChildren(svg(HEART, 12, 1.5), el("span", "", num(n(k).likes || 0)));
      };
      b.onclick = async () => {
        const on = !liked.has(k);
        b.disabled = busy = true;
        try {
          const r = await api("plugins/like", { name: k, on });
          liked = new Set(r.liked || []);
          got[k] = { ...n(k), likes: r.likes };
        } catch (e) {
          b.title = e.message;
        }
        busy = false;
        paint();
      };
      b.sync = paint;
      paint();
      return b;
    },
  };
})();
