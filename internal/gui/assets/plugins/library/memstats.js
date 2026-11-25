// Library › Memory's numbers (the owner, 11-25: how fast memory is made,
// recalled, updated and cleaned, with its trend): four tiles over the last
// 7 or 30 days, each a value and a small column line of its days, and a
// tile picked opens its chart. The counts are /api/memory/stats
// (memory.Stats, docs/subsystems/memory.md): this computer's own, counted
// from the release that began counting. A day before that has no counts,
// and draws none, except what was made, which the memories' files date.
// client.js imports this when the tab first draws, so it weighs nothing on
// the page until then.
(() => {
  let days = 7, open = "", data = {}, at = {}, seen = null, asking = null, card = null, failed = false;

  const sum = (ds, f) => ds.reduce((n, d) => n + (f(d) || 0), 0);
  const total = (m) => Object.values(m || {}).reduce((n, v) => n + v, 0);
  const counted = (d) => d.recorded || d.backfilled;
  const pct = (n, of) => (of ? Math.round((n / of) * 1000) / 10 : null);
  const num = (n) => (n == null ? "—" : n.toLocaleString(typeof intlLang === "function" ? intlLang() : undefined, { maximumFractionDigits: 1 }));
  const dateOf = (s, opts = { month: "short", day: "numeric" }) => {
    const [y, m, d] = s.split("-").map(Number);
    return new Date(y, m - 1, d).toLocaleDateString(typeof intlLang === "function" ? intlLang() : undefined, opts);
  };

  // each metric: its tile's value and line, a day's value for the chart,
  // and the rows that day's tooltip lists
  const METRICS = {
    created: {
      name: () => t("Creation rate"),
      about: () => t("Memories added a day: by you, by agents, learned from sessions or imported. The same text saved again is an update, not a new memory."),
      tile: (r, ds) => {
        const n = sum(ds, (d) => total(d.created));
        return { value: num(Math.round((n / ds.length) * 10) / 10), unit: t("new / day"), sub: t("{n} new", { n: num(n) }) };
      },
      day: (d) => (counted(d) ? total(d.created) : null),
      rows: (d) => [
        [t("By you"), (d.created || {}).user],
        [t("By agents"), (d.created || {}).agent],
        [t("Learned from sessions"), (d.created || {}).inferred],
        [t("From imports"), (d.created || {}).imported],
      ],
    },
    recall: {
      name: () => t("Recall rate"),
      about: () => t("Of the agents' memory searches, the share that returned at least one memory. The tab's own search isn't counted."),
      tile: (r, ds) => {
        const n = sum(ds, (d) => d.searches), f = sum(ds, (d) => d.found);
        return n ? { value: num(pct(f, n)), unit: "%", sub: t("{found} of {n} searches", { found: num(f), n: num(n) }) } : { value: "—", sub: t("No searches yet") };
      },
      pct: true,
      day: (d) => (d.recorded && d.searches ? pct(d.found, d.searches) : null),
      rows: (d) => [
        [t("Agent searches"), d.searches || 0],
        [t("Found something"), d.found || 0],
        [t("Memories returned"), d.recalled || 0],
      ],
      none: (d) => (d.recorded ? t("No searches") : ""),
    },
    updated: {
      name: () => t("Update rate"),
      about: () => t("Updates against the memories in use: text, kind, where it applies, why, tags or pin changed, the same text saved again, or a tidy's merge."),
      tile: (r, ds) => {
        const n = sum(ds, (d) => d.updated);
        return { value: num(pct(n, r.active)), unit: r.active ? "%" : "", sub: t("{n} updated", { n: num(n) }) };
      },
      day: (d) => (d.recorded ? d.updated || 0 : null),
      rows: (d) => [[t("Memories updated"), d.updated || 0]],
    },
    cleaned: {
      name: () => t("Cleanup rate"),
      about: () => t("Memories that left use, against the memories in use: archived or deleted by you or an agent, merged or set aside by tidying, or suggestions nobody reviewed in time."),
      tile: (r, ds) => {
        const n = sum(ds, (d) => total(d.cleaned));
        return { value: num(pct(n, r.active)), unit: r.active ? "%" : "", sub: t("{n} cleaned up", { n: num(n) }) };
      },
      day: (d) => (d.recorded ? total(d.cleaned) : null),
      rows: (d) => [
        [t("By you"), (d.cleaned || {}).you],
        [t("By agents"), (d.cleaned || {}).agent],
        [t("By tidying"), (d.cleaned || {}).tidy],
        [t("Not reviewed in time"), (d.cleaned || {}).expiry],
      ],
    },
  };
  const ORDER = ["created", "recall", "updated", "cleaned"];

  // read again when memory changed here (a new mem), or a minute on, for
  // what agents did meanwhile; what was drawn stays until it's in
  function want(mem) {
    const stale = !data[days] || seen !== mem || Date.now() - (at[days] || 0) > 60e3;
    if (!stale || asking === days) return;
    seen = mem;
    const n = (asking = days);
    api("memory/stats?days=" + n).then((r) => {
      // a magpie from before the counts answers with something else
      if (Array.isArray(r?.days) && r.days.length === n) data[n] = r;
      else failed = true;
      at[n] = Date.now();
    }, () => { failed = true; }).finally(() => {
      if (asking === n) asking = null;
      if (card?.isConnected && n === days) card.replaceWith(draw());
    });
  }

  function sparkline(vals, max) {
    const box = el("div", "ms-spark");
    box.setAttribute("aria-hidden", "true");
    vals.forEach((v, i) => {
      const c = el("span", i === vals.length - 1 ? "now" : "");
      if (v != null) {
        const i2 = el("i");
        i2.style.height = max && v ? Math.max(12, (v / max) * 100) + "%" : "0";
        c.append(i2);
      }
      box.append(c);
    });
    return box;
  }

  // a clean top for the axis: 1, 2 or 5 a power of ten, at least 2
  const nice = (v) => {
    if (v <= 2) return 2;
    const p = 10 ** Math.floor(Math.log10(v));
    return [1, 2, 2.5, 5, 10].map((s) => s * p).find((s) => s >= v);
  };

  function chart(r, key) {
    const M = METRICS[key];
    const vals = r.days.map(M.day);
    const top = M.pct ? 100 : nice(Math.max(0, ...vals.filter((v) => v != null)));
    const box = el("div", "ms-chart");
    box.dataset.metric = key;
    if (vals.every((v) => v == null)) {
      box.append(el("div", "ms-none", key === "recall" && r.days.some((d) => d.recorded) ? t("No searches in these days yet.") : t("Nothing counted in these days yet.")));
      if (r.since) box.append(el("div", "ms-note", t("Counted from {date}.", { date: dateOf(r.since) })));
      return box;
    }
    const plot = el("div", "ms-plot");
    plot.tabIndex = 0;
    plot.setAttribute("role", "list");
    plot.setAttribute("aria-label", M.name());
    for (const f of [1, 0.5]) {
      const g = el("div", "ms-grid");
      g.style.bottom = f * 100 + "%";
      g.append(el("span", "", num(top * f) + (M.pct ? "%" : "")));
      plot.append(g);
    }
    const cols = el("div", "ms-cols" + (r.days.length > 14 ? " dense" : ""));
    // the day under the pointer is read out over the plot, never on it;
    // with none, what the metric counts
    const read = el("div", "ms-read");
    read.setAttribute("aria-live", "polite");
    const rest = () => read.replaceChildren(el("span", "ms-about", M.about()));
    rest();
    r.days.forEach((d, i) => {
      const v = vals[i];
      const c = el("div", "ms-col" + (v == null ? " gap" : "") + (d.backfilled ? " back" : ""));
      c.setAttribute("role", "listitem");
      c.setAttribute("aria-label", `${dateOf(d.day)}: ${v == null ? (M.none?.(d) || t("Not counted yet")) : num(v) + (M.pct ? "%" : "")}`);
      if (v != null) {
        const bar = el("i");
        bar.style.height = v ? Math.max(1.5, (v / top) * 100) + "%" : "0";
        c.append(bar);
      }
      cols.append(c);
    });
    plot.append(cols);

    let hot = -1;
    const show = (i) => {
      if (i === hot) return;
      cols.children[hot]?.classList.remove("hot");
      hot = i;
      read.classList.toggle("on", i >= 0);
      if (i < 0) return void rest();
      const c = cols.children[i], d = r.days[i], v = vals[i];
      c.classList.add("hot");
      const line = [el("b", "", dateOf(d.day, { weekday: "short", month: "short", day: "numeric" }))];
      if (v == null) line.push(el("span", "ms-muted", M.none?.(d) || t("Not counted yet")));
      else {
        line.push(el("strong", "", num(v) + (M.pct ? "%" : "")));
        for (const [name, n] of M.rows(d)) {
          if (!n && key !== "recall") continue;
          const part = el("span", "ms-part");
          part.append(name + " ", el("em", "", num(n || 0)));
          line.push(part);
        }
        if (d.backfilled) line.push(el("span", "ms-muted", t("From the memories' own dates")));
      }
      read.replaceChildren(...line);
    };
    // the column under the pointer, by its x: the whole height is its target
    plot.onpointermove = (e) => {
      const b = cols.getBoundingClientRect();
      show(Math.max(0, Math.min(r.days.length - 1, Math.floor(((e.clientX - b.left) / b.width) * r.days.length))));
    };
    plot.onpointerleave = () => show(-1);
    plot.onfocus = () => show(r.days.length - 1);
    plot.onblur = () => show(-1);
    plot.onkeydown = (e) => {
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      e.stopPropagation();
      show(Math.max(0, Math.min(r.days.length - 1, (hot < 0 ? r.days.length - 1 : hot) + (e.key === "ArrowLeft" ? -1 : 1))));
    };

    const axis = el("div", "ms-axis" + (r.days.length > 14 ? " dense" : ""));
    const ticks = r.days.length > 14 ? [0, Math.floor((r.days.length - 1) / 2), r.days.length - 1] : r.days.map((_, i) => i);
    r.days.forEach((d, i) => axis.append(el("span", "", ticks.includes(i) ? (i === r.days.length - 1 ? t("Today") : dateOf(d.day, r.days.length > 14 ? undefined : { weekday: "short" })) : "")));
    box.append(read, plot, axis);
    if (r.since && r.days.some((d) => !d.recorded)) {
      box.append(el("div", "ms-note", key === "created" ? t("Counted from {date}; the days before are dated by the memories' own files.", { date: dateOf(r.since) }) : t("Counted from {date}.", { date: dateOf(r.since) })));
    } else if (!r.since) box.append(el("div", "ms-note", t("Dated by the memories' own files until counting begins.")));
    return box;
  }

  function draw() {
    const r = data[days];
    card = el("div", "list lib-list lib-memstats" + (r || failed ? "" : " wait"));
    card.setAttribute("aria-label", t("Memory activity"));
    const head = el("div", "ms-head");
    const range = segs([["7", t("7 days")], ["30", t("30 days")]], String(days), (v) => {
      days = +v;
      card.replaceWith(draw());
      want(seen);
    });
    range.classList.add("ms-range");
    head.append(el("div", "ms-title", t("Memory activity")), range);
    const tiles = el("div", "ms-tiles");
    const ds = r ? r.days : [];
    for (const key of ORDER) {
      const M = METRICS[key];
      const b = el("button", "ms-tile" + (open === key ? " on" : ""));
      b.type = "button";
      b.dataset.metric = key;
      b.title = M.about();
      b.setAttribute("aria-expanded", String(open === key));
      const v = r ? M.tile(r, ds.filter((d) => M.day(d) != null || key === "created")) : { value: "—", sub: "" };
      const val = el("div", "ms-value");
      val.append(el("span", "", v.value));
      if (v.unit) val.append(el("small", "", v.unit));
      const vals = ds.map(M.day);
      b.append(el("div", "ms-name", M.name()), val, el("div", "ms-sub", v.sub || " "), sparkline(vals, M.pct ? 100 : Math.max(0, ...vals.filter((x) => x != null))));
      b.onclick = () => {
        open = open === key ? "" : key;
        card.replaceWith(draw());
      };
      tiles.append(b);
    }
    card.append(head, tiles);
    if (r && open) card.append(chart(r, open));
    return card;
  }

  // the card for the tab as it is drawn now; mem is the tab's memories,
  // whose change reads the counts again
  window.memStatsCard = (mem) => {
    want(mem);
    return draw();
  };
})();
