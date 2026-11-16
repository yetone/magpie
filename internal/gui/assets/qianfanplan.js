// Baidu Qianfan's Token Plan Personal: its month is told only to a sign-in
// on Baidu Cloud's console, brought by the bookmarklet and paste StepFun's
// Step Plan uses (renderStepPlan) and kept per provider id
// (qianfan_plan.go). A sign-in the console has ended (sp.ended) asks for a
// new one with the same steps, saying why. app.js imports this when such a
// provider's editor opens, so it weighs nothing on the core.
window.qianfanPlanField = (p) => {
  const sp = p.qianfanPlan;
  const box = renderStepPlan(sp.ended ? { ...sp, signedIn: false } : sp, p.id, {
    route: "qianfan", bm: "magpie · Qianfan", state: t("Signed in · the Usage page shows the Token Plan"),
    open: t("Sign in to the Baidu Cloud console in your browser ↗"), ok: t("Signed in to the Baidu Cloud console"),
  });
  if (sp.ended) box.prepend(el("div", "state ended", t("Baidu Cloud ended this sign-in. Bring a new one:")));
  return field(t("Token Plan usage"), box, t("A key tells nothing of the plan's usage. Its month is told only to a sign-in on Baidu Cloud's console: bring yours here once and magpie keeps it, used for nothing else. If magpie can't use what the bookmarklet copied, paste the Cookie header of the console's request from your browser's developer tools."));
};
