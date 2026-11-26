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
    zh: { "Comment": "评论", "Comments": "评论", "Add comments": "添加评论", "Add a comment": "添加评论", "Reply": "回复", "Send to {agent}": "发给 {agent}", "Open": "未解决", "Resolved": "已解决", "Resolve": "解决", "Reopen": "重新打开", "Delete": "删除", "Delete?": "确定删除？", "Close": "关闭", "Stop": "停止", "Try again": "重试", "{agent} is working…": "{agent} 正在处理…", "Waiting for {agent}…": "等待 {agent}…", "{agent} couldn't finish: {error}": "{agent} 没能完成：{error}", "No comments yet": "还没有评论", "No resolved comments": "没有已解决的评论", "Comment on the page": "评论整个页面", "You": "你", "The whole page": "整个页面", "Couldn't post: {error}": "没能发出：{error}", "1 reply": "1 条回复", "{n} replies": "{n} 条回复", "magpie can't be reached": "连不上 magpie", "No agent can take it on this computer": "这台电脑上没有能处理它的 agent", "Save “{name}”?": "保存“{name}”？", "Save": "保存", "Cancel": "取消", "Reading aloud": "正在朗读", "Listening": "正在听", "Let “{name}” take dictation?": "允许“{name}”听写？", "It gets the words you say, never the sound.": "它只拿到你说的文字，拿不到声音。", "Allow": "允许", "Don't allow": "不允许", "Ask": "询问", "Permissions": "权限", "Dictation": "听写", "Let “{name}” use {server}?": "允许“{name}”使用 {server}？", "It can run {tools} as you.": "它能以你的身份运行 {tools}。", "Let “{name}” read the files in {folder}?": "允许“{name}”读取 {folder} 里的文件？", "Files": "文件", "Let “{name}” comment as you?": "允许“{name}”以你的身份评论？", "Install as app": "安装为应用", "Install “{name}”?": "安装“{name}”？", "It opens in its own window, always at its newest version.": "它在自己的窗口里打开，总是最新版本。", "Install": "安装", "In Safari, choose File › Add to Dock.": "在 Safari 里选择“文件 › 添加到程序坞”。", "Use the app button in the address bar.": "使用地址栏里的应用按钮。", "This browser can't install apps. Open the page in Chrome, Edge or Safari.": "这个浏览器不能安装应用。请在 Chrome、Edge 或 Safari 里打开这个页面。", "OK": "好" },
    "zh-TW": { "Comment": "留言", "Comments": "留言", "Add comments": "新增留言", "Add a comment": "新增留言", "Reply": "回覆", "Send to {agent}": "傳給 {agent}", "Open": "未解決", "Resolved": "已解決", "Resolve": "解決", "Reopen": "重新開啟", "Delete": "刪除", "Delete?": "確定刪除？", "Close": "關閉", "Stop": "停止", "Try again": "重試", "{agent} is working…": "{agent} 正在處理…", "Waiting for {agent}…": "等待 {agent}…", "{agent} couldn't finish: {error}": "{agent} 沒能完成：{error}", "No comments yet": "還沒有留言", "No resolved comments": "沒有已解決的留言", "Comment on the page": "留言給整個頁面", "You": "你", "The whole page": "整個頁面", "Couldn't post: {error}": "沒能送出：{error}", "1 reply": "1 則回覆", "{n} replies": "{n} 則回覆", "magpie can't be reached": "連不上 magpie", "No agent can take it on this computer": "這台電腦上沒有能處理它的 agent", "Save “{name}”?": "儲存「{name}」？", "Save": "儲存", "Cancel": "取消", "Reading aloud": "正在朗讀", "Listening": "正在聽", "Let “{name}” take dictation?": "允許「{name}」聽寫？", "It gets the words you say, never the sound.": "它只拿到你說的文字，拿不到聲音。", "Allow": "允許", "Don't allow": "不允許", "Ask": "詢問", "Permissions": "權限", "Dictation": "聽寫", "Let “{name}” use {server}?": "允許「{name}」使用 {server}？", "It can run {tools} as you.": "它能以你的身分執行 {tools}。", "Let “{name}” read the files in {folder}?": "允許「{name}」讀取 {folder} 裡的檔案？", "Files": "檔案", "Let “{name}” comment as you?": "允許「{name}」以你的身分留言？", "Install as app": "安裝為應用程式", "Install “{name}”?": "安裝「{name}」？", "It opens in its own window, always at its newest version.": "它在自己的視窗裡開啟，永遠是最新版本。", "Install": "安裝", "In Safari, choose File › Add to Dock.": "在 Safari 裡選擇「檔案 › 加入 Dock」。", "Use the app button in the address bar.": "使用網址列裡的應用程式按鈕。", "This browser can't install apps. Open the page in Chrome, Edge or Safari.": "這個瀏覽器無法安裝應用程式。請在 Chrome、Edge 或 Safari 裡開啟這個頁面。", "OK": "好" },
    ja: { "Comment": "コメント", "Comments": "コメント", "Add comments": "コメントを追加", "Add a comment": "コメントを追加", "Reply": "返信", "Send to {agent}": "{agent} に送る", "Open": "未解決", "Resolved": "解決済み", "Resolve": "解決", "Reopen": "再開", "Delete": "削除", "Delete?": "削除しますか？", "Close": "閉じる", "Stop": "停止", "Try again": "再試行", "{agent} is working…": "{agent} が作業中…", "Waiting for {agent}…": "{agent} を待っています…", "{agent} couldn't finish: {error}": "{agent} は完了できませんでした：{error}", "No comments yet": "コメントはまだありません", "No resolved comments": "解決済みのコメントはありません", "Comment on the page": "ページ全体にコメント", "You": "あなた", "The whole page": "ページ全体", "Couldn't post: {error}": "投稿できませんでした：{error}", "1 reply": "返信 1 件", "{n} replies": "返信 {n} 件", "magpie can't be reached": "magpie に接続できません", "No agent can take it on this computer": "このコンピュータには対応できる agent がありません", "Save “{name}”?": "「{name}」を保存しますか？", "Save": "保存", "Cancel": "キャンセル", "Reading aloud": "読み上げ中", "Listening": "聞き取り中", "Let “{name}” take dictation?": "「{name}」に音声入力を許可しますか？", "It gets the words you say, never the sound.": "渡るのは話した言葉だけで、音声は渡りません。", "Allow": "許可", "Don't allow": "許可しない", "Ask": "毎回確認", "Permissions": "権限", "Dictation": "音声入力", "Let “{name}” use {server}?": "「{name}」に {server} の使用を許可しますか？", "It can run {tools} as you.": "あなたとして {tools} を実行できます。", "Let “{name}” read the files in {folder}?": "「{name}」に {folder} のファイルの読み取りを許可しますか？", "Files": "ファイル", "Let “{name}” comment as you?": "「{name}」にあなたとしてのコメントを許可しますか？", "Install as app": "アプリとしてインストール", "Install “{name}”?": "「{name}」をインストールしますか？", "It opens in its own window, always at its newest version.": "専用のウインドウで、常に最新のバージョンで開きます。", "Install": "インストール", "In Safari, choose File › Add to Dock.": "Safari で「ファイル › Dock に追加」を選んでください。", "Use the app button in the address bar.": "アドレスバーのアプリボタンを使ってください。", "This browser can't install apps. Open the page in Chrome, Edge or Safari.": "このブラウザはアプリをインストールできません。Chrome、Edge または Safari でページを開いてください。", "OK": "OK" },
    de: { "Comment": "Kommentieren", "Comments": "Kommentare", "Add comments": "Kommentare hinzufügen", "Add a comment": "Kommentar hinzufügen", "Reply": "Antworten", "Send to {agent}": "An {agent} senden", "Open": "Offen", "Resolved": "Erledigt", "Resolve": "Erledigen", "Reopen": "Wieder öffnen", "Delete": "Löschen", "Delete?": "Löschen?", "Close": "Schließen", "Stop": "Stoppen", "Try again": "Erneut versuchen", "{agent} is working…": "{agent} arbeitet…", "Waiting for {agent}…": "Wartet auf {agent}…", "{agent} couldn't finish: {error}": "{agent} wurde nicht fertig: {error}", "No comments yet": "Noch keine Kommentare", "No resolved comments": "Keine erledigten Kommentare", "Comment on the page": "Die ganze Seite kommentieren", "You": "Du", "The whole page": "Die ganze Seite", "Couldn't post: {error}": "Nicht gesendet: {error}", "1 reply": "1 Antwort", "{n} replies": "{n} Antworten", "magpie can't be reached": "magpie ist nicht erreichbar", "No agent can take it on this computer": "Kein Agent auf diesem Computer kann es übernehmen", "Save “{name}”?": "„{name}“ sichern?", "Save": "Sichern", "Cancel": "Abbrechen", "Reading aloud": "Wird vorgelesen", "Listening": "Hört zu", "Let “{name}” take dictation?": "„{name}“ Diktieren erlauben?", "It gets the words you say, never the sound.": "Die Seite bekommt nur deine Wörter, nie den Ton.", "Allow": "Erlauben", "Don't allow": "Nicht erlauben", "Ask": "Fragen", "Permissions": "Berechtigungen", "Dictation": "Diktieren", "Let “{name}” use {server}?": "„{name}“ {server} verwenden lassen?", "It can run {tools} as you.": "Die Seite kann {tools} in deinem Namen ausführen.", "Let “{name}” read the files in {folder}?": "„{name}“ die Dateien in {folder} lesen lassen?", "Files": "Dateien", "Let “{name}” comment as you?": "„{name}“ in deinem Namen kommentieren lassen?", "Install as app": "Als App installieren", "Install “{name}”?": "„{name}“ installieren?", "It opens in its own window, always at its newest version.": "Sie öffnet sich in einem eigenen Fenster, immer in der neuesten Version.", "Install": "Installieren", "In Safari, choose File › Add to Dock.": "Wähle in Safari „Ablage › Zum Dock hinzufügen“.", "Use the app button in the address bar.": "Nutze die App-Taste in der Adressleiste.", "This browser can't install apps. Open the page in Chrome, Edge or Safari.": "Dieser Browser kann keine Apps installieren. Öffne die Seite in Chrome, Edge oder Safari.", "OK": "OK" }
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
    close: '<path d="m7 7 10 10M17 7 7 17" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/>',
    install: '<rect x="4" y="4" width="16" height="16" rx="4.5" fill="none" stroke="currentColor" stroke-width="1.7"/><path d="M12 8v7.5M8.75 12.25 12 15.5l3.25-3.25" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>'
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
  // loading: the list on its way, so a thread the page has just heard of
  // is waited for, not called missing
  var loading = null;
  function load() {
    var p = api("list").then(function (j) {
      threads = j.threads || [];
      agent = j.agent || { can: "no_session" };
      me = j.me || "";
      render();
    }, function () {}).then(function () { if (loading === p) loading = null; });
    loading = p;
    return p;
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
        toPage({ type: "hi", grants: grantsOf() });
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
        else if (fAsk) fAsk.done("dismissed");
        else if (cAsk) cAsk.done("dismissed");
        else if (dAsk) dAsk.done("dismissed");
        else if (mAsk) mAsk.done("dismissed");
        else if (perms) perms.done();
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
      case "dict-start":
        startDict(d);
        break;
      case "dict-stop":
        if (dict) stopDict(dict, d.viewer ? "viewer_stopped" : "stopped");
        break;
      case "dict-ask":
        (grantOf() === "prompt" && !dict ? askDictation() : Promise.resolve()).then(function () {
          toPage({ type: "grants", req: d.req, grants: grantsOf() });
        });
        break;
      case "perms":
        openPerms(d.req, d.serves || {});
        break;
      case "mcp":
        mcpOp(d);
        break;
      case "mcp-cancel":
        if (mcalls[d.req]) mcalls[d.req].abort();
        break;
      case "files":
        filesOp(d);
        break;
      case "comment":
        commentOp(d);
        break;
      case "comments-ask":
        (cGrantOf() === "prompt" ? askComments() : Promise.resolve()).then(function () {
          toPage({ type: "grants", req: d.req, grants: grantsOf() });
        });
        break;
      case "files-ask":
        filesAskFor().then(function () {
          toPage({ type: "grants", req: d.req, grants: grantsOf() });
        });
        break;
      case "mcp-ask":
        mcpAskFor(d.servers).then(function () {
          toPage({ type: "grants", req: d.req, grants: grantsOf() });
        });
        break;
    }
  });
  frame.addEventListener("load", function () {
    pageThere = false;
    drawReading("idle");
    // the page that asked is gone: so is its dictation
    if (dict) endDict(dict, "hidden");
    // and its MCP calls, and the question it asked
    Object.keys(mcalls).forEach(function (k) { mcalls[k].abort(); });
    if (mAsk) mAsk.done("dismissed");
    if (fAsk) fAsk.done("dismissed");
    if (cAsk) cAsk.done("dismissed");
    if (perms) perms.done();
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
      // closed once the thread is made, not once the list is back: the
      // page may ask again at once
      submit: function (text, toAgent) { return startThread(o, text, toAgent, function () { if (pop && pop.node === node) closePop(); }); }
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
  // made: called once the thread is, after o.done (a composer closed
  // first would tell the page it wasn't sent)
  function startThread(o, text, toAgent, made) {
    var body = { anchor: o.anchor || null, label: o.label || "", quote: o.quote || "", text: text, version: latest };
    var go = function () {
      return api("create", Object.assign({ send: toAgent }, body)).then(function (j) {
        if (o.done) o.done(j, toAgent);
        if (made) made();
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
    confirmSave(name, d.blob.size).then(function (ok) {
      if (!ok) return answer({ error: "declined" });
      hand(d.blob, name);
      answer({ status: "saved" });
    });
  }
  // the viewer is asked to save a file of this name and size: true on Save
  function confirmSave(name, size) {
    var node = el("div", "pop save");
    node.setAttribute("role", "alertdialog");
    var q = el("div", "ask", [t("Save “{name}”?", { name: name })]);
    q.id = "save-q";
    node.setAttribute("aria-labelledby", "save-q");
    var no = button(t("Cancel")), yes = button(t("Save"), "primary");
    node.appendChild(q);
    node.appendChild(el("div", "size", [sizeOf(size)]));
    node.appendChild(el("div", "acts", [no, yes]));
    // a click already on its way when the prompt appears doesn't count
    yes.disabled = no.disabled = true;
    var arm = setTimeout(function () { yes.disabled = no.disabled = false; }, 400);
    var timer = setTimeout(function () { mine.done(false); }, 60000);
    var mine = {};
    var p = new Promise(function (res) {
      mine.done = function (ok) {
        if (saving !== mine) return;
        saving = null;
        clearTimeout(timer);
        clearTimeout(arm);
        node.classList.remove("in");
        setTimeout(function () { node.remove(); }, 120);
        res(ok);
      };
    });
    saving = mine;
    no.addEventListener("click", function () { mine.done(false); });
    yes.addEventListener("click", function () { mine.done(true); });
    ui.appendChild(node);
    node.getBoundingClientRect();
    node.classList.add("in");
    return p;
  }
  // the bytes handed to the viewer's device as a file: never a page of
  // magpie's origin that could be opened
  function hand(blob, name) {
    var url = URL.createObjectURL(new Blob([blob], { type: "application/octet-stream" }));
    var a = el("a");
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 60000);
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

  // -- dictation --------------------------------------------------------
  // the viewer's speech as text for the page. Asked once for each
  // artifact (kept here, at magpie's origin, until the viewer changes it
  // in Permissions), the microphone is opened here, never in the page;
  // while it is open, Listening and a Stop sit at the bottom. The
  // recording is cut at each pause into phrases, each sent to magpie with
  // the shell's token, and the page gets only their words: a guess while
  // a phrase goes on, the settled text once it ends.
  var GRANT = "magpie.artifact." + C.id + ".dictation";
  var DMAX = 60 * 60 * 1000, PHRASE_MAX = 30000, QUIET = 800, GUESS_EVERY = 2500;
  var dict = null, dAsk = null, listenBar = null;
  function grantOf() {
    try { return localStorage.getItem(GRANT) || "prompt"; } catch (e) { return "prompt"; }
  }
  function setGrant(g) {
    try { if (g === "prompt") localStorage.removeItem(GRANT); else localStorage.setItem(GRANT, g); } catch (e) {}
    toPage({ type: "grants", grants: grantsOf() });
  }
  // the question: "granted", "denied", or "dismissed" when closed unanswered
  function askDictation() {
    if (dAsk) return dAsk.p;
    var node = el("div", "pop save");
    node.setAttribute("role", "alertdialog");
    var q = el("div", "ask", [t("Let “{name}” take dictation?", { name: C.title || "" })]);
    q.id = "dict-q";
    node.setAttribute("aria-labelledby", "dict-q");
    var no = button(t("Don't allow")), yes = button(t("Allow"), "primary");
    node.appendChild(q);
    node.appendChild(el("div", "size", [t("It gets the words you say, never the sound.")]));
    node.appendChild(el("div", "acts", [no, yes]));
    yes.disabled = no.disabled = true;
    var arm = setTimeout(function () { yes.disabled = no.disabled = false; }, 400);
    var timer = setTimeout(function () { mine.done("dismissed"); }, 60000);
    var mine = { node: node };
    mine.p = new Promise(function (res) {
      mine.done = function (a) {
        if (dAsk !== mine) return;
        dAsk = null;
        clearTimeout(timer);
        clearTimeout(arm);
        node.classList.remove("in");
        setTimeout(function () { node.remove(); }, 120);
        if (a !== "dismissed") setGrant(a);
        res(a);
      };
    });
    dAsk = mine;
    no.addEventListener("click", function () { mine.done("denied"); });
    yes.addEventListener("click", function () { mine.done("granted"); });
    ui.appendChild(node);
    node.getBoundingClientRect();
    node.classList.add("in");
    return mine.p;
  }
  function dState(st, reason, message) {
    var m = { type: "dict-state", state: st };
    if (reason) m.reason = reason;
    if (message) m.message = message;
    toPage(m);
    drawListening(st);
  }
  function recType() {
    var c = ["audio/webm;codecs=opus", "audio/webm", "audio/ogg;codecs=opus", "audio/mp4"];
    for (var i = 0; i < c.length; i++) if (MediaRecorder.isTypeSupported && MediaRecorder.isTypeSupported(c[i])) return c[i];
    return "";
  }
  // a piece of recording to magpie: {text} or {error, message}
  function hear(s, blob) {
    var h = { "Content-Type": (blob.type || s.mime || "audio/webm").split(";")[0], "X-Magpie-Token": C.token, "X-Magpie-Version": String(C.pinned ? C.version : 0) };
    if (s.lang) h["X-Magpie-Language"] = s.lang;
    return fetch(C.api + "/dictation", { method: "POST", body: blob, headers: h, cache: "no-store" }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (r.ok && j && typeof j.text === "string") return { text: j.text.trim() };
        var e = (j && j.error) || {};
        var code = r.status === 429 ? "rate_limited" : { not_granted: 1, unavailable: 1, rate_limited: 1 }[e.code] ? e.code : "upstream_error";
        return { error: code, message: typeof e.message === "string" ? e.message : "" };
      });
    }, function () { return { error: "upstream_error", message: t("magpie can't be reached") }; });
  }
  function phrase(s) {
    var rec = new MediaRecorder(s.stream, s.mime ? { mimeType: s.mime } : undefined);
    var p = { rec: rec, chunks: [], at: Date.now(), voiced: !s.an, guessing: false, guessAt: 0 };
    rec.ondataavailable = function (e) { if (e.data && e.data.size) p.chunks.push(e.data); };
    rec.onstop = function () { if (p.stopped) p.stopped(); };
    rec.start(500);
    s.phrase = p;
  }
  // a phrase ends: the next starts at once (unless it was the last), and
  // its words go to the page in the order the phrases were said
  function cut(s, last) {
    var p = s.phrase;
    if (!p || p.cut) return Promise.resolve();
    p.cut = true;
    if (!last) phrase(s);
    return new Promise(function (res) {
      p.stopped = res;
      try { p.rec.stop(); } catch (e) { res(); }
    }).then(function () {
      if (!p.voiced || !p.chunks.length) return;
      var heard = hear(s, new Blob(p.chunks, { type: p.chunks[0].type || s.mime }));
      s.chain = s.chain.then(function () {
        return heard.then(function (a) {
          if (dict !== s) return;
          if (a.error) return endDict(s, a.error, a.message);
          toPage({ type: "dict-final", text: a.text });
        });
      });
      return s.chain;
    });
  }
  function guess(s, p) {
    p.guessing = true;
    p.guessAt = Date.now();
    hear(s, new Blob(p.chunks, { type: p.chunks[0].type || s.mime })).then(function (a) {
      p.guessing = false;
      if (dict === s && s.phrase === p && !p.cut && !a.error && a.text) toPage({ type: "dict-partial", text: a.text });
    });
  }
  var levels = null;
  function tick(s) {
    var now = Date.now(), p = s.phrase;
    if (!p || s.ending) return;
    if (now - s.began >= DMAX) return stopDict(s, "time_limit");
    if (s.an && s.ctx.state === "running") {
      if (!levels || levels.length !== s.an.fftSize) levels = new Float32Array(s.an.fftSize);
      s.an.getFloatTimeDomainData(levels);
      var sum = 0;
      for (var i = 0; i < levels.length; i++) sum += levels[i] * levels[i];
      var rms = Math.sqrt(sum / levels.length);
      // a voice stands out of the room's own level, which follows the quiet
      if (rms > Math.max(0.012, s.floor * 2.5)) { p.voiced = true; s.voiceAt = now; }
      else s.floor = s.floor ? s.floor * 0.95 + rms * 0.05 : rms;
    } else p.voiced = true;
    if (p.voiced && s.an && s.ctx.state === "running" && now - s.voiceAt >= QUIET && now - p.at >= 1000 || now - p.at >= PHRASE_MAX || !p.voiced && now - p.at >= 10000) {
      cut(s);
      return;
    }
    if (p.voiced && !p.guessing && p.chunks.length && now - p.at >= 1200 && now - p.guessAt >= GUESS_EVERY) guess(s, p);
  }
  function startDict(d) {
    if (dict) { if (!dict.ending && dict.stream) dState("listening"); return; }
    var lang = typeof d.language === "string" && /^[a-z]{2}(-[A-Za-z]{2})?$/.test(d.language) ? d.language : "";
    var s = { lang: lang, chain: Promise.resolve(), began: Date.now(), floor: 0, voiceAt: 0 };
    dict = s;
    dState("starting");
    if (!(navigator.mediaDevices && navigator.mediaDevices.getUserMedia) || typeof MediaRecorder !== "function") return endDict(s, "unavailable", "this browser can't record");
    var g = grantOf();
    (g === "prompt" ? askDictation() : Promise.resolve(g)).then(function (a) {
      if (dict !== s) return;
      if (a === "denied") return endDict(s, "not_granted", "the viewer doesn't let this artifact take dictation");
      if (a !== "granted") return endDict(s, "dismissed", "the viewer closed the question");
      return navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } }).then(function (stream) {
        if (dict !== s) { stream.getTracks().forEach(function (k) { k.stop(); }); return; }
        s.stream = stream;
        s.mime = recType();
        var AC = window.AudioContext || window.webkitAudioContext;
        if (AC) {
          try {
            s.ctx = new AC();
            s.an = s.ctx.createAnalyser();
            s.an.fftSize = 1024;
            s.ctx.createMediaStreamSource(stream).connect(s.an);
            if (s.ctx.resume) s.ctx.resume().catch(function () {});
          } catch (e) { s.an = null; }
        }
        try { phrase(s); } catch (e) { return endDict(s, "upstream_error", "the microphone couldn't be recorded"); }
        stream.getTracks().forEach(function (k) { k.onended = function () { stopDict(s, "upstream_error"); }; });
        s.timer = setInterval(function () { tick(s); }, 100);
        dState("listening");
      }, function (e) {
        endDict(s, e && (e.name === "NotAllowedError" || e.name === "SecurityError") ? "not_granted" : "upstream_error", e && e.message || "");
      });
    });
  }
  // stop: the last phrase's words still go to the page, then idle
  function stopDict(s, reason) {
    if (!s || dict !== s || s.ending) return;
    s.ending = reason;
    if (!s.stream) return endDict(s, reason);
    clearInterval(s.timer);
    dState("finishing");
    cut(s, true).then(function () { return s.chain; }).then(function () { endDict(s, reason); });
  }
  function endDict(s, reason, message) {
    if (dict !== s) return;
    dict = null;
    clearInterval(s.timer);
    if (dAsk) dAsk.done("dismissed");
    if (s.phrase && !s.phrase.cut) try { s.phrase.rec.stop(); } catch (e) {}
    if (s.stream) s.stream.getTracks().forEach(function (k) { k.onended = null; k.stop(); });
    if (s.ctx && s.ctx.close) s.ctx.close().catch(function () {});
    dState("idle", reason, message);
  }
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "hidden" && dict) stopDict(dict, "hidden");
  });
  function drawListening(state) {
    var on = state === "listening" || state === "finishing";
    if (on && !listenBar) {
      var stop = button(t("Stop"));
      stop.addEventListener("click", function () { stopDict(dict, "viewer_stopped"); });
      listenBar = el("div", "reading listening", [el("span", "dot"), el("span", "what", [t("Listening")]), stop]);
      listenBar.setAttribute("role", "status");
      ui.appendChild(listenBar);
      listenBar.getBoundingClientRect();
      listenBar.classList.add("in");
    } else if (!on && listenBar) {
      var gone = listenBar;
      listenBar = null;
      gone.classList.remove("in");
      setTimeout(function () { gone.remove(); }, 120);
    }
    if (listenBar) listenBar.classList.toggle("paused", state === "finishing");
  }

  // -- MCP --------------------------------------------------------------
  // the page calls the tools of the MCP servers in magpie's Library that
  // its manifest names. Each server is asked about once for each artifact
  // (kept here, like dictation, until the viewer changes it in
  // Permissions); the calls go to magpie with the shell's token, which
  // checks the manifest again.
  var MGRANT = "magpie.artifact." + C.id + ".mcp:";
  var mAsk = null, mcalls = {};
  function mGrantOf(server) {
    try { return localStorage.getItem(MGRANT + server) || "prompt"; } catch (e) { return "prompt"; }
  }
  function setMGrant(server, g) {
    try { if (g === "prompt") localStorage.removeItem(MGRANT + server); else localStorage.setItem(MGRANT + server, g); } catch (e) {}
  }
  // every decision kept for this artifact: what the page reads without asking
  function grantsOf() {
    var g = { dictation: grantOf(), files: fGrantOf(), comments: cGrantOf(), mcp: {} };
    try {
      for (var i = 0; i < localStorage.length; i++) {
        var k = localStorage.key(i);
        if (k && k.indexOf(MGRANT) === 0) g.mcp[k.slice(MGRANT.length)] = localStorage.getItem(k);
      }
    } catch (e) {}
    return g;
  }
  function mErr(code, message, more) {
    return { error: Object.assign({ code: code, message: message }, more || {}) };
  }
  // one request to magpie's MCP route: its JSON, an error included
  function mcpFetch(body, signal) {
    var b = Object.assign({ t: C.token, version: C.pinned ? C.version : 0 }, body);
    return fetch(C.api + "/mcp", { method: "POST", body: JSON.stringify(b), headers: { "Content-Type": "text/plain" }, cache: "no-store", signal: signal })
      .then(function (r) {
        return r.json().catch(function () { return mErr("upstream_error", "magpie answered " + r.status, { retryable: true }); });
      }, function (e) {
        if (signal && signal.aborted) return mErr("cancelled", "the call was cancelled");
        return mErr("upstream_error", t("magpie can't be reached"), { retryable: true });
      })
      .then(function (j) { return j || mErr("upstream_error", "magpie answered nothing", { retryable: true }); });
  }
  // the manifest's servers as the page sees them: one allowed as magpie
  // lists it, one not asked about yet from the manifest alone, one the
  // viewer said no to not at all
  function mcpList(only) {
    return mcpFetch({ op: "list" }).then(function (j) {
      if (j.error) return j;
      var on = {}, out = [];
      (j.servers || []).forEach(function (x) { on[x.server] = x; });
      (j.manifest || []).forEach(function (m) {
        if (only != null && m.server !== only) return;
        var g = mGrantOf(m.server);
        if (g === "granted" && on[m.server]) out.push(on[m.server]);
        else if (g === "prompt") out.push({ server: m.server, kind: "connector", authStatus: "unknown", tools: (m.tools || []).map(function (n) { return { name: n, description: "" }; }) });
      });
      return { servers: out, manifest: j.manifest || [], connected: on, off: j.off };
    });
  }
  // the question, for one or more servers: "granted", "denied", or
  // "dismissed" when closed unanswered or another question is open
  function askMCP(list) {
    var names = list.map(function (m) { return m.server; }).join(", ");
    if (mAsk) return mAsk.names === names ? mAsk.p : Promise.resolve("dismissed");
    if (saving || dAsk || fAsk || cAsk || perms) return Promise.resolve("dismissed");
    var tools = [];
    list.forEach(function (m) { (m.tools || []).forEach(function (n) { if (tools.indexOf(n) < 0) tools.push(n); }); });
    var shown = tools.slice(0, 6).join(", ") + (tools.length > 6 ? ", …" : "");
    var node = el("div", "pop save");
    node.setAttribute("role", "alertdialog");
    var q = el("div", "ask", [t("Let “{name}” use {server}?", { name: C.title || "", server: names })]);
    q.id = "mcp-q";
    node.setAttribute("aria-labelledby", "mcp-q");
    var no = button(t("Don't allow")), yes = button(t("Allow"), "primary");
    node.appendChild(q);
    if (tools.length) node.appendChild(el("div", "size", [t("It can run {tools} as you.", { tools: shown })]));
    node.appendChild(el("div", "acts", [no, yes]));
    yes.disabled = no.disabled = true;
    var arm = setTimeout(function () { yes.disabled = no.disabled = false; }, 400);
    var timer = setTimeout(function () { mine.done("dismissed"); }, 60000);
    var mine = { node: node, names: names };
    mine.p = new Promise(function (res) {
      mine.done = function (a) {
        if (mAsk !== mine) return;
        mAsk = null;
        clearTimeout(timer);
        clearTimeout(arm);
        node.classList.remove("in");
        setTimeout(function () { node.remove(); }, 120);
        if (a !== "dismissed") {
          list.forEach(function (m) { setMGrant(m.server, a); });
          toPage({ type: "grants", grants: grantsOf() });
        }
        res(a);
      };
    });
    mAsk = mine;
    no.addEventListener("click", function () { mine.done("denied"); });
    yes.addEventListener("click", function () { mine.done("granted"); });
    ui.appendChild(node);
    node.getBoundingClientRect();
    node.classList.add("in");
    return mine.p;
  }
  // permissions.request: one question for every server named that is
  // connected and not asked about yet
  function mcpAskFor(servers) {
    return mcpList().then(function (j) {
      if (j.error) return;
      var want = (j.manifest || []).filter(function (m) {
        return (!servers || servers.indexOf(m.server) >= 0) && j.connected[m.server] && mGrantOf(m.server) === "prompt";
      });
      if (want.length) return askMCP(want);
    });
  }
  // a call, a listing or a description for the page
  function mcpOp(d) {
    var done = function (j) {
      delete mcalls[d.req];
      toPage(j.error ? { type: "mcp-done", req: d.req, error: j.error } : { type: "mcp-done", req: d.req, result: j });
    };
    if (d.op === "list") {
      mcpList(d.server).then(function (j) { done(j.error ? j : { servers: j.servers }); });
      return;
    }
    if (d.op !== "call" && d.op !== "describe") return done(mErr("bad_request", "no such MCP request"));
    var server = String(d.server || ""), g = mGrantOf(server);
    if (g === "denied" || d.op === "describe" && g !== "granted") return done(mErr("not_in_manifest", "the viewer hasn't allowed " + server + " for this page", { server: server }));
    var ac = new AbortController();
    mcalls[d.req] = ac;
    var ready = Promise.resolve("granted");
    if (g === "prompt") {
      if (!d.ask) return done(mErr("approval_required", "a watch never asks: call the tool once, or ask with permissions.request(\"mcp:" + server + "\")", { server: server }));
      ready = mcpList().then(function (j) {
        if (j.error) return j;
        var m = (j.manifest || []).filter(function (x) { return x.server === server; })[0];
        if (!m) return mErr("not_in_manifest", server + " isn't in the page's manifest", { server: server });
        // magpie's hub off runs no server: the page hears that, not a missing name
        if (!j.connected[server]) return mErr("server_not_connected", j.off || "no MCP server named " + server + " is in magpie's Library on this computer", { server: server });
        return askMCP([m]).then(function (a) {
          if (a === "granted") return a;
          if (a === "denied") return mErr("not_in_manifest", "the viewer didn't allow " + server + " for this page", { server: server });
          return mErr("upstream_error", "the viewer didn't answer", { server: server, retryable: true, retryAfterMs: 3000 });
        });
      });
    }
    ready.then(function (a) {
      if (a !== "granted") return done(a);
      if (ac.signal.aborted) return done(mErr("cancelled", "the call was cancelled", { server: server }));
      var b = { op: d.op, server: server, tool: d.tool };
      if (d.op === "call") {
        b.input = d.input;
        b.watch = !!d.watch;
      }
      mcpFetch(b, ac.signal).then(done);
    });
  }

  // -- files ------------------------------------------------------------
  // the page reads the files of the folder its version names, as
  // Claude's files capability reads a project's. The viewer is asked
  // once for each artifact (kept here, like dictation, until they change
  // it in Permissions); the calls go to magpie with the shell's token,
  // and a download is asked about here, file by file, like the page's own.
  var FGRANT = "magpie.artifact." + C.id + ".files";
  var fAsk = null, fWhere = null, fCool = 0;
  function fGrantOf() {
    try { return localStorage.getItem(FGRANT) || "prompt"; } catch (e) { return "prompt"; }
  }
  function setFGrant(g) {
    try { if (g === "prompt") localStorage.removeItem(FGRANT); else localStorage.setItem(FGRANT, g); } catch (e) {}
  }
  function fErr(code, message, more) {
    return { error: Object.assign({ code: code, message: message }, more || {}) };
  }
  // one request to magpie's files route: the Response, or an error
  function filesFetch(body) {
    var b = Object.assign({ t: C.token, version: C.pinned ? C.version : 0 }, body);
    return fetch(C.api + "/files", { method: "POST", body: JSON.stringify(b), headers: { "Content-Type": "text/plain" }, cache: "no-store" })
      .then(function (r) {
        if (r.ok) return r;
        return r.json().catch(function () { return null; }).then(function (j) {
          return j && j.error ? { error: j.error } : fErr("unavailable", "magpie answered " + r.status);
        });
      }, function () { return fErr("unavailable", t("magpie can't be reached")); });
  }
  function filesJSON(body) {
    return filesFetch(body).then(function (r) {
      return r.error ? r : r.json().catch(function () { return fErr("unavailable", "magpie's answer can't be read"); });
    });
  }
  function where() {
    return fWhere || (fWhere = filesJSON({ op: "where" }).then(function (j) {
      if (j.error) fWhere = null;
      return j;
    }));
  }
  // the question: "granted", "denied", or "dismissed" when closed
  // unanswered or another question is open
  function askFiles() {
    if (fAsk) return fAsk.p;
    if (saving || dAsk || mAsk || cAsk || perms) return Promise.resolve("dismissed");
    var mine = {};
    fAsk = mine;
    mine.p = where().then(function (w) {
      if (fAsk !== mine) return "dismissed";
      if (w.error) { fAsk = null; return w; }
      var node = el("div", "pop save");
      node.setAttribute("role", "alertdialog");
      var q = el("div", "ask", [t("Let “{name}” read the files in {folder}?", { name: C.title || "", folder: w.name })]);
      q.id = "files-q";
      node.setAttribute("aria-labelledby", "files-q");
      var no = button(t("Don't allow")), yes = button(t("Allow"), "primary");
      var path = el("div", "size", [w.folder]);
      path.title = w.folder;
      node.appendChild(q);
      node.appendChild(path);
      node.appendChild(el("div", "acts", [no, yes]));
      yes.disabled = no.disabled = true;
      var arm = setTimeout(function () { yes.disabled = no.disabled = false; }, 400);
      var timer = setTimeout(function () { mine.done("dismissed"); }, 60000);
      return new Promise(function (res) {
        mine.done = function (a) {
          if (fAsk !== mine) return;
          fAsk = null;
          clearTimeout(timer);
          clearTimeout(arm);
          node.classList.remove("in");
          setTimeout(function () { node.remove(); }, 120);
          if (a !== "dismissed") {
            setFGrant(a);
            toPage({ type: "grants", grants: grantsOf() });
          } else fCool = Date.now() + 3000;
          res(a);
        };
        no.addEventListener("click", function () { mine.done("denied"); });
        yes.addEventListener("click", function () { mine.done("granted"); });
        ui.appendChild(node);
        node.getBoundingClientRect();
        node.classList.add("in");
      });
    });
    // Escape before the question shows
    mine.done = function () { if (fAsk === mine) { fAsk = null; fCool = Date.now() + 3000; } };
    return mine.p;
  }
  // allowed, or why not
  function filesReady() {
    var g = fGrantOf();
    if (g === "granted") return Promise.resolve(null);
    if (g === "denied") return Promise.resolve(fErr("not_granted", "the viewer didn't allow this page to read the files"));
    if (Date.now() < fCool) return Promise.resolve(fErr("rate_limited", "the viewer just closed the question", { retryAfterMs: fCool - Date.now() }));
    return askFiles().then(function (a) {
      if (a && a.error) return a;
      if (a === "granted") return null;
      if (a === "denied") return fErr("not_granted", "the viewer didn't allow this page to read the files");
      return fErr("rate_limited", "the viewer didn't answer, or another question is open", { retryAfterMs: 3000 });
    });
  }
  function filesOp(d) {
    var done = function (j) { toPage(j.error ? { type: "files-done", req: d.req, error: j.error } : { type: "files-done", req: d.req, result: j }); };
    if (d.op !== "list" && d.op !== "read" && d.op !== "download") return done(fErr("bad_request", "no such files request"));
    filesReady().then(function (no) {
      if (no) return done(no);
      if (d.op === "list") return filesJSON({ op: "list", path: d.path, recursive: !!d.recursive, cursor: d.cursor }).then(done);
      if (d.op === "read") {
        return filesFetch({ op: "read", path: d.path, offset: d.offset, length: d.length }).then(function (r) {
          if (r.error) return done(r);
          var size = Number(r.headers.get("X-Magpie-Size")), rev = r.headers.get("X-Magpie-Revision") || "";
          return r.arrayBuffer().then(function (b) { done({ bytes: b, size: size, revision: rev }); }, function () { done(fErr("changed", "the file couldn't be read whole")); });
        });
      }
      if (saving) return done(fErr("rate_limited", "another file is waiting for the viewer", { retryAfterMs: 3000 }));
      return filesJSON({ op: "stat", path: d.path }).then(function (st) {
        if (st.error) return done(st);
        return confirmSave(st.name, st.size).then(function (ok) {
          if (!ok) return done(fErr("declined", "the viewer didn't save it"));
          return filesFetch({ op: "save", path: d.path, revision: st.revision }).then(function (r) {
            if (r.error) return done(r);
            return r.blob().then(function (b) {
              hand(b, st.name);
              done({ status: "saved" });
            }, function () { done(fErr("changed", "the file changed while it was saved")); });
          });
        });
      });
    });
  }
  // permissions.request
  function filesAskFor() {
    return fGrantOf() === "prompt" ? askFiles() : Promise.resolve();
  }

  // -- comments the page writes -----------------------------------------
  // a page declaring comments in full writes them as the viewer: it is
  // asked once for each artifact (kept here, like files, until the
  // viewer changes it in Permissions), and the write goes to magpie with
  // the shell's token; the page's own token writes nothing
  var CGRANT = "magpie.artifact." + C.id + ".comments";
  var cAsk = null, cCool = 0;
  function cGrantOf() {
    try { return localStorage.getItem(CGRANT) || "prompt"; } catch (e) { return "prompt"; }
  }
  function setCGrant(g) {
    try { if (g === "prompt") localStorage.removeItem(CGRANT); else localStorage.setItem(CGRANT, g); } catch (e) {}
  }
  // the question: "granted", "denied", or "dismissed" when closed
  // unanswered or another question is open
  function askComments() {
    if (cAsk) return cAsk.p;
    if (saving || dAsk || fAsk || mAsk || perms) return Promise.resolve("dismissed");
    var mine = {};
    cAsk = mine;
    var node = el("div", "pop save");
    node.setAttribute("role", "alertdialog");
    var q = el("div", "ask", [t("Let “{name}” comment as you?", { name: C.title || "" })]);
    q.id = "comments-q";
    node.setAttribute("aria-labelledby", "comments-q");
    var no = button(t("Don't allow")), yes = button(t("Allow"), "primary");
    node.appendChild(q);
    node.appendChild(el("div", "acts", [no, yes]));
    yes.disabled = no.disabled = true;
    var arm = setTimeout(function () { yes.disabled = no.disabled = false; }, 400);
    var timer = setTimeout(function () { mine.done("dismissed"); }, 60000);
    mine.p = new Promise(function (res) {
      mine.done = function (a) {
        if (cAsk !== mine) return;
        cAsk = null;
        clearTimeout(timer);
        clearTimeout(arm);
        node.classList.remove("in");
        setTimeout(function () { node.remove(); }, 120);
        if (a !== "dismissed") {
          setCGrant(a);
          toPage({ type: "grants", grants: grantsOf() });
        } else cCool = Date.now() + 3000;
        res(a);
      };
    });
    no.addEventListener("click", function () { mine.done("denied"); });
    yes.addEventListener("click", function () { mine.done("granted"); });
    ui.appendChild(node);
    node.getBoundingClientRect();
    node.classList.add("in");
    return mine.p;
  }
  var CWRITES = { create: ["anchor", "label", "text", "version"], reply: ["thread", "text", "version"], resolve: ["thread", "resolved"], "delete": ["thread"] };
  function commentOp(d) {
    var done = function (m) { m.type = "comment-done"; m.req = d.req; toPage(m); };
    var fields = Object.prototype.hasOwnProperty.call(CWRITES, d.op) ? CWRITES[d.op] : null;
    if (!fields) return done({ error: { code: "invalid", message: "no such comments call" } });
    var g = cGrantOf();
    var ready = g === "granted" ? Promise.resolve(g)
      : g === "denied" ? Promise.resolve("denied")
      : Date.now() < cCool ? Promise.resolve("dismissed")
      : askComments();
    ready.then(function (a) {
      if (a !== "granted") return done({ error: { code: "consent_required", message: a === "denied" ? "the viewer didn't allow this page to comment as them" : "the viewer didn't answer, or another question is open" } });
      // what the call names, and nothing else: never a send to the agent
      var body = {}, b = d.body || {};
      fields.forEach(function (k) { if (b[k] !== undefined) body[k] = b[k]; });
      return api(d.op, body).then(function (j) {
        done({ result: j });
        load();
      }, function (e) { done({ error: { code: e.code || "unavailable", message: e.message } }); });
    });
  }

  // the page's Permissions: what the viewer decided for this artifact,
  // and where they change it. serves: what the page can be allowed
  var perms = null;
  function openPerms(req, serves) {
    var answer = function () { toPage({ type: "perms-closed", req: req }); };
    if (perms) return answer();
    perms = { done: function () { perms = null; answer(); } };
    (serves.mcp ? mcpList() : Promise.resolve({})).then(function (j) {
      if (!perms) return;
      drawPerms(serves, (j && j.manifest) || [], answer);
    });
  }
  function drawPerms(serves, manifest, answer) {
    var node = el("div", "pop save perms");
    node.setAttribute("role", "dialog");
    var h = el("div", "ask", [t("Permissions")]);
    h.id = "perms-h";
    node.setAttribute("aria-labelledby", "perms-h");
    node.appendChild(h);
    function row(label, get, set) {
      var seg = el("div", "seg");
      [["prompt", t("Ask")], ["granted", t("Allow")], ["denied", t("Don't allow")]].forEach(function (o) {
        var b = el("button", get() === o[0] ? "on" : "", [o[1]]);
        b.type = "button";
        b.setAttribute("aria-pressed", String(get() === o[0]));
        b.addEventListener("click", function () {
          set(o[0]);
          Array.prototype.forEach.call(seg.children, function (x) { x.className = x === b ? "on" : ""; x.setAttribute("aria-pressed", String(x === b)); });
        });
        seg.appendChild(b);
      });
      var name = el("span", "", [label]);
      name.title = label;
      node.appendChild(el("div", "perm", [name, seg]));
    }
    if (serves.dictation) row(t("Dictation"), grantOf, setGrant);
    if (serves.comments) row(t("Comments"), cGrantOf, function (g) {
      setCGrant(g);
      toPage({ type: "grants", grants: grantsOf() });
    });
    if (serves.files) row(t("Files"), fGrantOf, function (g) {
      setFGrant(g);
      toPage({ type: "grants", grants: grantsOf() });
    });
    manifest.forEach(function (m) {
      row(m.server, function () { return mGrantOf(m.server); }, function (g) {
        setMGrant(m.server, g);
        toPage({ type: "grants", grants: grantsOf() });
      });
    });
    var close = button(t("Close"));
    node.appendChild(el("div", "acts", [close]));
    perms.done = function () {
      if (!perms) return;
      perms = null;
      node.classList.remove("in");
      setTimeout(function () { node.remove(); }, 120);
      answer();
    };
    close.addEventListener("click", function () { if (perms) perms.done(); });
    ui.appendChild(node);
    node.getBoundingClientRect();
    node.classList.add("in");
    close.focus();
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
            // posted: the box empties now, not once the list is back, so
            // the page's next ask finds nothing unsaved
            c.set("");
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
  function sendFromPage(d, waited) {
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
      if (!byId(d.threadId)) {
        if (loading && !waited) loading.then(function () { sendFromPage(d, true); });
        else answer({ error: "not_found" });
        return;
      }
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

  // -- install as an app (app.go) ----------------------------------------
  // The newest page installs from the browser: its button shows where the
  // browser offers it (Chromium), and Library's Install as app opens the
  // page with #install, which asks, or says how in this browser.
  var installEvt = null, installBtn = null, installAsk = null;
  var wantInstall = location.hash === "#install";
  if (wantInstall) history.replaceState(null, "", location.pathname + location.search);
  var standalone = matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
  if (!C.pinned && C.app) {
    if (navigator.serviceWorker) navigator.serviceWorker.register(C.app + "/app-sw.js", { scope: C.app.replace(/_api$/, "") }).catch(function () {});
    addEventListener("beforeinstallprompt", function (e) {
      e.preventDefault();
      installEvt = e;
      if (!installBtn && !standalone) {
        installBtn = icon("install", t("Install as app"), " ");
        installBtn.addEventListener("click", install);
        bar.appendChild(installBtn);
      }
      if (wantInstall) { wantInstall = false; askInstall(); }
    });
    addEventListener("appinstalled", function () {
      installEvt = null;
      if (installBtn) { installBtn.remove(); installBtn = null; }
      if (installAsk) installAsk.done();
    });
    // no offer by now: the browser can't, or it's installed already
    if (wantInstall && !standalone) setTimeout(function () { if (wantInstall) { wantInstall = false; askInstall(); } }, 2000);
  }
  function install() {
    var e = installEvt;
    if (!e) return;
    // an offer is asked once; the browser makes another if it can
    installEvt = null;
    if (installBtn) { installBtn.remove(); installBtn = null; }
    e.prompt();
  }
  // asked from Library: the browser's own question needs a click in the
  // page, so this one comes first; with no offer, how to do it here
  function askInstall() {
    if (installAsk) return;
    var node = el("div", "pop save");
    node.setAttribute("role", "alertdialog");
    var q = el("div", "ask"), acts = el("div", "acts");
    q.id = "install-q";
    node.setAttribute("aria-labelledby", "install-q");
    node.appendChild(q);
    var done = function () {
      if (installAsk !== mine) return;
      installAsk = null;
      node.classList.remove("in");
      setTimeout(function () { node.remove(); }, 120);
    };
    var mine = { done: done };
    if (installEvt) {
      q.textContent = t("Install “{name}”?", { name: C.title });
      node.appendChild(el("div", "size", [t("It opens in its own window, always at its newest version.")]));
      var no = button(t("Cancel")), yes = button(t("Install"), "primary");
      no.addEventListener("click", done);
      yes.addEventListener("click", function () { done(); install(); });
      acts.appendChild(no);
      acts.appendChild(yes);
    } else {
      var safari = /Safari\//.test(navigator.userAgent) && !/Chrom|Edg\/|Firefox|FxiOS|CriOS/.test(navigator.userAgent);
      q.textContent = safari ? t("In Safari, choose File › Add to Dock.") : window.chrome ? t("Use the app button in the address bar.") : t("This browser can't install apps. Open the page in Chrome, Edge or Safari.");
      var ok = button(t("OK"), "primary");
      ok.addEventListener("click", done);
      acts.appendChild(ok);
    }
    node.appendChild(acts);
    installAsk = mine;
    ui.appendChild(node);
    node.getBoundingClientRect();
    node.classList.add("in");
  }

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
      else if (installAsk) installAsk.done();
      else if (fAsk) fAsk.done("dismissed");
      else if (cAsk) cAsk.done("dismissed");
      else if (dAsk) dAsk.done("dismissed");
      else if (mAsk) mAsk.done("dismissed");
      else if (perms) perms.done();
      else if (listenBar) stopDict(dict, "viewer_stopped");
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
