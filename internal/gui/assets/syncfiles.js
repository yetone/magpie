// Settings › Sync's Export and Import forms, and what putting a file's
// sign-ins in came to (qianxunji, curtinp118 on Discord). app.js imports
// this when one of the forms opens, so it weighs nothing on the core.

window.exportForm = function () {
  const ed = el("div", "editor sync-form");
  const p1 = input("", t("passphrase"), "password");
  const p2 = input("", t("again"), "password");
  const [keysL, keys] = tick(t("With the providers' API keys"), true);
  const [libL, lib] = tick(t("With the library: instructions, MCP servers, skills and subagents"), true);
  // sign-ins: off unless picked (qianxunji, curtinp118 on Discord)
  const [siL, si] = tick(t("With the sign-ins: subscription accounts and plugin sign-ins"), false);
  const what = el("div", "stack");
  what.append(keysL, libL, siL);
  const passF = field(t("Passphrase"), p1, t("Needed to open the file. Subscriptions aren't in it: sign in to them on the other computer."));
  const passSay = passF[1].querySelector(".hint");
  si.onchange = () => {
    passSay.textContent = si.checked ? t("Needed to open the file. The sign-ins are in it: anyone with the file and the passphrase can use those accounts, so pick a strong one.")
      : t("Needed to open the file. Subscriptions aren't in it: sign in to them on the other computer.");
  };
  ed.append(...passF, ...field("", p2), ...field("", what));
  const go = el("button", "text primary", t("Export"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  go.onclick = async () => {
    if (!p1.value) return say(t("Pick a passphrase: the file is sealed with it"));
    if (p1.value !== p2.value) return say(t("The two passphrases differ"));
    go.classList.add("busy");
    try {
      const r = await api("backup/export", { pass: p1.value, keys: keys.checked, library: lib.checked, signIns: si.checked });
      ed.replaceChildren(el("div", "done", t("Saved to {path}", { path: r.path })));
    } catch (e) {
      go.classList.remove("busy");
      say(e.message);
    }
  };
  return ed;
};

// signInsSaid: what putting a backup's sign-ins in did, as lines
window.signInsSaid = function (r) {
  const lines = [t("Sign-ins: {added} added, {updated} updated, {kept} kept as they are here", { added: r.added || 0, updated: r.updated || 0, kept: r.kept || 0 })];
  if (r.kept) lines.push(t("One kept as it is here was renewed later here, or is the account the agent itself is signed in with."));
  if (r.codex) lines.push(t("Codex is signed in with the backup's account."));
  return lines;
};

window.importForm = function () {
  const ed = el("div", "editor sync-form");
  let data = "";
  const file = el("input");
  file.type = "file";
  file.accept = ".magpie-backup";
  file.hidden = true;
  const name = el("span", "fname", t("No file chosen"));
  const pick = el("button", "text", t("Choose…"));
  pick.onclick = () => file.click();
  file.onchange = async () => {
    const f = file.files[0];
    if (!f) return;
    // as base64 in JSON: the app's web view drops a File sent as the body
    const bytes = new Uint8Array(await f.arrayBuffer());
    let bin = "";
    for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    data = btoa(bin);
    name.textContent = f.name;
  };
  const pf = el("div", "pair");
  pf.append(name, pick, file);
  const pass = input("", t("passphrase"), "password");
  const [agentsL, agents] = tick(t("Set the agents' models too"), true);
  const [libL, lib] = tick(t("Bring in the library too: instructions, MCP servers, skills and subagents"), true);
  const [siL, si] = tick(t("Bring in the sign-ins too, when the file has them"), true);
  const what = el("div", "stack");
  what.append(agentsL, libL, siL);
  ed.append(...field(t("File"), pf), ...field(t("Passphrase"), pass), ...field("", what, t("Providers with the same id are replaced; one that came without a key keeps the key it has here. The library replaces the one here, which is kept with its backups.")));
  const go = el("button", "text primary", t("Import"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  go.onclick = async () => {
    if (!data) return say(t("Choose a file first"));
    go.classList.add("busy");
    try {
      const r = await api("backup/import", { data, pass: pass.value, agents: agents.checked, library: lib.checked, signIns: si.checked });
      const lines = [t("Providers: {added} added, {replaced} replaced; {profiles} profiles; {agents} agent settings changed", { added: r.Added, replaced: r.Replaced, profiles: r.Profiles, agents: r.Agents })];
      if (r.NeedKey?.length) lines.push(t("Needs a key: {names}", { names: r.NeedKey.join(", ") }));
      if (r.SignIns) lines.push(...signInsSaid(r.SignIns));
      // a backup carries a subscription's settings, its sign-in only when
      // made with them
      if (r.SignIn?.length) lines.push(r.SignIns
        ? t("This backup's settings use {names}, which isn't signed in on this computer, and the backup has no sign-in for it. To use it here, sign in in Providers → Add a provider; if you don't, nothing needs doing.", { names: subNames(r.SignIn) })
        : t("This backup's settings use {names}, which isn't signed in on this computer. A backup carries settings, not sign-ins: to use it here, sign in in Providers → Add a provider; if you don't, nothing needs doing.", { names: subNames(r.SignIn) }));
      if (r.Plugins?.length) lines.push(t("Plugins added: {names}", { names: r.Plugins.join(", ") }));
      for (const [spec, error] of Object.entries(r.PluginErrors || {})) lines.push(t("Couldn't add the plugin {spec}: {error}", { spec, error }));
      if (r.Library) lines.push(t("Library brought in and written into the agents"));
      for (const p of r.LibraryProblems || []) lines.push(t("{agent} couldn't get {what}: {error}", { agent: p.agent, what: p.what, error: p.error }));
      ed.replaceChildren(...lines.map((l) => el("div", "done", l)));
      refreshAfterSync();
    } catch (e) {
      go.classList.remove("busy");
      say(e.message);
    }
  };
  return ed;
};
