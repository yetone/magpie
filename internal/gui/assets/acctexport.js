// ChatGPT and Claude accounts taken out as a file Sub2API, CLIProxyAPI or
// magpie imports (heyiwei5 and Rk1ngggg on X). app.js imports this when
// "Export accounts to a file…" is pressed (startExport), so it weighs
// nothing on the core. The file is written by the server, 0600, into
// Downloads; its tokens never reach the page.

window.accountExportSheet = function (sub) {
  const flow = signing;
  const box = el("div", "signing export");
  const tt = el("span", "tt");
  box.append(tt);
  const logins = (providers?.providers || []).find((p) => p.account?.agent === flow.agent)?.account?.logins || [];
  if (flow.done) {
    box.append(el("span", "mark", "✓"));
    tt.append(el("span", "n", t("Saved to {path}", { path: flow.done.path })));
    const rs = el("span", "results");
    for (const u of flow.done.accounts) rs.append(el("span", "res added", u));
    for (const s of flow.done.skipped || []) {
      const row = el("span", "res failed");
      row.append(el("span", "u", s.user), el("span", "why", s.error));
      rs.append(row);
    }
    tt.append(rs);
    const done = el("button", "text primary", t("Done"));
    done.onclick = cancelSignIn;
    box.append(done);
    return box;
  }
  box.append(el("span", "mark", "↓"));
  tt.append(el("span", "n", t("Export {name} accounts", { name: sub.name })),
    el("span", "s why", t("The file holds each account's live sign-in: whoever has it is signed in as that account. Keep it private and delete it once it is imported.")));
  if (flow.agent === "codex") tt.append(el("span", "s", t("A ChatGPT sign-in can be renewed in one place only: the first tool to renew it signs the other out.")));
  const fmt = segs([["sub2api", "Sub2API"], ["cliproxyapi", "CLIProxyAPI"]], flow.format, (id) => { flow.format = id; });
  tt.append(fmt);
  const list = el("span", "results");
  const go = el("button", "text primary", t("Export"));
  const sync = () => { go.disabled = !!flow.busy || logins.every((l) => flow.off.has(l.user)); };
  for (const l of logins) {
    const [lab, cb] = tick(l.user + (l.plan ? " · " + l.plan : ""), !flow.off.has(l.user));
    cb.onchange = () => { if (cb.checked) flow.off.delete(l.user); else flow.off.add(l.user); sync(); };
    list.append(lab);
  }
  tt.append(list);
  const why = el("span", "s why", flow.error || "");
  tt.append(why);
  go.onclick = async () => {
    if (signing !== flow || flow.busy) return;
    flow.busy = true;
    sync();
    try {
      const users = logins.filter((l) => !flow.off.has(l.user)).map((l) => l.user);
      const r = await api("signin/export", { agent: flow.agent, format: flow.format, users });
      if (signing !== flow) return;
      flow.done = r;
    } catch (err) {
      if (signing !== flow) return;
      flow.error = err.message;
    }
    flow.busy = false;
    renderProviders();
  };
  sync();
  const close = el("button", "text", t("Cancel"));
  close.onclick = cancelSignIn;
  box.append(close, go);
  return box;
};
