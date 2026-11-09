// Settings › Network and sharing's gateway rows (the gateway plugin's part
// of Settings): its port, model ids without the provider, sharing on the
// local network and the addresses let in, and the web pages that may call
// it; and a busy model's place, drawn into Settings › Models › Routing.
// Run after app.js, whose helpers it shares (el, segs, input, api,
// writingPrefs), and before start.js draws Settings.

// renderPort: the gateway's address on this computer (Magic_zero on Discord:
// 3425 taken by another program, or one easier to tell apart). Only its
// port, 1024 up, is typed here; magpie moves its gateway there and every
// agent it connected with it, or says why it can't (another program has
// it). MAGPIE_ADDR, a whole host:port, comes first and the field shows it,
// off (Chicring on Discord: the row was titled a port while it showed one).
let portDraft = { value: null, err: "" };
// portSays is the gateway's word on a port it couldn't take, in the page's
// language: it says the port, so it is matched rather than looked up
function portSays(m) {
  let x;
  if ((x = /^port (\d+) is in use by another program/.exec(m))) return t("Port {p} is in use by another program: pick another one", { p: x[1] });
  if ((x = /^another magpie serves the gateway at (\S+):/.exec(m))) return t("Another magpie serves the gateway at {url}: set its port there, or quit it first", { url: x[1] });
  if (/from 1024 to 65535/.test(m)) return t("A port is a number from 1024 to 65535");
  return t(m);
}
function renderPort(s) {
  const box = $("#portList");
  const r = el("div", "row pref port-row");
  const who = el("div", "who");
  who.append(el("div", "name", t("Gateway address")));
  const sub = el("div", "sub");
  const d = portDraft;
  const now = s.addrEnv ? "" : String(s.port || 3425);
  const field = input(d.value ?? now, "3425");
  field.className = "words gateway-port";
  field.inputMode = "numeric";
  field.setAttribute("aria-label", t("Gateway address"));
  const save = el("button", "text", t("Apply"));
  if (s.addrEnv) {
    field.value = s.addrEnv;
    field.disabled = save.disabled = true;
    sub.textContent = t("MAGPIE_ADDR={addr} sets the gateway’s whole address, host:port: unset it to set the port here", { addr: s.addrEnv });
  } else if (d.err) {
    sub.textContent = d.err;
    sub.classList.add("err");
  } else sub.textContent = t("Only the port is set here, 1024 to 65535; MAGPIE_ADDR=host:port sets the whole address instead. Agents magpie connected move with it. Now {url}", { url: s.gateway || "" });
  who.append(sub);
  save.onclick = () => {
    const p = Number(field.value.trim());
    if (!Number.isInteger(p) || p < 1024 || p > 65535) {
      d.value = field.value; d.err = t("A port is a number from 1024 to 65535");
      return renderPort(s);
    }
    if (p === (s.port || 3425)) { d.value = null; d.err = ""; return renderPort(s); }
    save.disabled = true;
    writingPrefs(api("settings/port", { port: p }).then((res) => res.settings && (prefs = res.settings, res)))
      .then((res) => {
        portDraft = { value: null, err: "" };
        const n = res.port?.moved?.length || 0;
        if (res.port?.error) status(res.port.error, "err");
        else status(n ? t("Gateway on port {p}; {n} agents moved with it", { p, n }) : t("Gateway on port {p}", { p }), "ok", 2500);
        // the sidebar's gateway line, and the list's address the pages
        // copy, say where it is now, not the port before (noahhrreion on
        // Discord: it kept the old one until Providers was opened)
        if (res.settings?.gateway) renderSideGw({ ...sideGwAt, url: res.settings.gateway, running: true });
        if (providers) loadProviders().catch(() => {});
        renderSettings();
      })
      .catch((e) => { d.value = field.value; d.err = portSays(e.message); status(d.err, "err"); renderSettings(); });
  };
  field.oninput = () => { d.value = field.value; };
  field.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save.onclick(); if (e.key === "Escape") { portDraft = { value: null, err: "" }; renderPort(s); } };
  const val = el("div", "val");
  val.append(field, save);
  r.append(who, val);
  box.replaceChildren(r); // swapped whole: the list is never laid out empty
}

// renderBareIds: Model ids without the provider (settings.bareModelIDs,
// love1sbug on X, moving from CLIProxyAPI): the gateway's /v1/models
// lists each model by its own id as well, gpt-5.6-sol beside
// codex/gpt-5.6-sol. Posted on its own; off by default.
function renderBareIds(s) {
  renderBusyLoad(s); // Models › Routing's row
  $("#bareIdsSegs").replaceChildren(segs([[false, t("Off")], [true, t("On")]], !!s.bareModelIDs, (on) =>
    writingPrefs(api("settings/bare-ids", { on })).then((ns) => { prefs = state.settings = ns; renderSettings(); })
      .catch((e) => { status(t(e.message), "err"); renderSettings(); })));
}

// renderBusyLoad, in Settings › Models › Routing with the other rows on
// where a request goes: the load past which a model its plugin says is busy
// (magpie.load) goes behind the others (settings.BusyLoad, axelburks on
// Discord). Custom… opens a field for any other number, 1 to 9999, which
// is then shown as its own choice; Enter or Apply saves it, Escape closes
let busyDraft = null;
const BUSY_CUSTOM = -1;
function renderBusyLoad(s) {
  const at = s.busyLoad || 0, sub = $("#busyLoadRow .sub"), d = busyDraft;
  const put = (n) => writingPrefs(api("settings/busy-load", { at: n })).then((ns) => { busyDraft = null; prefs = state.settings = ns; renderSettings(); })
    .catch((e) => { status(t(e.message), "err"); renderSettings(); });
  sub.dataset.en ??= sub.textContent; // its English, as translateStatic keeps it
  sub.classList.toggle("err", !!d?.err);
  sub.textContent = d ? d.err || t("A load is in percent, as the plugin reports it: 100 is all its vendor serves at once, and past 100 requests queue (200 is twice that). Type 1 to 9999") : t(sub.dataset.en);
  if (d) {
    const field = input(d.value, "150");
    field.className = "words busy-load-at";
    field.style.cssText = "width:84px;flex:none";
    field.inputMode = "numeric";
    field.setAttribute("aria-label", t("Busy models last"));
    const save = el("button", "text", t("Apply")), no = el("button", "text", t("Cancel"));
    const close = () => { busyDraft = null; renderBusyLoad(s); };
    save.onclick = () => {
      const v = field.value.trim(), n = Number(v);
      if (!/^\d+$/.test(v) || n < 1 || n > 9999) { busyDraft = { value: field.value, err: t("Type a whole number from 1 to 9999") }; renderBusyLoad(s); return $("#busyLoadRow .busy-load-at").focus(NO_SCROLL); }
      save.disabled = true;
      put(n);
    };
    no.onclick = close;
    field.oninput = () => { d.value = field.value; };
    field.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save.onclick(); if (e.key === "Escape") close(); };
    const val = el("div", "val");
    val.append(field, save, no);
    return $("#busyLoadSegs").replaceChildren(val);
  }
  const opts = [0, 50, 70, 80, 90];
  if (!opts.includes(at)) opts.push(at);
  opts.sort((a, b) => a - b);
  $("#busyLoadSegs").replaceChildren(segs([...opts.map((n) => [n, n ? t("Over {n}%", { n }) : t("Off")]), [BUSY_CUSTOM, t("Custom…")]], at, (n) => {
    if (n !== BUSY_CUSTOM) return put(n);
    busyDraft = { value: [0, 50, 70, 80, 90].includes(at) ? "" : String(at), err: "" };
    renderBusyLoad(s);
    $("#busyLoadRow .busy-load-at").focus(NO_SCROLL);
  }));
}

// renderCORS: the web pages whose scripts may call the gateway from a
// browser (#1051), by origin — a row to add one, and one row each to take
// it away. A page listed calls with a gateway key; none listed, no page
// can read what the gateway answers, as before.
let corsDraft = { value: "", err: "" };
// corsSays is the gateway's word on an origin it didn't take, in the
// page's language
function corsSays(m) {
  if (/wildcard/.test(m)) return t("Name each origin: a wildcard would let every web page use magpie");
  if (/is not an (extension's )?origin/.test(m)) return t("Write an origin as http://localhost:3000 or https://app.example.com, or a browser extension's as chrome-extension://, moz-extension:// or safari-web-extension:// followed by its ID");
  return t(m);
}
function renderCORS(s) {
  renderWebPage(); // Browser access, the section under this one
  const box = $("#corsList");
  box.replaceChildren();
  const origins = s.corsOrigins || [];
  const d = corsDraft;
  const set = (next, done) => writingPrefs(api("settings/cors", { origins: next }))
    .then((ns) => { prefs = ns; d.err = ""; done?.(); renderSettings(); status(t("Saved"), "ok", 1500); })
    .catch((e) => { d.err = corsSays(e.message); status(d.err, "err"); renderSettings(); });
  const field = input(d.value, "http://localhost:3000");
  field.className = "words rule-match";
  field.setAttribute("aria-label", t("Web page origin"));
  field.autocomplete = "off";
  field.spellcheck = false;
  const add = el("button", "text", t("Add"));
  add.onclick = () => {
    d.value = field.value.trim();
    if (!d.value) return field.focus();
    set([...origins, d.value], () => { corsDraft = { value: "", err: "" }; });
  };
  field.oninput = () => { d.value = field.value; };
  field.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add.onclick(); };
  const r = el("div", "row pref rule-row");
  const who = el("div", "who");
  who.append(el("div", "name", t("Web pages that may call magpie")),
    el("div", "sub" + (d.err ? " err" : ""), d.err || t("Scripts on these origins can call the gateway from a browser, each request with a gateway key from Gateway. A page from any other site is refused; pages on this computer are not")));
  // where those keys are: the Gateway page shows them once a page is listed
  const nav = $('#nav [data-view="gateway"]');
  if (origins.length && nav && !nav.hidden) {
    const go = el("button", "link", t("Gateway keys"));
    go.type = "button";
    go.onclick = () => nav.click();
    const line = el("div", "sub feat-go");
    line.append(go);
    who.append(line);
  }
  const val = el("div", "val rule-add");
  val.append(field, add);
  r.append(who, val);
  box.append(r);
  for (const o of origins) {
    const row = el("div", "row pref cors-origin"); // an extension's id runs long: wrapped, never cut
    const w = el("div", "who");
    w.append(el("div", "name", o));
    const x = el("button", "text", t("Remove"));
    x.onclick = () => set(origins.filter((v) => v !== o));
    const v = el("div", "val");
    v.append(x);
    row.append(w, v);
    box.append(row);
  }
}

// renderAllow: the addresses other computers may reach magpie from at
// all (lc on Discord: magpie in Docker on a public host), as IPs and CIDR
// ranges, under Share on local network — a row to add one, and one row
// each to take it away. None listed lets every address in, with a gateway
// key, as before; this computer is always let in. MAGPIE_ALLOW_IPS, where
// set, is the list, shown and not edited here.
// A list that would shut out the page saving it names the address magpie
// sees the page from (Mathresearcher on Discord: which address?), and
// offers to add it to the list that was refused; behind Docker or a proxy
// that is the proxy's, not the user's computer's (gateway.peerAddr).
let allowDraft = { value: "", err: "", addr: "", tried: null };
function allowSays(m) {
  if (/is not an IP address/.test(m)) return t("Write an address as 203.0.113.7, or a range as 10.0.0.0/8 or 2001:db8::/32");
  const out = /shut out (\S+), the address/.exec(m);
  if (out) return t("That list leaves out {addr}, the address magpie sees this page open from: add it first, or this page can’t reach magpie", { addr: out[1] });
  if (/MAGPIE_ALLOW_IPS/.test(m)) return t("MAGPIE_ALLOW_IPS sets this list; change it there");
  return t(m);
}
function renderAllow(s) {
  const box = $("#allowList");
  box.replaceChildren();
  const env = s.allowIPsEnv || "";
  if (!s.lan && !s.onNetwork && !env) return;
  const list = env ? (s.allowIPsFromEnv || []) : (s.allowIPs || []);
  const d = allowDraft;
  const set = (next, done) => writingPrefs(api("settings/allowips", { ips: next }))
    .then((ns) => { prefs = ns; d.err = d.addr = ""; d.tried = null; done?.(); renderSettings(); status(t("Saved"), "ok", 1500); })
    .catch((e) => {
      d.err = allowSays(e.message);
      d.addr = /shut out (\S+), the address/.exec(e.message)?.[1] || "";
      d.tried = d.addr ? { next, done } : null;
      status(d.err, "err"); renderSettings();
    });
  const r = el("div", "row pref rule-row");
  const who = el("div", "who");
  const sub = d.err || (env ? t("Set by MAGPIE_ALLOW_IPS. Other computers reach magpie only from these addresses, even with a gateway key; this computer always can")
    : t("Other computers reach magpie only from these addresses or ranges (CIDR), even with a gateway key; this computer always can. None listed lets every address in"));
  who.append(el("div", "name", t("Allowed addresses")), el("div", "sub" + (d.err ? " err" : ""), sub));
  // the address the refusal named, added to the list that was refused in
  // one click
  if (d.err && d.addr && d.tried && !env) {
    const fix = el("button", "text allow-fix", t("Add {addr}", { addr: d.addr }));
    fix.type = "button";
    fix.title = t("Behind Docker or a reverse proxy, this is the proxy’s address, not your computer’s: adding it lets in everyone who comes through that proxy");
    const { next, done } = d.tried;
    fix.onclick = () => set([...next, d.addr], done);
    who.append(fix);
  }
  r.append(who);
  if (!env) {
    const field = input(d.value, "203.0.113.7, 10.0.0.0/8");
    field.className = "words rule-match allow-ip";
    field.setAttribute("aria-label", t("IP address or range"));
    field.autocomplete = "off";
    field.spellcheck = false;
    const add = el("button", "text", t("Add"));
    add.onclick = () => {
      d.value = field.value.trim();
      if (!d.value) return field.focus();
      set([...list, ...d.value.split(/[\s,;]+/).filter(Boolean)], () => { allowDraft = { value: "", err: "" }; });
    };
    field.oninput = () => { d.value = field.value; };
    field.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add.onclick(); };
    const val = el("div", "val rule-add");
    val.append(field, add);
    r.append(val);
  }
  box.append(r);
  for (const a of list) {
    const row = el("div", "row pref allow-ip-row");
    const w = el("div", "who");
    w.append(el("div", "name", a));
    const v = el("div", "val");
    if (!env) {
      const x = el("button", "text", t("Remove"));
      x.onclick = () => set(list.filter((o) => o !== a));
      v.append(x);
    }
    row.append(w, v);
    box.append(row);
  }
}

// renderLAN: the gateway shared on the local network, for agents on other
// machines. Gateway keys and connection examples live together in Gateway.
let lanSelectedURL = "", lanProtocol = "openai";
// the address picked under Share on local network is kept like Connect's
let lanWant = "";
try { lanWant = localStorage.getItem("magpie.lanAddress") || ""; } catch {}
// lanShown: the addresses the gateway is offered at beyond this computer,
// those of them for other containers on this host, and a line on them
// when magpie runs in a container, for Settings, Gateway, Connect, the
// sidebar and the key page alike. In a container magpie finds only its
// own addresses; the page opened over the network was reached at the
// host's, so the gateway is offered there, on its port. Other containers
// on the host reach it at the host's Docker bridge address, the
// container's default route (jiezhengj on Discord: other containers on a
// NAS needed http://172.17.0.1:3425), which follows, said to be theirs.
//
// In a LazyCat app (lazy: its domain, and whether the box's LAN addresses
// are listed), the domain serves only devices with LazyCat's client, and
// on a bridge network the box's LAN address isn't known, nor is the port
// there: the line says how to get it there (mtoolstec on X wanted
// http://192.168.0.100:3425/v1 and was offered the domain and the
// container bridge's 172.28.3.65).
function lanShown(urls, container, bridge, lazy = {}) {
  if (lazy.url && !lazy.box) {
    const port = lazyPort();
    return { urls, others: [], head: t("LazyCat"),
      sub: t("Only devices with the LazyCat client reach this address. For other devices to use the box’s LAN IP on port {port}, forward port {port} to Magpie in LazyCat’s LAN port forwarding app", { port }) };
  }
  const others = bridge ? [bridge] : [];
  if (!container) return { urls: [...urls, ...others.filter((u) => !urls.includes(u))], others, sub: "" };
  const host = location.hostname, at = urls[0] || bridge ? new URL(urls[0] || bridge) : null;
  if (web && host && at?.port && !/^(localhost|127\.|\[::1\]$)/.test(host))
    return { urls: [at.protocol + "//" + host + ":" + at.port, ...others], others, // an IPv6 hostname comes bracketed
      sub: others.length ? t("Other devices use the host’s address this page was opened at, and other containers on this host the host’s Docker bridge address, both on the gateway’s port; MAGPIE_PUBLIC_URL sets another")
        : t("Where this page was opened, on the gateway’s port; MAGPIE_PUBLIC_URL sets another") };
  if (others.length) return { urls: others, others,
    sub: t("Other containers on this host use the host’s Docker bridge address, on the gateway’s port; other devices use the host’s own, which MAGPIE_PUBLIC_URL puts here") };
  return { urls, others, sub: t("The container’s own addresses, which other devices can’t reach: set MAGPIE_PUBLIC_URL to the host’s") };
}
// gwLAN: lanShown for the gateway's read (providers', or boot.js's line),
// which says whether its addresses are a container's and where other
// containers reach it
function gwLAN(g) {
  return lanShown(g?.lan && g.lanURLs || [], !!g?.container, g?.lan && g.containerHost || "",
    { url: g?.lazyCat || "", box: !!g?.lazyCatBox });
}
// lazyPort: the gateway's port, for the LazyCat line: its listen address'
// (bootPrefs or Settings' gateway), 3425 when it names none
function lazyPort() {
  const at = prefs?.gateway || sideGwAt?.url || "";
  return /:(\d+)\/?$/.exec(at)?.[1] || "3425";
}
// addrWho: the words every list of the gateway's addresses names one by,
// for g (providers' gateway, or Settings' read as one): this computer's,
// the one other containers on this host use, one on the internet, and
// MAGPIE_PUBLIC_URL by its kind, a private address being the local
// network's like the rest. jiezhengj on Discord: Connect called the NAS's
// 192.168.31.218 "Public address" (公网地址) and the Docker bridge's
// 172.17.0.1 "Local network", while the list above it said otherwise.
function addrWho(g, u, others = []) {
  if (u === g.url) return t("This computer");
  if (others.includes(u)) return t("Other containers on this host");
  if (u && u === g.lazyCat) return t("LazyCat client");
  if ((g.lanPublic || []).includes(u) || u === g.publicURL && g.publicKind === "internet") return t("On the internet");
  if (u === g.publicURL && g.publicKind !== "local") return t("Public address");
  return t("Local network");
}
// pubWho: About's MAGPIE_PUBLIC_URL row, from /api/settings, by addrWho
// (a private address is the local network's; jiezhengj on Discord)
function pubWho(s) {
  return addrWho({ url: s.gateway, publicURL: s.publicURL, publicKind: s.publicKind, lanPublic: s.lanPublic, lazyCat: s.lanLazyCat }, s.publicURL,
    s.lanContainerHost ? [s.lanContainerHost] : []);
}
// gwFar: where other machines reach the gateway, for the sidebar and the
// key page: other containers' address only when there is no other
function gwFar(g) {
  const { urls, others } = gwLAN(g), far = urls.filter((u) => !others.includes(u));
  return far.length ? far : urls;
}
// sideLAN: where other machines reach the gateway while it is shared, for
// the sidebar's line (coeo91 on Discord): MAGPIE_PUBLIC_URL, else the
// address picked here, else the first
function sideLAN(g) {
  const u = gwFar(g);
  // a LazyCat app's domain is MAGPIE_PUBLIC_URL's too, and the box's LAN
  // address before it is the one other devices use
  const pub = g.publicURL && !(g.lazyCatBox && g.publicURL === g.lazyCat) ? g.publicURL : "";
  return u[0] && (pub || (u.includes(lanWant) ? lanWant : u[0]));
}
// the line was drawn from boot.js before this script came
if (sideGwAt) renderSideGw();
function renderLAN(s) {
  const box = $("#lanList");
  box.replaceChildren();
  const row = (name, sub, value, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name));
    if (sub) who.append(el("div", "sub", sub));
    const val = el("div", "val");
    if (value) val.append(el("code", "", value));
    val.append(...tools);
    r.append(who, val);
    box.append(r);
    return r;
  };
  const set = (body) => writingPrefs(api("settings/lan", body)).then((ns) => {
    prefs = ns; renderSettings();
    // the sidebar's gateway names the address shared, or this computer's
    api("providers").then((r) => renderSideGw(r?.gateway), () => {});
    if (ns.rekeyed) status(ns.rekeyed, "err");
  })
    .catch((e) => { status(t(e.message), "err"); renderSettings(); });
  row(t("Share on local network"), t("Agents on other computers on this network, or through a tunnel to this one such as Cloudflare Tunnel, can use magpie’s models with a gateway key from Gateway"), "",
    segs([["off", t("Off")], ["on", t("On")]], s.lan ? "on" : "off", (v) => set({ on: v === "on" })));
  if (!s.lan) return;
  const { urls, others, sub, head } = lanShown(s.lanURLs || [], s.lanContainer, s.lanContainerHost,
    { url: s.lanLazyCat || "", box: !!s.lanLazyCatBox });
  if (!urls.length) row(t("Address"), t("This computer has no local network address right now"), "");
  else {
    lanSelectedURL = urls.includes(lanWant) ? lanWant : urls[0];
    // an interface can hold an internet address (an ISP's static one, a
    // VPN's): the gateway answers there too, so the row says so
    const far = new Set(s.lanPublic || []);
    const listed = (s.allowIPsEnv ? s.allowIPsFromEnv : s.allowIPs)?.length > 0;
    const controls = el("div", "lan-address-controls");
    const address = urls.length === 1 ? el("code", "lan-address-text") : el("button", "proto pick lan-interface");
    if (urls.length > 1) {
      address.type = "button";
      address.setAttribute("aria-label", t("Address"));
      address.onclick = (e) => {
        e.stopPropagation();
        if (address.classList.contains("open")) return closeProtoMenu();
        const suffix = lanProtocol === "openai" ? "/v1" : "";
        openProtoMenu(address, urls.map((u) => ({ v: u, name: u + suffix, note: far.has(u) ? t("On the internet") : others.includes(u) ? t("Other containers on this host") : u === s.lanLazyCat ? t("LazyCat client") : "" })), lanSelectedURL,
          (v) => { lanSelectedURL = lanWant = v; keepPick("magpie.lanAddress", v); update(); renderSideGw(); }, "Address", "lan-address-menu");
      };
    }
    const copyControl = el("span", "lan-copy");
    const update = () => {
      const label = lanProtocol === "openai" ? "OpenAI" : "Anthropic";
      const url = lanSelectedURL + (lanProtocol === "openai" ? "/v1" : "");
      if (urls.length === 1) address.textContent = url;
      else address.replaceChildren(el("span", "lan-url", url), svg(CHEV, 11, 1.6));
      address.title = url;
      const what = t("{label} address", { label });
      const button = copyBtn(url, what, t("Copied {label} address", { label }));
      button.setAttribute("aria-label", t("Copy") + " " + what);
      copyControl.replaceChildren(button);
      const who = addressRow?.querySelector(".who");
      if (!who) return;
      who.querySelector(".sub")?.remove();
      if (far.has(lanSelectedURL)) who.append(el("div", "sub", listed
        ? t("An internet address: only the addresses under Allowed addresses get in")
        : t("An internet address: anyone online can reach magpie here with a gateway key. To keep it to this network, list its range under Allowed addresses")));
    };
    controls.append(segs([["openai", "OpenAI"], ["anthropic", "Anthropic"]], lanProtocol,
      (v) => { lanProtocol = v; update(); }), address, copyControl);
    const addressRow = row(t("Address"), "", "", controls);
    addressRow.classList.add("lan-address-row");
    update();
  }
  if (sub) row(head || t("In a container"), sub, "").classList.add("lan-container");
}

// renderWebPage: Browser access, the app's own page served to a browser
// (apppage.go; jiakun_zhao on X managed magpie through a Cloudflare tunnel
// with `magpie web` kept running in a terminal beside the app). Off, this
// computer alone (for a tunnel or an SSH forward to reach), or the local
// network. The page always asks for its key: the link carries it, and a
// browser without it gets nothing. The app's windows alone draw it; a
// browser on the page can't turn it off under itself.
let webPageState = null, webPagePort = null;
// Each draw asks again, as a port another program held can come free; the
// answer draws without asking.
function renderWebPage(asked) {
  const head = $("#webPageHead"), box = $("#webPageList");
  if (!box) return;
  head.hidden = box.hidden = web;
  if (web) return;
  if (!asked) api("web-page").then((r) => { webPageState = r; renderWebPage(true); }).catch(() => {});
  const s = webPageState;
  if (!s) return;
  const set = (body) => writingPrefs(api("web-page", body))
    .then((r) => { webPageState = r; webPagePort = null; renderWebPage(true); })
    .catch((e) => { status(t(e.message), "err"); webPageState = null; renderWebPage(); });
  const rows = [];
  const row = (name, sub, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name));
    if (sub) who.append(sub instanceof Node ? sub : el("div", "sub", sub));
    const val = el("div", "val");
    val.append(...tools);
    r.append(who, val);
    rows.push(r);
    return r;
  };
  row(t("Open magpie in a browser"),
    t("The app serves this window’s page with a key in its link, as magpie web does, while it runs. For a tunnel such as Cloudflare Tunnel, point it at http://127.0.0.1:{port}", { port: s.port }),
    segs([["", t("Off")], ["local", t("This computer")], ["lan", t("Local network")]], s.mode || "", (mode) => set({ mode }))).classList.add("web-page-mode");
  if (s.mode) {
    const why = s.error ? el("div", "sub err", s.error.startsWith("port ") ? t("Port {p} is taken by another program, magpie web perhaps: magpie tries again every few seconds", { p: s.port }) : t(s.error))
      : el("div", "sub say", s.serving ? t("Served; anyone with this link can change every setting") : t("Starting…"));
    const links = [s.link, ...(s.mode === "lan" ? s.network || [] : [])].filter(Boolean);
    const box2 = el("div", "web-page-links");
    for (const l of links) {
      const line = el("span", "web-page-link");
      line.append(el("code", "", l.replace(/\?k=.*/, "?k=…")), copyBtn(l, t("Link"), t("Copied")));
      box2.append(line);
    }
    const rekey = el("button", "text", t("New key"));
    rekey.onclick = () => set({ newKey: true });
    row(t("Link"), why, box2, rekey).classList.add("web-page-link-row");
    const field = input(webPagePort ?? String(s.port), "3430");
    field.className = "words gateway-port";
    field.inputMode = "numeric";
    field.setAttribute("aria-label", t("Port"));
    const save = el("button", "text", t("Apply"));
    save.onclick = () => {
      const p = Number(field.value.trim());
      if (!Number.isInteger(p) || p < 1024 || p > 65535) return status(t("A port is a number from 1024 to 65535"), "err");
      set({ port: p });
    };
    field.oninput = () => { webPagePort = field.value; };
    field.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") save.onclick(); };
    row(t("Port"), "", field, save);
    if (s.mode === "lan" && !s.tls) {
      const r = row(t("Not encrypted"), t("Other computers reach the page over plain HTTP, so anyone on this network can read its key: on a network you don’t trust, keep it on This computer and reach it through a tunnel"));
      r.classList.add("web-page-warn");
    }
  }
  box.replaceChildren(...rows);
}
