// rotateFigure: In turn, drawn: two conversations, three of the user's
// messages each (a pill naming the account it went to), each followed by
// the agent's tool calls (dots in that account's shade). As Stays has it
// (gateway affine): Auto and Within a turn move each message on, Session
// and Strict keep a conversation on one, Off moves every request on.
// Wing8Aqua on X: the words alone left the unit that goes round unclear.
// app.js's drawRotFig imports this when In turn is first shown, so it
// weighs nothing on the core.
window.rotateFigure = function (affinity) {
  const NS = "http://www.w3.org/2000/svg";
  const mk = (tag, attrs, text) => {
    const n = document.createElementNS(NS, tag);
    for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
    if (text) n.textContent = text;
    return n;
  };
  const s = mk("svg", { viewBox: "0 0 232 70", width: 232, height: 70, class: "rot-fig", "aria-hidden": "true" });
  s.dataset.affinity = affinity || "";
  const whole = affinity === "session" || affinity === "strict";
  const each = affinity === "off";
  for (let row = 0; row < 2; row++) {
    const y = 4 + row * 26;
    s.append(mk("rect", { x: 0.5, y: y + 0.5, width: 231, height: 21, rx: 10.5, class: "rot-track" }));
    let n = row; // a new conversation starts one further along
    for (let m = 0; m < 3; m++) {
      const x = 6 + m * 75, at = whole ? row : each ? n : row + m;
      const g = mk("g", { class: "rot-msg" });
      g.append(mk("line", { x1: x + 22, y1: y + 11, x2: x + 50, y2: y + 11, class: "rot-link" }));
      g.append(mk("rect", { x, y: y + 4, width: 22, height: 14, rx: 7, class: "rot-acct rot-" + "abc"[at % 3] }));
      g.append(mk("text", { x: x + 11, y: y + 14.5, "text-anchor": "middle", class: "rot-l rot-" + "abc"[at % 3] }, "ABC"[at % 3]));
      if (each) n++;
      for (let d = 0; d < 2; d++) {
        const k = each ? n++ : at;
        g.append(mk("circle", { cx: x + 34 + d * 10, cy: y + 11, r: 3, class: "rot-call rot-" + "abc"[k % 3] }));
      }
      g.dataset.to = "ABC"[at % 3];
      s.append(g);
    }
  }
  // what a pill and a dot are
  const ly = 65;
  s.append(mk("rect", { x: 0.5, y: ly - 7, width: 14, height: 9, rx: 4.5, class: "rot-key" }));
  const msg = mk("text", { x: 19, y: ly, class: "rot-legend" }, t("Your message"));
  s.append(msg);
  s.append(mk("circle", { cx: 122, cy: ly - 2.5, r: 2.5, class: "rot-key" }));
  s.append(mk("circle", { cx: 130, cy: ly - 2.5, r: 2.5, class: "rot-key" }));
  s.append(mk("text", { x: 138, y: ly, class: "rot-legend" }, t("Tool calls")));
  return s;
};
