// Partners: the sponsors whose product is a plugin, first in Discover
// (yetone: 有些合作方不是做 LLM provider 的…适合在插件页的赞助位). Who
// they are comes from usemagpie.ai (provider.PluginPartners), each shown
// in the languages it names, its tagline its own words. One is counted as
// the add sheet's partners are: shown once a showing of the page, and
// opened; an install of its plugin, from anywhere, magpie counts itself
// (provider.AddPlugin). plugins.js imports this when its page is first
// drawn, so it weighs nothing on the core; it hands over the page's picks
// and the helpers its rows are drawn with.
let seen = new Set();
window.pmPartners = function ({ f, all, kindPick, srcPick, onlyNew, entryOf, kindOf, opens, npm, logo, actionFor, glyph, OUTL }) {
  const card = (pa) => all.find((x) => x.package === pa.plugin);
  const list = (providers?.pluginPartners || []).filter((pa) => {
    if (!partnerShown(pa)) return false;
    const l = card(pa);
    if (kindPick && (!l || kindOf(l) !== kindPick)) return false;
    if (onlyNew && entryOf(pa.plugin)) return false;
    return !f || [pa.name, pa.plugin, partnerNote(pa)].join(" ").toLowerCase().includes(f);
  });
  if (!list.length || srcPick) return null;
  const sec = el("section", "pm-sec pm-partners");
  const h = el("div", "pm-sechead");
  const join = el("button", "sponsor-me");
  join.type = "button";
  join.append(svg(HEART, 12, 1.4), el("span", "", t("Become a sponsor")));
  join.onclick = () => (web ? (location.href = SPONSOR_MAIL) : api("open", { url: SPONSOR_MAIL }));
  h.append(el("h3", "", t("Partners")), el("span", "", t("magpie's sponsors")), el("span", "grow"), join);
  const box = el("div", "pm-plist");
  for (const pa of list) box.append(row(pa, card(pa)));
  sec.append(h, box);
  const fresh = list.filter((pa) => !seen.has(pa.id));
  for (const pa of fresh) seen.add(pa.id);
  countPartner("shown", fresh.map((pa) => pa.id));
  return sec;

  function row(pa, l) {
    const r = el("div", "pm-rrow pm-prow");
    // a plugin the market doesn't list opens as npm has it
    opens(r, l || { package: pa.plugin, name: pa.name, npm: npm[pa.plugin] });
    const open = r.onclick;
    r.onclick = () => { countPartner("opened", [pa.id]); open(); };
    r.onkeydown = (ev) => { if (ev.target === r && (ev.key === "Enter" || ev.key === " ")) { ev.preventDefault(); r.onclick(); } };
    const who = el("div", "pm-rwho");
    who.append(el("b", "", pa.name));
    const note = partnerNote(pa);
    if (note) who.append(el("span", "", note));
    r.title = pa.name + (note ? " · " + note : "") + "\n" + pa.plugin;
    const ic = pa.icon && pa.icon !== "generic" ? pa.icon : l?.icon || l?.npm?.icon;
    r.append(logo(ic, false, l ? kindOf(l) : ""), who);
    if (pa.website) {
      // its site, through usemagpie.ai/go, which counts the visit
      const site = el("button", "pm-psite");
      site.type = "button";
      site.append(glyph(OUTL, 12, 1.5));
      site.setAttribute("aria-label", t("Website"));
      site.title = pa.siteHost || t("Website");
      site.onclick = (ev) => { ev.stopPropagation(); api("open", { url: pa.website }).catch(() => {}); };
      site.onkeydown = (ev) => ev.stopPropagation();
      r.append(site);
    }
    r.append(actionFor(pa.plugin, pa.name, l || null));
    return r;
  }
};
window.pmPartners.again = () => { seen = new Set(); };
