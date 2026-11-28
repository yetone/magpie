// The provider editor's models by maker, the newest first (WJaunita45604
// on X: 供应商的模型里面应该支持默认按厂商分类，并且按模型里的数字从大到小排序).
// Each maker's models stand under its name, a plain small label, makers in
// the order below; one whose maker magpie doesn't know goes under Other,
// last, in the vendor's order. The maker is the server's (modelJSON.Maker: agent.ModelIcon, the
// logo the agents' pickers draw beside it). A list of one maker has no
// labels. Within a maker the version its name holds decides, compared
// number by number (5.10 over 5.9, 5 over 4.8): a higher version first,
// a release before its preview, the alias before its dated snapshots, a
// later snapshot first, and the vendor's order between the rest. Only the order drawn changes: the 24 a vendor's list
// serves with none picked are still its first 24.
// advgroups.js imports it as the Providers page first draws, which an
// editor waits for, and app.js when it draws a provider's models without
// it (they show in the vendor's order for that moment), and an agent's
// model list as it opens (makerGroups); it weighs nothing on the core.
// It also holds a model's API menu (modelAPIPick), opened from a chip's
// right-click.

// the makers, in the order their groups go, by the logo the server names
const MAKERS = {
  "claude-color": "Anthropic", openai: "OpenAI", "gemini-color": "Google", "deepseek-color": "DeepSeek", xai: "xAI",
  "qwen-color": "Qwen", kimi: "Moonshot AI", "zhipu-color": "Zhipu", "minimax-color": "MiniMax", "mistral-color": "Mistral",
  mimocode: "Xiaomi", stepfun: "StepFun",
};
const RANK = Object.keys(MAKERS);

// versionOf reads what catalog.FamilyOf reads off a name: its version
// numbers, its snapshot (a date or revision after them) and whether it is
// a preview. A size (70b, a22b, 32k) is no version.
const NUM = /^\d+(?:\.\d+)*$/, TAIL = /^([a-z][a-z.]*?[a-z])(\d+(?:\.\d+)*)$|^([a-z])(\d+(?:\.\d+)*)$/, HEAD = /^(\d+(?:\.\d+)*)([a-z]+)$/;
const SIZE = /^(?:[a-z]?\d+(?:\.\d+)?|\d+x\d+(?:\.\d+)?)[bmkte]$/, PREVIEW = new Set(["preview", "exp", "experimental", "beta", "alpha"]);
const versions = new Map();
function versionOf(name) {
  let f = versions.get(name);
  if (f) return f;
  const w = name.toLowerCase().trim().split(/[-_/:@[\]\s]+/), snap = [];
  f = { v: [], snap: "", pre: false };
  w.forEach((x, i) => { if (/^\d+$/.test(x) && (x.length >= 4 || (x.length >= 2 && x[0] === "0"))) snap[i] = true; });
  // the day after a month (05-20), the month before a year (12-2024)
  w.forEach((x, i) => { if (!snap[i] && /^\d\d$/.test(x) && (snap[i - 1] || /^(?:19|20)\d\d$/.test(w[i + 1] || ""))) snap[i] = true; });
  const add = (s) => f.v.push(...s.split(".").map((n) => parseInt(n, 10) || 0));
  w.forEach((x, i) => {
    let m;
    if (!x || x === "latest") return;
    if (snap[i]) f.snap += (f.snap ? "-" : "") + x;
    else if (PREVIEW.has(x)) f.pre = true;
    else if (NUM.test(x)) add(x);
    else if (SIZE.test(x)) return;
    else if ((m = TAIL.exec(x))) add(m[2] || m[4]);
    else if ((m = HEAD.exec(x))) add(m[1]);
  });
  versions.set(name, f);
  return f;
}

// newer: below 0 when a goes before b
function newer(a, b) {
  for (let i = 0; i < Math.min(a.v.length, b.v.length); i++) if (a.v[i] !== b.v[i]) return b.v[i] - a.v[i];
  if (a.v.length !== b.v.length) return b.v.length - a.v.length;
  if (a.pre !== b.pre) return a.pre ? 1 : -1;
  if (!a.snap !== !b.snap) return a.snap ? 1 : -1;
  return a.snap === b.snap ? 0 : a.snap > b.snap ? -1 : 1;
}

const makerOf = (m) => (RANK.includes(m.maker) ? m.maker : "");

// byMaker: models by maker, then the newest first
function byMaker(models) {
  const rank = (m) => (makerOf(m) ? RANK.indexOf(m.maker) : RANK.length);
  // the name shown, else its id, where it says a version
  const ver = (m) => { const a = versionOf(m.name || m.id); return a.v.length || !m.name ? a : versionOf(m.id); };
  // Other stays in the vendor's order: its numbers are different makers'
  return models.map((m, i) => [m, rank(m), makerOf(m) && ver(m), i]).sort((a, b) => a[1] - b[1] || (a[2] && newer(a[2], b[2])) || a[3] - b[3]).map((x) => x[0]);
}

// modelOrder: models in the order drawn, and put(chip, model), which
// appends a chip to chips under its maker's label
window.modelOrder = function (models, chips) {
  const list = byMaker(models);
  const many = new Set(models.map(makerOf)).size > 1;
  let last;
  return {
    list,
    put(c, m) {
      const k = makerOf(m);
      if (many && k !== last) chips.append(el("div", "mmaker", k ? t(MAKERS[k]) : t("Other")));
      last = k;
      chips.append(c);
    },
  };
};

// modelAPIPick: the API a model is asked on (01huadalang), picked from a
// chip's right-click and staged as Model settings stages it
window.modelAPIPick = function (c, p, id, apis, apiNow, draw) {
  const m = p.models.find((x) => x.id === id) || { id };
  const auto = (m.auto || []).map(apiLabel).join(" · ");
  const opts = [{ v: "", name: "Auto", note: auto ? t("As its vendor's list says: {apis}", { apis: auto }) : "Each API the provider has, as it answers" },
    ...apis.map(([k, l, hint]) => ({ v: k, name: l, note: hint }))];
  openProtoMenu(c, opts, apiNow(id), (v) => {
    if (v === apiNow(id)) return;
    const prefs = draft.modelPrefs = draft.modelPrefs || {};
    prefs[id] = prefs[id] || {};
    if (v === (m.api || "")) delete prefs[id].api; else prefs[id].api = v;
    if (!Object.keys(prefs[id]).length) delete prefs[id];
    draw();
    status(v ? t("{model} is asked on {api} once saved", { model: id, api: apiLabel(v) }) : t("{model} is asked as its vendor's list says once saved", { model: id }), "ok");
  }, "API this model is asked on", "model-api-menu");
};

// makerGroups: an agent's model list by maker, as the editor has a
// provider's (elis_pina on X: 给Agent配置模型的那里也加一个分类): a group
// a maker, in the editor's order, named and drawn by its logo, its models
// the newest first, each provider's together where they tie. A group's
// name is its key, apart from any provider's.
window.makerGroups = function (models) {
  const out = [];
  for (const m of byMaker(models)) {
    const k = makerOf(m);
    if (out.at(-1)?.key !== k) out.push({ key: k, name: "\0" + k, label: k ? t(MAKERS[k]) : t("Other"), icon: k, models: [] });
    out.at(-1).models.push(m);
  }
  return out;
};
