// Settings › Usage › Advanced › Time zone (JackyFill on X): the zone this
// magpie's days and times are in, in place of its computer's, so that a
// magpie on a server and one on the desk show and add up the same days.
// System is the computer's, as before. It is this magpie's setting, not
// the reader's: the days are cut where the data is (usage.Zone), for the
// tray, the CLI and the keys' daily caps too. The page's zone is the one
// boot.js named (tzshim.js), so a zone other than it, saved here or found
// set elsewhere when Settings is drawn, is taken by loading the page
// again. currencyprefs.js imports this when Settings draws its usage
// prefs, so it weighs nothing on the core.

window.timeZonePref = function timeZonePref(s, keep) {
  const val = $("#timeZoneVal");
  if (!val) return;
  const zone = s.timeZone || "";
  if (zone !== (window.bootPrefs?.timeZone || "")) return location.reload();
  if (zone) $("#usageMore").open = true;
  let b = val.querySelector(".pref-pick");
  if (!b) {
    b = el("button", "sess-pick pref-pick");
    b.type = "button";
    menuOpener(b);
    val.replaceChildren(b);
  }
  b.setAttribute("aria-label", t("Time zone"));
  sessPick(b, "System", zone, zoneOpts(zone), "Time zone", (v) => {
    if (v !== zone) savePrefs({ ...keep, timeZone: v });
  });
};

// zoneOpts is every zone this browser knows, each with its offset this
// hour, worked out once an hour (Settings is drawn again on each save); a
// browser too old to list them offers the one set and UTC.
let zoneList = null;
function zoneOpts(zone) {
  const hour = Math.floor(Date.now() / 36e5);
  if (zoneList?.hour !== hour || zone && !zoneList.opts.some((o) => o.v === zone)) {
    const names = typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : [];
    const offset = (z) => {
      try {
        const p = new Intl.DateTimeFormat("en-US", { timeZone: z, timeZoneName: "longOffset" }).formatToParts(hour * 36e5).find((x) => x.type === "timeZoneName");
        return p ? p.value.replace(/^GMT$/, "GMT+00:00").replace("GMT", "UTC") : "";
      } catch { return ""; }
    };
    const list = [...new Set([...(zone ? [zone] : []), "UTC", ...names])].sort();
    zoneList = { hour, opts: list.map((z) => ({ v: z, name: z, note: offset(z) })) };
  }
  return zoneList.opts;
}
