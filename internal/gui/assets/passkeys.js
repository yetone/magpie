// Settings › Network and sharing › Passkeys, in a browser on magpie's page
// (magpie web, or the app's Browser access): passkeys that sign a browser
// in without the key pasted (weixiongbirnary on X: every new browser or
// computer needed the key copied again). The page signed in with the key
// adds one, named, lists them and removes one; the page turned away then
// offers "Sign in with passkey" (web.go). A passkey needs https at a name,
// or localhost: elsewhere this says so, and how. passkey.go keeps them.
// The gateway plugin's settings.js imports this in a browser alone, so it
// weighs nothing on the core.

(function () {
  const enc = (a) => {
    a = new Uint8Array(a);
    let s = "";
    for (let i = 0; i < a.length; i++) s += String.fromCharCode(a[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  };
  const dec = (s) => {
    s = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
    const a = new Uint8Array(s.length);
    for (let i = 0; i < s.length; i++) a[i] = s.charCodeAt(i);
    return a;
  };
  // why this page can't make a passkey, when it can't: an IP address is
  // no site to WebAuthn, and plain http on a network address is no secure
  // context (localhost is one); the docs' routes to https in a line
  function cant() {
    const h = location.hostname;
    if (/^[0-9.]+$/.test(h) || h.includes(":") || h.startsWith("[") || !window.isSecureContext) return t("Passkeys need https at a name: open this page through tailscale serve, a proxy with TLS, or MAGPIE_TLS_CERT");
    if (!window.PublicKeyCredential || !navigator.credentials?.create) return t("This browser has no passkeys");
    return "";
  }
  // a name for this browser's passkey, as "Chrome on macOS"
  function guess() {
    const ua = navigator.userAgent;
    const b = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "";
    const o = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
    return b && o ? t("{browser} on {os}", { browser: b, os: o }) : b || o;
  }
  const says = (m) => /took too long/.test(m) ? t("That took too long: try again")
    : /has this passkey already/.test(m) ? t("This passkey is added already") : t(m);

  let state = null, draftName = "", busy = false;
  window.renderPasskeys = function renderPasskeys(asked) {
    const head = $("#passkeyHead"), box = $("#passkeyList");
    if (!box) return;
    if (!asked) api("passkeys").then((r) => { state = r; renderPasskeys(true); }, () => { state = null; renderPasskeys(true); });
    // a redraw from elsewhere (another setting saved) never takes the
    // name field from under whoever types in it
    if (box.contains(document.activeElement) && document.activeElement.tagName === "INPUT") return;
    draw();
  };
  function draw() {
    const head = $("#passkeyHead"), box = $("#passkeyList");
    head.hidden = box.hidden = !state;
    if (!state) return;
    const row = (name, sub, ...tools) => {
      const r = el("div", "row pref");
      const who = el("div", "who");
      who.append(el("div", "name", name));
      if (sub) who.append(sub instanceof Node ? sub : el("div", "sub", sub));
      const val = el("div", "val");
      val.append(...tools);
      r.append(who, val);
      return r;
    };
    const rows = [];
    const no = cant();
    if (no) {
      rows.push(row(t("Passkeys"), el("div", "sub passkey-cant", no)));
    } else {
      const name = input(draftName, guess() || t("Name"));
      name.className = "words passkey-name";
      name.maxLength = 60;
      name.spellcheck = false;
      name.setAttribute("aria-label", t("Name"));
      name.oninput = () => { draftName = name.value; };
      const add = el("button", "text passkey-add", t("Add passkey"));
      add.disabled = busy;
      add.onclick = () => addPasskey(name.value.trim() || guess());
      name.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") add.onclick(); };
      const sub = state.runKey ? t("Lasts until magpie web restarts: set MAGPIE_WEB_KEY to keep passkeys") : t("Sign in without pasting the key");
      rows.push(row(t("Passkeys"), sub, name, add));
    }
    for (const p of state.passkeys || []) {
      let sub = t("Added {when}", { when: stamp(p.created) });
      if (p.used) sub += " · " + t("Used {when}", { when: ago(p.used) });
      if (p.origin && p.origin !== location.origin) sub += " · " + p.origin;
      const rm = el("button", "text passkey-remove", t("Remove"));
      rm.onclick = () => {
        rm.disabled = true;
        api("passkeys/remove", { id: p.id })
          .then((r) => {
            state = r;
            draw();
            // the browser's password manager may drop it too
            try { PublicKeyCredential.signalUnknownCredential?.({ rpId: location.hostname, credentialId: p.id }).catch(() => {}); } catch {}
          })
          .catch((e) => { rm.disabled = false; status(says(e.message), "err"); });
      };
      const r = row(p.name, sub, rm);
      r.classList.add("passkey-row");
      rows.push(r);
    }
    box.replaceChildren(...rows);
  }

  async function addPasskey(name) {
    if (busy) return;
    busy = true;
    draw();
    try {
      const o = await api("passkeys/options", {});
      const cred = await navigator.credentials.create({
        publicKey: {
          challenge: dec(o.challenge),
          rp: o.rp,
          user: { ...o.user, id: dec(o.user.id) },
          pubKeyCredParams: o.pubKeyCredParams,
          excludeCredentials: (o.excludeCredentials || []).map((c) => ({ type: c.type, id: dec(c.id) })),
          authenticatorSelection: o.authenticatorSelection,
          attestation: o.attestation,
          timeout: o.timeout,
        },
      });
      if (!cred) return;
      state = await api("passkeys/add", {
        name,
        id: cred.id,
        clientDataJSON: enc(cred.response.clientDataJSON),
        attestationObject: enc(cred.response.attestationObject),
      });
      draftName = "";
      status(t("Passkey added"), "ok");
    } catch (e) {
      // the user closed the browser's sheet: nothing to say
      if (e?.name === "NotAllowedError" || e?.name === "AbortError") return;
      status(e?.name === "InvalidStateError" ? t("This passkey is added already") : says(e?.message || String(e)), "err");
    } finally {
      busy = false;
      draw();
    }
  }
})();
