// The viewer shell (docs/subsystems/artifacts.md, "Comments"): it frames
// the artifact's page and draws its comments over it: the comment mode a
// click on the page anchors a new thread in, the pins where threads sit,
// a thread's card, and the list of every thread. The page's runtime
// tells it where things are on the page; the shell alone reads and
// writes the threads, with its own token. Called with its config
// (ShellConfig).
(function (C) {
  "use strict";
  var frame = document.getElementById("page"), ui = document.getElementById("ui");
  frame.src = C.src + location.hash;
  var MSG = "artifact-shell";

  // -- words ------------------------------------------------------------
  var WORDS = {
    zh: { "Comment": "评论", "Comments": "评论", "Add comments": "添加评论", "Add a comment": "添加评论", "Reply": "回复", "Send to {agent}": "发给 {agent}", "Open": "未解决", "Resolved": "已解决", "Resolve": "解决", "Reopen": "重新打开", "Delete": "删除", "Delete?": "确定删除？", "Close": "关闭", "Stop": "停止", "Try again": "重试", "{agent} is working…": "{agent} 正在处理…", "Waiting for {agent}…": "等待 {agent}…", "{agent} couldn't finish: {error}": "{agent} 没能完成：{error}", "No comments yet": "还没有评论", "No resolved comments": "没有已解决的评论", "Comment on the page": "评论整个页面", "You": "你", "The whole page": "整个页面", "Couldn't post: {error}": "没能发出：{error}", "1 reply": "1 条回复", "{n} replies": "{n} 条回复", "magpie can't be reached": "连不上 magpie", "No agent can take it on this computer": "这台电脑上没有能处理它的 agent", "Save “{name}”?": "保存“{name}”？", "Save": "保存", "Cancel": "取消", "Reading aloud": "正在朗读" },
    "zh-TW": { "Comment": "留言", "Comments": "留言", "Add comments": "新增留言", "Add a comment": "新增留言", "Reply": "回覆", "Send to {agent}": "傳給 {agent}", "Open": "未解決", "Resolved": "已解決", "Resolve": "解決", "Reopen": "重新開啟", "Delete": "刪除", "Delete?": "確定刪除？", "Close": "關閉", "Stop": "停止", "Try again": "重試", "{agent} is working…": "{agent} 正在處理…", "Waiting for {agent}…": "等待 {agent}…", "{agent} couldn't finish: {error}": "{agent} 沒能完成：{error}", "No comments yet": "還沒有留言", "No resolved comments": "沒有已解決的留言", "Comment on the page": "留言給整個頁面", "You": "你", "The whole page": "整個頁面", "Couldn't post: {error}": "沒能送出：{error}", "1 reply": "1 則回覆", "{n} replies": "{n} 則回覆", "magpie can't be reached": "連不上 magpie", "No agent can take it on this computer": "這台電腦上沒有能處理它的 agent", "Save “{name}”?": "儲存「{name}」？", "Save": "儲存", "Cancel": "取消", "Reading aloud": "正在朗讀" },
    ja: { "Comment": "コメント", "Comments": "コメント", "Add comments": "コメントを追加", "Add a comment": "コメントを追加", "Reply": "返信", "Send to {agent}": "{agent} に送る", "Open": "未解決", "Resolved": "解決済み", "Resolve": "解決", "Reopen": "再開", "Delete": "削除", "Delete?": "削除しますか？", "Close": "閉じる", "Stop": "停止", "Try again": "再試行", "{agent} is working…": "{agent} が作業中…", "Waiting for {agent}…": "{agent} を待っています…", "{agent} couldn't finish: {error}": "{agent} は完了できませんでした：{error}", "No comments yet": "コメントはまだありません", "No resolved comments": "解決済みのコメントはありません", "Comment on the page": "ページ全体にコメント", "You": "あなた", "The whole page": "ページ全体", "Couldn't post: {error}": "投稿できませんでした：{error}", "1 reply": "返信 1 件", "{n} replies": "返信 {n} 件", "magpie can't be reached": "magpie に接続できません", "No agent can take it on this computer": "このコンピュータには対応できる agent がありません", "Save “{name}”?": "「{name}」を保存しますか？", "Save": "保存", "Cancel": "キャンセル", "Reading aloud": "読み上げ中" },
    de: { "Comment": "Kommentieren", "Comments": "Kommentare", "Add comments": "Kommentare hinzufügen", "Add a comment": "Kommentar hinzufügen", "Reply": "Antworten", "Send to {agent}": "An {agent} senden", "Open": "Offen", "Resolved": "Erledigt", "Resolve": "Erledigen", "Reopen": "Wieder öffnen", "Delete": "Löschen", "Delete?": "Löschen?", "Close": "Schließen", "Stop": "Stoppen", "Try again": "Erneut versuchen", "{agent} is working…": "{agent} arbeitet…", "Waiting for {agent}…": "Wartet auf {agent}…", "{agent} couldn't finish: {error}": "{agent} wurde nicht fertig: {error}", "No comments yet": "Noch keine Kommentare", "No resolved comments": "Keine erledigten Kommentare", "Comment on the page": "Die ganze Seite kommentieren", "You": "Du", "The whole page": "Die ganze Seite", "Couldn't post: {error}": "Nicht gesendet: {error}", "1 reply": "1 Antwort", "{n} replies": "{n} Antworten", "magpie can't be reached": "magpie ist nicht erreichbar", "No agent can take it on this computer": "Kein Agent auf diesem Computer kann es übernehmen", "Save “{name}”?": "„{name}“ sichern?", "Save": "Sichern", "Cancel": "Abbrechen", "Reading aloud": "Wird vorgelesen" }
  };
  var TAGS = { en: "en", zh: "zh-CN", "zh-TW": "zh-TW", ja: "ja-JP", de: "de-DE" };
  function sysLang(s) {
    s = (s || "").toLowerCase();
    if (/^zh-(tw|hk|mo|hant)\b/.test(s)) return "zh-TW";
    return s.indexOf("zh") === 0 ? "zh" : s.indexOf("ja") === 0 ? "ja" : s.indexOf("de") === 0 ? "de" : "en";
  }
  var lang = C.lang && C.lang !== "system" && TAGS[C.lang] ? C.lang : sysLang(navigator.language);
  document.documentElement.lang = TAGS[lang];
  function t(s, vars) {
    var w = (WORDS[lang] && WORDS[lang][s]) || s;
    return vars ? w.replace(/\{(\w+)\}/g, function (m, k) { return k in vars ? String(vars[k]) : m; }) : w;
  }
  var rtf = window.Intl && Intl.RelativeTimeFormat ? new Intl.RelativeTimeFormat(TAGS[lang], { numeric: "auto", style: "short" }) : null;
  function ago(iso) {
    var s = (Date.now() - Date.parse(iso)) / 1000;
    if (!rtf || !isFinite(s)) return "";
    if (s < 45) return rtf.format(0, "second");
    if (s < 3600) return rtf.format(-Math.round(s / 60), "minute");
    if (s < 86400) return rtf.format(-Math.round(s / 3600), "hour");
    if (s < 86400 * 30) return rtf.format(-Math.round(s / 86400), "day");
    return new Date(iso).toLocaleDateString(TAGS[lang]);
  }

  // -- DOM --------------------------------------------------------------
  function el(tag, cls, kids) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    (kids || []).forEach(function (k) { if (k != null) e.appendChild(typeof k === "string" ? document.createTextNode(k) : k); });
    return e;
  }
  var SVG = {
    comment: '<path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.2 3.6c-.5.4-1.3.1-1.3-.6V16A2.5 2.5 0 0 1 4 13.5z" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"/><path d="M12 6.5v6M9 9.5h6" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/>',
    list: '<path d="M4 6h16M4 12h16M4 18h10" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/>',
    check: '<path d="m5 12.5 4.5 4.5L19 7.5" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"/>',
    reopen: '<path d="M5 12a7 7 0 1 0 2.1-5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/><path d="M6.5 3.5v4h4" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>',
    trash: '<path d="M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>',
    close: '<path d="m7 7 10 10M17 7 7 17" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>'
  };
  function icon(name, label, cls) {
    var b = el("button", cls || "icon");
    b.type = "button";
    b.title = label;
    b.setAttribute("aria-label", label);
    var s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    s.setAttribute("viewBox", "0 0 24 24");
    s.setAttribute("aria-hidden", "true");
    s.innerHTML = SVG[name];
    b.appendChild(s);
    return b;
  }
  function button(text, cls) {
    var b = el("button", "btn" + (cls ? " " + cls : ""), [text]);
    b.type = "button";
    return b;
  }
  var mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  function grow(ta) {
    ta.addEventListener("input", function () {
      ta.style.height = "auto";
      ta.style.height = Math.min(ta.scrollHeight + 1, 200) + "px";
    });
  }

  // -- the API ----------------------------------------------------------
  function api(op, body) {
    return fetch(C.api + "/comments", { method: "POST", body: JSON.stringify(Object.assign({ t: C.token, op: op }, body || {})), headers: { "Content-Type": "text/plain" }, cache: "no-store" })
      .then(function (r) {
        return r.json().catch(function () { return { error: { code: "unavailable", message: "magpie answered " + r.status } }; });
      }, function () {
        return { error: { code: "unavailable", message: t("magpie can't be reached") } };
      })
      .then(function (j) {
        if (j && j.error) {
          var e = new Error(j.error.code === "claude_unavailable" ? t("No agent can take it on this computer") : j.error.message || j.error.code);
          e.code = j.error.code;
          throw e;
        }
        return j;
      });
  }

  // -- state ------------------------------------------------------------
  var threads = [], agent = { can: "no_session" }, me = "", latest = C.version;
  var mode = false, tab = "open", drawer = null, pop = null, hoverBox = null;
  var placed = {}, pins = {}, pageFollows = false, pageThere = false;
  function byId(id) {
    for (var i = 0; i < threads.length; i++) if (threads[i].id === id) return threads[i];
    return null;
  }
  function load() {
    return api("list").then(function (j) {
      threads = j.threads || [];
      agent = j.agent || { can: "no_session" };
      me = j.me || "";
      render();
    }, function () {});
  }
  function whoName(c) {
    if (c.agent) return agent.id === c.agent && agent.name ? agent.name : c.agent;
    return !c.author || c.author === me ? t("You") : c.author.slice(0, 8);
  }
  function whereOf(th) {
    return th.label || (th.anchor ? th.anchor.name || "" : "") || t("The whole page");
  }

  // -- the page's runtime -----------------------------------------------
  function toPage(m) {
    m.magpie = MSG;
    try { frame.contentWindow.postMessage(m, "*"); } catch (e) {}
  }
  function sendPins() {
    var list = [];
    threads.forEach(function (th) {
      if (th.anchor && th.anchor.path && (!th.resolved || drawer && tab === "resolved")) list.push({ id: th.id, path: th.anchor.path, x: th.anchor.x, y: th.anchor.y });
    });
    toPage({ type: "pins", list: list });
  }
  var asks = {}, askN = 0, composeReqs = {};
  window.addEventListener("message", function (e) {
    var d = e.data;
    // the page's own frame alone: another window's word is nothing here
    if (e.source !== frame.contentWindow || !d || d.magpie !== MSG) return;
    switch (d.type) {
      case "hello":
        pageThere = true;
        pageFollows = !!d.follows;
        toPage({ type: "hi" });
        toPage({ type: "mode", on: mode });
        sendPins();
        break;
      case "placed":
        placed = d.map || {};
        drawPins();
        break;
      case "hover":
        drawHover(d.rect);
        break;
      case "compose":
        if (!mode && !d.req) break;
        var opened = openComposer({ anchor: d.anchor, label: d.label, quote: d.quote, at: d.at, text: d.text, send: d.send });
        if (d.req) toPage({ type: "composed", req: d.req, opened: opened });
        break;
      case "send":
        // the page asks to send a comment to the agent: the viewer sees it
        // in the composer, and sends it or not
        sendFromPage(d);
        break;
      case "escape":
        if (saving) saving.done(false);
        else if (pop) closePop();
        else if (mode) setMode(false);
        break;
      case "answer":
        if (asks[d.id]) asks[d.id](d);
        break;
      case "save":
        askSave(d);
        break;
      case "tts-line":
        readLine(d);
        break;
      case "tts-state":
        drawReading(d.state);
        break;
    }
  });
  frame.addEventListener("load", function () {
    pageThere = false;
    drawReading("idle");
    placed = {};
    drawPins();
  });

  // ask the page's onSendToClaude listeners first (up to 130s)
  function askPage(req) {
    if (!pageThere) return Promise.resolve({ handled: false });
    var id = ++askN;
    return new Promise(function (res) {
      var timer = setTimeout(function () { delete asks[id]; toPage({ type: "abort", id: id }); res({ handled: false }); }, 130000);
      asks[id] = function (d) { clearTimeout(timer); delete asks[id]; res({ handled: !!d.handled, reply: d.reply }); };
      toPage({ type: "ask", id: id, text: req.text, anchor: req.anchor, label: req.label });
    });
  }

  // -- comment mode -----------------------------------------------------
  function setMode(on) {
    if (on === mode) return;
    if (!on && pop && pop.dirty && pop.dirty()) return;
    mode = on;
    if (!on) { drawHover(null); if (pop && pop.composer) closePop(); }
    toPage({ type: "mode", on: on });
    render();
  }
  function drawHover(r) {
    if (!r || !mode || pop) {
      if (hoverBox) { hoverBox.remove(); hoverBox = null; }
      return;
    }
    if (!hoverBox) { hoverBox = el("div", "hover noev"); ui.insertBefore(hoverBox, ui.firstChild); }
    var f = frame.getBoundingClientRect();
    hoverBox.style.left = f.left + r.x - 2 + "px";
    hoverBox.style.top = f.top + r.y - 2 + "px";
    hoverBox.style.width = r.w + 4 + "px";
    hoverBox.style.height = r.h + 4 + "px";
  }

  // -- pins -------------------------------------------------------------
  function drawPins() {
    var f = frame.getBoundingClientRect(), n = 0, seen = {};
    threads.forEach(function (th) {
      if (!th.anchor || !th.anchor.path) return;
      n++;
      var at = placed[th.id];
      if (!at || (th.resolved && !(drawer && tab === "resolved"))) return;
      seen[th.id] = true;
      var p = pins[th.id];
      if (!p) {
        p = pins[th.id] = el("button", "pin");
        p.type = "button";
        p.addEventListener("click", function () { openCard(th.id); });
        ui.insertBefore(p, ui.firstChild);
      }
      var cur = byId(th.id);
      p.textContent = String(n);
      p.setAttribute("aria-label", whereOf(cur));
      p.title = cur.comments && cur.comments[0] ? cur.comments[0].text.slice(0, 80) : "";
      p.classList.toggle("resolved", !!cur.resolved);
      p.classList.toggle("working", !!(cur.run && (cur.run.state === "working" || cur.run.state === "waiting")));
      p.classList.toggle("open", !!(pop && pop.thread === th.id));
      var x = f.left + at.x, y = f.top + at.y;
      // a pin shows while its anchor is in the frame, and stays inside it:
      // it hangs 26px above its tip, so near the top edge it drops to fit
      var off = x < f.left - 4 || y < f.top - 4 || x > f.right + 4 || y > f.bottom + 4;
      p.style.display = off ? "none" : "";
      p.style.left = Math.min(x - 4, f.right - 28) + "px";
      p.style.top = Math.min(Math.max(y + 4, f.top + 28), f.bottom) + "px";
    });
    Object.keys(pins).forEach(function (id) {
      if (!seen[id]) { pins[id].remove(); delete pins[id]; }
    });
  }

  // -- popovers ---------------------------------------------------------
  function closePop() {
    if (!pop) return;
    var p = pop;
    pop = null;
    p.node.classList.remove("in");
    setTimeout(function () { p.node.remove(); }, 120);
    if (p.onClose) p.onClose();
    drawPins();
  }
  // place node near point (x, y) in the window, inside it
  function place(node, x, y) {
    ui.appendChild(node);
    var w = node.offsetWidth, h = node.offsetHeight, W = innerWidth, H = innerHeight;
    var left = x + 14, top = y - 20;
    if (left + w > W - 12) left = Math.max(12, x - w - 14);
    if (top + h > H - 12) top = Math.max(12, H - h - 12);
    if (top < 12) top = 12;
    node.style.left = left + "px";
    node.style.top = top + "px";
    node.style.transformOrigin = (left > x ? "0 " : "100% ") + Math.max(0, Math.min(h, y - top)) + "px";
    requestAnimationFrame(function () { node.classList.add("in"); });
  }
  function composerBox(opts) {
    // opts: placeholder, submit(text, send) -> Promise, onEsc
    var ta = el("textarea");
    ta.placeholder = opts.placeholder;
    ta.rows = 1;
    grow(ta);
    var err = el("div", "err");
    var go = button(opts.submitWord, "primary"), send = null;
    var canSend = agent.can === "available" && agent.name;
    if (canSend) send = button(t("Send to {agent}", { agent: agent.name }));
    var row = el("div", "row", [err, send, go]);
    var box = el("div", "compose", [ta, row]);
    var busy = false;
    function sync() {
      var empty = !ta.value.trim();
      go.disabled = empty || busy;
      if (send) send.disabled = empty || busy;
    }
    function submit(toAgent) {
      var text = ta.value.trim();
      if (!text || busy) return;
      busy = true;
      sync();
      err.textContent = "";
      opts.submit(text, toAgent).then(function () {
        busy = false;
        ta.value = "";
        sync();
      }, function (e) {
        busy = false;
        sync();
        err.textContent = t("Couldn't post: {error}", { error: e.message });
      });
    }
    ta.addEventListener("input", sync);
    ta.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && (mac ? e.metaKey : e.ctrlKey)) {
        e.preventDefault();
        submit(!!e.shiftKey && !!send);
      } else if (e.key === "Escape") {
        e.preventDefault();
        if (opts.onEsc) opts.onEsc();
      }
    });
    go.addEventListener("click", function () { submit(false); });
    if (send) send.addEventListener("click", function () { submit(true); });
    if (opts.title) {
      if (send) send.title = (mac ? "⌘⇧↩" : "Ctrl+Shift+Enter");
      go.title = (mac ? "⌘↩" : "Ctrl+Enter");
    }
    sync();
    return { node: box, ta: ta, set: function (v) { ta.value = v; ta.dispatchEvent(new Event("input")); }, send: send };
  }

  // a new thread at an anchor (null: the whole page)
  function openComposer(o) {
    if (pop && pop.dirty && pop.dirty()) return false;
    closePop();
    drawHover(null);
    var f = frame.getBoundingClientRect();
    var x = o.at ? f.left + o.at.x : innerWidth / 2, y = o.at ? f.top + o.at.y : innerHeight / 3;
    var node = el("div", "pop");
    node.setAttribute("role", "dialog");
    var close = icon("close", t("Close"));
    node.appendChild(el("div", "head", [el("div", "where", [o.label || t("The whole page")]), close]));
    if (o.quote) node.appendChild(el("div", "quote", [o.quote]));
    var c = composerBox({
      placeholder: t("Add a comment"), submitWord: t("Comment"), title: true,
      onEsc: function () { closePop(); },
      submit: function (text, toAgent) { return startThread(o, text, toAgent).then(function () { closePop(); }); }
    });
    node.appendChild(c.node);
    close.addEventListener("click", closePop);
    pop = { node: node, composer: true, dirty: function () { return !!c.ta.value.trim(); }, onClose: o.onClose };
    place(node, x, y);
    if (o.text) c.set(o.text);
    c.ta.focus();
    if (o.send && c.send) c.send.focus();
    return true;
  }
  function startThread(o, text, toAgent) {
    var body = { anchor: o.anchor || null, label: o.label || "", quote: o.quote || "", text: text, version: latest };
    var go = function () {
      return api("create", Object.assign({ send: toAgent }, body)).then(function (j) {
        if (o.done) o.done(j, toAgent);
        return load();
      });
    };
    if (!toAgent) return go();
    return askPage({ text: text, anchor: o.anchor ? o.anchor.path : null, label: o.label }).then(function (a) {
      if (!a.handled) return go();
      // the page did it: a plain comment, and its line beside it
      toAgent = false;
      return go().then(function () { if (a.reply) flash(a.reply); });
    });
  }
  function flash(text) {
    var n = el("div", "pop in noev", [el("div", "compose", [String(text).slice(0, 500)])]);
    n.style.left = "50%";
    n.style.bottom = "64px";
    n.style.transform = "translateX(-50%)";
    ui.appendChild(n);
    setTimeout(function () { n.remove(); }, 4000);
  }

  // -- downloads --------------------------------------------------------
  // a file the page offers: the viewer is asked here, at magpie's origin,
  // one file at a time, and it is saved only on their Save. The page's
  // word is checked again: it may post without its runtime.
  var EXTS = /\.(gif|png|jpe?g|webp|mp4|webm|txt|json|md|docx|pptx|epub|csv|ttf|html|svg|pdf|xlsx|zip|fig)$/i, MAXSAVE = 64 << 20;
  var saving = null;
  function sizeOf(n) {
    var u = n < 1024 ? ["byte", n] : n < 1048576 ? ["kilobyte", n / 1024] : ["megabyte", n / 1048576];
    try { return new Intl.NumberFormat(TAGS[lang], { style: "unit", unit: u[0], unitDisplay: "short", maximumFractionDigits: u[0] === "byte" ? 0 : 1 }).format(u[1]); } catch (e) { return Math.round(u[1]) + " " + u[0]; }
  }
  function askSave(d) {
    var answer = function (a) { a.type = "saved"; a.req = d.req; toPage(a); };
    var name = typeof d.filename === "string" ? d.filename : "";
    if (!(d.blob instanceof Blob) || !d.blob.size || !name || name.length > 240 || /[\/\\\u0000-\u001f\u007f]/.test(name) || /^\./.test(name) || !EXTS.test(name)) return answer({ error: "bad_request" });
    if (d.blob.size > MAXSAVE) return answer({ error: "too_large" });
    if (saving) return answer({ error: "rate_limited" });
    var node = el("div", "pop save");
    node.setAttribute("role", "alertdialog");
    var q = el("div", "ask", [t("Save “{name}”?", { name: name })]);
    q.id = "save-q";
    node.setAttribute("aria-labelledby", "save-q");
    var no = button(t("Cancel")), yes = button(t("Save"), "primary");
    node.appendChild(q);
    node.appendChild(el("div", "size", [sizeOf(d.blob.size)]));
    node.appendChild(el("div", "acts", [no, yes]));
    // a click already on its way when the prompt appears doesn't count
    yes.disabled = no.disabled = true;
    var arm = setTimeout(function () { yes.disabled = no.disabled = false; }, 400);
    var timer = setTimeout(function () { done(false); }, 60000);
    function done(ok) {
      if (saving !== mine) return;
      saving = null;
      clearTimeout(timer);
      clearTimeout(arm);
      node.classList.remove("in");
      setTimeout(function () { node.remove(); }, 120);
      if (!ok) return answer({ error: "declined" });
      // saved as bytes: never a page of magpie's origin that could be opened
      var url = URL.createObjectURL(new Blob([d.blob], { type: "application/octet-stream" }));
      var a = el("a");
      a.href = url;
      a.download = name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(function () { URL.revokeObjectURL(url); }, 60000);
      answer({ status: "saved" });
    }
    var mine = { done: done };
    saving = mine;
    no.addEventListener("click", function () { done(false); });
    yes.addEventListener("click", function () { done(true); });
    ui.appendChild(node);
    node.getBoundingClientRect();
    node.classList.add("in");
  }

  // -- reading aloud ----------------------------------------------------
  // the page's tts: each line's audio is asked for here, with the shell's
  // token, and handed to the page to play; while it reads, a Stop sits
  // at the bottom, and Escape stops it too
  var readingBar = null;
  function readLine(d) {
    var answer = function (a) { a.type = "tts-audio"; a.req = d.req; toPage(a); };
    if (typeof d.text !== "string" || typeof d.voice !== "string") return answer({ error: "upstream_error" });
    fetch(C.api + "/tts", { method: "POST", body: JSON.stringify({ t: C.token, text: d.text, voice: d.voice, version: C.pinned ? C.version : 0 }), headers: { "Content-Type": "text/plain" }, cache: "no-store" })
      .then(function (r) {
        if (r.ok) return r.blob().then(function (b) { answer({ blob: b }); });
        var after = parseFloat(r.headers.get("Retry-After") || "");
        return r.json().catch(function () { return {}; }).then(function (j) {
          var e = (j && j.error) || {};
          var a = { error: e.code || "upstream_error", message: e.message || "" };
          if (isFinite(after) && after > 0) a.retryAfterMs = after * 1000;
          answer(a);
        });
      }, function () { answer({ error: "unavailable", message: t("magpie can't be reached") }); });
  }
  function drawReading(state) {
    var on = state === "starting" || state === "speaking" || state === "paused";
    if (on && !readingBar) {
      var stop = button(t("Stop"));
      stop.addEventListener("click", function () { toPage({ type: "tts-stop" }); });
      readingBar = el("div", "reading", [el("span", "dot"), el("span", "what", [t("Reading aloud")]), stop]);
      readingBar.setAttribute("role", "status");
      ui.appendChild(readingBar);
      readingBar.getBoundingClientRect();
      readingBar.classList.add("in");
    } else if (!on && readingBar) {
      var gone = readingBar;
      readingBar = null;
      gone.classList.remove("in");
      setTimeout(function () { gone.remove(); }, 120);
    }
    if (readingBar) readingBar.classList.toggle("paused", state === "paused");
  }

  // a thread's card
  // prefill: the page's sendToClaude into the thread {text, sent(j), onClose}
  function openCard(id, at, prefill) {
    var th = byId(id);
    if (!th) return;
    if (pop && pop.thread === id) { closePop(); return; }
    if (pop && pop.dirty && pop.dirty()) return;
    closePop();
    var node = el("div", "pop");
    node.setAttribute("role", "dialog");
    var resolve = icon(th.resolved ? "reopen" : "check", th.resolved ? t("Reopen") : t("Resolve"));
    var del = icon("trash", t("Delete")), close = icon("close", t("Close"));
    node.appendChild(el("div", "head", [el("div", "where", [whereOf(th)]), resolve, del, close]));
    if (th.quote) node.appendChild(el("div", "quote", [th.quote]));
    var msgs = el("div", "msgs");
    node.appendChild(msgs);
    var run = el("div");
    node.appendChild(run);
    var c = composerBox({
      placeholder: t("Reply"), submitWord: t("Reply"), title: true,
      onEsc: function () { closePop(); },
      submit: function (text, toAgent) {
        var go = function (send) {
          return api("reply", { thread: id, text: text, send: send }).then(function (j) {
            if (prefill) { var p = prefill; prefill = null; p.sent(j, send); }
            return load();
          });
        };
        if (!toAgent) return go(false);
        return askPage({ text: text, anchor: th.anchor ? th.anchor.path : null, label: th.label }).then(function (a) {
          return go(!a.handled).then(function () { if (a.handled && a.reply) flash(a.reply); });
        });
      }
    });
    node.appendChild(c.node);
    function fill() {
      var cur = byId(id);
      if (!cur) { closePop(); return; }
      msgs.textContent = "";
      cur.comments.forEach(function (cm) {
        var who = el("div", "who", [whoName(cm), el("time", null, [ago(cm.at)])]);
        if (!cm.agent && (cm.author === me || !cm.author) && cur.comments.length > 1) {
          var x = icon("trash", t("Delete"));
          x.addEventListener("click", function () { api("delete", { thread: id, comment: cm.id }).then(load, function () {}); });
          who.appendChild(x);
        }
        msgs.appendChild(el("div", "msg", [who, el("div", "text", [cm.text])]));
      });
      run.textContent = "";
      var r = cur.run, name = r && (r.agent === agent.id && agent.name || r.agent);
      if (r && (r.state === "working" || r.state === "waiting")) {
        var stop = button(t("Stop"));
        stop.className = "";
        stop.addEventListener("click", function () { api("stop", { thread: id }).then(load, function () {}); });
        run.appendChild(el("div", "run", [el("span", "spin"), el("span", "grow", [t(r.state === "working" ? "{agent} is working…" : "Waiting for {agent}…", { agent: name })]), stop]));
      } else if (r && r.state === "failed") {
        var again = button(t("Try again"));
        again.className = "";
        again.disabled = agent.can !== "available";
        again.addEventListener("click", function () { api("send", { thread: id }).then(load, function (e) { flash(e.message); }); });
        run.appendChild(el("div", "run", [el("span", "grow", [t("{agent} couldn't finish: {error}", { agent: name, error: r.error || "" })]), again]));
      }
      resolve.replaceWith(resolve = icon(cur.resolved ? "reopen" : "check", cur.resolved ? t("Reopen") : t("Resolve")));
      resolve.addEventListener("click", function () {
        api("resolve", { thread: id, resolved: !byId(id).resolved }).then(function () { closePop(); return load(); }, function () {});
      });
      msgs.scrollTop = msgs.scrollHeight;
    }
    // delete asks once more, in place, for three seconds
    var armed = 0, delSvg = del.firstChild;
    function disarm() {
      armed = 0;
      del.className = "icon";
      del.textContent = "";
      del.appendChild(delSvg);
    }
    del.addEventListener("click", function () {
      if (!armed) {
        armed = setTimeout(disarm, 3000);
        del.textContent = t("Delete?");
        del.className = "icon danger";
        return;
      }
      clearTimeout(armed);
      api("delete", { thread: id }).then(function () { closePop(); return load(); }, disarm);
    });
    close.addEventListener("click", closePop);
    pop = { node: node, thread: id, fill: fill, dirty: function () { return !!c.ta.value.trim(); }, onClose: function () { if (prefill) prefill.onClose(); } };
    fill();
    var p = placed[id], f = frame.getBoundingClientRect(), x, y;
    if (at) { x = at.x; y = at.y; }
    else if (p) { x = f.left + p.x; y = f.top + p.y; }
    else { x = innerWidth - (drawer ? 360 : 40); y = innerHeight / 3; }
    if (drawer && x > innerWidth - 360) x = innerWidth - 352 - 14 - 320;
    place(node, Math.max(12, x), y);
    drawPins();
    if (th.anchor && th.anchor.path) toPage({ type: "reveal", path: th.anchor.path });
    c.ta.focus();
    if (prefill) {
      c.set(prefill.text);
      if (c.send) c.send.focus();
    }
  }

  // the page's sendToClaude: the composer, written, for the viewer to send
  function sendFromPage(d) {
    var answer = function (m) { m.type = "sent"; m.req = d.req; toPage(m); };
    if (agent.can !== "available") { answer({ error: "claude_unavailable" }); return; }
    var done = false;
    var o = {
      text: d.text, send: true, anchor: d.anchor || null, label: d.label, at: d.at,
      // posted as a comment alone, it wasn't sent: the page hears so
      done: function (j, sent) { done = true; answer(sent ? { threadId: j.threadId, commentId: j.commentId } : { error: "consent_required" }); },
      onClose: function () { if (!done) answer({ error: "consent_required" }); }
    };
    if (d.threadId) {
      if (!byId(d.threadId)) { answer({ error: "not_found" }); return; }
      if (pop && pop.dirty && pop.dirty()) { answer({ error: "unavailable" }); return; }
      if (pop && pop.thread === d.threadId) closePop();
      openCard(d.threadId, null, {
        text: d.text,
        sent: function (j, sent) { answer(sent ? { threadId: d.threadId, commentId: j.commentId } : { error: "consent_required" }); },
        onClose: function () { answer({ error: "consent_required" }); }
      });
      return;
    }
    if (!openComposer(o)) answer({ error: "unavailable" });
  }

  // -- the bar and the list ---------------------------------------------
  var bar = el("div", "bar");
  bar.setAttribute("role", "toolbar");
  var modeBtn = icon("comment", t("Add comments"), " "), listBtn = icon("list", t("Comments"), " ");
  var count = el("span", "count");
  listBtn.appendChild(count);
  bar.appendChild(modeBtn);
  bar.appendChild(listBtn);
  ui.appendChild(bar);
  modeBtn.addEventListener("click", function () { setMode(!mode); });
  listBtn.addEventListener("click", function () { drawer ? closeDrawer() : openDrawer(); });

  function openDrawer() {
    if (drawer) return;
    drawer = el("aside", "drawer");
    drawer.setAttribute("aria-label", t("Comments"));
    ui.appendChild(drawer);
    requestAnimationFrame(function () { if (drawer) drawer.classList.add("in"); });
    render();
  }
  function closeDrawer() {
    if (!drawer) return;
    var d = drawer;
    drawer = null;
    d.classList.remove("in");
    setTimeout(function () { d.remove(); }, 200);
    if (tab === "resolved") { tab = "open"; sendPins(); }
    render();
  }
  var pageComposer = null;
  function fillDrawer() {
    var keep = pageComposer && pageComposer.ta.value;
    drawer.textContent = "";
    var close = icon("close", t("Close"));
    close.addEventListener("click", closeDrawer);
    drawer.appendChild(el("div", "top", [el("h2", null, [t("Comments")]), close]));
    var open = threads.filter(function (th) { return !th.resolved; }), done = threads.filter(function (th) { return th.resolved; });
    var a = button(t("Open") + " " + open.length), b = button(t("Resolved") + " " + done.length);
    a.className = tab === "open" ? "on" : "";
    b.className = tab === "resolved" ? "on" : "";
    a.addEventListener("click", function () { tab = "open"; sendPins(); render(); });
    b.addEventListener("click", function () { tab = "resolved"; sendPins(); render(); });
    var seg = el("div", "seg", [a, b]);
    seg.setAttribute("role", "tablist");
    drawer.appendChild(seg);
    var list = el("div", "list"), shown = tab === "open" ? open : done;
    if (!shown.length) list.appendChild(el("div", "empty", [tab === "open" ? t("No comments yet") : t("No resolved comments")]));
    shown.slice().reverse().forEach(function (th) {
      var first = th.comments[0] || { text: "" }, replies = th.comments.length - 1;
      var where = el("div", "where", [whereOf(th)]);
      if (th.run && (th.run.state === "working" || th.run.state === "waiting")) where.insertBefore(el("span", "spin"), where.firstChild);
      var meta = whoName(first) + " · " + ago(th.created) + (replies > 0 ? " · " + (replies === 1 ? t("1 reply") : t("{n} replies", { n: replies })) : "");
      var it = el("button", "item" + (pop && pop.thread === th.id ? " on" : ""), [where, el("div", "first", [first.text]), el("div", "meta", [meta])]);
      it.type = "button";
      it.addEventListener("click", function () {
        var r = it.getBoundingClientRect();
        openCard(th.id, placed[th.id] ? null : { x: r.left - 334, y: r.top });
      });
      list.appendChild(it);
    });
    drawer.appendChild(list);
    pageComposer = composerBox({
      placeholder: t("Comment on the page"), submitWord: t("Comment"),
      onEsc: closeDrawer,
      submit: function (text, toAgent) { return startThread({}, text, toAgent); }
    });
    drawer.appendChild(pageComposer.node);
    if (keep) pageComposer.set(keep);
  }

  function render() {
    modeBtn.classList.toggle("on", mode);
    modeBtn.setAttribute("aria-pressed", String(mode));
    listBtn.classList.toggle("on", !!drawer);
    listBtn.setAttribute("aria-expanded", String(!!drawer));
    var open = threads.filter(function (th) { return !th.resolved; }).length;
    count.textContent = open ? String(open) : "";
    count.style.display = open ? "" : "none";
    if (drawer) {
      var focused = document.activeElement && drawer.contains(document.activeElement) && document.activeElement.tagName === "TEXTAREA";
      fillDrawer();
      if (focused) pageComposer.ta.focus();
    }
    if (pop && pop.fill) pop.fill();
    sendPins();
    drawPins();
  }

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !e.defaultPrevented) {
      if (saving) saving.done(false);
      else if (readingBar) toPage({ type: "tts-stop" });
      else if (pop) closePop();
      else if (mode) setMode(false);
      else if (drawer) closeDrawer();
    }
  });
  addEventListener("resize", drawPins);

  // -- changes ----------------------------------------------------------
  function listen() {
    if (!window.EventSource) return;
    var es = new EventSource(C.api + "/shell?t=" + encodeURIComponent(C.token));
    es.onmessage = function (e) {
      var d;
      try { d = JSON.parse(e.data); } catch (x) { return; }
      if (d.comments) load();
      if (d.version > latest) {
        latest = d.version;
        // a page that follows its versions reloads itself
        if (!C.pinned && !pageFollows) frame.src = C.src;
      }
    };
  }
  load();
  listen();
})
