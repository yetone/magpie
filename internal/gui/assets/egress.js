// The line over the Providers list saying the Claude accounts' way out to
// the internet is in a region Anthropic doesn't list as supported
// (internal/egress; @alterxyz4 on X asked for this "canary"). app.js
// imports this when /api/providers says so, so it weighs nothing on the
// core for everyone else.

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
  const why = [t("{region} isn't on Anthropic's list of supported regions. A proxy or VPN that turned off, or a node in another country, can cause this.", { region })];
  if (state?.settings?.claudeRegionHold) why.push(t("Claude requests are held until it changes."));
  const card = depLine("egress", t("Claude's way out to the internet is in {region}", { region }),
    why.join(locale === "zh" || locale === "zh-TW" || locale === "ja" ? "" : " "), again);
  if (p.egress.via) card.title = p.egress.via === "direct" ? t("Looked up going direct") : t("Looked up through {proxy}", { proxy: p.egress.via });
  return card;
};
