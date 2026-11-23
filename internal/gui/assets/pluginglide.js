// Discover's kinds (the owner, 11-23: 切换下面的 Tab 的时候，不要直接把上面
// 的推荐榜删掉，而是丝滑地滚动到下面去). A kind picked, or a shelf's See all,
// keeps the week's picks above and glides the page until the kind bar is at
// its top. plugins.js imports this when its page first draws, so it weighs
// nothing on the core.
//
// A kind gets room after its shelves (pm-room, Discover's own: app.js's
// view-room is trimmed as the page scrolls): a view's height when it is
// picked, so the page never jumps up as a short kind is drawn, trimmed to
// what the bar at the top needs once the glide is done; All gives it back.
// The glide is our own, the strong ease-in-out of a move on screen, under
// 300ms, the same in both engines; a wheel or drag on the way takes over.
// Reduced motion goes there at once.
(() => {
  const box = el("div", "pm-room");
  box.setAttribute("aria-hidden", "true");
  box.hidden = true;
  let roomH = 0, roomKind = "", busy = false, heard = null;
  const set = (h) => {
    roomH = Math.max(0, Math.ceil(h));
    box.hidden = !roomH;
    box.style.height = roomH ? roomH + "px" : "";
  };
  // only the room that keeps the view where it is now
  const trim = (page) => {
    if (box.isConnected) set(page.scrollTop + page.clientHeight - (contentEnd(page) - roomH));
  };
  // cubic-bezier(.77, 0, .175, 1): the curve's t for x, then its y
  const c = (t, a, b) => 3 * a * t * (1 - t) ** 2 + 3 * b * t * t * (1 - t) + t ** 3;
  const inOut = (x) => {
    let lo = 0, hi = 1, t = x;
    for (let i = 0; i < 24; i++) { t = (lo + hi) / 2; if (c(t, 0.77, 0.175) < x) lo = t; else hi = t; }
    return c(t, 0, 1);
  };
  const done = (page) => { busy = false; arrived(page); trim(page); };
  window.pmGlide = {
    // a glide on its way (tests wait for it to land before the next click)
    get busy() { return busy; },
    // drawBody places it after the shelves on every draw. A kind newly
    // picked is drawn with a view's height of room, so a short kind never
    // pulls the page up before the glide; the glide's end trims it.
    room(kind, page) {
      // a kind's tab, or a shelf's See all: heard once the page has drawn
      // the kind picked (its handler runs first, the click bubbling here)
      if (page && heard !== page) {
        heard = page;
        page.addEventListener("click", (e) => {
          if (e.target.closest?.(".pm-bar .pm-kinds .opt, .pm-sec[data-kind] .pm-more")) pmGlide.to(e, page, page.querySelector(".pm-kinds .opt.on")?.dataset.seg || "");
        });
      }
      if (!kind) set(0);
      else if (kind !== roomKind && page) set(Math.max(roomH, page.clientHeight));
      roomKind = kind;
      return box;
    },
    to(e, page, kind) {
      if (!kind) set(0);
      if (!scrollOnPurpose(e, 2500)) return requestAnimationFrame(() => trim(page));
      busy = true;
      requestAnimationFrame(() => {
        const bar = page.querySelector(".pm-bar");
        if (!bar) { busy = false; return trim(page); }
        const pad = parseFloat(getComputedStyle(page).paddingTop) || 0;
        // narrow (no sidebar) the page's tabs stay stuck at its top, on a
        // strip reaching 6px below them: the bar stops under it, not beneath
        const head = page.querySelector(":scope > .pm-head"), hs = head && getComputedStyle(head);
        const over = hs?.position === "sticky" ? (parseFloat(hs.top) || 0) + head.offsetHeight + 6 + 4 : 0;
        const to = Math.round(page.scrollTop + bar.getBoundingClientRect().top - page.getBoundingClientRect().top - pad - over);
        const from = page.scrollTop, d = to - from;
        const short = Math.ceil(to + page.clientHeight - contentEnd(page));
        if (short > 0 && box.isConnected) set(roomH + short);
        if (Math.abs(d) < 2) return done(page);
        if (calm()) { page.scrollTop = to; return done(page); }
        const t0 = performance.now(), ms = 280;
        let last = from;
        const step = (now) => {
          if (Math.abs(page.scrollTop - last) > 1) { busy = false; return arrived(page); } // the reader took over
          const p = Math.min(1, (now - t0) / ms);
          page.scrollTop = last = Math.round(from + d * inOut(p));
          if (p < 1) requestAnimationFrame(step);
          else done(page);
        };
        requestAnimationFrame(step);
      });
    },
  };
})();
