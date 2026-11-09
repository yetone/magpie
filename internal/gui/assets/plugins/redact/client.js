// Insights › Masking: what masking kept from vendors, request by request.
//
// A page the redact plugin adds to the window (kernel.js), its words in
// i18n.js beside this file. This file registers the page and its markup,
// before app.js; page.js, after app.js, is the page's code. The card on
// top says how much was masked over 7 or 30 days and where it went, the
// list under it each request (or each value) masking changed (between
// them, folded, demo.js's 小鹊 shows how masking works), and a
// request opened says what it carried, what the vendor got instead, and
// how each value was found and put back. The Masking alert is with
// masking's other settings, in Settings › Privacy (settings.js). The
// hooks are what the shell asks of it: open, refresh (the window came
// back), reload (Refresh in the sidebar), leave, and focus (a masked
// call's badge opens its request).
magpie.plugin("redact", (ctx) => {
  ctx.slots.add("page", {
    key: "masking",
    title: "Masking",
    html: `
  <section class="msk-card" id="maskCard"></section>
  <section class="msk-demo" id="maskDemo" data-no-pii>
    <button type="button" class="md-row" id="mdRow" aria-expanded="false" aria-controls="mdBody">
      <span class="md-thumb" aria-hidden="true"></span>
      <span class="md-words"><span class="md-name" data-t>How masking works</span><span class="md-sub" data-t>Little Magpie walks one request through: passwords become placeholders before it leaves, and come back on your computer with the answer</span></span>
      <span class="md-len">0:27</span>
      <svg class="md-chev" viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>
    </button>
    <div class="md-body" id="mdBody" hidden></div>
  </section>
  <section class="msk-list" id="maskList"></section>
  <section class="msk-one" id="maskOne" hidden></section>
`,
    open() { return maskPageOpen(); },
    refresh(again) { return maskPageLoad(again); },
    reload() { return maskPageLoad(true); },
    leave() { maskTipOff(); },
    focus(key) { maskOneOpen(String(key)); },
  });
});
