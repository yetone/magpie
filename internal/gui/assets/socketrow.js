// A provider editor's WebSocket row under Advanced (#5781): its tick
// sends the Responses turns over the Responses WebSocket. Ticked it saves
// auto, or keeps the CLI's on; unticked it saves "", HTTP alone. OpenAI's
// own API goes over it unset (provider.SocketByDefault, KawaiiZapic on
// X), so there it opens ticked, saves off unticked and "" ticked again.
// app.js imports this when it draws the editor, so it weighs nothing on
// the core; show is handed the row's showSocket.
window.socketRow = function ({ p, draft, at, socketable, addressOf, show }) {
  const def = () => /^\s*https:\/\/api\.openai\.com(?:[:/]|\s*$)/i.test(addressOf("responses"));
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
