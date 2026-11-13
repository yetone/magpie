// Settings › Usage › Currency, its Totals in row and Advanced's Menu bar
// currency. app.js imports this when Settings draws its usage prefs
// (renderTrayUsage), so it weighs nothing on the core.

window.currencyPrefs = function currencyPrefs(s, keep) {
  const two = CURRENCIES.slice(0, 2).map(([id, name]) => [id, t(name)]);
  $("#currencySegs").replaceChildren(segs(CURRENCIES.map(([id, name]) => [id, t(name)]), s.currency || "usd", (v) => savePrefs({ ...keep, currency: v })));
  // "provider": each provider's costs in what it sells in, every sum in
  // Totals in (onkashiko on X)
  $("#totalCurrencyRow").hidden = s.currency !== "provider";
  $("#totalCurrencySegs").replaceChildren(segs(two, s.totalCurrency || "usd", (v) => savePrefs({ ...keep, totalCurrency: v })));
  // the menu bar's and its tooltip's totals in a currency of their own, or
  // as the totals are (onkashiko on X): seldom changed, so under Advanced,
  // open when it is set
  $("#trayCurrencySegs").replaceChildren(segs([["", t("Same as totals")], ...two], s.trayCurrency || "", (v) => savePrefs({ ...keep, trayCurrency: v })));
  if (s.trayCurrency) $("#usageMore").open = true;
  const rate = s.fx?.rate, sub = $("#currencySub");
  sub.textContent = s.currency === "provider" ? t("Each provider's costs in what it sells in: yuan for DeepSeek, Kimi, Zhipu and other Chinese vendors, dollars for the rest")
    : t("What a cost — the Usage page's, the tray panel's, the TUI's and the CLI's — is shown as; a vendor's own balance, already in its own currency, is never converted");
  sub.title = rate ? t("1 USD = {rate} CNY{when}", { rate: rate.toFixed(2), when: s.fx.at ? " · " + (s.fx.stale ? t("last fetched {when}", { when: syncWhen(s.fx.at) }) : t("fetched {when}", { when: syncWhen(s.fx.at) })) : "" }) : "";
};
