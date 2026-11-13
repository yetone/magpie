// Settings › Sync's forms: the server's (WebDAV, S3 or GitHub), Export and
// Import, what putting a file's sign-ins in came to (qianxunji,
// curtinp118 on Discord), and the asks before a Restore or an Upload
// (#847, #5720). app.js imports this when one of them opens (syncFile), so
// it weighs nothing on the core.

// davForm: the kind picked shows one set of fields; drafts survive switching.
// Inactive servers keep their settings for moving sync back.
window.davForm = function (v) {
  const ed = el("div", "editor sync-form");
  const active = v.on ? (v.kind || "webdav") : "";
  let kind = active || "webdav";
  const saved = t("saved · type a new one to replace it");
  const servers = syncServers(v);
  const server = (k) => active === k ? v : servers[k] || {};
  const dav = server("webdav"), bk = server("s3"), gh = server("github");
  const url = input(dav.url || "", "https://dav.jianguoyun.com/dav/");
  const user = input(dav.user || "", t("user name"));
  const pass = input("", dav.passwordSet ? saved : t("password, or an app password"), "password");
  // s3://bucket/prefix, as the address is kept
  const [bucketName, prefixName] = (() => { const m = /^s3:\/\/([^/]*)\/?(.*)$/i.exec(bk.url || ""); return m ? [m[1], m[2]] : ["", ""]; })();
  const endpoint = input(bk.endpoint || "", "https://<account>.r2.cloudflarestorage.com");
  const bucket = input(bucketName, t("bucket name"));
  const prefix = input(prefixName, t("optional"));
  const region = input(bk.region || "", t("us-east-1, or auto for R2"));
  const keyID = input(bk.user || "", "AKIA…");
  const secret = input("", bk.passwordSet ? saved : t("secret access key"), "password");
  const [pathL, pathStyle] = tick(t("Path-style: the bucket in the path, not the host name"), !!bk.pathStyle);
  const ghParts = /^github:\/\/([^/]+\/[^/]+)\/?(.*)$/i.exec(gh.url || "");
  const repo = input(ghParts?.[1] || "", "owner/repo");
  const branch = input(gh.branch || "", t("default branch"));
  const ghPrefix = input(ghParts?.[2] || "", t("optional"));
  const token = input("", gh.passwordSet ? saved : t("GitHub token"), "password");
  const phrase = input("", v.passphraseSet ? saved : t("the same on every computer"), "password");
  // what syncs (#6428, jinpwng on X): a part not ticked stays as it is on
  // each computer, and the server keeps the others' copy of it
  const [provL, prov] = tick(t("Providers"), v.providers !== false);
  const [keysL, keys] = tick(t("With their API keys"), v.keys !== false);
  keysL.classList.add("sync-sub-tick");
  const [setL, sets] = tick(t("Settings"), v.settings !== false);
  const [agentsL, agents] = tick(t("Agents' models"), v.agents !== false);
  const [libL, lib] = tick(t("Library: instructions, MCP servers, skills, hooks and subagents"), v.library !== false);
  const [memL, mem] = tick(t("Memory: what the agents know, merged memory by memory"), v.memory !== false);
  const keysShow = () => { keysL.hidden = !prov.checked; };
  keysShow();
  prov.onchange = keysShow;
  // rarely changed: under Advanced, open when one is set away from its default
  const [profL, prof] = tick(t("Profiles"), v.profiles !== false);
  // usage: off unless picked, each computer its own (#542)
  const [useL, use] = tick(t("Usage: this computer's calls, for the Usage page on the others"), !!v.usage);
  // sign-ins: off unless picked, sealed with the passphrase like the rest
  // (qianxunji, curtinp118 on Discord); what it lets in is said beside it
  const [siL, si] = tick(t("Sign-ins: subscription accounts and plugin sign-ins"), !!v.signIns);
  const siSay = el("div", "hint sync-signins-say", t("Another computer that syncs is signed in without signing in again. Anyone with the passphrase and the server's file can use these accounts. An account used on two computers can be signed out on one when the other renews it."));
  siSay.hidden = !si.checked;
  si.onchange = () => { siSay.hidden = !si.checked; };
  const what = el("div", "stack");
  what.append(provL, keysL, setL, agentsL, libL, memL);
  const adv = el("details", "more advanced sync-advanced");
  adv.open = v.profiles === false || !!v.usage || !!v.signIns;
  const advIn = el("div", "stack");
  advIn.append(profL, useL, siL, siSay, el("div", "hint", t("Usage goes as a file a day: times, tokens, models and costs, never what was said. Turn it on on each computer that shares its own.")));
  adv.append(el("summary", "", t("Advanced")), advIn);
  const davFields = [...field(t("Address"), url, t("A folder named magpie is made in it.")),
    ...field(t("User"), user),
    ...field(t("Password"), pass)];
  const s3Fields = [...field(t("Endpoint"), endpoint, t("Empty for AWS S3; for R2, B2, MinIO and the like, their S3 API address.")),
    ...field(t("Bucket"), bucket),
    ...field(t("Prefix"), prefix, t("The backup goes in a folder named magpie under it.")),
    ...field(t("Region"), region),
    ...field(t("Access key"), keyID),
    ...field(t("Secret"), secret),
    ...field("", pathL, t("MinIO and most NAS servers need it."))];
  const ghFields = [...field(t("Repository"), repo, t("An existing private GitHub repository, in owner/repo form.")),
    ...field(t("Branch"), branch, t("Empty uses the repository's default branch. Empty repositories are initialized by the first sync; other branches must already exist.")),
    ...field(t("Folder"), ghPrefix, t("The encrypted backup goes in <folder>/magpie/magpie.magpie-backup. Each update creates a commit.")),
    ...field(t("GitHub token"), token, t("A fine-grained token for this repository with Contents: read and write. Kept on this computer, separately from the library's GitHub token."))];
  // which one is synced to, and that the other's settings stay
  const to = field(t("Sync to"), segs(Object.entries(syncNames).map(([id, n]) => [id, id === active ? t("{kind} · on", { kind: n }) : n]), kind, (k) => { kind = k; show(); }), " ");
  const where = to[1].querySelector(".hint");
  const save = el("button", "text primary", "");
  const other = Object.keys(servers).map((k) => syncNames[k]).filter(Boolean).join(" / ");
  const show = () => {
    for (const x of davFields) x.hidden = kind !== "webdav";
    for (const x of s3Fields) x.hidden = kind !== "s3";
    for (const x of ghFields) x.hidden = kind !== "github";
    where.textContent = !active ? ""
      : kind === active ? (other ? t("Syncing here now. The {other} settings are kept, not synced to: pick {other} to see them.", { other }) : t("Syncing here now."))
      : t("{active} is synced to now. Saving moves sync here; the {active} settings are kept for moving back.", { active: syncNames[active] });
    where.hidden = !active;
    save.textContent = !active ? t("Turn on") : kind === active ? t("Save") : t("Move sync to {kind}", { kind: syncNames[kind] });
  };
  ed.append(...to,
    ...davFields, ...s3Fields, ...ghFields,
    ...field(t("Passphrase"), phrase, t("The file is sealed with it on this computer; the server only ever sees it sealed. Keep it: without it the file can't be opened.")),
    ...field(t("What syncs"), what, t("What isn't ticked stays as it is on each computer.")),
    ...field("", adv));
  show();
  const off = v.on ? el("button", "text danger", t("Turn off")) : el("span");
  const cancel = el("button", "text", t("Cancel"));
  // this setup as text, for another computer (WJaunita45604 on X): sealed
  // with the passphrase, which the text never holds
  const copySetup = v.on ? el("button", "text sync-copy", t("Copy setup")) : el("span");
  // what saving would sync, before it does (#6428)
  const peek = el("button", "text sync-peek", t("Preview"));
  const said = syncBar(ed, "", off, copySetup, el("span", "grow"), cancel, peek, save);
  const say = (msg, note) => { said(msg); ed.querySelector(".editor-error").classList.toggle("note", !!note); };
  copySetup.onclick = async () => {
    try {
      const r = await api("davsync/setup-text", {});
      await copy(r.text, t("Sync setup"), copySetup, t("Sync setup copied: the server and its password, sealed with your passphrase. On the other computer, paste it into this form and type the same passphrase."));
    } catch (e) { say(t(e.message)); }
  };
  // a setup copied on another computer, pasted in any field here, fills
  // the form once the passphrase it was sealed with is typed
  let pasted = "";
  const fill = (s) => {
    kind = /^s3:\/\//i.test(s.url) ? "s3" : /^github:\/\//i.test(s.url) ? "github" : "webdav";
    if (kind === "s3") {
      const m = /^s3:\/\/([^/]*)\/?(.*)$/i.exec(s.url);
      bucket.value = m[1]; prefix.value = m[2];
      endpoint.value = s.endpoint || ""; region.value = s.region || ""; keyID.value = s.user || ""; secret.value = s.password || ""; pathStyle.checked = !!s.pathStyle;
    } else if (kind === "github") {
      const m = /^github:\/\/([^/]+\/[^/]+)\/?(.*)$/i.exec(s.url);
      repo.value = m?.[1] || ""; ghPrefix.value = m?.[2] || ""; branch.value = s.branch || ""; token.value = s.password || "";
    } else { url.value = s.url; user.value = s.user || ""; pass.value = s.password || ""; }
    keys.checked = !!s.keys; agents.checked = !!s.agents; lib.checked = !!s.library; mem.checked = !!s.memory; use.checked = !!s.usage; si.checked = !!s.signIns;
    // left out only when the setup says so: one from before the choice syncs them
    prov.checked = s.providers !== false; sets.checked = s.settings !== false; prof.checked = s.profiles !== false;
    keysShow();
    if (!prof.checked || use.checked || si.checked) adv.open = true;
    siSay.hidden = !si.checked;
    // the kind picked as a click on it would, its thumb moved too
    to[1].querySelector(`.segs [data-seg="${kind}"]`)?.click();
    show();
  };
  const openPasted = async () => {
    if (!pasted) return true;
    if (!phrase.value) {
      say(t("Type the passphrase sync uses on the other computer: the pasted setup is sealed with it"), true);
      phrase.focus();
      return false;
    }
    try {
      fill(await api("davsync/setup-open", { text: pasted, passphrase: phrase.value }));
      pasted = "";
      say(t("Filled in from the pasted setup."), true);
      return true;
    } catch (e) { say(t(e.message)); return false; }
  };
  ed.addEventListener("paste", (e) => {
    const text = e.clipboardData?.getData("text") || "";
    if (!text.includes("magpie-sync:")) return;
    e.preventDefault();
    pasted = text;
    openPasted();
  });
  phrase.addEventListener("keydown", (e) => { if (e.key === "Enter" && pasted) openPasted(); });
  phrase.addEventListener("change", () => { if (pasted) openPasted(); });
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  off.onclick = async () => { syncOpen = ""; renderSync(await api("davsync/off", {}).catch(() => null) || undefined); };
  // form: the setup as typed, or a reason it can't be saved yet
  const form = () => {
    const b = bucket.value.trim(), p = prefix.value.trim().replace(/^\/+|\/+$/g, "");
    if (kind === "s3" && !b) return t("Name the bucket");
    const repository = repo.value.trim().replace(/^https:\/\/github\.com\//i, "").replace(/\/+$/, "").replace(/\.git$/i, "");
    const ghFolder = ghPrefix.value.trim().replace(/^\/+|\/+$/g, "");
    if (kind === "github" && !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) return t("Name the GitHub repository as owner/repo");
    if (kind === "github" && !gh.passwordSet && !token.value.trim()) return t("Enter a GitHub token with Contents read and write permission");
    if (!v.passphraseSet && !phrase.value) return t("Pick a passphrase: the file is sealed with it");
    const where = kind === "github"
      ? { url: "github://" + repository + (ghFolder ? "/" + ghFolder : ""), branch: branch.value.trim(), password: token.value.trim() }
      : kind === "s3"
      ? { url: "s3://" + b + (p ? "/" + p : ""), user: keyID.value.trim(), password: secret.value, endpoint: endpoint.value.trim(), region: region.value.trim(), pathStyle: pathStyle.checked }
      : { url: url.value.trim(), user: user.value.trim(), password: pass.value };
    // a part left out is said; one synced only when it was left out before,
    // so a setup from before the choice is saved as it was
    const outs = {};
    for (const [k, c] of [["providers", prov], ["settings", sets], ["profiles", prof]]) if (!c.checked || v[k] === false) outs[k] = c.checked;
    return { ...where, passphrase: phrase.value, keys: keys.checked, agents: agents.checked, library: lib.checked, memory: mem.checked, ...outs, ...(use.checked || v.usage ? { usage: use.checked } : {}), ...(si.checked || v.signIns ? { signIns: si.checked } : {}) };
  };
  peek.onclick = async () => {
    if (!(await openPasted())) return;
    const f = form();
    if (typeof f === "string") return say(f);
    peek.classList.add("busy");
    try { askSyncPreview(await api("davsync/preview", { form: f }), () => save.click()); } catch (e) { say(t(e.message)); }
    peek.classList.remove("busy");
  };
  save.onclick = async () => {
    if (!(await openPasted())) return;
    const f = form();
    if (typeof f === "string") return say(f);
    save.classList.add("busy");
    try {
      const r = await api("davsync/save", f);
      if (!r.error) syncOpen = "";
      renderSync(r);
      if (r.error) return;
      refreshAfterSync();
    } catch (e) {
      save.classList.remove("busy");
      say(e.message);
    }
  };
  return ed;
};

window.exportForm = function () {
  const ed = el("div", "editor sync-form");
  const p1 = input("", t("passphrase"), "password");
  const p2 = input("", t("again"), "password");
  const [keysL, keys] = tick(t("With the providers' API keys"), true);
  const [libL, lib] = tick(t("With the library: instructions, MCP servers, skills, hooks and subagents"), true);
  const [memL, mem] = tick(t("With memory: what the agents know"), true);
  // sign-ins: off unless picked (qianxunji, curtinp118 on Discord)
  const [siL, si] = tick(t("With the sign-ins: subscription accounts and plugin sign-ins"), false);
  const what = el("div", "stack");
  what.append(keysL, libL, memL, siL);
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
      const r = await api("backup/export", { pass: p1.value, keys: keys.checked, library: lib.checked, memory: mem.checked, signIns: si.checked });
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
  const [libL, lib] = tick(t("Bring in the library too: instructions, MCP servers, skills, hooks and subagents"), true);
  const [memL, mem] = tick(t("Bring in memory too, merged with the memories here"), true);
  const [siL, si] = tick(t("Bring in the sign-ins too, when the file has them"), true);
  const what = el("div", "stack");
  what.append(agentsL, libL, memL, siL);
  ed.append(...field(t("File"), pf), ...field(t("Passphrase"), pass), ...field("", what, t("Providers with the same id are replaced; one that came without a key keeps the key it has here. The library replaces the one here, which is kept with its backups.")));
  const go = el("button", "text primary", t("Import"));
  const cancel = el("button", "text", t("Cancel"));
  const say = syncBar(ed, "", el("span", "grow"), cancel, go);
  cancel.onclick = () => { syncOpen = ""; renderSync(); };
  go.onclick = async () => {
    if (!data) return say(t("Choose a file first"));
    go.classList.add("busy");
    try {
      const r = await api("backup/import", { data, pass: pass.value, agents: agents.checked, library: lib.checked, memory: mem.checked, signIns: si.checked });
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

// askRestore asks before the server's setup replaces this computer's
// (#847); what is here is kept, and the notice it leaves can undo it.
window.askRestore = function (v, parts) {
  const ed = el("div", "editor restore-ask");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Restore from the server?")));
  ed.append(head);
  const goes = ["providers", "settings", "profiles", "agents", "library"].filter((p) => v[p] !== false);
  ed.append(el("p", "lib-confirm", t("This computer's {parts} become the ones on the server, as your other computers last synced them. Whatever was changed here since is replaced.", { parts: parts(goes) })));
  ed.append(el("p", "lib-confirm", t("What is here now is sealed into the sync folder first, and Undo puts it back.")));
  const err = el("p", "editor-error", "");
  ed.append(err);
  const bar = el("div", "bar");
  const go = el("button", "text primary danger-fill", t("Restore setup"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      const r = await api("davsync/restore", {});
      closeConfirmAsk();
      renderSync(r);
      if (r.error) return;
      if (r.brought?.length) refreshAfterSync();
      else status(t("This computer's setup is already the server's: nothing to restore"), "ok");
    } catch (x) {
      go.disabled = false;
      go.classList.remove("busy");
      err.textContent = x.message;
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelAsk;
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus(NO_SCROLL);
};

// askUpload asks before this computer's setup replaces a server file that
// isn't a backup; the server's file is kept in the sync folder first.
window.askUpload = function (f) {
  const ed = el("div", "editor restore-ask upload-ask");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Replace the file on the server?")));
  ed.append(head);
  ed.append(el("p", "lib-confirm", t("This computer's setup goes up in place of the file on the server, and your other computers sync with it from then on.")));
  if (f.what === "cut") ed.append(el("p", "lib-confirm", t("What another computer wrote into that file and has nowhere else is lost. If one of them synced it last, Sync now there may rebuild it instead.")));
  ed.append(el("p", "lib-confirm", t("The file on the server is kept in the sync folder first.")));
  const err = el("p", "editor-error", "");
  ed.append(err);
  const bar = el("div", "bar");
  const go = el("button", "text primary danger-fill", t("Upload this computer's setup"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      const r = await api("davsync/upload", {});
      closeConfirmAsk();
      renderSync(r);
    } catch (x) {
      go.disabled = false;
      go.classList.remove("busy");
      err.textContent = x.message;
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelAsk;
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus(NO_SCROLL);
};

// askSyncPreview shows what syncing now would do (#6428, jinpwng on X),
// nothing done yet: what comes here, what goes to the server, and what
// stays as it is. go is the form's save, when the preview is of a setup
// not saved yet; otherwise the sync shown is the one run, or none.
window.askSyncPreview = function (plan, go) {
  const ed = el("div", "editor restore-ask sync-preview");
  const draw = (plan, note) => {
    ed.replaceChildren();
    const head = el("div", "ehead");
    head.append(el("b", "", t("Preview of the sync")));
    ed.append(head);
    const by = (d) => plan.parts.filter((p) => p.do === d);
    const items = (p) => {
      const list = el("div", "sync-items");
      const shown = (p.items || []).slice(0, 8);
      for (const it of shown) {
        const r = el("div", "sync-item");
        r.append(el("span", "", it.name || t(it.kind)), el("span", "sub", t({ added: "added", removed: "removed", changed: "changed" }[it.change] || it.change)));
        list.append(r);
      }
      if ((p.items || []).length > shown.length) list.append(el("div", "sub", t("and {n} more", { n: p.items.length - shown.length })));
      return list;
    };
    const ins = by("in"), ups = by("up");
    const mem = plan.memory || { in: 0, up: 0 };
    if (plan.empty) ed.append(el("p", "lib-confirm", t("The server has no backup yet: this computer's setup goes up.")));
    for (const p of ins) {
      ed.append(el("p", "lib-confirm", t("Comes here from the server: {part}", { part: syncParts([p.part]) })));
      if (p.replaces) ed.append(el("p", "lib-confirm sub", t("It was changed here too: the server's newer one replaces it, and what is here is kept in the sync folder first.")));
      if (p.items?.length) ed.append(items(p));
    }
    for (const p of ups) {
      ed.append(el("p", "lib-confirm", t("Goes to the server from here: {part}", { part: syncParts([p.part]) })));
      if (!plan.empty && p.items?.length) ed.append(items(p));
    }
    if (mem.in) ed.append(el("p", "lib-confirm", t("Memory: {n} come here", { n: mem.in })));
    if (mem.up) ed.append(el("p", "lib-confirm", t("Memory: {n} go to the server", { n: mem.up })));
    if (plan.signIns) ed.append(el("p", "lib-confirm", t("Sign-ins: {n} change here", { n: plan.signIns })));
    if (plan.pluginsGone?.length) ed.append(el("p", "lib-confirm", t("Plugins removed here, as another computer removed them: {names}", { names: plan.pluginsGone.join(", ") })));
    const nothing = !ins.length && !ups.length && !mem.in && !mem.up && !plan.signIns && !plan.pluginsGone?.length;
    if (nothing) ed.append(el("p", "lib-confirm", t("Nothing to sync: this computer and the server are the same.")));
    const same = by("same").map((p) => p.part), off = by("off").map((p) => p.part);
    if (same.length && !nothing) ed.append(el("p", "lib-confirm sub", t("Already the same: {parts}", { parts: syncParts(same) })));
    if (off.length) ed.append(el("p", "lib-confirm sub", t("Stays as it is on each computer: {parts}", { parts: syncParts(off) })));
    const err = el("p", "editor-error", note || "");
    err.setAttribute("role", "alert");
    ed.append(err);
    const bar = el("div", "bar");
    const cancel = el("button", "text", t(nothing && !go ? "Close" : "Cancel"));
    cancel.onclick = cancelAsk;
    bar.append(el("span", "grow"), cancel);
    if (go || !nothing) {
      const ok = el("button", "text primary", t(go ? "Save and sync" : "Sync now"));
      ok.onclick = async (e) => {
        e.stopPropagation();
        if (go) { closeConfirmAsk(); go(); return; }
        ok.disabled = true;
        ok.classList.add("busy");
        try {
          const r = await api("davsync/sync-previewed", { token: plan.token });
          closeConfirmAsk();
          renderSync(r);
          if (!r.error) refreshAfterSync();
        } catch (x) {
          // the server or this computer changed since: shown again, as it is now
          try { draw(await api("davsync/preview", {}), t(x.message)); } catch (y) { ok.disabled = false; ok.classList.remove("busy"); err.textContent = t(y.message); }
        }
      };
      bar.append(ok);
    }
    ed.append(bar);
    cancel.focus(NO_SCROLL);
  };
  draw(plan);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  ed.querySelector(".bar .text")?.focus(NO_SCROLL);
};

// askUndoSync asks before the parts the last sync brought in go back to
// how they were on this computer (#6428). A part changed here since is
// named, and written over only when asked again.
window.askUndoSync = function (v) {
  const ed = el("div", "editor restore-ask sync-undo-ask");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Undo the last sync?")));
  ed.append(head);
  const a = v.applied;
  ed.append(el("p", "lib-confirm", t("This computer's {parts} go back to how they were before the sync {when}. The server and your other computers keep theirs.", { parts: syncParts(a.parts), when: syncWhen(a.at) })));
  ed.append(el("p", "lib-confirm", t("What is here now is kept in the sync folder first.")));
  const err = el("p", "editor-error", "");
  err.setAttribute("role", "alert");
  ed.append(err);
  const bar = el("div", "bar");
  const go = el("button", "text primary danger-fill", t("Undo sync"));
  let force = false;
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.classList.add("busy");
    try {
      const r = await api("davsync/undo-sync", { force });
      closeConfirmAsk();
      renderSync(r);
      refreshAfterSync();
    } catch (x) {
      go.disabled = false;
      go.classList.remove("busy");
      if (x.changed?.length) {
        force = true;
        err.classList.add("note");
        err.textContent = t("{parts} changed here since the sync. Undoing replaces those changes too.", { parts: syncParts(x.changed) });
        go.textContent = t("Undo anyway");
      } else err.textContent = t(x.message);
    }
  };
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelAsk;
  bar.append(el("span", "grow"), cancel, go);
  ed.append(bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  cancel.focus(NO_SCROLL);
};

// askPreviewRows draws Settings › Sync's Preview a sync, and Undo the last
// sync while there is one to undo (#6428), into at: renderSync's place for
// them, kept here so the core weighs nothing more. Under sub, the sync's
// line, it says which parts each computer keeps as it is.
window.askPreviewRows = function (at, v, sub) {
  const left = ["providers", "settings", "profiles", "agents", "library", "memory"].filter((p) => v[p] === false);
  if (left.length) sub.after(el("div", "sub wraps sync-left", t("Stays as it is on each computer: {parts}", { parts: syncParts(left) })));
  const row = (name, sub, b, cls) => {
    const r = el("div", "row pref wraps " + cls), who = el("div", "who"), val = el("div", "val");
    who.append(el("div", "name", name), el("div", "sub", sub));
    val.append(b);
    r.append(who, val);
    at.append(r);
  };
  const peek = el("button", "text", t("Preview…"));
  peek.onclick = async () => {
    peek.classList.add("busy");
    try { askSyncPreview(await api("davsync/preview", {})); } catch (x) { status(t(x.message), "err"); }
    peek.classList.remove("busy");
  };
  row(t("Preview a sync"), t("What syncing now would change, here and on the server. Nothing changes until you sync."), peek, "sync-preview-row");
  if (v.applied?.parts?.length) {
    const undo = el("button", "text", t("Undo…"));
    undo.onclick = () => askUndoSync(v);
    row(t("Undo the last sync"), t("It brought {parts} here {when}. Undo puts back what was here before, on this computer only.", { parts: syncParts(v.applied.parts), when: syncWhen(v.applied.at) }), undo, "sync-undo-row");
  }
};
