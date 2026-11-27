// The line over the Providers list saying the Claude accounts' way out to
// the internet is in a region Anthropic doesn't list as supported
// (internal/egress; @alterxyz4 on X asked for this "canary"), and the
// Claude editor's Way out and Allowed ways out rows (mamba on Discord).
// app.js imports this when /api/providers says so, or Settings draws
// Claude's rows, so it weighs nothing on the core.

// egressLine: an amber dot, where the way out is and why that matters,
// whether magpie is holding Claude's requests for it, and Check again,
// which looks it up through each proxy now. An Unknown never comes here.
let egressBusy = false;
window.egressLine = function () {
  const p = providers.providers.find((p) => p.egress?.state === "unsupported");
  if (!p) return null;
  const loc = p.egress.loc;
  let region = loc;
  try { region = new Intl.DisplayNames([locale], { type: "region" }).of(loc) || loc; } catch {}
  const again = el("button", "text action", egressBusy ? t("Checking…") : t("Check again"));
  again.disabled = again.classList.toggle("busy", egressBusy);
  again.onclick = async () => {
    egressBusy = true;
    renderMovable();
    try {
      p.egress = await api("egress/check", {});
    } catch (e) {
      status(e.message, "err");
    }
    egressBusy = false;
    renderMovable();
  };
  const why = [p.egress.mine
    ? t("{where} isn't one of the ways out you allowed for Claude. A proxy or VPN that turned off, or a node switched, can cause this.", { where: p.egress.ip ? `${p.egress.ip} (${region})` : region })
    : t("{region} isn't on Anthropic's list of supported regions. A proxy or VPN that turned off, or a node in another country, can cause this.", { region })];
  if (state?.settings?.claudeRegionHold) why.push(t("Claude requests are held until it changes."));
  const card = depLine("egress", t("Claude's way out to the internet is in {region}", { region }),
    why.join(locale === "zh" || locale === "zh-TW" || locale === "ja" ? "" : " "), again);
  if (p.egress.via) card.title = p.egress.via === "direct" ? t("Looked up going direct") : t("Looked up through {proxy}", { proxy: p.egress.via });
  return card;
};

// wayOutRow: where Claude's requests go out to the internet, as the last
// lookup found it (internal/egress): the address and its region, and
// Check, which looks again through each proxy now (mamba on Discord: a
// node switched inside a proxy app). A lookup that failed says so, not
// "Not looked up yet".
let wayOutBusy = false;
function wayOutRow() {
  const p = providers?.providers?.find((p) => p.id === "claude");
  const e = p?.egress;
  let where = e?.error ? t("Couldn't look it up") : t("Not looked up yet");
  if (e?.loc || e?.ip) {
    let region = e.loc || "";
    try { if (/^[A-Z]{2}$/.test(region)) region = new Intl.DisplayNames([locale], { type: "region" }).of(region) || region; } catch {}
    where = [e.ip, region].filter(Boolean).join(" · ");
  }
  const check = el("button", "text action", wayOutBusy ? t("Checking…") : t("Check"));
  check.disabled = check.classList.toggle("busy", wayOutBusy);
  const row = segRow("claudeWayOutRow", t("Way out"), where, [], "", () => {});
  row.querySelector(".val").replaceChildren(check);
  if (e?.state === "unsupported") row.querySelector(".sub").classList.add("warn");
  if (e?.via) row.title = e.via === "direct" ? t("Looked up going direct") : t("Looked up through {proxy}", { proxy: e.via });
  check.onclick = checkWayOut;
  return row;
}
// wayOutShown: the editor shows the way out without a Check (mamba on
// Discord, again: the address showed only after one). The Providers list
// may have been read before the gateway's first lookup ended, and an open
// editor isn't read again, so with no address in it the editor looks it
// up once as it opens: Cloudflare's trace, through each proxy, never
// Anthropic. At most every 30 s, so a lookup that fails isn't asked again
// on each draw.
let wayOutAuto = 0;
window.wayOutShown = function () {
  const e = providers?.providers?.find((p) => p.id === "claude")?.egress;
  if (e?.loc || e?.ip || wayOutBusy || Date.now() - wayOutAuto < 30e3) return;
  wayOutAuto = Date.now();
  checkWayOut();
};
// checkWayOut: Check, and a changed list of allowed ways out, which the
// way out is judged against again
async function checkWayOut() {
  if (wayOutBusy) return;
  wayOutBusy = true;
  $("#claudeWayOutRow")?.replaceWith(wayOutRow());
  try {
    const got = await api("egress/check", {});
    const p = providers?.providers?.find((p) => p.id === "claude");
    if (p) p.egress = got;
  } catch (err) {
    statusErr(err);
  }
  wayOutBusy = false;
  $("#claudeWayOutRow")?.replaceWith(wayOutRow());
  renderMovable();
}

// waysOutRow: the ways out the user allows Claude's requests, regions and
// addresses (claudeWaysOut); empty, Anthropic's supported regions. Saved
// on Enter and on leaving the field.
function waysOutRow(s, keep) {
  let now = (s.claudeWaysOut || []).join(", ");
  const i = input(now, t("Anthropic's supported regions"));
  i.className = "words";
  i.setAttribute("aria-label", t("Allowed ways out"));
  const save = () => {
    const ways = i.value.split(/[\s,，、;；]+/).filter(Boolean);
    // Chromium blurs a field the save's redraw takes away: once is enough
    if (ways.join(", ") === now) return;
    now = ways.join(", ");
    savePrefs({ ...keep, claudeWaysOut: ways });
    prefsQueue.then(checkWayOut, () => {});
  };
  const keys = i.onkeydown;
  i.onkeydown = (e) => { keys(e); if (e.key === "Enter") save(); };
  i.onblur = save;
  const row = segRow("claudeWaysOutRow", t("Allowed ways out"),
    t("Regions and addresses Claude requests may go out from, such as US, JP or 203.0.113.0/24"), [], "", () => {});
  row.querySelector(".val").replaceChildren(i);
  return row;
}

// holdRow: Claude's requests held while their way out is in a region
// Anthropic doesn't list, or isn't one the user allowed (internal/egress,
// off unless on)
function holdRow(s, keep) {
  return segRow("claudeRegionRow", t("Hold requests outside supported regions"),
    t("While this computer's way out to the internet is in a region Anthropic doesn't list, or isn't one you allowed, Claude requests aren't sent and the agent is told why. A lookup that fails holds nothing"),
    [["off", t("Off")], ["on", t("On")]], s.claudeRegionHold ? "on" : "off", (v) => savePrefs({ ...keep, claudeRegionHold: v === "on" }));
}

// egressRows puts the Way out, hold and Allowed ways out rows at the end
// of renderReplies' Claude list, in place of those a render before put
// there (one whose import resolved late included). They live here, not
// in app.js, as only a Claude subscription has them.
window.egressRows = (list, s, keep) => {
  list.querySelectorAll("#claudeWayOutRow, #claudeRegionRow, #claudeWaysOutRow").forEach((e) => e.remove());
  list.append(wayOutRow(), holdRow(s, keep), waysOutRow(s, keep));
  if (list.closest(".prov-prefs")) wayOutShown();
};
