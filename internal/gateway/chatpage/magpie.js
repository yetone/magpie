// magpie's addition to DeepSeek's chat page: the key on this computer,
// and which of magpie's models answers. The page asks for one model of
// DeepSeek's; magpie answers with the one picked here, which it reads
// from the magpie_chat_model cookie.
(function () {
  "use strict";
  var KEY = "chat-demo:api-key";
  var COOKIE = "magpie_chat_model";
  var PICKED = "magpie:chat-model";
  var local = /^(localhost|127\.0\.0\.1|\[::1\])$/.test(location.hostname);
  var store = {
    get: function (k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set: function (k, v) { try { localStorage.setItem(k, v); } catch (e) {} },
  };
  // on this computer the page needs no key of the user's: magpie takes
  // its own agent's
  if (local && !store.get(KEY)) store.set(KEY, "magpie-deepseek-chat");

  function setCookie(id) {
    document.cookie = COOKIE + "=" + encodeURIComponent(id) + "; Path=/deepseek-chat; SameSite=Strict; Max-Age=31536000";
  }
  var picked = store.get(PICKED);
  if (picked) setCookie(picked);

  var css = [
    "#magpie-model{position:fixed;top:12px;right:64px;z-index:2147483000;font:13px/1.2 system-ui,-apple-system,sans-serif;color-scheme:inherit}",
    "#magpie-model button{all:unset;box-sizing:border-box;cursor:pointer}",
    "#magpie-model .mm-btn{display:inline-flex;align-items:center;gap:6px;height:30px;padding:0 10px;border-radius:8px;max-width:260px;color:#3a3a3c;color:light-dark(#3a3a3c,#e5e5ea);background:transparent;transition:background-color .15s ease}",
    "#magpie-model .mm-btn span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}",
    "#magpie-model .mm-btn svg{flex:none;opacity:.6}",
    "@media (hover:hover) and (pointer:fine){#magpie-model .mm-btn:hover{background:rgba(127,127,127,.12)}}",
    "#magpie-model .mm-btn:active{transform:scale(.97)}",
    "#magpie-model .mm-menu{position:absolute;top:36px;right:0;min-width:220px;max-width:min(360px,calc(100vw - 24px));max-height:min(420px,calc(100vh - 72px));overflow:auto;padding:4px;border-radius:10px;background:#fff;background:light-dark(#fff,#2c2c2e);box-shadow:0 0 0 .5px rgba(0,0,0,.12),0 8px 28px rgba(0,0,0,.16);transform-origin:top right;opacity:0;transform:scale(.95);transition:opacity .15s cubic-bezier(.23,1,.32,1),transform .15s cubic-bezier(.23,1,.32,1);pointer-events:none}",
    "#magpie-model.open .mm-menu{opacity:1;transform:none;pointer-events:auto;transition-duration:.2s}",
    "#magpie-model .mm-item{display:flex;align-items:center;gap:8px;width:100%;padding:7px 10px;border-radius:6px;color:#1c1c1e;color:light-dark(#1c1c1e,#f2f2f7)}",
    "#magpie-model .mm-item span{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}",
    "#magpie-model .mm-item:hover,#magpie-model .mm-item:focus-visible{background:rgba(127,127,127,.14)}",
    "#magpie-model .mm-item .mm-on{visibility:hidden}",
    "#magpie-model .mm-item[aria-checked=true] .mm-on{visibility:visible}",
    "@media (max-width:640px){#magpie-model{right:56px}}",
    "@media (prefers-reduced-motion:reduce){#magpie-model .mm-menu{transform:none}}",
  ].join("\n");

  var chevron = '<svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><path d="M2 3.5 5 6.5 8 3.5" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  var check = '<svg class="mm-on" width="12" height="12" viewBox="0 0 12 12" aria-hidden="true"><path d="M2.5 6.2 5 8.5 9.5 3.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>';

  function el(tag, cls, html) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (html) e.innerHTML = html;
    return e;
  }

  function mount(list) {
    if (!list.data || !list.data.length || document.getElementById("magpie-model")) return;
    var current = list.data.some(function (m) { return m.id === picked; }) ? picked : list["default"];
    var style = el("style");
    style.textContent = css;
    document.head.appendChild(style);
    var box = el("div");
    box.id = "magpie-model";
    var btn = el("button", "mm-btn");
    btn.setAttribute("aria-haspopup", "menu");
    btn.setAttribute("aria-expanded", "false");
    var name = el("span");
    btn.appendChild(name);
    btn.insertAdjacentHTML("beforeend", chevron);
    var menu = el("div", "mm-menu");
    menu.setAttribute("role", "menu");
    var shown = function (id) {
      var m = list.data.filter(function (x) { return x.id === id; })[0];
      return (m && (m.display_name || m.id)) || id;
    };
    list.data.forEach(function (m) {
      var it = el("button", "mm-item");
      it.setAttribute("role", "menuitemradio");
      it.dataset.id = m.id;
      it.title = m.id;
      var s = el("span");
      s.textContent = m.display_name || m.id;
      it.appendChild(s);
      it.insertAdjacentHTML("beforeend", check);
      it.addEventListener("click", function () {
        current = m.id;
        store.set(PICKED, m.id);
        setCookie(m.id);
        draw();
        toggle(false);
        btn.focus();
      });
      menu.appendChild(it);
    });
    function draw() {
      name.textContent = shown(current);
      btn.title = current;
      menu.querySelectorAll(".mm-item").forEach(function (it) {
        it.setAttribute("aria-checked", String(it.dataset.id === current));
      });
    }
    function toggle(open) {
      box.classList.toggle("open", open);
      btn.setAttribute("aria-expanded", String(open));
      if (open) {
        var on = menu.querySelector('[aria-checked="true"]');
        if (on) { on.scrollIntoView({ block: "nearest" }); on.focus(); }
      }
    }
    btn.addEventListener("click", function () { toggle(!box.classList.contains("open")); });
    document.addEventListener("mousedown", function (e) { if (!box.contains(e.target)) toggle(false); });
    box.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { toggle(false); btn.focus(); return; }
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      var items = Array.prototype.slice.call(menu.querySelectorAll(".mm-item"));
      var i = items.indexOf(document.activeElement);
      if (i < 0) { toggle(true); e.preventDefault(); return; }
      i = (i + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
      items[i].focus();
      e.preventDefault();
    });
    draw();
    box.appendChild(btn);
    box.appendChild(menu);
    document.body.appendChild(box);
  }

  var tries = 0;
  function load() {
    var key = store.get(KEY);
    if (!key) {
      // the page asks for the key; once it is typed the models can be read
      if (tries++ < 600) setTimeout(load, 1000);
      return;
    }
    fetch("models", { headers: { Authorization: "Bearer " + key } })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (list) {
        if (list) mount(list);
        else if (tries++ < 600) setTimeout(load, 3000);
      })
      .catch(function () { if (tries++ < 600) setTimeout(load, 3000); });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", load);
  else load();
})();
