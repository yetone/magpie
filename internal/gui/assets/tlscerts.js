// A provider editor's certificate rows under Advanced, for an https://
// address: Client certificate (#5596, mTLS), and Server certificate
// (Zhangzhangi on X), the pin it trusts, shown as off, or the certificate
// the server shows, to trust after checking its fingerprint. app.js
// imports this when it draws the editor, so it weighs nothing on the core.
// colonHex is a SHA-256 pin as fingerprints are read: AB:CD:…
const colonHex = (h) => (String(h).toUpperCase().match(/../g) || []).join(":");
function drawPin(pin, draft, peek, seen) {
  const again = (s) => drawPin(pin, draft, peek, s);
  pin.replaceChildren();
  if (draft.serverPin) {
    const off = el("div", "pin-off", t("Certificate not verified: only this one is accepted"));
    const fp = el("code", "pin-fp", colonHex(draft.serverPin));
    const drop = el("button", "text action pin-drop", t("Verify it again"));
    drop.onclick = (e) => { e.preventDefault(); draft.serverPin = ""; again(); };
    pin.append(off, fp, drop);
    return;
  }
  if (!seen) {
    const go = el("button", "text action pin-peek", t("Trust its certificate…"));
    go.onclick = async (e) => {
      e.preventDefault();
      go.classList.add("busy");
      go.disabled = true;
      try { again(await peek()); } catch (err) {
        again();
        pin.append(el("div", "hint err", err.message));
      } finally { go.classList.remove("busy"); }
    };
    pin.append(go);
    return;
  }
  const card = el("div", "pin-seen");
  card.append(el("code", "pin-fp", colonHex(seen.sha256)));
  const until = new Date(seen.notAfter).toLocaleDateString(intlLang(), { year: "numeric", month: "short", day: "numeric" });
  card.append(el("div", "hint", t("{subject}, issued by {issuer}, until {date}", { subject: seen.subject || "—", issuer: seen.issuer || "—", date: until })));
  if (seen.checks) card.append(el("div", "hint", t("This computer already trusts it: nothing to pin.")));
  else card.append(el("div", "hint err", seen.why));
  const acts = el("div", "pin-acts");
  if (!seen.checks) {
    const yes = el("button", "text action pin-trust", t("Trust this certificate"));
    yes.onclick = (e) => { e.preventDefault(); draft.serverPin = seen.sha256; again(); };
    acts.append(yes);
  }
  const no = el("button", "text action pin-cancel", t("Cancel"));
  no.onclick = (e) => { e.preventDefault(); again(); };
  acts.append(no);
  card.append(acts);
  pin.append(card);
}

// tlsCertRows draws the rows where app.js left at. Server certificate is
// for an https:// address at a host the user chose (provider.PinHosts):
// every one of theirs and Remote magpie's, and a preset's pointed at their
// own server, never the vendor's own hosts; save has the Save body carry
// the pin. The client certificate's fields are saved by app.js itself.
window.tlsCertRows = function ({ p, pr, draft, at, certTakes, onHTTPS, addressOf, asTyped, show, save }) {
  if (!at.isConnected) return;
  const rows = [];
  const takes = certTakes() || (p ? !!p.takesServerPin || (!!p.preset && !p.account) : !!pr) && (p?.preset || pr?.id) !== "google-vertex";
  if (takes) rows.push(pinRow({ p, pr, draft, certTakes, addressOf, asTyped, show, save }));
  if (certTakes()) {
    const box = el("div", "client-cert");
    for (const [k, label, hold] of [["clientCert", t("Certificate"), "client.pem"], ["clientKey", t("Key"), "client-key.pem"], ["serverCA", t("Server CA"), "ca.pem"]]) {
      const i = input(draft[k] || "", "~/magpie-laptop/" + hold);
      i.className = "words cert-" + k;
      i.setAttribute("aria-label", label);
      i.oninput = () => { draft[k] = i.value; };
      box.append(i);
    }
    const crow = field(t("Client certificate"), box, t("PEM files, for a server that asks for one (mTLS)"));
    rows.push(crow);
    show(() => { for (const e of crow) e.style.display = onHTTPS() ? "" : "none"; });
  }
  at.replaceWith(...rows.flat());
};

function pinRow({ p, pr, draft, certTakes, addressOf, asTyped, show, save }) {
  const host = (u) => { try { return new URL(String(u).trim()).hostname.toLowerCase(); } catch { return ""; } };
  const own = new Set([pr?.chat, pr?.responses, pr?.anthropic, pr?.decide, ...(pr?.regions || []).flatMap((r) => [r.chat, r.responses, r.anthropic])].filter(Boolean).map(host));
  const addrs = () => {
    const us = ["chat", "responses", "anthropic", "gemini", "decide"].map(addressOf).filter((u) => /^\s*https:\/\//i.test(u));
    return certTakes() ? us : us.filter((u) => host(u) && !own.has(host(u)));
  };
  const pin = el("div", "server-pin");
  drawPin(pin, draft, () => api("provider/peekcert", { ...asTyped(), id: p?.id || "", preset: draft.preset || "" }));
  const row = field(t("Server certificate"), pin, t("For your own server with a self-signed certificate. Check the fingerprint against the server's before you trust it; a certificate renewed later has to be trusted again."));
  show(() => { for (const e of row) e.style.display = addrs().length || draft.serverPin ? "" : "none"; });
  save((body) => { if (addrs().length || draft.serverPin) body.serverPin = addrs().length ? (draft.serverPin || "").trim() : ""; });
  return row;
}
