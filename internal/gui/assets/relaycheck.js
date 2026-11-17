// The Usage speed card's check on a provider's row (skingstyle48487 on X,
// with more than ten relays): one model on one provider checked with a
// few small real calls through the gateway (/api/relay-check), each with
// an answer known in advance: count to 40, three multiplications, two
// longer sums, three answers in an exact form, two questions a tool
// answers. What each found is said as found, a number or what came
// back, never a verdict on the provider: a model can miss a sum on one
// call, and a provider can count tokens its own way. The right
// answers are counted and kept (relayKeep), so the card sets each
// provider's last check beside the others'. The calls cost what they
// cost, so only the dialog's Check sends them. plugins/usage/page.js
// imports this on the first press.
// a prompt this short is a few dozen tokens with any tokenizer and any
// chat template; well past that, something was added or counted another way
const RELAY_PADDED = 300;

window.relayCheck = function (provider, model, providerName, modelName) {
  const ed = el("div", "editor relay-check");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Check {model} on {provider}", { model: modelName, provider: providerName })));
  const say = el("p", "lib-confirm", t("A few small real calls with known answers, billed as any other: write the numbers 1 to 40, work out sums, answer in an exact form, call the right tool. Each says what came back."));
  const list = el("div", "relay-parts");
  list.setAttribute("role", "status");
  list.hidden = true;
  const bar = el("div", "bar");
  const close = el("button", "text", t("Close"));
  close.onclick = (e) => { e.stopPropagation(); closeConfirmAsk(); };
  const go = el("button", "text primary", t("Check"));
  go.onclick = async (e) => {
    e.stopPropagation();
    go.disabled = true;
    go.textContent = t("Checking…");
    list.hidden = false;
    list.replaceChildren(el("div", "relay-wait faint", t("Checking…")));
    let r;
    try { r = await api("relay-check", { provider, model }); } catch (err) { r = { error: t(err.message) }; }
    if (!r.error) relayKeep(provider, model, r);
    if (confirmAsk !== ed) return; // closed while it ran
    go.disabled = false;
    go.textContent = t("Check again");
    relayFill(list, r);
  };
  bar.append(el("span", "grow"), close, go);
  ed.append(head, say, list, bar);
  confirmAsk = ed;
  openModal(ed);
  $("#modal").classList.add("lib");
  go.focus({ preventScroll: true });
};

// relayTally counts a check's right answers apart from its right tool
// calls; a call that failed counts as asked and not right
function relayTally(r) {
  const n = { right: 0, of: 0, tools: 0, tools_of: 0 };
  for (const p of r.parts || []) {
    if (p.kind === "tool") { n.tools_of++; if (p.match) n.tools++; }
    else { n.of++; if (p.match) n.right++; }
  }
  return n;
}

// relayKeep keeps the tally of each provider's last check of a model, in
// this window's storage, for the speed card's Answers and Tool calls
// columns, and has the card drawn again
function relayKeep(provider, model, r) {
  try {
    const all = JSON.parse(localStorage.getItem("magpie.relayChecks") || "{}");
    all[provider + "\u0000" + model] = { ...relayTally(r), at: Date.now() };
    const keys = Object.keys(all).sort((a, b) => all[b].at - all[a].at);
    for (const k of keys.slice(200)) delete all[k];
    localStorage.setItem("magpie.relayChecks", JSON.stringify(all));
  } catch {}
  window.dispatchEvent(new CustomEvent("magpie-relay-checked"));
}

// the exact items by the id the gateway gives them
const RELAY_EXACT = { letters: "Count the r's in strawberry raspberry", primes: "List five primes after 50", sort: "Sort five words" };

function relayName(p) {
  if (p.kind === "count") return t("Count to 40");
  if (p.kind === "math") return t("Multiply {q}", { q: p.ask });
  if (p.kind === "steps") return t("Work out {q}", { q: p.ask });
  if (p.kind === "exact") return RELAY_EXACT[p.ask] ? t(RELAY_EXACT[p.ask]) : p.ask;
  return t("Call {tool}", { tool: p.ask });
}

// relayFill says how many answers were right, then each part: its name,
// whether the answer was the one asked for, and what it measured
function relayFill(list, r) {
  if (r.error) return list.replaceChildren(el("div", "relay-part err", r.error));
  const n = relayTally(r);
  const sum = el("div", "relay-sum");
  sum.append(el("span", "", t("Answers right: {n} of {m}", { n: n.right, m: n.of })), el("span", "", t("Right tool calls: {n} of {m}", { n: n.tools, m: n.tools_of })));
  list.replaceChildren(sum, ...(r.parts || []).map((p) => {
    const row = el("div", "relay-part");
    const top = el("div", "relay-top");
    const mark = el("span", "relay-mark " + (p.match ? "ok" : p.ok ? "off" : "err"));
    mark.setAttribute("aria-hidden", "true");
    const said = p.match ? t("Expected answer") : p.ok ? t("Unexpected answer") : t("Failed");
    top.append(mark, el("span", "relay-name", relayName(p)), el("span", "grow"), el("span", "relay-said" + (p.match ? " faint" : ""), said));
    row.append(top);
    const lines = [];
    if (!p.ok) lines.push([(p.status ? p.status + " · " : "") + (p.error || ""), "err"]);
    else if (!p.match) {
      if (p.kind === "tool" && !/^get_\w+\(/.test(p.got)) lines.push([t("Answered in text, without calling a tool"), ""]);
      lines.push([t("Got: {text}", { text: p.got }), "mono"]);
    }
    if (p.served) lines.push([t("The reply named {model}", { model: p.served }), "note"]);
    if (p.by) lines.push([t("Answered by {provider}, after this one failed", { provider: p.by }), "note"]);
    if (p.kind === "count" && p.input) {
      lines.push([t("A {n}-character prompt was counted as {m} input tokens", { n: p.prompt_chars, m: ledNum(p.input) }), p.input > RELAY_PADDED ? "note" : "faint"]);
      if (p.input > RELAY_PADDED) lines.push([t("More than a prompt this short holds: the provider may add instructions of its own, or count another way"), "faint"]);
    }
    // the count writes the most, so its speed is the one worth reading;
    // a call not answered as asked says how long it took too
    const speed = [];
    if (p.kind === "count" || !p.match) {
      if (p.ttft_ms) speed.push(t("First token {took}", { took: ledTook(p.ttft_ms) }));
      if (p.tps) speed.push(t("{n} tok/s", { n: ledNum(Math.round(p.tps)) }));
      if (p.ms) speed.push(t("{took} in all", { took: ledTook(p.ms) }));
    }
    if (speed.length) lines.push([speed.join(" · "), "faint"]);
    for (const [text, cls] of lines) {
      const line = el("div", "relay-line" + (cls ? " " + cls : ""), text);
      line.title = text;
      row.append(line);
    }
    return row;
  }));
}
