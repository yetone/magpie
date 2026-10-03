// Analytics View Controller (Issue #213)
// Quality & Analytics dashboard with independent drilldown page view (left compact chart, right calls list & detail).
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
  let loadSeq = 0;           // ignore stale async responses
  let pendingFetch = false;  // true while GET /api/analytics is in flight
  let dashboardScrollTop = 0;// saved scrollTop when entering drill page

  // Independent Drill Page state
  let drillMode = false;     // true when on drill page
  let drillChartId = null;   // "1.1" | "2.1" | "2.2" | "3.1" | "3.2" | null
  let drillEntity = null;    // { dim: "model"|"provider"|"agent", val: string } | null
  let drillCalls = null;     // []Record from GET /api/analytics/calls
  let drillSeq = 0;          // ignore stale call responses
  let drillLoading = false;
  let drillError = null;     // error message or null
  let activeCall = null;     // selected call record or null (detail mode)
  let callsScrollTop = 0;    // saved scroll position of calls list
  let activeCallIndex = null;// index of selected call item
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
  if (backBtn) {
    backBtn.onclick = () => {
      if (typeof show === "function") show("usage");
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
    if (usd === 0) return "$0.00";
    if (usd < 0.01) return "<$0.01";
    return "$" + usd.toFixed(2);
  }

  function fmtTok(n) {
    if (n === null || n === undefined || isNaN(n)) return "0";
    if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
    if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
    return String(n);
  }

  function fmtTime(iso) {
    if (!iso) return "—";
    try {
      const d = new Date(iso);
      return d.toLocaleTimeString([], { hour12: false });
    } catch {
      return iso;
    }
  }

  function fmtDateTime(iso) {
    if (!iso) return "—";
    try {
      const d = new Date(iso);
      return new Intl.DateTimeFormat(undefined, {
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

  function getStatusExplanation(code, err) {
    if (err) return err;
    if (code === 200) return t("Request succeeded");
    if (code === 429) return t("Rate limited by provider or quota exceeded");
    if (code === 499) return t("Client closed connection");
    if (code >= 500) return t("Upstream server or provider failure");
    if (code === 400) return t("Invalid request parameters or payload");
    if (code === 401 || code === 403) return t("Authentication or permissions failed");
    if (code === 404) return t("Model or endpoint not found");
    return t("HTTP error {code}", { code: String(code) });
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
      pendingFetch = false;
      render();
    } catch (e) {
      if (seq !== loadSeq) return;
      pendingFetch = false;
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
    if (e && typeof scrollOnPurpose === "function") scrollOnPurpose(e, 800);

    drillMode = true;
    drillChartId = chartId;
    drillEntity = entity;
    activeCall = null;
    activeCallIndex = null;
    callsScrollTop = 0;

    // Save dashboard scroll position before hiding
    dashboardScrollTop = page.scrollTop || 0;

    // Switch view sections
    controlsEl.hidden = true;
    bodyEl.hidden = true;
    drillPageEl.hidden = false;

    // Reset view scroll to top on entering drill page
    page.scrollTop = 0;

    renderDrillPage();
    fetchDrillCalls(chartId, entity);
  }

  // Return from drill page to main analytics dashboard
  function exitDrill(e) {
    if (e && typeof scrollOnPurpose === "function") scrollOnPurpose(e, 800);
    drillSeq++; // Cancel any in-flight call requests
    drillMode = false;
    drillLoading = false;
    drillCalls = null;
    drillError = null;
    activeCall = null;
    activeCallIndex = null;

    // Switch view sections
    drillPageEl.hidden = true;
    controlsEl.hidden = false;
    bodyEl.hidden = false;

    // Restore dashboard scroll position
    requestAnimationFrame(() => {
      if (drillMode || page.hidden) return;
      if (e && typeof scrollOnPurpose === "function") scrollOnPurpose(e, 500);
      page.scrollTop = dashboardScrollTop;
    });
  }

  // Fetch /api/analytics/calls for drill-down
  async function fetchDrillCalls(chartId, entity) {
    const seq = ++drillSeq;
    drillLoading = true;
    drillCalls = null;
    drillError = null;

    renderDrillRight();

    const params = new URLSearchParams();
    params.set("period", currentPeriod);
    params.set("chart_id", chartId);
    params.set("limit", "50");

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
    if (m) params.set("model", m);
    if (p) params.set("provider", p);
    if (a) params.set("agent", a);

    try {
      const res = await api("analytics/calls?" + params.toString());
      if (seq !== drillSeq) return; // Stale request, ignore
      drillCalls = res?.calls || [];
      drillLoading = false;
      drillError = null;
      renderDrillRight();
    } catch (e) {
      if (seq !== drillSeq) return;
      drillCalls = null;
      drillLoading = false;
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
    const noData = !summary.calls;

    // 1. Theme 1: Reliability
    renderReliabilitySection(summary, noData);

    // 2. Theme 2: Speed
    renderSpeedSection(summary, noData);

    // 3. Theme 3: Cost & Cache
    renderCostCacheSection(summary, noData);

    if (drillMode) {
      renderDrillPage();
    }
  }

  function renderControls() {
    // Period segments
    periodSeg.replaceChildren();
    for (const [id, label] of PERIOD_LIST) {
      const b = el("button", "opt" + (id === currentPeriod ? " on" : ""), t(label));
      b.onclick = () => {
        if (id === currentPeriod) return;
        currentPeriod = id;
        if (drillMode) exitDrill();
        fetchAnalytics();
      };
      periodSeg.append(b);
    }
    if (typeof slide === "function") slide(periodSeg, "an-period");

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
          render();
        }
      };
      dimSeg.append(b);
    }
    if (typeof slide === "function") slide(dimSeg, "an-dim");

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

    for (const fdim of filterDims) {
      const curVal = filters[fdim];
      const wrap = el("div", "an-filter-wrap" + (curVal ? " has-val" : ""));
      wrap.dataset.filterDim = fdim;
      const btn = el("button", "an-filter-btn" + (curVal ? " set" : ""));
      btn.type = "button";
      btn.dataset.dim = fdim;
      btn.setAttribute("aria-haspopup", "true");
      const displayLabel = curVal ? `${dimNames[fdim]}: ${curVal}` : t("All {name}", { name: dimNames[fdim] });
      btn.append(el("span", "an-filter-label", displayLabel), svg("M4 6l4 4 4-4", 10, 1.6));

      btn.onclick = (e) => {
        const options = [{ value: "", label: t("All {name}", { name: dimNames[fdim] }) }];
        for (const item of availFilters[fdim] || []) {
          options.push({ value: item, label: item });
        }
        openFilterPicker(btn, fdim, options, curVal, e, (newVal) => {
          if (filters[fdim] === newVal) return;
          filters[fdim] = newVal;
          if (drillMode) exitDrill();
          fetchAnalytics();
        });
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
  function renderReliabilitySection(s, noData) {
    const sec = el("section", "an-theme-section");
    const head = el("div", "an-theme-head");
    head.append(el("span", "an-theme-title", t("Reliability")));
    sec.append(head);

    // KPI Tiles
    const kpis = el("div", "an-kpis");

    const tSuccess = el("div", "an-kpi-tile");
    tSuccess.append(
      el("span", "an-kpi-label", t("Success Rate")),
      el("span", "an-kpi-val", fmtPct(s.success_rate)),
      el("span", "an-kpi-sub", s.calls ? t("{n} total calls", { n: String(s.calls) }) : t("No data in this period"))
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

    // Charts Grid (1.1 and 1.2)
    const grid = el("div", "an-charts-grid");

    // 1.1 Error Rate Ranking
    const c11 = el("div", "an-chart-card");
    const h11 = el("div", "an-chart-head");
    h11.append(el("span", "an-chart-title", t("Error Rate")));
    c11.append(h11);

    if (currentDim === "all") {
      const isCardActive = drillChartId === "1.1" && !drillEntity;
      const card = el("button", "an-single-card" + (isCardActive ? " active" : ""));
      card.type = "button";
      card.dataset.chartId = "1.1";
      card.append(
        el("span", "an-single-val", s.calls ? t("Error Rate {rate}", { rate: fmtPct(s.error_rate) }) : "—"),
        el("span", "an-single-sub", s.calls ? t("429: {r} · 5xx: {s} · Other 4xx: {o}", { r: String(s.rate_limited || 0), s: String(s.server_err || 0), o: String(s.other_err || 0) }) : t("No data in this period"))
      );
      card.onclick = (e) => triggerDrill("1.1", null, e);
      c11.append(card);
    } else {
      const items = data.rankings?.[currentDim]?.by_error_rate || [];
      // Issue review item #2: seg shares sum to 1 across errors, so total bar width = error_rate
      c11.append(renderBarChart("1.1", items, "error_rate", (item) => {
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
    grid.append(c11);

    // 1.2 Error Trend (Read-only stacked bar chart)
    const c12 = el("div", "an-chart-card");
    const h12 = el("div", "an-chart-head");
    h12.append(el("span", "an-chart-title", t("Error Trend")));
    c12.append(h12);
    c12.append(renderTrendChart(data.error_trend || []));
    grid.append(c12);

    sec.append(grid);
    bodyEl.append(sec);
  }

  // --- Theme 2: Speed ---
  function renderSpeedSection(s, noData) {
    const sec = el("section", "an-theme-section");
    const head = el("div", "an-theme-head");
    head.append(el("span", "an-theme-title", t("Response Speed")));
    sec.append(head);

    const kpis = el("div", "an-kpis");

    if (currentDim === "all") {
      const isTtftActive = drillChartId === "2.1";
      const bTtft = el("button", "an-kpi-tile" + (isTtftActive ? " active" : ""));
      bTtft.type = "button";
      bTtft.dataset.chartId = "2.1";
      bTtft.append(
        el("span", "an-kpi-label", t("End-to-End TTFT P95")),
        el("span", "an-kpi-val", fmtMs(s.ttft_p95)),
        el("span", "an-kpi-sub", s.timed ? t("P50 {p50} · {n} valid samples", { p50: fmtMs(s.ttft_p50), n: String(s.timed) }) : t("No streaming samples"))
      );
      bTtft.onclick = (e) => triggerDrill("2.1", null, e);

      const isSpeedActive = drillChartId === "2.2";
      const bSpeed = el("button", "an-kpi-tile" + (isSpeedActive ? " active" : ""));
      bSpeed.type = "button";
      bSpeed.dataset.chartId = "2.2";
      let speedSub = s.decode_calls ? t("{n} valid decode samples", { n: String(s.decode_calls) }) : t("No decode samples");
      if (s.excluded_decode_calls) {
        speedSub = s.decode_calls
          ? t("{n} valid decode samples ({x} excluded)", { n: String(s.decode_calls), x: String(s.excluded_decode_calls) })
          : t("No decode samples ({x} excluded)", { x: String(s.excluded_decode_calls) });
      }
      bSpeed.append(
        el("span", "an-kpi-label", t("TPS")),
        el("span", "an-kpi-val", fmtSpeed(s.speed)),
        el("span", "an-kpi-sub", speedSub)
      );
      bSpeed.onclick = (e) => triggerDrill("2.2", null, e);

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
      let speedSub = s.decode_calls ? t("{n} valid decode samples", { n: String(s.decode_calls) }) : t("No decode samples");
      if (s.excluded_decode_calls) {
        speedSub = s.decode_calls
          ? t("{n} valid decode samples ({x} excluded)", { n: String(s.decode_calls), x: String(s.excluded_decode_calls) })
          : t("No decode samples ({x} excluded)", { x: String(s.excluded_decode_calls) });
      }
      tSpeed.append(
        el("span", "an-kpi-label", t("TPS")),
        el("span", "an-kpi-val", fmtSpeed(s.speed)),
        el("span", "an-kpi-sub", speedSub)
      );

      kpis.append(tTtft, tSpeed);
      sec.append(kpis);

      const grid = el("div", "an-charts-grid");

      // 2.1 TTFT P95 Ranking (DESC)
      const c21 = el("div", "an-chart-card");
      const h21 = el("div", "an-chart-head");
      h21.append(el("span", "an-chart-title", t("TTFT")));
      c21.append(h21);
      const ttftItems = data.rankings?.[currentDim]?.by_ttft || [];
      c21.append(renderBarChart("2.1", ttftItems, "ttft_p95", (item) => {
        return [{ cls: "seg-accent", share: 1 }];
      }, (item) => {
        if (item.ttft_p95 === null || item.ttft_p95 === undefined) return "—";
        return fmtMs(item.ttft_p95);
      }));
      grid.append(c21);

      // 2.2 Decode Speed Ranking (ASC)
      const c22 = el("div", "an-chart-card");
      const h22 = el("div", "an-chart-head");
      h22.append(el("span", "an-chart-title", t("TPS")));
      c22.append(h22);
      const speedItems = data.rankings?.[currentDim]?.by_speed || [];
      c22.append(renderBarChart("2.2", speedItems, "speed", (item) => {
        return [{ cls: "seg-accent", share: 1 }];
      }, (item) => {
        if (item.speed === null || item.speed === undefined) return "—";
        return fmtSpeed(item.speed);
      }));
      grid.append(c22);

      sec.append(grid);
    }

    bodyEl.append(sec);
  }

  // --- Theme 3: Cost & Cache ---
  function renderCostCacheSection(s, noData) {
    const sec = el("section", "an-theme-section");
    const head = el("div", "an-theme-head");
    head.append(el("span", "an-theme-title", t("Cost & Cache")));
    sec.append(head);

    const kpis = el("div", "an-kpis");

    // Issue review item #6: Distinguish empty period, fully unpriced, and known zero cost
    let costDisplay = "—";
    let costSub = t("No data in this period");
    if (s.calls) {
      if (!s.cost && s.unpriced) {
        costDisplay = t("Unknown cost");
        costSub = t("No price for these models");
      } else {
        costDisplay = fmtCostVal(s.cost);
        costSub = s.unpriced
          ? t("≈{cost} ({n} unpriced)", { cost: fmtCostVal(s.cost), n: String(s.unpriced) })
          : t("At model list price");
      }
    }
    if (currentDim === "all") {
      const isCostActive = drillChartId === "3.1";
      const bCost = el("button", "an-kpi-tile" + (isCostActive ? " active" : ""));
      bCost.type = "button";
      bCost.dataset.chartId = "3.1";
      bCost.append(
        el("span", "an-kpi-label", t("Total Cost")),
        el("span", "an-kpi-val", costDisplay),
        el("span", "an-kpi-sub", costSub)
      );
      bCost.onclick = (e) => triggerDrill("3.1", null, e);

      const isCacheActive = drillChartId === "3.2";
      const bCache = el("button", "an-kpi-tile" + (isCacheActive ? " active" : ""));
      bCache.type = "button";
      bCache.dataset.chartId = "3.2";
      bCache.append(
        el("span", "an-kpi-label", t("Cache Hit Rate")),
        el("span", "an-kpi-val", fmtPct(s.cache_hit_rate)),
        el("span", "an-kpi-sub", s.input || s.cache_read ? t("Uncached {in} · Written {w}", { in: fmtTok(s.input), w: fmtTok(s.cache_write) }) : t("No prompt token records"))
      );
      bCache.onclick = (e) => triggerDrill("3.2", null, e);

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

      // 3.1 Cost Ranking (DESC)
      const c31 = el("div", "an-chart-card");
      const h31 = el("div", "an-chart-head");
      h31.append(el("span", "an-chart-title", t("Cost")));
      c31.append(h31);
      const costItems = data.rankings?.[currentDim]?.by_cost || [];
      // Issue review item #8: Display item.share in tail text
      c31.append(renderBarChart("3.1", costItems, "cost", (item) => {
        return [{ cls: "seg-accent", share: 1 }];
      }, (item) => {
        return fmtCostVal(item.cost);
      }));
      grid.append(c31);

      // 3.2 Cache Hit Rate Ranking (ASC)
      const c32 = el("div", "an-chart-card");
      const h32 = el("div", "an-chart-head");
      h32.append(el("span", "an-chart-title", t("Cache Hit Rate")));
      c32.append(h32);
      const cacheItems = data.rankings?.[currentDim]?.by_cache_rate || [];
      // Issue review item #7: formatted cache rate tail note via i18n
      c32.append(renderBarChart("3.2", cacheItems, "cache_hit_rate", (item) => {
        return [{ cls: "seg-green", share: 1 }];
      }, (item) => {
        if (item.cache_hit_rate === null || item.cache_hit_rate === undefined) return "—";
        return fmtPct(item.cache_hit_rate);
      }));
      grid.append(c32);

      sec.append(grid);
    }

    bodyEl.append(sec);
  }
  // Ranking eligibility helper: returns true if entity has sufficient samples and data
  // - Insufficient (<10 calls / <10 timed / <10 decode / <10k cache tokens) is hidden
  // - UnknownCache (prompt >= 10k but read=write=0) is hidden
  // - In cost chart (3.1): entities with unpriced models and cost=0/null are hidden.
  //   Partially-priced models with cost>0 remain eligible with approx indicator.
  // - Real 0 metric values with sufficient samples (e.g. error_rate = 0, cost = 0 on known free tier) are preserved.
  function isRankEligible(chartId, item, valKey) {
    if (!item) return false;
    if (item.unknown_cache) return false;
    if (item.insufficient) return false;
    if (chartId === "3.1" && item.has_unpriced && (item.cost === 0 || item.cost === null)) {
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
    lines.push(`${t("Calls")}: ${calls} (${t("Success")}: ${calls - errors - canceled}, ${t("Errors")}: ${errors}, ${t("Canceled")}: ${canceled})`);
    if (item.error_rate !== null && item.error_rate !== undefined) {
      lines.push(`${t("Error Rate")}: ${fmtPct(item.error_rate)} (429: ${item.rate_limited || 0}, 5xx: ${item.server_err || 0}, 4xx: ${item.other_err || 0})`);
    }
    if (item.ttft_p95 !== null && item.ttft_p95 !== undefined) {
      lines.push(`${t("TTFT")}: P95 ${fmtMs(item.ttft_p95)} · P50 ${fmtMs(item.ttft_p50)}`);
    }
    if (item.speed !== null && item.speed !== undefined) {
      let spdSamples = t("{n} samples", { n: String(item.decode_calls || 0) });
      if (item.excluded_decode_calls) {
        spdSamples = t("{n} valid, {x} excluded", { n: String(item.decode_calls || 0), x: String(item.excluded_decode_calls) });
      }
      lines.push(`${t("TPS")}: ${fmtSpeed(item.speed)} (${spdSamples})`);
    } else if (item.excluded_decode_calls) {
      lines.push(`${t("TPS")}: — (${t("{n} valid, {x} excluded", { n: "0", x: String(item.excluded_decode_calls) })})`);
    }
    if (item.cost !== null && item.cost !== undefined) {
      let costLine = "";
      if (item.has_unpriced && (!item.cost || item.cost === 0)) {
        costLine = `${t("Cost")}: ${t("Unknown cost")}`;
      } else {
        const costStr = item.has_unpriced ? t("≈{cost}", { cost: fmtCostVal(item.cost) }) : fmtCostVal(item.cost);
        costLine = `${t("Cost")}: ${costStr}`;
        if (item.share > 0) costLine += ` · ${fmtPct(item.share)}`;
        if (item.has_unpriced) costLine += ` (${t("unpriced")})`;
      }
      lines.push(costLine);
    }
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

  // --- Render Ranked Bar Chart ---
  function renderBarChart(chartId, items, valKey, getSegs, getTailText) {
    const wrap = el("div", "an-bars");

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
      "1.1": t("Error Rate"),
      "2.1": t("TTFT"),
      "2.2": t("TPS"),
      "3.1": t("Cost"),
      "3.2": t("Cache Hit Rate"),
    };

    // Header: Back button, Title & chips
    drillHeadEl.replaceChildren();

    const back = el("button", "an-drill-back");
    back.type = "button";
    back.setAttribute("aria-label", t("Back to Quality & Analytics"));
    back.title = t("Back to Quality & Analytics");
    back.append(svg("M10 2L4 7l6 5", 14, 2));
    back.onclick = (e) => exitDrill(e);

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

    drillHeadEl.append(back, titleWrap);

    // Left compact chart
    renderDrillLeft();

    // Right pane: list or detail
    renderDrillRight();
  }

  function renderDrillLeft() {
    drillLeftEl.replaceChildren();

    const chartNames = {
      "1.1": t("Error Rate"),
      "2.1": t("TTFT"),
      "2.2": t("TPS"),
      "3.1": t("Cost"),
      "3.2": t("Cache Hit Rate"),
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
      if (drillChartId === "1.1") {
        kpi.append(
          el("span", "an-drill-left-kpi-val", s.calls ? fmtPct(s.error_rate) : "—"),
          el("span", "an-drill-left-kpi-sub", s.calls ? t("429: {r} · 5xx: {s} · Other 4xx: {o}", { r: String(s.rate_limited || 0), s: String(s.server_err || 0), o: String(s.other_err || 0) }) : t("No data in this period"))
        );
      } else if (drillChartId === "2.1") {
        kpi.append(
          el("span", "an-drill-left-kpi-val", fmtMs(s.ttft_p95)),
          el("span", "an-drill-left-kpi-sub", s.timed ? t("P50 {p50} · {n} valid samples", { p50: fmtMs(s.ttft_p50), n: String(s.timed) }) : t("No streaming samples"))
        );
      } else if (drillChartId === "2.2") {
        let speedSub = s.decode_calls ? t("{n} valid decode samples", { n: String(s.decode_calls) }) : t("No decode samples");
        if (s.excluded_decode_calls) {
          speedSub = s.decode_calls
            ? t("{n} valid decode samples ({x} excluded)", { n: String(s.decode_calls), x: String(s.excluded_decode_calls) })
            : t("No decode samples ({x} excluded)", { x: String(s.excluded_decode_calls) });
        }
        kpi.append(
          el("span", "an-drill-left-kpi-val", fmtSpeed(s.speed)),
          el("span", "an-drill-left-kpi-sub", speedSub)
        );
      } else if (drillChartId === "3.1") {
        let costValStr = "—";
        let costSubStr = t("No data in this period");
        if (s.calls) {
          if (!s.cost && s.unpriced) {
            costValStr = t("Unknown cost");
            costSubStr = t("No price for these models");
          } else {
            costValStr = fmtCostVal(s.cost);
            costSubStr = s.unpriced
              ? t("≈{cost} ({n} unpriced)", { cost: fmtCostVal(s.cost), n: String(s.unpriced) })
              : t("At model list price");
          }
        }
        kpi.append(
          el("span", "an-drill-left-kpi-val", costValStr),
          el("span", "an-drill-left-kpi-sub", costSubStr)
        );
      } else if (drillChartId === "3.2") {
        kpi.append(
          el("span", "an-drill-left-kpi-val", fmtPct(s.cache_hit_rate)),
          el("span", "an-drill-left-kpi-sub", s.input || s.cache_read ? t("Uncached {in} · Written {w}", { in: fmtTok(s.input), w: fmtTok(s.cache_write) }) : t("No prompt token records"))
        );
      }
      drillLeftEl.append(kpi);
      return;
    }

    // In dimensional rankings: show list of entities, clickable to switch active drill entity
    let items = [];
    let valKey = "error_rate";
    let fmtVal = (item) => fmtPct(item.error_rate);

    if (drillChartId === "1.1") {
      items = data?.rankings?.[currentDim]?.by_error_rate || [];
      valKey = "error_rate";
      fmtVal = (item) => fmtPct(item.error_rate);
    } else if (drillChartId === "2.1") {
      items = data?.rankings?.[currentDim]?.by_ttft || [];
      valKey = "ttft_p95";
      fmtVal = (item) => fmtMs(item.ttft_p95);
    } else if (drillChartId === "2.2") {
      items = data?.rankings?.[currentDim]?.by_speed || [];
      valKey = "speed";
      fmtVal = (item) => fmtSpeed(item.speed);
    } else if (drillChartId === "3.1") {
      items = data?.rankings?.[currentDim]?.by_cost || [];
      valKey = "cost";
      fmtVal = (item) => fmtCostVal(item.cost);
    } else if (drillChartId === "3.2") {
      items = data?.rankings?.[currentDim]?.by_cache_rate || [];
      valKey = "cache_hit_rate";
      fmtVal = (item) => fmtPct(item.cache_hit_rate);
    }
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
      drillEntity = null;
      activeCall = null;
      activeCallIndex = null;
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
      const pct = maxVal > 0 ? Math.min(100, Math.max(2, (raw / maxVal) * 100)) : 0;
      seg.style.width = pct + "%";
      track.append(seg);

      row.append(name, num, track);
      row.onclick = (e) => {
        if (drillEntity?.val === item.key && drillEntity?.dim === currentDim) return;
        if (e && typeof scrollOnPurpose === "function") scrollOnPurpose(e, 500);
        drillEntity = { dim: currentDim, val: item.key };
        activeCall = null;
        activeCallIndex = null;
        renderDrillPage();
        fetchDrillCalls(drillChartId, drillEntity);
      };

      itemsBox.append(row);
    }
    drillLeftEl.append(itemsBox);
  }

  function renderDrillRight(returnEvent) {
    drillRightEl.replaceChildren();

    if (activeCall) {
      // Right pane completely replaced by call detail view
      renderDrillDetail(activeCall);
      return;
    }

    // Calls list view
    const head = el("div", "an-drill-right-head");
    head.append(
      el("span", "an-drill-right-title", t("Top 50 Calls")),
      el("span", "an-drill-right-meta", drillCalls ? `${drillCalls.length} ${t("Calls")}` : "")
    );
    drillRightEl.append(head);

    if (drillLoading) {
      const loadingBox = el("div", "an-drill-state-box");
      loadingBox.append(el("span", "", t("Loading calls…")));
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
      const isSelected = activeCallIndex === i;
      const item = el("button", "an-call-item" + (isSelected ? " active" : ""));
      item.type = "button";
      item.dataset.index = String(i);

      let stCls = "st-ok";
      if (c.err || c.error) {
        stCls = c.status >= 500 ? "st-5xx" : (c.status === 429 ? "st-429" : (c.status === 499 ? "st-499" : "st-5xx"));
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
        el("span", "", `${t("TTFT")}: ${fmtMs(c.ttft_ms)}`),
        el("span", "", tokStr),
        el("span", "", `${c.session || "—"}${c.kind ? " · " + c.kind : ""}`)
      );

      item.append(top, bottom);

      item.onclick = (e) => {
        callsScrollTop = page.scrollTop || 0;
        if (e && typeof scrollOnPurpose === "function") scrollOnPurpose(e, 800);
        activeCall = c;
        activeCallIndex = i;
        renderDrillRight();
        requestAnimationFrame(() => {
          if (e && typeof scrollOnPurpose === "function") scrollOnPurpose(e, 500);
          page.scrollTop = 0;
        });
      };

      list.append(item);
    }

    drillRightEl.append(list);

    // Restore calls list scroll position if returning from detail
    if (callsScrollTop > 0) {
      const targetScroll = callsScrollTop;
      requestAnimationFrame(() => {
        if (returnEvent && typeof scrollOnPurpose === "function") scrollOnPurpose(returnEvent, 1000);
        page.scrollTop = targetScroll;
      });
    }
  }

  function renderDrillDetail(c) {
    const head = el("div", "an-drill-right-head");

    const returnBtn = el("button", "an-drill-return-btn");
    returnBtn.type = "button";
    returnBtn.setAttribute("aria-label", t("Back to Calls"));
    returnBtn.title = t("Back to Calls");
    returnBtn.append(svg("M10 2L4 7l6 5", 12, 2));
    returnBtn.onclick = (e) => {
      if (e && typeof scrollOnPurpose === "function") scrollOnPurpose(e, 1500);
      activeCall = null;
      renderDrillRight(e);
    };

    head.append(
      returnBtn,
      el("span", "an-drill-right-title", t("Call Details"))
    );
    drillRightEl.append(head);

    drillRightEl.append(renderCallDetailCard(c));
  }

  // --- Render In-Place Call Detail Card ---
  function renderCallDetailCard(c) {
    const card = el("div", "an-call-detail-box");

    // 1. Basic Info
    const g1 = el("div", "an-detail-group");
    g1.append(el("b", "", t("Basic Info")));
    g1.append(createDetailRow(t("Time"), c.t ? new Date(c.t).toLocaleString() : "—"));
    g1.append(createDetailRow(t("Agent"), c.agent || "—"));
    g1.append(createDetailRow(t("Kind"), c.kind || "—"));

    const sessRow = el("div", "an-detail-row");
    sessRow.append(el("span", "", t("Session ID")));
    const sessValWrap = el("span");
    sessValWrap.append(document.createTextNode(c.session || "—"));
    if (c.session) {
      const cp = copyBtn(c.session, t("Session id"));
      cp.classList.add("an-sess-copy-btn");
      cp.type = "button";
      cp.setAttribute("aria-label", t("Copy") + " " + t("Session id"));
      cp.onclick = (ev) => {
        ev.stopPropagation();
        copy(c.session, t("Session id"), cp, undefined, t("Failed to copy session ID"));
      };
      sessValWrap.append(cp);
    }
    sessRow.append(sessValWrap);
    g1.append(sessRow);

    // 2. Routing
    const g2 = el("div", "an-detail-group");
    g2.append(el("b", "", t("Routing")));
    g2.append(createDetailRow(t("Provider"), c.provider || "—"));
    g2.append(createDetailRow(t("Host"), c.host || "—"));
    g2.append(createDetailRow(t("Model"), c.model || "—"));
    g2.append(createDetailRow(t("Effort"), c.effort || "—"));

    // 3. Performance
    const g3 = el("div", "an-detail-group");
    g3.append(el("b", "", t("Performance")));
    g3.append(createDetailRow(t("Total Duration"), fmtMs(c.ms)));
    g3.append(createDetailRow(t("End-to-End TTFT"), fmtMs(c.ttft_ms)));
    // Decode speed formula: status < 400 && !err && ttft_ms > 0 && out > 0 && ms > ttft_ms && (ms - ttft_ms) >= 100
    let spdVal = "—";
    if (c.status < 400 && !c.err && !c.error && c.ttft_ms > 0 && c.out > 0 && c.ms > c.ttft_ms) {
      if (c.ms - c.ttft_ms >= 100) {
        const spd = c.out / ((c.ms - c.ttft_ms) / 1000);
        spdVal = fmtSpeed(spd);
      } else {
        spdVal = t("— (interval < 100ms)");
      }
    }
    g3.append(createDetailRow(t("TPS"), spdVal));

    // 4. Tokens & Cache
    const g4 = el("div", "an-detail-group");
    g4.append(el("b", "", t("Tokens & Cache")));
    g4.append(createDetailRow(t("Input Tokens"), String(c.in || 0)));
    g4.append(createDetailRow(t("Output Tokens"), String(c.out || 0)));
    g4.append(createDetailRow(t("Cache Read"), String(c.cache_read || 0)));
    g4.append(createDetailRow(t("Cache Write"), String(c.cache_write || 0)));
    const fullPrompt = (c.in || 0) + (c.cache_read || 0) + (c.cache_write || 0);
    g4.append(createDetailRow(t("Full Prompt Tokens"), String(fullPrompt)));

    // 5. Cache Rate & Cost
    const g5 = el("div", "an-detail-group");
    g5.append(el("b", "", t("Cache Rate & Cost")));
    const denom = (c.in || 0) + (c.cache_read || 0);
    const hitRate = denom > 0 ? (c.cache_read || 0) / denom : null;
    g5.append(createDetailRow(t("Cache Hit Rate"), fmtPct(hitRate)));
    g5.append(createDetailRow(t("Cost"), fmtCostVal(c.cost)));

    // 6. Reasoning Output Split
    const g6 = el("div", "an-detail-group");
    g6.append(el("b", "", t("Reasoning")));
    const rCount = c.reasoning || 0;
    const bodyOut = Math.max(0, (c.out || 0) - rCount);
    g6.append(createDetailRow(t("Thinking Output"), String(rCount)));
    g6.append(createDetailRow(t("Content Output"), String(bodyOut)));

    // 7. Status & Explanation
    const g7 = el("div", "an-detail-group");
    g7.append(el("b", "", t("Status")));
    g7.append(createDetailRow(t("HTTP Code"), String(c.status)));
    g7.append(createDetailRow(t("Explanation"), getStatusExplanation(c.status, c.err || c.error)));

    card.append(g1, g2, g3, g4, g5, g6, g7);
    return card;
  }

  function createDetailRow(label, val) {
    const row = el("div", "an-detail-row");
    row.append(el("span", "", label), el("span", "", val));
    return row;
  }

  // Issue review item #1: Popover picker using openPicker contract with onPick / menu
  function openFilterPicker(anchor, key, options, currentVal, ev, onPick) {
    if (typeof openPicker === "function") {
      const pseudoField = {
        key: key,
        label: key,
        value: currentVal,
        options: options,
        menu: true,
        onPick: (val) => {
          onPick(val);
        },
      };
      openPicker({ id: "analytics", name: "Analytics", fields: [] }, pseudoField, anchor, ev);
      return;
    }

    // Fallback if openPicker is not available
    const pop = $("#pop");
    if (!pop) return;

    pop.querySelector(".search").hidden = true;
    pop.querySelector(".picker-body").hidden = false;
    pop.querySelector("#pickerRail").hidden = true;
    if (pop.querySelector("#effortControl")) pop.querySelector("#effortControl").hidden = true;

    const list = pop.querySelector("#list");
    list.replaceChildren();

    for (const opt of options) {
      const li = el("li", opt.value === currentVal ? "selected" : "");
      li.append(el("span", "n", opt.label));
      li.onclick = () => {
        if (typeof closePicker === "function") closePicker();
        pop.hidden = true;
        onPick(opt.value);
      };
      list.append(li);
    }

    pop.hidden = false;
    if (typeof placePop === "function") {
      placePop(anchor, 220, Math.min(280, options.length * 36 + 16));
    }
  }

  // Export module lifecycle
  // Issue review item #3: close drill & increment drillSeq when period changes on load
  async function load(initialPeriod) {
    if (initialPeriod && initialPeriod !== currentPeriod) {
      currentPeriod = initialPeriod;
      if (drillMode) exitDrill();
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
      if (drillMode) {
        exitDrill();
      }
    }
  });
  observer.observe(page, { attributes: true, attributeFilter: ["hidden"] });

  window.loadAnalytics = load;
  // React to locale change
  const origSetLocale = window.setLocale;
  if (typeof origSetLocale === "function") {
    window.setLocale = function (pref) {
      const res = origSetLocale(pref);
      if (!page.hidden) render();
      return res;
    };
  }

  // If page was opened directly via ?view=analytics before analytics.js executed
  if (!page.hidden) {
    const p = typeof period === "string" ? period : "30d";
    load(p);
  }
})();
