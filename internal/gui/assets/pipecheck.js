// Settings › Models' Quick check (EV_John_Liu on X): each pipeline besides
// chat checked with one real call through the gateway (/api/pipelines/
// check): a one-dot drawing, a second of silence to transcribe, a word to
// speak, one web search. Each costs what the call costs, so only a press
// runs one, never a draw. app.js imports this when Settings draws Models.

const pipes = [["image", "Image generation"], ["transcription", "Speech to text"], ["speech", "Text to speech"], ["search", "Web search"]];
const pipeChecks = {}; // kind → { busy } or what the check found

function pipeRun(kind) {
  if (pipeChecks[kind]?.busy) return;
  pipeChecks[kind] = { busy: true };
  pipeCheckFill();
  api("pipelines/check", { kind })
    .then((r) => { pipeChecks[kind] = r; }, (e) => { pipeChecks[kind] = { error: t(e.message) }; })
    .finally(pipeCheckFill);
}

// pipeSaid: what a check found, and whether it is a failure
function pipeSaid(c) {
  if (!c || c.busy) return ["", false];
  if (c.ok) return [(c.model ? c.model + " · " : "") + ledTook(c.ms), false];
  if (c.unset) return [t(c.error), false];
  return [(c.model ? c.model + " · " : "") + (c.status ? c.status + " · " : "") + t(c.error || ""), true];
}

// pipeCheckFill draws the rows into #pipeCheckList, once, and then each
// row's state in place, so a press keeps its focus
window.pipeCheckFill = function () {
  const box = document.getElementById("pipeCheckList");
  if (!box) return;
  const all = document.getElementById("pipeCheckAll");
  all.onclick = () => pipes.forEach(([k]) => pipeRun(k));
  all.disabled = pipes.every(([k]) => pipeChecks[k]?.busy);
  if (!box.children.length) {
    for (const [kind, name] of pipes) {
      const row = el("div", "row pref pipe-check");
      row.dataset.pipe = kind;
      const who = el("div", "who");
      const line = el("div", "sub say pipe-said");
      line.setAttribute("role", "status");
      who.append(el("div", "name", t(name)), line);
      const b = el("button", "text pipe-test");
      b.type = "button";
      b.onclick = () => pipeRun(kind);
      row.append(who, b);
      box.append(row);
    }
  }
  for (const row of box.children) {
    const c = pipeChecks[row.dataset.pipe];
    const b = row.querySelector(".pipe-test"), line = row.querySelector(".pipe-said");
    b.textContent = c?.busy ? t("Testing…") : t("Test");
    b.disabled = !!c?.busy;
    const [text, bad] = pipeSaid(c);
    line.hidden = !text;
    line.textContent = line.title = text;
    line.classList.toggle("err", bad);
  }
};
