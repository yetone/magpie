// Analytics View Controller
// Quality & Analytics dashboard with independent drilldown page view (left compact chart, right calls list).
(() => {
  const page = $("#view-analytics");
  if (!page) return;

  const headEl = $("#anHead");
  const controlsEl = $("#anControls");
  const periodSeg = $("#anPeriod");
  const dimSeg = $("#anDim");
  const filtersBox = $("#anFilters");
  const bodyEl = $("#anBody");
  const backBtn = $("#anBack");
  const drillPageEl = $("#anDrillPage");
  const drillHeadEl = $("#anDrillHead");
  const drillLeftEl = $("#anDrillLeft");
  const drillRightEl = $("#anDrillRight");

  // Active dashboard state
  let currentPeriod = "30d"; // inherited from usage or private to analytics
  let currentDim = "all";    // "all" | "model" | "provider" | "agent"
  let filters = { model: "", provider: "", agent: "" };
  let data = null;           // full AnalyticsData from GET /api/analytics
  let shown = null;          // the period, dimension and filters data answers
  let loadSeq = 0;           // ignore stale async responses
  let pendingFetch = false;  // true while GET /api/analytics is in flight
  let dashboardScrollTop = 0;// saved scrollTop when entering drill page

  // Independent Drill Page state
  let drillMode = false;     // true when on drill page
  let drillChartId = null;   // "error_rate" | "ttft" | "speed" | "cost" | "cache_hit_rate" | null
  let drillEntity = null;    // { dim: "model"|"provider"|"agent", val: string } | null
  let drillCalls = null;     // []Record from GET /api/analytics/calls
  let drillSeq = 0;          // ignore stale call responses
  let drillLoading = false;
  let drillPendingKey = null;// pending period+filter+chart+entity key for dedup
  let drillError = null;     // error message or null
  let routeReqToken = 0;     // attempt token to invalidate stale inline-route mounts
  let routePendingId = null; // route_id currently opening inline
  let drillSavedScroll = null;// saved list scrollTop while the route is shown inline
  let drillRoute = null;     // { id, t } the call whose routing is shown inline, or null
  let drillRouteIndex = null;// index of that call in the list, kept selected
  let drillRouteError = null;// error message when the inline route could not be loaded
  const DIMS = [
    ["all", "All"],
    ["model", "By Model"],
    ["provider", "By Provider"],
    ["agent", "By Agent"],
  ];

  const PERIOD_LIST = [
    ["today", "Today"],
    ["7d", "7 days"],
    ["30d", "30 days"],
    ["all", "All"],
  ];

  const PERIOD_LABELS = {
    today: "Today",
    "7d": "7 days",
    "30d": "30 days",
    all: "All",
  };

  // Wire back button
  function syncBackBtn() {
    if (!backBtn) return;
    const text = drillMode ? t("Back to Quality & Analytics") : t("Back to Usage");
    const en = drillMode ? "Back to Quality & Analytics" : "Back to Usage";
    backBtn.dataset.tt = en;
    backBtn.dataset.enTitle = en;
    backBtn.title = text;
    backBtn.setAttribute("aria-label", text);
  }

  if (backBtn) {
    syncBackBtn();
    backBtn.onclick = (e) => {
      if (drillMode) {
        exitDrill(e);
      } else {
        show("usage");
      }
    };
  }

  // Helper formatting functions
  function fmtPct(n) {
    if (n === null || n === undefined || isNaN(n)) return "—";
    return (n * 100).toFixed(1) + "%";
  }

  function fmtMs(ms) {
    if (ms === null || ms === undefined || isNaN(ms)) return "—";
    if (ms >= 1000) return (ms / 1000).toFixed(2) + "s";
    return Math.round(ms) + "ms";
  }

  function fmtSpeed(spd) {
    if (spd === null || spd === undefined || isNaN(spd)) return "—";
    return spd.toFixed(1) + " tok/s";
  }

  function fmtCostVal(usd) {
    if (usd === null || usd === undefined || isNaN(usd)) return "—";
    return fmtCost({ cost: usd, unpriced: 0 }) || "—";
  }

  function fmtTok(n) {
    if (n === null || n === undefined || isNaN(n)) return "0";
    if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
    return String(n);
  }
  function getCostDisplayText(s) {
    if (!s || !s.calls) return { val: "—", sub: t("No data in this period") };
    if (!s.cost && s.unpriced) {
      return { val: t("Unknown cost"), sub: t("No price for these models") };
    }
    return {
      val: fmtCostVal(s.cost),
      sub: s.unpriced ? t("≈{cost} ({n} unpriced)", { cost: fmtCostVal(s.cost), n: String(s.unpriced) }) : t("At model list price"),
    };
  }


  function getAppLocale() {
    return document.documentElement.lang || "en";
  }

  function fmtTime(iso) {
    if (!iso) return "—";
    try {
      const d = new Date(iso);
      return d.toLocaleTimeString(getAppLocale(), { hour12: false });
    } catch {
      return iso;
    }
  }

  function fmtDateTime(iso) {
    if (!iso) return "—";
    try {
      const d = new Date(iso);
      return new Intl.DateTimeFormat(getAppLocale(), {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        timeZoneName: "short",
        hour12: false,
      }).format(d);
    } catch {
      return iso;
    }
  }


  // Fetch /api/analytics
  async function fetchAnalytics() {
    const seq = ++loadSeq;
    pendingFetch = true;
    page.classList.add("loading");
    page.setAttribute("aria-busy", "true");

    const params = new URLSearchParams();
    params.set("period", currentPeriod);
    if (filters.model) params.set("model", filters.model);
    if (filters.provider) params.set("provider", filters.provider);
    if (filters.agent) params.set("agent", filters.agent);

    try {
      const res = await api("analytics?" + params.toString());
      if (seq !== loadSeq) return; // Stale request, ignore
      data = res;
      shown = { period: currentPeriod, dim: currentDim, filters: { ...filters } };
      pendingFetch = false;
      render();
    } catch (e) {
      if (seq !== loadSeq) return;
      pendingFetch = false;
      // the page still shows the last answer: put the query back to it, so
      // the controls, and a drill from its charts, are of what is shown
      if (shown) {
        currentPeriod = shown.period;
        currentDim = shown.dim;
        filters = { ...shown.filters };
        render();
      }
      status(e.message || String(e), "err");
    } finally {
      if (seq === loadSeq) {
        page.classList.remove("loading");
        page.removeAttribute("aria-busy");
      }
    }
  }

  // Open independent drill page for chart and optional entity
  function triggerDrill(chartId, entity, e) {
    if (pendingFetch) return;
    if (e) scrollOnPurpose(e, 800);

    drillMode = true;
    drillChartId = chartId;
    drillEntity = entity;

    // Save dashboard scroll position before hiding
    dashboardScrollTop = page.scrollTop || 0;

    // Switch view sections
    controlsEl.hidden = true;
    bodyEl.hidden = true;
    drillPageEl.hidden = false;

    // Update back button title and aria for drill mode
    syncBackBtn();

    // Reset view scroll to top on entering drill page
    page.scrollTop = 0;

    renderDrillPage();
    fetchDrillCalls(chartId, entity);
  }

  // Return from drill page to main analytics dashboard
  function exitDrill(e) {
    if (e) scrollOnPurpose(e, 800);
    drillSeq++; // Cancel any in-flight call requests
    routeReqToken++;
    routePendingId = null;
    drillSavedScroll = null;
    drillRoute = null;
    drillRouteIndex = null;
    drillRouteError = null;
    if (window.unmountRoutingInline) window.unmountRoutingInline();
    drillMode = false;
    drillLoading = false;
    drillPendingKey = null;
    drillCalls = null;
    drillError = null;
    // Drop the drill panes: the inline stage and its return button must
    // not linger anywhere once Analytics is left
    drillRightEl.replaceChildren();
    drillLeftEl.replaceChildren();
    drillHeadEl.replaceChildren();
    // Switch view sections
    drillPageEl.hidden = true;
    controlsEl.hidden = false;
    bodyEl.hidden = false;

    // Restore back button title and aria for dashboard mode
    syncBackBtn();

    // Restore dashboard scroll position
    requestAnimationFrame(() => {
      if (drillMode || page.hidden) return;
      if (e) scrollOnPurpose(e, 500);
      page.scrollTop = dashboardScrollTop;
    });
  }

  // Show the calls list again after the inline routing drill (keeps the
  // left ranking, filters, period and the row the reader clicked)
  function closeDrillRoute(e) {
    if (e) scrollOnPurpose(e, 1000);
    routeReqToken++;
    routePendingId = null;
    const idx = drillRouteIndex;
    drillRoute = null;
    drillRouteError = null;
    if (window.unmountRoutingInline) window.unmountRoutingInline();
    if (!drillMode || page.hidden) return;
    renderDrillRight();
    if (idx != null) {
      const row = drillRightEl.querySelector(`.an-call-item[data-index="${idx}"]`);
      if (row) row.classList.add("active");
    }
    requestAnimationFrame(() => {
      if (!drillMode || page.hidden) return;
      if (drillSavedScroll != null) page.scrollTop = drillSavedScroll;
    });
  }

  // Any change of what the calls list shows (entity, period, dimension,
  // filter) releases the inline route: the stage belongs to one call
  function resetInlineRoute() {
    if (!drillRoute && !drillRouteError) return;
    routeReqToken++;
    routePendingId = null;
    drillRoute = null;
    drillRouteIndex = null;
    drillRouteError = null;
    drillSavedScroll = null;
    if (window.unmountRoutingInline) window.unmountRoutingInline();
  }

  // Fetch /api/analytics/calls for drill-down
  async function fetchDrillCalls(chartId, entity) {
    // Inherit active global filters
    let m = filters.model;
    let p = filters.provider;
    let a = filters.agent;

    // Merge entity filter
    if (entity) {
      if (entity.dim === "model") m = entity.val;
      else if (entity.dim === "provider") p = entity.val;
      else if (entity.dim === "agent") a = entity.val;
    }

    const reqKey = JSON.stringify([currentPeriod, m || "", p || "", a || "", chartId || ""]);
    if (drillLoading && drillPendingKey === reqKey) {
      return;
    }

    const seq = ++drillSeq;
    routeReqToken++;
    routePendingId = null;
    drillSavedScroll = null;
    drillLoading = true;
    drillPendingKey = reqKey;
    drillCalls = null;
    drillError = null;

    renderDrillRight();

    const params = new URLSearchParams();
    params.set("period", currentPeriod);
    params.set("chart_id", chartId);
    params.set("limit", "50");

    if (m) params.set("model", m);
    if (p) params.set("provider", p);
    if (a) params.set("agent", a);

    try {
      const res = await api("analytics/calls?" + params.toString());
      if (seq !== drillSeq) return; // Stale request, ignore
      drillCalls = res?.calls || [];
      drillLoading = false;
      drillPendingKey = null;
      drillError = null;
      renderDrillRight();
    } catch (e) {
      if (seq !== drillSeq) return;
      drillCalls = null;
      drillLoading = false;
      drillPendingKey = null;
      drillError = e.message || String(e);
      renderDrillRight();
    }
  }

  // Main UI Renderer (draws header controls & theme blocks into bodyEl)
  function render() {
    renderControls();

    bodyEl.replaceChildren();
    if (!data) return;

    const summary = data.summary || {};

    // Reliability
    renderReliabilitySection(summary);

    // Speed
    renderSpeedSection(summary);

    // Cost & Cache
    renderCostCacheSection(summary);
    if (drillMode) {
      renderDrillPage();
    }
  }

  function renderControls() {
    // Period segments
    periodSeg.replaceChildren();
    for (const [id, label] of PERIOD_LIST) {
      const b = el("button", "opt" + (id === currentPeriod ? " on" : ""), t(label));
      b.onclick = (e) => {
        if (id === currentPeriod) return;
        currentPeriod = id;
        if (drillMode) exitDrill(e);
        fetchAnalytics();
      };
      periodSeg.append(b);
    }
    slide(periodSeg, "an-period");

    // Dimension segments
    dimSeg.replaceChildren();
    for (const [id, label] of DIMS) {
      const b = el("button", "opt" + (id === currentDim ? " on" : ""), t(label));
      b.onclick = () => {
        if (id === currentDim) return;
        currentDim = id;
        if (drillMode) exitDrill();
        // Mutually exclusive filter rule:
        // Switching to "By X" clears X's filter, preserves others.
        // Switching to "All" preserves all existing filters.
        let needRefetch = false;
        if (id !== "all") {
          if (filters[id]) {
            filters[id] = "";
            needRefetch = true;
          }
        }
        if (needRefetch) {
          fetchAnalytics();
        } else {
          if (shown && (currentDim === "all" || !shown.filters[currentDim])) shown.dim = currentDim;
          render();
        }
      };
      dimSeg.append(b);
    }
    slide(dimSeg, "an-dim");

    // Dropdown filters
    filtersBox.replaceChildren();
    const filterDims = [];
    if (currentDim === "all") {
      filterDims.push("model", "provider", "agent");
    } else if (currentDim === "model") {
      filterDims.push("provider", "agent");
    } else if (currentDim === "provider") {
      filterDims.push("model", "agent");
    } else if (currentDim === "agent") {
      filterDims.push("model", "provider");
    }

    const availFilters = data?.filters || { model: [], provider: [], agent: [] };
    const dimNames = { model: t("Model"), provider: t("Provider"), agent: t("Agent") };
    const allLabels = {
      model: t("All Models"),
      provider: t("All Providers"),
      agent: t("All Agents"),
    };

    for (const fdim of filterDims) {
      const curVal = filters[fdim];
      const wrap = el("div", "an-filter-wrap" + (curVal ? " has-val" : ""));
      wrap.dataset.filterDim = fdim;
      const btn = el("button", "an-filter-btn" + (curVal ? " set" : ""));
      btn.type = "button";
      btn.dataset.dim = fdim;
      btn.setAttribute("aria-haspopup", "true");
      btn.setAttribute("aria-expanded", "false");
      const displayLabel = curVal ? `${dimNames[fdim]}: ${curVal}` : (allLabels[fdim] || t("All {name}", { name: dimNames[fdim] }));
      btn.append(el("span", "an-filter-label", displayLabel), svg("M4 6l4 4 4-4", 10, 1.6));

      btn.onclick = (e) => {
        e.stopPropagation();
        if (btn.classList.contains("open")) {
          if (typeof closeProtoMenu === "function") closeProtoMenu();
          return;
        }
        const options = [{ v: "", name: allLabels[fdim] || t("All {name}", { name: dimNames[fdim] }), note: "" }];
        for (const item of availFilters[fdim] || []) {
          const masked = maskAccounts ? maskAccounts(item) : item;
          options.push({ v: item, name: masked, note: "", literalName: true });
        }
        openProtoMenu(btn, options, curVal, (newVal) => {
          if (filters[fdim] === newVal) return;
          filters[fdim] = newVal;
          if (drillMode) exitDrill();
          fetchAnalytics();
        }, dimNames[fdim], "an-filter-menu");
      };

      wrap.append(btn);

      if (curVal) {
        const clearBtn = el("button", "an-filter-clear");
        clearBtn.type = "button";
        clearBtn.setAttribute("aria-label", t("Clear {name} filter", { name: dimNames[fdim] }));
        clearBtn.title = t("Clear");
        clearBtn.append(svg("M4 4l8 8M12 4l-8 8", 9, 1.8));
        clearBtn.onclick = (e) => {
          e.stopPropagation();
          e.preventDefault();
          filters[fdim] = "";
          if (drillMode) exitDrill();
          fetchAnalytics();
        };
        wrap.append(clearBtn);
      }

      filtersBox.append(wrap);
    }
  }

  // --- Theme 1: Reliability ---
  function renderReliabilitySection(s) {
    const sec = el("section", "an-theme-section");
    const head = el("div", "an-theme-head");
    head.append(el("span", "an-theme-title", t("Reliability")));
    sec.append(head);

    // KPI Tiles
    const kpis = el("div", "an-kpis");

    const tSuccess = el("div", "an-kpi-tile");
    const cancelPart = s.canceled ? ` · ${t("Canceled")}: ${s.canceled}` : "";
    const cancelRatePart = s.cancel_rate ? ` (${fmtPct(s.cancel_rate)})` : "";
    tSuccess.append(
      el("span", "an-kpi-label", t("Success Rate")),
      el("span", "an-kpi-val", fmtPct(s.success_rate)),
      el("span", "an-kpi-sub", s.calls ? (t("{n} total calls", { n: String(s.calls) }) + cancelPart + cancelRatePart) : t("No data in this period"))
    );

    const t429 = el("div", "an-kpi-tile");
    t429.append(
      el("span", "an-kpi-label", t("Rate Limits (429)")),
      el("span", "an-kpi-val", s.calls ? String(s.rate_limited || 0) : "—"),
      el("span", "an-kpi-sub", t("Requests rate limited"))
    );

    const t5xx = el("div", "an-kpi-tile");
    t5xx.append(
      el("span", "an-kpi-label", t("Server Errors (5xx)")),
      el("span", "an-kpi-val", s.calls ? String(s.server_err || 0) : "—"),
      el("span", "an-kpi-sub", t("Upstream server failures"))
    );

    kpis.append(tSuccess, t429, t5xx);
    sec.append(kpis);

    // Charts Grid
    const grid = el("div", "an-charts-grid");

    // Error Rate Ranking
    const errorCard = el("div", "an-chart-card");
    const errorHead = el("div", "an-chart-head");
    errorHead.append(el("span", "an-chart-title", t("Error Rate")));
    errorCard.append(errorHead);
    if (currentDim === "all") {
      const isCardActive = drillChartId === "error_rate" && !drillEntity;
      const card = el("button", "an-single-card" + (isCardActive ? " active" : ""));
      card.type = "button";
      card.dataset.chartId = "error_rate";
      const cancelSub = s.canceled ? ` · ${t("Canceled")}: ${s.canceled}` : "";
      card.append(
        el("span", "an-single-val", s.calls ? t("Error Rate {rate}", { rate: fmtPct(s.error_rate) }) : "—"),
        el("span", "an-single-sub", s.calls ? (t("429: {r} · 5xx: {s} · Other 4xx: {o}", { r: String(s.rate_limited || 0), s: String(s.server_err || 0), o: String(s.other_err || 0) }) + cancelSub) : t("No data in this period"))
      );
      card.onclick = (e) => triggerDrill("error_rate", null, e);
      errorCard.append(card);
    } else {
      const items = data.rankings?.[currentDim]?.by_error_rate || [];
      errorCard.append(renderBarChart("error_rate", items, "error_rate", (item) => {
        const totalErr = (item.rate_limited || 0) + (item.server_err || 0) + (item.other_err || 0);
        if (!totalErr) return [];
        return [
          { cls: "seg-429", share: (item.rate_limited || 0) / totalErr },
          { cls: "seg-5xx", share: (item.server_err || 0) / totalErr },
          { cls: "seg-4xx", share: (item.other_err || 0) / totalErr },
        ];
      }, (item) => {
        return fmtPct(item.error_rate);
      }));
    }
    grid.append(errorCard);

    // Error Trend (Read-only stacked bar chart)
    const trendCard = el("div", "an-chart-card");
    const trendHead = el("div", "an-chart-head");
    trendHead.append(el("span", "an-chart-title", t("Error Trend")));
    trendCard.append(trendHead);
    trendCard.append(renderTrendChart(data.error_trend || []));
    grid.append(trendCard);
    sec.append(grid);
    bodyEl.append(sec);
  }

  // --- Theme 2: Speed ---
  function renderSpeedSection(s) {
    const sec = el("section", "an-theme-section");
    const head = el("div", "an-theme-head");
    head.append(el("span", "an-theme-title", t("Response Speed")));
    sec.append(head);

    const kpis = el("div", "an-kpis");

    if (currentDim === "all") {
      const isTtftActive = drillChartId === "ttft";
      const bTtft = el("button", "an-kpi-tile" + (isTtftActive ? " active" : ""));
      bTtft.type = "button";
      bTtft.dataset.chartId = "ttft";
      bTtft.append(
        el("span", "an-kpi-label", t("End-to-End TTFT P95")),
        el("span", "an-kpi-val", fmtMs(s.ttft_p95)),
        el("span", "an-kpi-sub", s.timed ? t("P50 {p50} · {n} valid samples", { p50: fmtMs(s.ttft_p50), n: String(s.timed) }) : t("No streaming samples"))
      );
      bTtft.onclick = (e) => triggerDrill("ttft", null, e);

      const isSpeedActive = drillChartId === "speed";
      const bSpeed = el("button", "an-kpi-tile" + (isSpeedActive ? " active" : ""));
      bSpeed.type = "button";
      bSpeed.dataset.chartId = "speed";
      const speedSub = s.decode_calls ? t("{n} valid decode samples", { n: String(s.decode_calls) }) : t("No decode samples");
      bSpeed.append(
        el("span", "an-kpi-label", t("TPS")),
        el("span", "an-kpi-val", fmtSpeed(s.speed)),
        el("span", "an-kpi-sub", speedSub)
      );
      bSpeed.onclick = (e) => triggerDrill("speed", null, e);
      kpis.append(bTtft, bSpeed);
      sec.append(kpis);
    } else {
      const tTtft = el("div", "an-kpi-tile");
      tTtft.append(
        el("span", "an-kpi-label", t("End-to-End TTFT P95")),
        el("span", "an-kpi-val", fmtMs(s.ttft_p95)),
        el("span", "an-kpi-sub", s.timed ? t("P50 {p50} · {n} timed samples", { p50: fmtMs(s.ttft_p50), n: String(s.timed) }) : t("No timed samples"))
      );

      const tSpeed = el("div", "an-kpi-tile");
      const speedSub = s.decode_calls ? t("{n} valid decode samples", { n: String(s.decode_calls) }) : t("No decode samples");
      tSpeed.append(
        el("span", "an-kpi-label", t("TPS")),
        el("span", "an-kpi-val", fmtSpeed(s.speed)),
        el("span", "an-kpi-sub", speedSub)
      );

      kpis.append(tTtft, tSpeed);
      sec.append(kpis);

      const grid = el("div", "an-charts-grid");

      // TTFT P95 Ranking (DESC)
      const ttftCard = el("div", "an-chart-card");
      const ttftHead = el("div", "an-chart-head");
      ttftHead.append(el("span", "an-chart-title", t("TTFT")));
      ttftCard.append(ttftHead);
      const ttftItems = data.rankings?.[currentDim]?.by_ttft || [];
      ttftCard.append(renderBarChart("ttft", ttftItems, "ttft_p95", (item) => {
        return [{ cls: "seg-accent", share: 1 }];
      }, (item) => {
        if (item.ttft_p95 === null || item.ttft_p95 === undefined) return "—";
        return fmtMs(item.ttft_p95);
      }));
      grid.append(ttftCard);

      // Speed Ranking
      const speedCard = el("div", "an-chart-card");
      const speedHead = el("div", "an-chart-head");
      speedHead.append(el("span", "an-chart-title", t("TPS")));
      speedCard.append(speedHead);
      const speedItems = data.rankings?.[currentDim]?.by_speed || [];
      speedCard.append(renderBarChart("speed", speedItems, "speed", (item) => {
        return [{ cls: "seg-accent", share: 1 }];
      }, (item) => {
        if (item.speed === null || item.speed === undefined) return "—";
        return fmtSpeed(item.speed);
      }));
      grid.append(speedCard);

      sec.append(grid);
    }

    bodyEl.append(sec);
  }

  // --- Theme 3: Cost & Cache ---
  function renderCostCacheSection(s) {
    const sec = el("section", "an-theme-section");
    const head = el("div", "an-theme-head");
    head.append(el("span", "an-theme-title", t("Cost & Cache")));
    sec.append(head);

    const kpis = el("div", "an-kpis");

    const { val: costDisplay, sub: costSub } = getCostDisplayText(s);
    if (currentDim === "all") {
      const isCostActive = drillChartId === "cost";
      const bCost = el("button", "an-kpi-tile" + (isCostActive ? " active" : ""));
      bCost.type = "button";
      bCost.dataset.chartId = "cost";
      bCost.append(
        el("span", "an-kpi-label", t("Total Cost")),
        el("span", "an-kpi-val", costDisplay),
        el("span", "an-kpi-sub", costSub)
      );
      bCost.onclick = (e) => triggerDrill("cost", null, e);

      const isCacheActive = drillChartId === "cache_hit_rate";
      const bCache = el("button", "an-kpi-tile" + (isCacheActive ? " active" : ""));
      bCache.type = "button";
      bCache.dataset.chartId = "cache_hit_rate";
      bCache.append(
        el("span", "an-kpi-label", t("Cache Hit Rate")),
        el("span", "an-kpi-val", fmtPct(s.cache_hit_rate)),
        el("span", "an-kpi-sub", s.input || s.cache_read ? t("Uncached {in} · Written {w}", { in: fmtTok(s.input), w: fmtTok(s.cache_write) }) : t("No prompt token records"))
      );
      bCache.onclick = (e) => triggerDrill("cache_hit_rate", null, e);

      kpis.append(bCost, bCache);
      sec.append(kpis);
    } else {
      const tCost = el("div", "an-kpi-tile");
      tCost.append(
        el("span", "an-kpi-label", t("Total Cost")),
        el("span", "an-kpi-val", costDisplay),
        el("span", "an-kpi-sub", costSub)
      );

      const tCache = el("div", "an-kpi-tile");
      tCache.append(
        el("span", "an-kpi-label", t("Cache Hit Rate")),
        el("span", "an-kpi-val", fmtPct(s.cache_hit_rate)),
        el("span", "an-kpi-sub", s.input || s.cache_read ? t("Uncached {in} · Written {w}", { in: fmtTok(s.input), w: fmtTok(s.cache_write) }) : t("No prompt token records"))
      );

      kpis.append(tCost, tCache);
      sec.append(kpis);

      const grid = el("div", "an-charts-grid");

      // Cost Ranking (DESC)
      const costCard = el("div", "an-chart-card");
      const costHead = el("div", "an-chart-head");
      costHead.append(el("span", "an-chart-title", t("Cost")));
      costCard.append(costHead);
      const costItems = data.rankings?.[currentDim]?.by_cost || [];
      // Display item.share in tail text
      costCard.append(renderBarChart("cost", costItems, "cost", (item) => {
        return [{ cls: "seg-accent", share: 1 }];
      }, (item) => {
        return fmtCostVal(item.cost);
      }));
      grid.append(costCard);

      // Cache Hit Rate Ranking
      const cacheCard = el("div", "an-chart-card");
      const cacheHead = el("div", "an-chart-head");
      cacheHead.append(el("span", "an-chart-title", t("Cache Hit Rate")));
      cacheCard.append(cacheHead);
      const cacheItems = data.rankings?.[currentDim]?.by_cache_rate || [];
      // Formatted cache rate tail note via i18n
      cacheCard.append(renderBarChart("cache_hit_rate", cacheItems, "cache_hit_rate", (item) => {
        return [{ cls: "seg-green", share: 1 }];
      }, (item) => {
        if (item.cache_hit_rate === null || item.cache_hit_rate === undefined) return "—";
        return fmtPct(item.cache_hit_rate);
      }));
      grid.append(cacheCard);
      sec.append(grid);
    }

    bodyEl.append(sec);
  }
  // Ranking eligibility helper: returns true if entity has sufficient samples and data
  // - Insufficient (<10 calls / <10 timed / <10 decode / <10k cache tokens) is hidden
  // - UnknownCache (prompt >= 10k but read=write=0) is hidden
  // - In cost chart: entities with unpriced models and cost=0/null are hidden.
  //   Partially-priced models with cost>0 remain eligible with approx indicator.
  // - Real 0 metric values with sufficient samples (e.g. error_rate = 0, cost = 0 on known free tier) are preserved.
  function isRankEligible(chartId, item, valKey) {
    if (!item) return false;
    if (item.unknown_cache) return false;
    if (item.insufficient) return false;
    if (chartId === "cost" && item.has_unpriced && (item.cost === 0 || item.cost === null)) {
      return false;
    }
    const val = item[valKey];
    if (val === null || val === undefined || isNaN(val)) return false;
    return true;
  }
  function getEntityTooltip(item, chartId) {
    const lines = [];
    lines.push(item.key);
    const calls = item.calls || 0;
    const errors = (item.rate_limited || 0) + (item.server_err || 0) + (item.other_err || 0);
    const canceled = item.canceled || 0;
    lines.push(`${t("Call count")}: ${calls} (${t("Success")}: ${calls - errors - canceled}, ${t("Errors")}: ${errors}, ${t("Canceled")}: ${canceled})`);
    if (item.error_rate !== null && item.error_rate !== undefined) {
      lines.push(`${t("Error Rate")}: ${fmtPct(item.error_rate)} (429: ${item.rate_limited || 0}, 5xx: ${item.server_err || 0}, 4xx: ${item.other_err || 0})`);
    }
    if (item.ttft_p95 !== null && item.ttft_p95 !== undefined) {
      lines.push(`${t("TTFT")}: P95 ${fmtMs(item.ttft_p95)} · P50 ${fmtMs(item.ttft_p50)}`);
    }
    if (item.speed !== null && item.speed !== undefined) {
      const spdSamples = t("{n} samples", { n: String(item.decode_calls || 0) });
      lines.push(`${t("TPS")}: ${fmtSpeed(item.speed)} (${spdSamples})`);
    }
    const formatted = fmtCost(item);
    let costLine = `${t("Cost")}: ${formatted ? "≈" + formatted : "—"}`;
    if (item.unpriced) {
      costLine += ` (${t("{n} unpriced", { n: item.unpriced })})`;
    }
    lines.push(costLine);
    if (item.input || item.output || item.cache_read || item.cache_write) {
      const tokDetail = t("{in} in, {out} out, {hit} hit, {w} write", {
        in: fmtTok(item.input),
        out: fmtTok(item.output),
        hit: fmtTok(item.cache_read),
        w: fmtTok(item.cache_write),
      });
      lines.push(`${t("Tokens")}: ${tokDetail}`);
    }
    if (item.cache_hit_rate !== null && item.cache_hit_rate !== undefined) {
      lines.push(`${t("Cache Hit Rate")}: ${fmtPct(item.cache_hit_rate)}`);
    }
    return lines.join("\n");
  }

  function resolveEntityItem(rawItem) {
    const dimSummaries = data?.rankings?.[currentDim]?.summaries;
    if (dimSummaries && rawItem?.key && dimSummaries[rawItem.key]) {
      return { ...dimSummaries[rawItem.key], ...rawItem };
    }
    return rawItem;
  }

  // --- Render Ranked Bar Chart ---
  function renderBarChart(chartId, rawItems, valKey, getSegs, getTailText) {
    const wrap = el("div", "an-bars");
    const items = (rawItems || []).map(resolveEntityItem);

    // Filter to only ranking-eligible entities (sufficient samples, non-null metrics, priced/known)
    const eligibleItems = items.filter((it) => isRankEligible(chartId, it, valKey));

    if (!eligibleItems.length) {
      wrap.append(el("div", "an-chart-sub", t("No entities found for this dimension")));
      return wrap;
    }

    // Find max value for normalization across eligibleItems
    let maxVal = 0;
    for (const item of eligibleItems) {
      const v = item[valKey] || 0;
      if (v > maxVal) maxVal = v;
    }
    if (valKey.includes("rate") && maxVal < 1) maxVal = 1;

    for (const item of eligibleItems) {
      const row = el("button", "an-bar-row");
      row.type = "button";
      row.dataset.chartId = chartId;
      row.dataset.dim = currentDim;
      row.dataset.val = item.key;

      const isSelected = drillChartId === chartId && drillEntity?.val === item.key && drillEntity?.dim === currentDim;
      if (isSelected) row.classList.add("active");

      const tooltipText = getEntityTooltip(item, chartId);
      row.title = tooltipText;
      row.setAttribute("aria-label", tooltipText.replace(/\n/g, ", "));

      const nameSpan = el("span", "an-bar-name", item.key);

      const track = el("div", "an-bar-track");
      const segs = getSegs(item);

      // Do not invent proportion for zero/missing values
      const rawVal = item[valKey];
      let totalNorm = 0;
      if (rawVal !== null && rawVal !== undefined && !isNaN(rawVal) && maxVal > 0 && rawVal > 0) {
        totalNorm = Math.min(1, Math.max(0.005, rawVal / maxVal));
      }

      for (const seg of segs) {
        const segEl = el("span", "an-bar-seg " + seg.cls);
        segEl.style.width = (seg.share * totalNorm * 100) + "%";
        track.append(segEl);
      }

      const numSpan = el("span", "an-bar-num", getTailText(item));
      row.append(nameSpan, track, numSpan);

      row.onclick = (e) => {
        triggerDrill(chartId, { dim: currentDim, val: item.key }, e);
      };

      wrap.append(row);
    }

    return wrap;
  }

  // --- Render Error Trend Chart (Read-Only) ---
  function renderTrendChart(trend) {
    const wrap = el("div", "an-trend-chart");
    if (!trend.length) {
      wrap.append(el("div", "an-chart-sub", t("No error records in this period")));
      return wrap;
    }

    let maxCount = 0;
    for (const b of trend) {
      const sum = (b.rate_limited || 0) + (b.server_err || 0) + (b.other_err || 0);
      if (sum > maxCount) maxCount = sum;
    }
    if (maxCount === 0) maxCount = 1;

    const bars = el("div", "an-trend-bars");
    for (const b of trend) {
      const col = el("div", "an-trend-col");
      const c429 = b.rate_limited || 0;
      const c5xx = b.server_err || 0;
      const c4xx = b.other_err || 0;

      if (c4xx > 0) {
        const i4xx = el("i", "c-4xx");
        i4xx.style.height = ((c4xx / maxCount) * 100) + "%";
        col.append(i4xx);
      }
      if (c5xx > 0) {
        const i5xx = el("i", "c-5xx");
        i5xx.style.height = ((c5xx / maxCount) * 100) + "%";
        col.append(i5xx);
      }
      if (c429 > 0) {
        const i429 = el("i", "c-429");
        i429.style.height = ((c429 / maxCount) * 100) + "%";
        col.append(i429);
      }

      const otherLabel = t("Other 4xx");
      const fullTimeStr = b.time ? fmtDateTime(b.time) : (b.label || "—");
      const tt = `${fullTimeStr}: 429=${c429}, 5xx=${c5xx}, ${otherLabel}=${c4xx}`;
      col.title = tt;
      col.setAttribute("aria-label", tt);
      col.tabIndex = 0;
      bars.append(col);
    }
    wrap.append(bars);

    // Labels
    const labels = el("div", "an-trend-labels");
    const count = trend.length;
    for (let i = 0; i < count; i++) {
      const sp = el("span", "");
      if (i === 0 || i === Math.floor(count / 2) || i === count - 1) {
        sp.textContent = trend[i].label || fmtTime(trend[i].time);
      }
      labels.append(sp);
    }
    wrap.append(labels);

    // Legend
    const legend = el("div", "an-trend-legend");
    legend.innerHTML = `
      <span><i class="an-legend-dot" style="background:var(--amber)"></i>429</span>
      <span><i class="an-legend-dot" style="background:var(--red)"></i>5xx</span>
      <span><i class="an-legend-dot" style="background:var(--muted)"></i>${t("Other 4xx")}</span>
    `;
    wrap.append(legend);

    return wrap;
  }

  // --- Render Independent Drill Page ---
  function renderDrillPage() {
    const chartTitles = {
      "error_rate": t("Error Rate"),
      "ttft": t("TTFT"),
      "speed": t("TPS"),
      "cost": t("Cost"),
      "cache_hit_rate": t("Cache Hit Rate"),
    };

    // Header: Title & chips
    drillHeadEl.replaceChildren();

    const titleWrap = el("div", "an-drill-title-wrap");
    titleWrap.append(el("span", "an-drill-page-title", chartTitles[drillChartId] || t("Call Details")));

    const chips = el("div", "an-drill-page-chips");
    const pLabel = PERIOD_LABELS[currentPeriod] || currentPeriod;
    chips.append(el("span", "an-drill-page-chip", t(pLabel)));
    if (filters.model) chips.append(el("span", "an-drill-page-chip", `${t("Model")}: ${filters.model}`));
    if (filters.provider) chips.append(el("span", "an-drill-page-chip", `${t("Provider")}: ${filters.provider}`));
    if (filters.agent) chips.append(el("span", "an-drill-page-chip", `${t("Agent")}: ${filters.agent}`));
    if (drillEntity) {
      chips.append(el("span", "an-drill-page-chip chip-entity", `${t(drillEntity.dim)}: ${drillEntity.val}`));
    }
    titleWrap.append(chips);

    drillHeadEl.append(titleWrap);
    // Left compact chart
    renderDrillLeft();

    // Right pane: list or detail
    renderDrillRight();
  }

  function renderDrillLeft() {
    drillLeftEl.replaceChildren();

    const chartNames = {
      "error_rate": t("Error Rate"),
      "ttft": t("TTFT"),
      "speed": t("TPS"),
      "cost": t("Cost"),
      "cache_hit_rate": t("Cache Hit Rate"),
    };

    const head = el("div", "an-drill-left-head");
    const titleSpan = el("span", "an-drill-left-title", chartNames[drillChartId] || t("Chart Scope"));
    titleSpan.title = currentDim === "all" ? t("Overall Metric") : `${t("Dimension")}: ${t(currentDim)}`;
    head.append(titleSpan);
    drillLeftEl.append(head);

    const s = data?.summary || {};

    if (currentDim === "all") {
      // Overall single metric card
      const kpi = el("div", "an-drill-left-kpi");
      if (drillChartId === "error_rate") {
        const cancelSub = s.canceled ? ` · ${t("Canceled")}: ${s.canceled}` : "";
        kpi.append(
          el("span", "an-drill-left-kpi-val", s.calls ? fmtPct(s.error_rate) : "—"),
          el("span", "an-drill-left-kpi-sub", s.calls ? (t("429: {r} · 5xx: {s} · Other 4xx: {o}", { r: String(s.rate_limited || 0), s: String(s.server_err || 0), o: String(s.other_err || 0) }) + cancelSub) : t("No data in this period"))
        );
      } else if (drillChartId === "ttft") {
        kpi.append(
          el("span", "an-drill-left-kpi-val", fmtMs(s.ttft_p95)),
          el("span", "an-drill-left-kpi-sub", s.timed ? t("P50 {p50} · {n} valid samples", { p50: fmtMs(s.ttft_p50), n: String(s.timed) }) : t("No streaming samples"))
        );
      } else if (drillChartId === "speed") {
        const speedSub = s.decode_calls ? t("{n} valid decode samples", { n: String(s.decode_calls) }) : t("No decode samples");
        kpi.append(
          el("span", "an-drill-left-kpi-val", fmtSpeed(s.speed)),
          el("span", "an-drill-left-kpi-sub", speedSub)
        );
      } else if (drillChartId === "cost") {
        const { val: costValStr, sub: costSubStr } = getCostDisplayText(s);
        kpi.append(
          el("span", "an-drill-left-kpi-val", costValStr),
          el("span", "an-drill-left-kpi-sub", costSubStr)
        );
      } else if (drillChartId === "cache_hit_rate") {
        kpi.append(
          el("span", "an-drill-left-kpi-val", fmtPct(s.cache_hit_rate)),
          el("span", "an-drill-left-kpi-sub", s.input || s.cache_read ? t("Uncached {in} · Written {w}", { in: fmtTok(s.input), w: fmtTok(s.cache_write) }) : t("No prompt token records"))
        );
      }
      drillLeftEl.append(kpi);
      return;
    }

    // In dimensional rankings: show list of entities, clickable to switch active drill entity
    const rankingProps = {
      "error_rate": { list: data?.rankings?.[currentDim]?.by_error_rate, key: "error_rate", fmt: (it) => fmtPct(it.error_rate) },
      "ttft": { list: data?.rankings?.[currentDim]?.by_ttft, key: "ttft_p95", fmt: (it) => fmtMs(it.ttft_p95) },
      "speed": { list: data?.rankings?.[currentDim]?.by_speed, key: "speed", fmt: (it) => fmtSpeed(it.speed) },
      "cost": { list: data?.rankings?.[currentDim]?.by_cost, key: "cost", fmt: (it) => fmtCostVal(it.cost) },
      "cache_hit_rate": { list: data?.rankings?.[currentDim]?.by_cache_rate, key: "cache_hit_rate", fmt: (it) => fmtPct(it.cache_hit_rate) },
    };
    const cfg = rankingProps[drillChartId] || rankingProps["error_rate"];
    let valKey = cfg.key;
    let fmtVal = cfg.fmt;
    let items = (cfg.list || []).map(resolveEntityItem);
    items = items.filter((it) => isRankEligible(drillChartId, it, valKey));
    // Button to select "All"
    const allBtn = el("button", "an-drill-left-all-btn" + (!drillEntity ? " active" : ""));
    allBtn.type = "button";
    allBtn.append(
      el("span", "", t("All Entities")),
      el("span", "", String(items.length))
    );
    allBtn.onclick = () => {
      if (!drillEntity) return;
      resetInlineRoute();
      drillEntity = null;
      renderDrillPage();
      fetchDrillCalls(drillChartId, null);
    };
    drillLeftEl.append(allBtn);

    const itemsBox = el("div", "an-drill-left-items");

    let maxVal = 0;
    for (const it of items) {
      const v = it[valKey] || 0;
      if (v > maxVal) maxVal = v;
    }
    if (valKey.includes("rate") && maxVal < 1) maxVal = 1;

    for (const item of items) {
      const isSelected = drillEntity?.val === item.key && drillEntity?.dim === currentDim;
      const row = el("button", "an-drill-mini-row" + (isSelected ? " active" : ""));
      row.type = "button";
      row.dataset.key = item.key;
      const tooltipText = getEntityTooltip(item, drillChartId);
      row.title = tooltipText;
      row.setAttribute("aria-label", tooltipText.replace(/\n/g, ", "));

      const name = el("span", "an-drill-mini-name", item.key);
      const num = el("span", "an-drill-mini-num", fmtVal(item));

      const track = el("div", "an-drill-mini-track");
      const seg = el("div", "an-drill-mini-seg");
      const raw = item[valKey] || 0;
      const pct = maxVal > 0 && raw > 0 ? Math.min(100, Math.max(2, (raw / maxVal) * 100)) : 0;
      seg.style.width = pct + "%";
      track.append(seg);

      row.append(name, num, track);
      row.onclick = (e) => {
        if (drillEntity?.val === item.key && drillEntity?.dim === currentDim) return;
        if (e) scrollOnPurpose(e, 500);
        resetInlineRoute();
        drillEntity = { dim: currentDim, val: item.key };
        renderDrillPage();
        fetchDrillCalls(drillChartId, drillEntity);
      };

      itemsBox.append(row);
    }
    drillLeftEl.append(itemsBox);
  }

  function renderDrillRight() {
    // a re-render (locale, costs) keeps the inline stage where it is: the
    // Routing view redraws its own words on a language change
    if (drillRoute && !drillRouteError) {
      const live = drillRightEl.querySelector(".an-drill-routing");
      if (live && live.dataset.routeId === String(drillRoute.id)) return;
    }
    const keepIndex = drillRouteIndex;
    const keepScroll = page.scrollTop || 0;
    drillRightEl.replaceChildren();

    if (drillLoading) {
      drillRightEl.setAttribute("aria-busy", "true");
    } else {
      drillRightEl.removeAttribute("aria-busy");
    }


    // The routing drill: the same stage and story the Routing view plays,
    // mounted here in place of the call list, the view never leaving Analytics
    if (drillRoute) {
      if (drillRouteError) {
        const errBox = el("div", "an-drill-state-box an-err");
        const backErr = el("button", "an-drill-route-back", t("Back to analytics calls"));
        backErr.type = "button";
        backErr.onclick = (e) => closeDrillRoute(e);
        const retryBtn = el("button", "an-drill-retry-btn", t("Retry"));
        retryBtn.type = "button";
        retryBtn.onclick = () => { drillRouteError = null; renderDrillRight(); };
        errBox.append(el("span", "", drillRouteError), retryBtn, backErr);
        drillRightEl.append(errBox);
        return;
      }
      const host = el("div", "an-drill-routing");
      host.id = "anDrillRouting";
      host.dataset.routeId = drillRoute.id;
      drillRightEl.append(host);
      drillRightEl.setAttribute("aria-busy", "true");
      const token = routeReqToken;
      const myRouteId = drillRoute.id;
      const isCurrent = () =>
        routeReqToken === token &&
        !!drillRoute && drillRoute.id === myRouteId &&
        drillMode && !page.hidden;
      const mount = window.mountRoutingInline;
      const settle = () => {
        if (routeReqToken !== token) return;
        if (routePendingId === myRouteId) routePendingId = null;
        drillRightEl.removeAttribute("aria-busy");
      };
      if (mount) {
        Promise.resolve(mount(host, drillRoute.id, drillRoute.t, {
          isCurrent,
          onReturn: (e) => closeDrillRoute(e),
        })).then(settle).catch((err) => {
          if (!isCurrent()) { settle(); return; }
          drillRouteError = err?.message || t("Routing history for this request is no longer available.");
          renderDrillRight();
        });
      } else {
        settle();
      }
      return;
    }

    // Calls list view
    const head = el("div", "an-drill-right-head");
    const drillTitle = drillChartId === "cache_hit_rate"
      ? t("Largest Uncached Input Calls")
      : drillChartId === "cost"
      ? t("Highest Cost Calls")
      : drillChartId === "speed"
      ? t("Slowest Decode Calls")
      : drillChartId === "ttft"
      ? t("Slowest TTFT Calls")
      : t("Top 50 Calls");
    head.append(
      el("span", "an-drill-right-title", drillTitle),
      el("span", "an-drill-right-meta", drillCalls ? `${drillCalls.length} ${t("Calls")}` : "")
    );
    drillRightEl.append(head);
    if (drillLoading) {
      const loadingBox = el("div", "an-drill-state-box");
      loadingBox.setAttribute("role", "status");
      loadingBox.setAttribute("aria-live", "polite");

      const spinner = el("span", "an-spinner");
      spinner.setAttribute("aria-hidden", "true");

      loadingBox.append(spinner, el("span", "", t("Loading calls…")));
      drillRightEl.append(loadingBox);
      return;
    }

    if (drillError) {
      const errBox = el("div", "an-drill-state-box an-err");
      errBox.append(
        el("span", "", `${t("Failed to load calls")}: ${drillError}`),
        (() => {
          const retryBtn = el("button", "an-drill-retry-btn", t("Retry"));
          retryBtn.type = "button";
          retryBtn.onclick = () => fetchDrillCalls(drillChartId, drillEntity);
          return retryBtn;
        })()
      );
      drillRightEl.append(errBox);
      return;
    }

    const calls = drillCalls || [];
    if (!calls.length) {
      const emptyBox = el("div", "an-drill-state-box");
      emptyBox.append(el("span", "", t("No matching calls found")));
      drillRightEl.append(emptyBox);
      return;
    }
    const list = el("div", "an-calls-list");
    for (let i = 0; i < calls.length; i++) {
      const c = calls[i];
      const hasRoute = Boolean(c.route_id);
      const item = el("button", "an-call-item" + (!hasRoute ? " disabled" : ""));
      item.type = "button";
      item.dataset.index = String(i);
      if (!hasRoute) {
        item.disabled = true;
        item.setAttribute("aria-disabled", "true");
        item.title = t("This call has no linked routing record.");
      }

      let stCls = "st-ok";
      if (c.err) {
        stCls = c.status >= 500 ? "st-5xx" : (c.status === 429 ? "st-429" : (c.status === 499 ? "st-499" : "st-err"));
      } else if (c.status === 429) stCls = "st-429";
      else if (c.status === 499) stCls = "st-499";
      else if (c.status >= 500) stCls = "st-5xx";
      else if (c.status >= 400) stCls = "st-err";
      const top = el("div", "an-call-item-top");
      top.append(
        el("span", "an-call-item-time", fmtTime(c.t)),
        el("span", "an-st-badge " + stCls, String(c.status)),
        el("span", "an-call-item-agent", c.agent || "—"),
        el("span", "an-call-item-model", `${c.model || "—"} · ${c.provider || "—"}`)
      );

      let tokStr = t("{in} in · {out} out", { in: fmtTok(c.in), out: fmtTok(c.out) });
      if (c.cache_read) tokStr += " " + t("({hit} hit)", { hit: fmtTok(c.cache_read) });

      const bottom = el("div", "an-call-item-bottom");
      bottom.append(
        el("span", "", `${t("Duration")}: ${fmtMs(c.ms)}`),
        el("span", "", `${t("TTFT")}: ${fmtMs(c.ttft_ms)}`)
      );
      if (c.speed !== undefined && c.speed !== null) {
        bottom.append(el("span", "", `${t("TPS")}: ${fmtSpeed(c.speed)}`));
      }
      if (c.cost !== undefined && c.cost !== null) {
        bottom.append(el("span", "", `${t("Cost")}: ${fmtCostVal(c.cost)}`));
      }
      bottom.append(
        el("span", "", tokStr),
        el("span", "", `${c.session || "—"}${c.kind ? " · " + c.kind : ""}`)
      );
      item.append(top, bottom);

      if (hasRoute) {
        item.onclick = () => {
          if (routePendingId === c.route_id) return;
          if (drillRoute && drillRoute.id === c.route_id) return;
          if (drillRoute && window.unmountRoutingInline) window.unmountRoutingInline();
          routePendingId = c.route_id;
          routeReqToken++;
          drillSavedScroll = page.scrollTop || 0;
          drillRouteIndex = i;
          drillRouteError = null;
          drillRoute = { id: c.route_id, t: c.t };
          renderDrillRight();
        };
      }

      list.append(item);
    }

    drillRightEl.append(list);
    if (keepIndex != null) {
      const row = list.querySelector(`.an-call-item[data-index="${keepIndex}"]`);
      if (row) row.classList.add("active");
    }
    requestAnimationFrame(() => {
      if (!drillMode || page.hidden) return;
      if (keepScroll && Math.abs((page.scrollTop || 0) - keepScroll) > 1) page.scrollTop = keepScroll;
    });
  }



  // Export module lifecycle
  async function load(initialPeriod, restore = false) {
    if (restore) {
      syncBackBtn();
      render();
      return;
    }
    if (drillMode) exitDrill();
    if (initialPeriod && initialPeriod !== currentPeriod) {
      currentPeriod = initialPeriod;
    }
    await fetchAnalytics();
  }
  // Invalidate any in-flight requests and close drilldown if navigating away from Analytics
  const observer = new MutationObserver(() => {
    if (page.hidden) {
      loadSeq++;
      pendingFetch = false;
      page.classList.remove("loading");
      page.removeAttribute("aria-busy");
      drillSeq++;
      routeReqToken++;
      routePendingId = null;
      drillPendingKey = null;
      if (drillMode) exitDrill();
    }
  });
  observer.observe(page, { attributes: true, attributeFilter: ["hidden"] });

  window.loadAnalytics = load;
  window.renderAnalytics = () => {
    if (!page.hidden) render();
  };
  document.addEventListener("magpie-locale-changed", () => {
    syncBackBtn();
    if (!page.hidden) render();
  });
  document.addEventListener("magpie-costs-changed", () => {
    if (!page.hidden) render();
  });
  // If page was opened directly via ?view=analytics before analytics.js executed
  if (!page.hidden) {
    const p = typeof period === "string" ? period : "30d";
    load(p);
  }
})();
