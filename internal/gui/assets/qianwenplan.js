// The Qwen AI platform's Token Plan: its usage is told only to a sign-in
// on the platform, approved in the user's browser and kept per provider
// id (qianwen_plan.go). app.js imports this when such a provider's
// editor opens, so it weighs nothing on the core.
window.qianwenPlanField = (p) => field(t("Token Plan allowances"), renderQianwenPlan(p.qianwenPlan, p.id), t("The plan's usage is told only to a sign-in on the Qwen AI platform. magpie keeps it for this and nothing else."));

// renderQianwenPlan: a sign-in to the Qwen AI platform, approved in the
// user's browser, per provider id (qianwen_plan.go)
const qianwenWaiting = new Set();
function renderQianwenPlan(qp, id) {
  const box = el("div", "stepplan");
  const path = "qianwen/" + encodeURIComponent(id);
  const row = el("div", "pair");
  const side = el("div", "side");
  if (qp.signedIn) {
    const out = el("button", "text", t("Sign out"));
    out.onclick = async () => {
      try { providers = await api(path + "/signout", {}); renderProviders(); } catch (e) { statusErr(e); }
    };
    side.append(out);
    row.append(el("span", "state", qp.account ? t("Signed in as {user}", { user: qp.account }) : t("Signed in")), side);
    box.append(row);
    return box;
  }
  if (qianwenWaiting.has(id)) {
    const stop = el("button", "text", t("Cancel"));
    stop.onclick = () => {
      qianwenWaiting.delete(id);
      api(path + "/signin/cancel", {}).catch(() => {});
      renderProviders();
    };
    side.append(stop);
    row.append(el("span", "state", t("Approve the sign-in in your browser…")), side);
    box.append(row);
    return box;
  }
  const go = el("button", "link", t("Sign in to the Qwen AI platform ↗"));
  go.onclick = async () => {
    go.disabled = true;
    try {
      const r = await api(path + "/signin", {});
      qianwenWaiting.add(id);
      api("open", { url: r.url }).catch(() => {});
      renderProviders();
      qianwenWatch(id);
    } catch (e) {
      go.disabled = false;
      statusErr(e);
    }
  };
  box.append(go);
  return box;
}

// qianwenWatch waits on the provider's sign-in, which magpie finishes
// once the user approves it in their browser
async function qianwenWatch(id) {
  const path = "qianwen/" + encodeURIComponent(id) + "/signin";
  while (qianwenWaiting.has(id)) {
    await new Promise((r) => setTimeout(r, 1500));
    if (!qianwenWaiting.has(id)) return;
    let s;
    try { s = await api(path); } catch (_) { continue; }
    if (s?.state === "pending") continue;
    qianwenWaiting.delete(id);
    if (s?.state === "done") {
      try { providers = await api("providers"); } catch (_) { /* keep the last known list */ }
      status(t("Signed in to the Qwen AI platform"), "ok");
    } else if (s?.error) {
      statusErr(new Error(s.error));
    }
    renderProviders();
    return;
  }
}

