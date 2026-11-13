// omp's advisor's rows (#6408, Awad_Yoo on X), in an omp's settings:
// whether it reviews and when, how long the agent waits for its review,
// and which note calls a finished agent back, as omp's config.yml has them
// (omp_advisor.go). The last two only once it reviews. app.js imports this
// the first time an omp's settings show (ompAdvisorIn), so it weighs
// nothing on the core.
window.ompAdvisorRows = function (a) {
  const val = (k) => a.fields.find((f) => f.key === k)?.value || "";
  // the rows answer in place, in a sheet too, as telemetryRow does
  const again = () => {
    const now = state.agents.find((x) => x.id === a.id);
    if (now && rows[0].isConnected) {
      rows[0].before(...ompAdvisorRows(now));
      for (const r of rows) r.remove();
    }
    if (view === "agents") renderAgents();
  };
  const set = (key, v) => api("set", { agent: a.id, field: key, value: v }).then((ns) => {
    state = ns;
    status(t("Takes effect when {agent} next starts", { agent: a.name }), "ok");
    again();
  }, (e) => { statusErr(e); again(); });
  const row = (name, tip, items, cur, key) => {
    const r = el("div", "row pref ag-advisor");
    r.dataset.key = key;
    const who = el("div", "who");
    who.append(el("div", "name", t(name)));
    who.title = t(tip);
    r.append(who, segs(items.map(([id, label]) => [id, t(label)]), cur, (v) => { if (v !== cur) set(key, v); }));
    return r;
  };
  const review = val("advisor_review") || "off";
  const rows = [row("Advisor", "A second model, on the advisor role, reviews the agent's work and sends it notes. A WATCHDOG.yml with advisors sets each one's own review instead",
    [["off", "Off"], ["turn", "Every turn"], ["agent-end", "At the end"]], review, "advisor_review")];
  if (review === "off") return rows;
  // 3 and 5 are omp's too, written by hand or in omp itself
  const wait = val("advisor_wait") || "off";
  const waits = [["off", "Don't wait"], ["1", "Up to 30s"], ["strict", "Until done"]];
  if (wait === "3" || wait === "5") waits.splice(2, 0, [wait, t("{n} behind", { n: wait })]);
  rows.push(row("Waits for its review", "Before going on, the agent waits for a review it is owed: up to 30 seconds, or with no limit until every review is in",
    waits, wait, "advisor_wait"));
  rows.push(row("Calls a finished agent back on", "The least note that brings back an agent that has finished; a lesser one waits for your next message. omp 18.12.1 or later",
    [["nit", "Any note"], ["concern", "Concern"], ["blocker", "Blocker"]], val("advisor_wake") || "concern", "advisor_wake"));
  return rows;
};
