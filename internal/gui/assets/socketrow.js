// A provider editor's WebSocket row under Advanced (#5781): its tick
// sends the Responses turns over the Responses WebSocket. Ticked it saves
// auto, or keeps the CLI's on; unticked it saves "", HTTP alone. A key's
// provider at an https Responses address goes over it unset, OpenAI's own
// API and relays alike (provider.SocketByDefault, KawaiiZapic on X), but
// another magpie (remote); there it opens ticked, saves off unticked and
// "" ticked again. app.js imports this when it draws the editor, so it
// weighs nothing on the core; show is handed the row's showSocket.
window.socketRow = function ({ p, draft, at, socketable, addressOf, remote, show }) {
  const def = () => !remote() && /^\s*https:\/\/[^\s/?#]/i.test(addressOf("responses"));
  const [stk, scb] = tick(t("Send Responses turns over WebSocket"), draft.socket ? draft.socket !== "off" : def());
  stk.classList.add("socket-tick");
  scb.onchange = () => { draft.socket = scb.checked ? (p?.socket === "on" ? "on" : def() ? "" : "auto") : def() ? "off" : ""; };
  const srow = field(t("WebSocket"), stk);
  at.replaceWith(...srow);
  show(() => {
    for (const e of srow) e.style.display = socketable() ? "" : "none";
    if (!draft.socket) scb.checked = def();
  });
};
