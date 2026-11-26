// Settings' search, and what it finds on the other pages (canlee886 on X):
// a routing group's strategy, a provider's proxy or an agent are set on
// their own pages, and a search for one in Settings said no setting
// matched. Each page's places that match are listed after Settings' own
// rows, under the page's name, and Open goes to that page and to the
// place, lit. ⌘K (Ctrl+K off the Mac) and the header's magnifier bring the
// search up from any page (app.js findAnywhere). app.js imports this the
// first time a search runs, so it weighs nothing on the core.

// What a search is about (canlee886 on X: "可访问性", "监控" and
// "自动选择模型" found nothing, "缺少搜索时的智能联想"). A concept's words
// in each language lead to the English names of the settings it covers (a
// setting's English is in its data-en in every language), listed with the
// settings that have the words themselves. When nothing has them, a long
// run of Chinese or Japanese is read by its two-character words and a
// Latin word may have a typo, and with no setting that has the words, the
// line over the parts says "Settings related to “…”". All of it is here,
// in the page: nothing is sent anywhere.
// Each line: the words a reader may type (_ joins a phrase's words) | the
// names they lead to.
const CONCEPTS = [
  "accessibility a11y 可访问性 无障碍 辅助功能 可訪問性 無障礙 輔助使用 協助工具 アクセシビリティ 見やすさ barrierefrei bedienungshilfe contrast low_vision 对比度 对比 看不清 视力 對比 視力 コントラスト 見えにくい kontrast sehschwäche | text size|increase contrast|reduce motion|colorblind|appearance|interface font|code font",
  "theme dark_mode light_mode darkmode 深色 暗色 夜间 浅色 主题 外观 皮肤 主題 外觀 深色模式 ダーク ライト テーマ 外観 dunkel hell erscheinungsbild | appearance|accent color|background image",
  "zoom font_size bigger smaller 字体 字號 字号 放大 缩小 缩放 字體 縮放 フォント 拡大 縮小 schrift vergrößern | text size|interface font|code font",
  "animation 动画 动效 動畫 アニメーション animation | reduce motion|loading bird",
  "monitor metrics telemetry tracing trace logging grafana prometheus jaeger 监控 监测 遥测 指标 追踪 链路 日志 監控 監測 遙測 追蹤 監視 モニタ テレメトリ トレース überwach metrik | observability|opentelemetry|otel|requests",
  "auto_model autoselect auto_select auto_switch model_routing smart_routing 自动选择 自动选模型 自动切换 自动路由 智能路由 模型路由 智能选择 自動選擇 自動切換 智慧路由 自動選択 自動切替 モデル選択 modellwahl modellauswahl automatische_auswahl | routing|smart|system one|busy models last",
  "notify notification alert remind 通知 提醒 告警 预警 提示 警報 預警 アラート リマインド benachrichtig erinner | alert|reminder|bark",
  "startup launch_at autostart boot 开机 自启 启动 開機 自啟 啟動 起動 自動起動 systemstart | open at login",
  "vpn socks 代理 翻墙 梯子 科学上网 プロキシ | proxy|proxies",
  "sleep awake caffeinate 休眠 睡眠 待机 待機 スリープ ruhezustand | prevent sleep",
  "language locale 语言 中文 英文 語言 言語 sprache | language",
  "upgrade version 升级 版本 升級 バージョン aktualisier | app updates|about",
  "backup export import restore 备份 导出 导入 恢复 備份 匯出 匯入 還原 バックアップ 復元 sicherung | sync and backup|backup",
  "redact desensitiz sensitive secret password pii 脱敏 隐私 敏感 密码 掩码 去敏 隱私 遮罩 密碼 マスク 秘密 パスワード maskier datenschutz | privacy|what to mask|masking",
  "lan remote phone mobile tailscale 局域网 远程 共享 手机 内网 區域網 遠端 共用 手機 リモート 共有 スマホ fernzugriff | sharing|local network|browser access|another magpie|network and sharing",
  "cost money price spend billing 费用 花费 价格 账单 金额 花了多少 費用 價格 帳單 料金 価格 kosten preis | currency|menu bar cost|price rate|totals in|low balance alert",
  "quota rate_limit 额度 配额 限额 余量 限流 額度 配額 クォータ 上限 kontingent | allowance|requests per minute|rate limited|concurrency",
  "tray menubar menu_bar status_bar 托盘 菜单栏 状态栏 系統匣 選單列 狀態列 メニューバー トレイ menüleiste | menu bar|tray|bar icon",
  "hotkey shortcut keybind keyboard 快捷键 热键 快捷鍵 熱鍵 ショートカット tastenkürzel | quick panel shortcut",
  "api_key apikey token 密钥 秘钥 令牌 金鑰 キー schlüssel | api key|gateway keys|accounts",
  "web_search 联网 搜索引擎 網路搜尋 ウェブ検索 websuche | web search",
  "image_generat picture 生图 画图 图片 图像 繪圖 圖片 画像 bild | images|background image",
  "context_length long_context 上下文 压缩 長度 コンテキスト kontext | longest context|context window|compact at|context",
  "timeout latency 超时 延迟 逾時 延遲 タイムアウト zeitüberschreitung | slow to start|speed first",
  "disable_model hide_model 禁用模型 隐藏模型 停用模型 屏蔽模型 隱藏模型 モデル無効 | disabled models",
  "checkin check-in 签到 簽到 チェックイン | check in|check-in",
  "warmup warm-up 预热 重置 預熱 ウォームアップ | warm up|resets|reset reminder",
  "memory ram usage performance battery 内存 性能 省电 耗电 卡顿 記憶體 效能 メモリ 省電力 speicher akku | lightweight mode",
  "sidebar navigation 侧边栏 侧栏 导航 側邊欄 導覽 サイドバー seitenleiste | sidebar pages",
  "history chat_log 历史 记录 聊天记录 歷史 紀錄 履歴 verlauf | session history|sessions",
  "terminal iterm ghostty 终端 終端 ターミナル | session terminal",
].map((l) => {
  const [say, to] = l.split(" | ");
  return { say: say.split(" ").map((w) => w.replace(/_/g, " ")), to: to.split("|") };
});
const CJK = /[\u3040-\u30ff\u3400-\u9fff\uf900-\ufaff]/;
// a concept's word in what was typed: Chinese or Japanese anywhere in it, a
// Latin word at a word's start (monitor finds monitoring; "ram" is not in
// "program"), a short one whole ("lan" is not "language"), and a word
// still being typed as its start ("accessib")
function conceptsOf(q) {
  const at = " " + q.replace(/[^\p{L}\p{N}]+/gu, " ") + " ";
  const last = q.split(/\s+/).pop();
  return CONCEPTS.filter((c) => c.say.some((w) => CJK.test(w) ? q.includes(w) || (last.length > 1 && w.startsWith(last))
    : at.includes(" " + w + (w.length > 3 ? "" : " ")) || (last.length > 3 && w.startsWith(last))));
}
// one typo: a letter more, less or other
function near1(a, b) {
  if (Math.abs(a.length - b.length) > 1) return false;
  let i = 0, j = 0, e = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) { i++; j++; continue; }
    if (++e > 1) return false;
    if (a.length > b.length) i++;
    else if (b.length > a.length) j++;
    else { i++; j++; }
  }
  return e + (a.length - i) + (b.length - j) <= 1;
}
// a word's pieces: a run of Chinese or Japanese longer than two by its
// two-character words, anything else whole
const pieces = (w) => !CJK.test(w) || w.length <= 2 ? [w] : [...w].slice(0, -1).map((c, i) => c + w[i + 1]);
const piecesIn = (ps, text) => {
  let toks = null;
  return ps.filter((p) => text.includes(p) || (p.length > 3 && !CJK.test(p) && (toks ||= text.split(/[^\p{L}\p{N}]+/u)).some((x) => near1(p, x)))).length;
};
// findQuery: what a place must say for the words. By default the words
// themselves (every one in a place's text) or, by its name, a setting a
// concept in them is about; "words" the words alone; "near" a concept's
// settings or most of the words' pieces, by name. test(text, name) is
// whether a place matches; any(text) whether some of what is asked is there
function findQuery(q, mode = "") {
  const words = q.split(/\s+/).filter(Boolean);
  const to = mode === "words" ? [] : conceptsOf(q).flatMap((c) => c.to);
  const about = (name) => to.some((x) => name.includes(x));
  if (mode !== "near") return { key: mode + q, test: (text, name = text) => words.every((w) => text.includes(w)) || about(name), any: (text) => words.some((w) => text.includes(w)) || about(text) };
  const ps = words.flatMap(pieces);
  const loose = (name) => {
    const n = piecesIn(ps, name);
    return n > 0 && n * 2 >= ps.length;
  };
  return { key: "~" + q, near: true, test: (text, name = text) => about(name) || loose(name), any: (text) => about(text) || piecesIn(ps, text) > 0 };
}
window.findQuery = findQuery;

// Settings' own rows matching the words, shown or English, by their text,
// section heading or part name (sgfvdsg86265 on X); Escape restores
const setText = (n) => [n.textContent, ...[...n.querySelectorAll("[data-en]")].map((e) => e.dataset.en)].join(" ").toLowerCase();
// a row's name, shown and in English (the name's own data-en)
const nameText = (n) => {
  const e = n.querySelector(".name") || n.querySelector(".who") || n;
  return setText(e) + " " + (e.dataset.en || "").toLowerCase();
};
// a row found: shown where it is, not under a hidden part of the page, a
// built-in switched off or a provider not there
function rowShows(n, page) {
  for (let a = n; a && a !== page; a = a.parentElement) if (a.hidden) return false;
  if (featureHides(n)) return false;
  const ps = n.agents && (providers || providersEarly?.got)?.providers;
  return !(ps && !ps.some((x) => n.agents.includes(x.account?.agent)));
}
// each part's rows the query finds, and whether each matched at all
function setRows(fq) {
  const out = [];
  for (const id of SET_TABS) {
    const page = $("#setPage-" + id), tab = $("#setTab-" + id);
    const part = setText(tab) + (id === "otel" ? " otel" : ""); // not "usage" for Allowances
    let head = "";
    const rows = [];
    for (const n of page.querySelectorAll(".row-head, .row")) {
      if (n.matches(".row-head")) {
        head = setText(n);
        continue;
      }
      if (n.parentElement.closest(".row")) continue; // a row in a row goes with it
      const hit = fq.test(part + " " + head + " " + setText(n), part + " " + head + " " + nameText(n));
      rows.push([n, hit, hit && rowShows(n, page)]);
    }
    out.push([id, page, tab, rows]);
  }
  return out;
}
const setCount = (fq) => setRows(fq).reduce((s, [, , , rows]) => s + rows.filter((r) => r[2]).length, 0)
  + (window.setFeatsFind?.(fq, true) || 0) + elseFind(fq, true);
window.setFind = function () {
  if (!provLinksDrawn && setQuery) provLinksDraw();
  const v = $("#view-settings");
  v.classList.add("set-searching");
  // the words themselves, and the settings a concept in them is about;
  // their pieces, or a typo's word, when nothing has them. The line says
  // so when no setting has the words themselves.
  let fq = findQuery(setQuery), near = false;
  if (!setCount(fq)) fq = findQuery(setQuery, "near");
  if (!setCount(findQuery(setQuery, "words"))) near = setCount(fq) > 0;
  let any = false;
  for (const [, page, tab, rows] of setRows(fq)) {
    let hits = 0;
    const lit = new Set();
    for (const [n, hit, shows] of rows) {
      n.classList.toggle("set-miss", !shows);
      if (!shows) continue;
      hits++;
      lit.add(n.closest(".list") || n.parentElement);
    }
    // a section's heading and list go when nothing in them is left
    for (const box of page.querySelectorAll(".list")) {
      if (box.parentElement.closest(".row")) continue; // inside a row: it goes with the row
      box.classList.toggle("set-miss", ![...lit].some((l) => box.contains(l)));
    }
    for (const h of page.querySelectorAll(":scope > .row-head")) {
      const next = h.nextElementSibling;
      h.classList.toggle("set-miss", !next || next.classList.contains("set-miss"));
    }
    page.dataset.part = tab.textContent;
    page.hidden = !hits;
    any ||= hits > 0;
  }
  window.setFolds?.(v); // a hit under a folded Advanced (provlinks.js)
  // the built-ins that match (setfeats.js)
  if (window.setFeatsFind) any = setFeatsFind(fq) > 0 || any;
  else import("./setfeats.js").then(() => setQuery && setFind(), () => {});
  any = elseFind(fq) > 0 || any;
  const none = $("#setNone"), typed = $("#setQ").value.trim();
  none.hidden = any && !near;
  none.classList.toggle("set-near", near);
  none.textContent = near ? t("Settings related to “{q}”", { q: typed }) : t("No setting matches “{q}”", { q: typed });
};

// a place's words: its English and the reader's language, lower case
const say = (...s) => s.filter(Boolean).map((x) => x + " " + t(x)).join(" ").toLowerCase();
// the pages the sidebar has: one switched off is not offered
const pageOn = (v) => {
  const b = $(`#nav button[data-view="${v}"]`);
  return !!b && !b.hidden && !featureHides(b);
};
// a place found lit, once more on a second Open
function lightUp(n) {
  n.classList.remove("find-lit");
  void n.offsetWidth;
  n.classList.add("find-lit");
}
// to a page from Settings' search, Back coming back to the search
async function goTo(v) {
  jumpFrom(v);
  return show(v);
}
// at a place on a page now shown: in sight when the click asked, and lit
function landAt(n, go, v) {
  if (!n) return;
  if (go) { n.scrollIntoView({ block: "center" }); arrived($("#view-" + v)); }
  lightUp(n);
}
// a page's control drawn a moment after it is shown (a plugin page's tabs)
async function drawn(sel) {
  for (let i = 0; i < 40; i++) {
    const n = $(sel);
    if (n) return n;
    await new Promise((r) => setTimeout(r, 50));
  }
  return null;
}
const LIB_TABS = [["instructions", "Instructions"], ["memory", "Memory"], ["mcp", "MCP servers", "mcp"], ["skills", "Skills"], ["hooks", "Hooks"],
  ["env", "Environment variables", "env"], ["subagents", "Subagents"], ["plugins", "Claude plugins"]];
const USE_TABS = [["usage", "Overview", "allowances quota"], ["requests", "Requests", "ledger"], ["sessions", "Sessions"], ["context", "Context"]];
// what a provider's editor sets, and the words a reader may look for it by;
// when says whether this provider's editor has it
const PROV_FIELDS = [["Models", "model"], ["Accounts", "keys", (p) => p.account || p.keyList?.length], ["API key", "token", (p) => !p.account && !p.keyList?.length],
  ["Endpoints", "base url address", (p) => !p.account], ["Proxy"], ["Fallback"], ["Context window"], ["Max output", "tokens", (p) => !p.account],
  ["Concurrency"], ["Requests per minute", "rpm rate limit"], ["Price rate"], ["Free for me"], ["Headers", "", (p) => !p.account], ["Extra body", "", (p) => !p.account]];

// every place on the other pages: { page, key, name, sub, own, of, open }.
// own is the place's own words, of its parent's (a group's or a provider's
// name): a field of every group is listed only for words that say the
// field, not for a group's name alone
function places() {
  const out = [];
  for (const v of ["agents", "sessions", "library", "artifacts", "providers", "routing", "groups", "gateway", "usage", "masking", "plugins"]) {
    if (!pageOn(v)) continue;
    out.push({ page: v, key: v, name: t(pageName(v)), own: say(pageName(v)) + " " + v, open: () => goTo(v) });
  }
  if (pageOn("agents")) {
    for (const a of state.agents || []) out.push({ page: "agents", key: "a:" + a.id, name: a.name, own: (a.name + " " + a.id).toLowerCase(), open: async (go) => {
      if (await goTo("agents")) landAt($(`#agents .row.agent[data-id="${CSS.escape(a.id)}"]`), go, "agents");
    } });
    out.push({ page: "agents", key: "profiles", name: t("Profiles"), own: say("Profiles"), open: async (go) => { if (await goTo("agents")) landAt($("#view-agents .profiles"), go, "agents"); } });
  }
  const tabs = (v, list, sel) => {
    if (!pageOn(v)) return;
    for (const [id, name, more] of list) out.push({ page: v, key: v + ":" + id, name: t(name), own: say(name) + " " + (more || ""), open: async (go) => {
      if (!await goTo(v)) return;
      const b = await drawn(sel(id));
      if (!b) return;
      if (!b.classList.contains("on")) b.click();
      landAt($(sel(id)), go, v);
    } });
  };
  tabs("library", LIB_TABS, (id) => `#view-library .lib-tabs .opt[data-seg="${id}"]`);
  tabs("usage", USE_TABS, (id) => `#usageTab .opt[data-seg="${id}"]`);
  if (pageOn("providers")) {
    for (const p of providers?.providers || []) {
      const name = p.name || p.id;
      const open = (f) => async (go) => {
        if (f) advanced = { of: p.id, open: true }; // a field under Advanced is in sight
        jumpFrom("providers");
        await openProvider(p.id);
        if (view !== "providers") return;
        // the editor is drawn in the modal, and again as the list comes in:
        // the place is lit once the same one is there a moment later
        const at = () => {
          const ed = $("#modal .editor");
          return (f && ed && [...ed.querySelectorAll("label")].find((x) => x.textContent.trim().startsWith(t(f)))) || ed;
        };
        let n = await drawn("#modal .editor");
        for (let i = 0; i < 10 && n; i++) {
          await new Promise((r) => setTimeout(r, 150));
          const m = at();
          if (m === n) break;
          n = m;
        }
        if (view === "providers") landAt(n, go, "providers");
      };
      const of = (name + " " + p.id).toLowerCase();
      out.push({ page: "providers", key: "p:" + p.id, name, own: of, open: open(null) });
      for (const [f, more, when] of PROV_FIELDS) {
        if (when && !when(p)) continue;
        out.push({ page: "providers", key: "p:" + p.id + ":" + f, name: t(f), sub: name, own: say(f) + " " + (more || ""), of, open: open(f) });
      }
    }
  }
  // the routing groups, read by their page (plugins/routing/client.js)
  const gs = pageOn("groups") && window.routingFind?.();
  for (const g of gs?.groups || []) {
    const of = (g.name + " " + g.id + " " + (g.note || "")).toLowerCase();
    out.push({ page: "groups", key: "g:" + g.id, name: g.name, sub: g.note, own: of, open: (go) => { jumpFrom("groups"); return routingOpenGroup(g.id, "", go); } });
    for (const [f, name, words] of gs.fields) {
      out.push({ page: "groups", key: "g:" + g.id + ":" + f, name, sub: g.name, own: words, of, open: (go) => { jumpFrom("groups"); return routingOpenGroup(g.id, f, go); } });
    }
  }
  return out;
}

// the rows a page shows before Show more
const FIRST = 8;
let elseMore = new Set(), elseWords = "";
function elseFind(fq, dry) {
  const q = fq?.key || "";
  const found = !q ? [] : places().filter((x) => fq.test(x.own + " " + (x.of || ""), x.own + " " + (x.of || "")) && (!x.of || fq.any(x.own)));
  if (dry) return found.length;
  let box = document.getElementById("setElse");
  if (!box) {
    box = el("div", "set-else");
    box.id = "setElse";
    $("#view-settings").append(box);
  }
  if (q !== elseWords) elseMore = new Set(), elseWords = q;
  // a page's places, its own name first, then what a name says before a field
  const by = new Map();
  for (const x of found) {
    if (!by.has(x.page)) by.set(x.page, []);
    by.get(x.page).push(x);
  }
  const key = document.documentElement.lang + " " + q + " " + [...elseMore].join(",") + " " + found.map((x) => x.key + "=" + x.name + "/" + (x.sub || "")).join(" ");
  if (box.dataset.key === key) return found.length;
  box.dataset.key = key;
  const secs = [];
  for (const [v, list] of by) {
    list.sort((a, b) => !!a.of - !!b.of);
    const sec = el("section", "set-page find-page");
    sec.dataset.page = v;
    sec.dataset.part = t(pageName(v));
    const rows = el("div", "list prefs");
    const all = elseMore.has(v);
    for (const x of all ? list : list.slice(0, FIRST)) {
      const r = el("div", "row pref find-row");
      r.dataset.key = x.key;
      const who = el("div", "who");
      who.append(el("div", "name", x.name));
      if (x.sub) who.append(el("div", "sub", x.sub));
      const go = el("button", "text", t("Open"));
      go.type = "button";
      go.onclick = (e) => x.open(scrollOnPurpose(e, 3000));
      r.append(who, go);
      rows.append(r);
    }
    if (!all && list.length > FIRST) {
      const more = el("button", "text find-more", t("Show {n} more", { n: list.length - FIRST }));
      more.type = "button";
      more.onclick = () => { elseMore.add(v); elseFind(fq); };
      sec.append(rows, more);
    } else sec.append(rows);
    secs.push(sec);
  }
  box.replaceChildren(...secs);
  // after the built-ins found, which setfeats.js adds once
  const feats = document.getElementById("setFeats");
  if (feats && feats.nextElementSibling !== box) feats.after(box);
  box.hidden = !found.length;
  return found.length;
}
window.findElse = elseFind;
