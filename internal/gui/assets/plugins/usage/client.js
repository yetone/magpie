// The Usage page: the allowances, what was spent and on what, the
// sessions, each request, and what filled the context window.
//
// A page the Usage plugin adds to the window (kernel.js), its words in
// i18n.js beside this file. This file registers the page and its markup,
// before app.js; page.js, after app.js and the scripts it shares the page
// with, is the page's code, and start.js then shows the first page. The
// hooks are what the shell asks of it: open (the reader came to the page),
// refresh (the window came back: the tab shown read again, quietly when it
// has its figures), reload (Refresh in the sidebar), costs (the currency
// or a rate changed) and tab (the tab shown, for the allowance card a
// link opens on). The allowance cards themselves, which the tray panel
// shows too, stay app.js's.
magpie.plugin("usage", (ctx) => {
  ctx.slots.add("page", {
    key: "usage",
    title: "Usage",
    html: `
  <div class="usage-head">
    <div class="segs" id="usageTab"></div>
    <div class="segs" id="period"></div>
    <div class="segs" id="sessRange" hidden></div>
    <span class="ctx-range" id="ctxRange" hidden></span>
    <button class="usage-range-menu" id="usageRangeMenu" type="button" aria-haspopup="menu" hidden></button>
    <span class="grow"></span>
    <span class="usage-refresh" id="usageRefresh">
      <button class="icon" id="usageReload" type="button" aria-label="Refresh now" data-tt><svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9M13.5 2.5v3h-3"/></svg></button>
      <button class="sess-pick" id="usageEvery" type="button"></button>
    </span>
    <span class="usage-cost" id="usageCost"></span>
  </div>
  <div class="usage-pane" id="usagePane">
    <div class="row-head quota-head" id="quotaHead" hidden>
      <span class="label" data-t>Allowances</span>
      <span class="grow"></span>
      <button class="sess-pick quota-trend" id="quotaTrend" type="button" hidden></button>
      <button class="text us-toset" type="button" id="usageToSet"><span data-t>Settings</span> ›</button>
      <button class="text rt-mask" id="usageMask" aria-pressed="false" data-tt="Mask the accounts on every page — email addresses and account names — for a screenshot to share"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9.9 4.2A10.4 10.4 0 0 1 12 4c6.5 0 10 8 10 8a17.6 17.6 0 0 1-2.2 3.2M6.6 6.6C3.9 8.4 2 12 2 12s3.5 8 10 8a9.7 9.7 0 0 0 5.4-1.6M9.9 9.9a3 3 0 0 0 4.2 4.2M2 2l20 20"/></svg><span data-t>Hide accounts</span></button>
      <div class="segs" id="quotaMode" hidden></div>
    </div>
    <section class="subscription-usage" id="subscriptionUsage"></section>
    <div class="us-hid" id="quotaHid" hidden><button class="text us-hidline" type="button" aria-expanded="false"></button><div class="list" hidden></div></div>
    <p class="usage-note" id="usageScope" data-t>Gateway calls only; session-log calls are in Requests. Local rejections are excluded from totals.</p>
    <section class="stats" id="stats"></section>
    <section class="chart" id="chart"></section>
    <div class="row-head"><button class="fold" type="button" data-fold="agents" aria-expanded="true" aria-controls="usageAgents"><span class="label" data-t>Agents</span></button></div>
    <div class="list" id="usageAgents"></div>
    <div class="us-hid" id="usageAgentsHid" hidden><button class="text us-hidline" type="button" aria-expanded="false"></button><div class="list" hidden></div></div>
    <div class="row-head"><button class="fold" type="button" data-fold="models" aria-expanded="true" aria-controls="usageModels"><span class="label" data-t>Models</span></button></div>
    <div class="list" id="usageModels"></div>
    <div class="row-head" id="usageAccountsHead" hidden><button class="fold" type="button" data-fold="accounts" aria-expanded="true" aria-controls="usageAccounts"><span class="label" data-t>Accounts</span></button></div>
    <div class="list" id="usageAccounts" hidden></div>
    <div class="row-head" id="usageKeysHead" hidden><button class="fold" type="button" data-fold="keys" aria-expanded="true" aria-controls="usageKeys"><span class="label" data-t>Gateway keys</span></button></div>
    <div class="list" id="usageKeys" hidden></div>
    <p class="usage-note" id="usageNote"></p>
    <p class="usage-clear"><button class="text" id="usageClear" type="button" hidden><span data-t>Clear all usage…</span></button></p>
    <div class="plugin-slot" data-slot="usage" hidden></div>
  </div>
  <div class="usage-pane" id="ledgerPane" hidden>
    <div class="sess-tools">
      <button class="sess-pick" id="ledComputer" type="button" hidden></button>
      <button class="sess-pick" id="ledAgent" type="button" hidden></button>
      <button class="sess-pick" id="ledPurpose" type="button" hidden></button>
      <button class="sess-pick" id="ledProvider" type="button" hidden></button>
      <button class="sess-pick led-more" id="ledProviderMore" type="button" aria-haspopup="menu" hidden></button>
      <button class="sess-pick" id="ledModel" type="button" hidden></button>
      <button class="sess-pick" id="ledAccount" type="button" hidden></button>
      <button class="sess-pick" id="ledKey" type="button" hidden></button>
      <div class="segs" id="ledStatus"></div>
      <span id="ledRoute" hidden><span id="ledRouteLabel"></span><button class="text" id="ledRouteClear" type="button" data-tt="Clear filter" aria-label="Clear filter">×</button></span>
      <span class="grow"></span>
      <input class="sess-filter" id="ledQ" type="search" spellcheck="false" autocomplete="off" placeholder="Search models, providers, sessions" data-tp>
      <button class="text" id="ledExport" type="button" title="Save every request listed, all pages, as a CSV file in Downloads" data-tt><span data-t>Export CSV</span></button>
    </div>
    <p class="usage-note" id="ledScope" data-t>Gateway and session-log calls; local rejections excluded from totals.</p>
    <div class="led-dash" id="ledDash" hidden>
      <div class="led-strip" id="ledKpi"></div>
      <section class="led-trend">
        <div class="led-bar">
          <div class="segs" id="ledMetric"></div>
          <div class="segs led-style" id="ledStyle"></div>
          <span class="grow"></span>
          <span class="led-split"><span class="led-by" data-t>Split by</span><span class="segs" id="ledSplit"></span></span>
        </div>
        <div class="led-body">
          <div class="led-chart" id="ledChart"></div>
          <div class="led-rank" id="ledRank"></div>
          <div class="led-rail" id="ledRail" hidden><i></i></div>
        </div>
      </section>
      <section class="led-speed" id="ledSpeed" hidden></section>
    </div>
    <section class="led-heat" id="ledHeat" hidden>
      <div class="led-bar">
        <div class="heat-title"><b data-t>Usage heatmap</b><span data-t>Daily usage over the last 53 weeks</span></div>
        <span class="grow"></span>
        <div class="segs" id="ledHeatMetric"></div>
      </div>
      <div class="heat-scroll" id="ledHeatScroll"></div>
      <div class="heat-foot"></div>
      <div class="heat-tip" id="ledHeatTip" hidden></div>
    </section>
    <div class="led-head"><p class="led-sum" id="ledSum"></p><span class="grow"></span><span class="led-arch" id="ledArchive"></span></div>
    <div class="led-strip led-vias" id="ledVia" hidden></div>
    <div class="led-wrap" id="ledWrap"></div>
            <div class="led-hscroll" id="ledHScroll" hidden><div></div></div>
    <div class="led-pager" id="ledPager" hidden></div>
    <p class="usage-note" id="ledNote"></p>
  </div>
  <div class="usage-pane led-req" id="ledReqPane" hidden></div>
  <div class="usage-pane ctx-pane" id="contextPane" hidden></div>
  <div class="usage-pane sess-pane" id="sessionsPane" hidden>
    <div class="sess-tools">
      <button class="sess-pick" id="sessAgent" type="button" hidden></button>
      <button class="sess-pick" id="sessModel" type="button" hidden></button>
      <button class="sess-pick" id="sessFolder" type="button" hidden></button>
    </div>
    <section class="stats" id="sessStats"></section>
    <section class="chart sess-chart" id="sessChart" hidden></section>
    <div class="sess-grid" id="sessGrid" hidden>
      <section class="sess-card" id="sessProjects"></section>
      <section class="sess-card" id="sessModels"></section>
      <section class="sess-card" id="sessHours"></section>
      <section class="sess-card" id="sessTop"></section>
      <section class="sess-card" id="sessShape"></section>
      <section class="sess-card" id="sessSkills"></section>
      <section class="sess-card wide" id="sessTools"></section>
    </div>
    <div class="row-head" id="sessListHead" hidden>
      <span class="label" data-t>Latest sessions</span>
      <span class="grow"></span>
      <button class="text sess-all" id="sessAll" type="button" data-feature="sessions" data-t>All sessions</button>
      <input class="sess-filter" id="sessQ" type="search" spellcheck="false" autocomplete="off" placeholder="Search sessions" data-tp>
    </div>
    <div class="list sess-list" id="sessList"></div>
    <p class="usage-note" id="sessNote"></p>
  </div>
`,
    mount() { mountUsageFolds(); },
    open() { return loadUsage(true).catch((e) => status(e.message, "err")); },
    refresh(again) {
      // on the Overview, where the allowances are, coming back is opening them (#1588)
      if (again && usageTab === "usage") quotasOpened();
      return (again && usageDrawn() ? refreshUsage() : loadUsage()).catch((e) => status(e.message, "err"));
    },
    reload() { return refreshUsage(true); },
    costs() { renderCosts(); },
    tab() { return usageTab; },
    // the reader goes to another page: the Requests give back what a
    // click lent them (ledLeave)
    leave() { if (usageTab === "requests") ledLeave(true); },
  });
});
