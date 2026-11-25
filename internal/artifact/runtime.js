// The page runtime (docs/subsystems/artifacts.md, "Page runtime"): the
// window.claude.use(name) a page written for claude.ai's artifact runtime
// (contract 0.2.99) calls, served by magpie. It goes into the page's
// <head> ahead of the page's own scripts, with its config: the API it
// calls, the token that names this artifact's page, the capabilities the
// version declares, the viewer, who on this computer is the artifact's
// owner, and the version the page is (pinned: its address names it).
(function (C) {
  "use strict";
  if (window.claude && window.claude.use) return;
  // a shared copy (usemagpie.ai) serves no store and knows no viewer:
  // every capability resolves null there, as it does for a page's
  // visitor that claude.ai can't serve
  var publishing = !!(C.caps.artifact || C.caps.self);
  var served = C.shared ? { permissions: true } : { db: !!C.caps.db, user: true, permissions: true, artifact: publishing, self: publishing, assets: !!C.caps.assets, sample: !!C.caps.sample, comments: !!C.caps.comments && !!C.origin, downloads: !!C.caps.downloads && !!C.origin, tts: !!C.caps.tts && !!C.origin && typeof Audio === "function", dictation: !!C.caps.dictation && !!C.origin && typeof MediaRecorder === "function", mcp: !!C.caps.mcp && !!C.origin && window.parent !== window };
  var freeze = Object.freeze;
  function deepFreeze(v) {
    if (v && typeof v === "object" && !Object.isFrozen(v)) {
      freeze(v);
      for (var k in v) deepFreeze(v[k]);
    }
    return v;
  }
  function later(fn) {
    return new Promise(function (res) { setTimeout(function () { res(fn()); }, 0); });
  }
  function dbError(code, message) {
    var e = new Error(message || code);
    e.name = "DbError";
    e.code = code;
    return e;
  }

  // -- the API ------------------------------------------------------------
  // a POST to <api>/<what>, as text/plain so it needs no preflight; a
  // refusal rejects with mkErr(code, message, the error's other fields),
  // and magpie out of reach with down
  function post(what, body, mkErr, down) {
    var text;
    try {
      text = JSON.stringify(body);
    } catch (e) {
      return Promise.reject(mkErr("transform_error", "the arguments aren't JSON: " + e.message));
    }
    return fetch(C.api + "/" + what, { method: "POST", body: text, headers: { "Content-Type": "text/plain" }, cache: "no-store" })
      .then(function (r) {
        return r.json().catch(function () { return { error: { code: down, message: "magpie answered " + r.status } }; });
      }, function (e) {
        throw mkErr(down, "magpie can't be reached: " + e.message);
      })
      .then(function (j) {
        if (j && j.error) throw mkErr(j.error.code || down, j.error.message, j.error);
        return j;
      });
  }
  function call(op, body) {
    return post("db", Object.assign({ t: C.token, op: op }, body), dbError, "unavailable");
  }

  // -- paths --------------------------------------------------------------
  var segRe = /^[A-Za-z0-9_\-.~:@+]+$/;
  function checkPath(p, doc) {
    if (typeof p !== "string") throw new TypeError("a path is a string, not " + (p === null ? "null" : typeof p) + (p && typeof p.then === "function" ? " (await the id first)" : ""));
    var segs = p.split("/");
    if (p.length > 1000) throw new TypeError("a path is at most 1000 bytes");
    if (!p || segs.length > 16) throw new TypeError("a path has 1 to 16 segments; " + JSON.stringify(p) + " has " + (p ? segs.length : 0));
    for (var i = 0; i < segs.length; i++) {
      var s = segs[i];
      if (!s) throw new TypeError("a path has no empty segment: " + JSON.stringify(p));
      if (s === "." || s === "..") throw new TypeError(JSON.stringify(s) + " can't be a segment");
      if (new TextEncoder().encode(s).length > 200) throw new TypeError("a segment is at most 200 bytes");
      if (!segRe.test(s)) throw new TypeError("segment " + JSON.stringify(s) + ": segments use letters, digits and _ - . ~ : @ + only");
    }
    if (doc && segs.length % 2) throw new TypeError(JSON.stringify(p) + " has " + segs.length + " segments, so it is a collection: a document's path has an even number");
    if (!doc && !(segs.length % 2)) throw new TypeError(JSON.stringify(p) + " has " + segs.length + " segments, so it is a document: a collection's path has an odd number");
    return p;
  }
  function newId() {
    var a = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", b = new Uint8Array(20), s = "";
    crypto.getRandomValues(b);
    for (var i = 0; i < b.length; i++) s += a[b[i] % 62];
    return s;
  }
  function parentOf(p) { return p.slice(0, p.lastIndexOf("/")); }

  // -- snapshots ----------------------------------------------------------
  var meta = { cached: freeze({ fromCache: false, hasPendingWrites: false }), pending: freeze({ fromCache: false, hasPendingWrites: true }) };
  function docSnap(id, d, pending) {
    var data = d ? deepFreeze(d.data) : undefined;
    return freeze({ id: id, exists: !!d, data: function () { return data; }, metadata: pending ? meta.pending : meta.cached });
  }
  function key(d) { return d ? d.version + "@" + d.updated : ""; }

  // -- live updates -------------------------------------------------------
  // one stream for the page; each listener reads again when a write it
  // can see is made, and every 30 s while the stream is down. A page that
  // publishes itself (artifact) hears each new version on it too, and
  // reloads to it, unless its address names its version
  var listeners = new Set(), source = null, poll = null;
  var version = C.version || 0, mine = new Set(), leaving = false;
  // a version heard while this view's own publish is on its way may be
  // that publish's: it waits for the answer
  var inflight = 0, heldN = 0;
  var follows = served.artifact && !C.pinned && version > 0;
  function heard(paths) {
    listeners.forEach(function (l) { if (!paths || l.touches(paths)) l.refresh(); });
  }
  function newer(n) {
    if (!follows) return;
    if (inflight) heldN = Math.max(heldN, n);
    else if (n > version && !mine.has(n)) reload();
  }
  function settled() {
    if (--inflight || !heldN) return;
    var n = heldN;
    heldN = 0;
    newer(n);
  }
  // the view goes to the newest version: a pinned one to the artifact's
  // own address
  function reload() {
    if (leaving) return;
    leaving = true;
    setTimeout(function () {
      if (C.pinned && C.home) location.assign(C.home);
      else location.reload();
    }, 30);
  }
  function connect() {
    if (source || !(listeners.size || follows) || typeof EventSource !== "function") return;
    source = new EventSource(C.api + "/events?t=" + encodeURIComponent(C.token) + (follows ? "&v=" + version : ""));
    source.onmessage = function (e) {
      var m;
      try { m = JSON.parse(e.data); } catch (_) { return; }
      if (m.version) return newer(+m.version);
      heard(m.paths || null);
    };
    source.onopen = function () {
      if (poll) { clearInterval(poll); poll = null; heard(null); }
    };
    source.onerror = function () {
      if (!poll) poll = setInterval(function () { heard(null); }, 30000);
    };
  }
  function disconnect() {
    if (listeners.size || follows) return;
    if (source) { source.close(); source = null; }
    if (poll) { clearInterval(poll); poll = null; }
  }
  function listen(l, error) {
    if (listeners.size >= 64) {
      setTimeout(function () { fail(error, dbError("resource_exhausted", "a page has at most 64 subscriptions")); }, 0);
      return function () {};
    }
    listeners.add(l);
    connect();
    l.refresh();
    return function () { l.dead = true; listeners.delete(l); disconnect(); };
  }
  function fail(error, e) {
    if (error) error(e);
    else if (typeof reportError === "function") reportError(e);
    else setTimeout(function () { throw e; }, 0);
  }
  // listener: reads one at a time, again when asked mid-read
  function reader(read, error) {
    var l = { busy: false, again: false, dead: false };
    l.refresh = function () {
      if (l.dead) return;
      if (l.busy) { l.again = true; return; }
      l.busy = true;
      read().then(function () {
        l.busy = false;
        if (l.again) { l.again = false; l.refresh(); }
      }, function (e) {
        l.busy = false;
        if (l.dead) return;
        if (e.code === "unavailable") { l.again = false; return; } // the next event or poll reads again
        l.dead = true;
        listeners.delete(l);
        disconnect();
        fail(error, e);
      });
    };
    return l;
  }

  // -- refs ---------------------------------------------------------------
  var pendingDocs = new Map(); // path → count of this page's writes in flight
  function written(path, p) {
    pendingDocs.set(path, (pendingDocs.get(path) || 0) + 1);
    var done = function () {
      var n = pendingDocs.get(path) - 1;
      if (n) pendingDocs.set(path, n); else pendingDocs.delete(path);
      heard([path]);
    };
    return p.then(function (v) { done(); return v; }, function (e) { done(); throw e; });
  }
  function docRef(path) {
    checkPath(path, true);
    var id = path.slice(path.lastIndexOf("/") + 1);
    var self;
    var optimistic = new Set();
    function write(op, data) {
      if (op !== "delete" && (data === null || typeof data !== "object" || Array.isArray(data))) {
        return Promise.reject(dbError("invalid_argument", "a document's body is a JSON object"));
      }
      var body = { path: path };
      if (op !== "delete") {
        try { body.data = JSON.parse(JSON.stringify(data)); } catch (e) { return Promise.reject(dbError("transform_error", e.message)); }
      }
      optimistic.forEach(function (f) { f(op, body.data); });
      return written(path, call(op, body)).then(function () {});
    }
    self = {
      id: id,
      path: path,
      get: function () {
        return call("get", { path: path }).then(function (j) { return docSnap(id, j.doc, false); });
      },
      set: function (data) { return write("set", data); },
      update: function (data) { return write("update", data); },
      delete: function () { return write("delete"); },
      acquire: function (o) {
        o = o || {};
        return call("acquire", { path: path, holder: String(o.holder == null ? "" : o.holder), ttlMs: o.ttlMs | 0, data: o.data || {} })
          .then(function (j) { heard([path]); return freeze(j.lease); });
      },
      onSnapshot: function (next, error) {
        var last = null, lastKey = null, l;
        l = reader(function () {
          return call("get", { path: path }).then(function (j) {
            if (l.dead) return;
            var k = key(j.doc) + (pendingDocs.has(path) ? "*" : "");
            if (k === lastKey) return;
            lastKey = k;
            last = docSnap(id, j.doc, pendingDocs.has(path));
            next(last);
          });
        }, error);
        l.touches = function (paths) { return paths.indexOf(path) >= 0; };
        // this page's own writes show at once, marked pending
        var mine = function (op, data) {
          if (l.dead || !last) return;
          var base = last.exists ? last.data() : null, d;
          if (op === "delete") d = null;
          else if (op === "set") d = { data: data };
          else if (base) d = { data: merge(base, data) };
          else return;
          lastKey = null;
          last = docSnap(id, d, true);
          next(last);
        };
        optimistic.add(mine);
        var off = listen(l, error);
        return function () { optimistic.delete(mine); off(); };
      },
      collection: function (sub) { return collRef(path + "/" + checkSegs(sub)); },
    };
    return freeze(self);
  }
  function checkSegs(p) {
    if (typeof p !== "string") throw new TypeError("a path is a string");
    return p;
  }
  function merge(base, patch) {
    var out = {};
    for (var k in base) out[k] = base[k];
    for (var k2 in patch) {
      var b = out[k2], p = patch[k2];
      out[k2] = p && b && typeof p === "object" && typeof b === "object" && !Array.isArray(p) && !Array.isArray(b) ? merge(b, p) : p;
    }
    return out;
  }
  function query(coll, q) {
    var self = {
      where: function (field, op, value) {
        return query(coll, Object.assign({}, q, { where: q.where.concat([{ field: String(field), op: String(op), value: value }]) }));
      },
      orderBy: function (field, dir) {
        return query(coll, Object.assign({}, q, { orderBy: q.orderBy.concat([{ field: String(field), desc: dir === "desc" }]) }));
      },
      limit: function (n) { return query(coll, Object.assign({}, q, { limit: n })); },
      get: function () {
        return run().then(function (docs) { return querySnap(docs.map(function (d) { return docSnap(d.id, d, pendingDocs.has(d.path)); }), null); });
      },
      onSnapshot: function (next, error) {
        var prev = null, byPath = new Map(), l;
        l = reader(function () {
          return run().then(function (docs) {
            if (l.dead) return;
            var now = new Map(), changed = !prev || prev.docs.length !== docs.length;
            var snaps = docs.map(function (d, i) {
              var k = key(d) + (pendingDocs.has(d.path) ? "*" : ""), was = byPath.get(d.path);
              var s = was && was.k === k ? was.s : docSnap(d.id, d, pendingDocs.has(d.path));
              now.set(d.path, { k: k, s: s, i: i });
              if (!prev || prev.docs[i] !== s) changed = true;
              return s;
            });
            if (!changed) return;
            var changes = [];
            byPath.forEach(function (v, p) {
              if (!now.has(p)) changes.push(freeze({ type: "removed", doc: v.s, oldIndex: v.i, newIndex: -1 }));
            });
            now.forEach(function (v, p) {
              var was = byPath.get(p);
              if (!was) changes.push(freeze({ type: "added", doc: v.s, oldIndex: -1, newIndex: v.i }));
              else if (was.s !== v.s || was.i !== v.i) changes.push(freeze({ type: "modified", doc: v.s, oldIndex: was.i, newIndex: v.i }));
            });
            byPath = now;
            prev = querySnap(snaps, freeze(changes));
            next(prev);
          });
        }, error);
        l.touches = function (paths) {
          return paths.some(function (p) { return parentOf(p) === coll; });
        };
        return listen(l, error);
      },
    };
    function run() {
      if (q.orderBy.length > 1) return Promise.reject(dbError("invalid_argument", "a query has at most one orderBy"));
      var o = q.orderBy[0] || {};
      var lim = q.limit;
      if (lim !== undefined && (typeof lim !== "number" || lim !== Math.floor(lim) || lim < 1 || lim > 1000)) {
        return Promise.reject(dbError("invalid_argument", "a limit is 1 to 1000"));
      }
      return call("query", { collection: coll, where: q.where, orderBy: o.field || "", desc: !!o.desc, limit: lim || 0 }).then(function (j) { return j.docs; });
    }
    return self;
  }
  function querySnap(docs, changes) {
    var all = changes || freeze(docs.map(function (d, i) { return freeze({ type: "added", doc: d, oldIndex: -1, newIndex: i }); }));
    var pending = docs.some(function (d) { return d.metadata.hasPendingWrites; });
    return freeze({ docs: freeze(docs), size: docs.length, empty: !docs.length, docChanges: function () { return all; }, metadata: pending ? meta.pending : meta.cached });
  }
  function collRef(path) {
    checkPath(path, false);
    var q = query(path, { where: [], orderBy: [] });
    return freeze(Object.assign({}, q, {
      path: path,
      doc: function (id) { return docRef(path + "/" + (id === undefined ? newId() : checkSegs(id))); },
      add: function (data) {
        var ref = docRef(path + "/" + newId());
        return ref.set(data).then(function () { return ref; });
      },
    }));
  }
  var db = freeze({
    doc: function (p) { return docRef(checkPath(p, true)); },
    collection: function (p) { return collRef(checkPath(p, false)); },
  });

  // -- user ---------------------------------------------------------------
  // on this computer the viewer is the artifact's owner, with no name or
  // picture magpie may give a page
  var uid = C.caps.user ? C.viewer.id : null;
  var palette = ["#5E81AC", "#A3785F", "#6F8F72", "#8E6C8A", "#B0884A", "#5F8A8B", "#9C6B6B", "#7A7F8C"];
  function colorOf(id) {
    var h = 0;
    for (var i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
    return palette[h % palette.length];
  }
  function avatar(color) {
    return "data:image/svg+xml," + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="16" fill="' + color + '"/><circle cx="16" cy="12.5" r="5.5" fill="#fff" fill-opacity=".85"/><path d="M6.5 27a9.5 9.5 0 0 1 19 0" fill="#fff" fill-opacity=".85"/></svg>');
  }
  var myColor = uid ? colorOf(uid) : "#8E8E93";
  var me = freeze({ id: uid, name: "", avatarUrl: avatar(myColor), color: myColor, email: null, isOwner: true, canEdit: true });
  var can = { "data.write": true, "files.write": false, "assets.write": false, "people.add": false };
  var profileCache = new Map();
  function profile(id) {
    if (!profileCache.has(id)) {
      var c = colorOf(id);
      profileCache.set(id, freeze({ id: id, name: "", avatarUrl: avatar(c), color: c, email: null, isMe: id === uid, guest: false, roles: null }));
    }
    return profileCache.get(id);
  }
  var user = freeze({
    isOwner: function () { return Promise.resolve(true); },
    canEdit: function () { return Promise.resolve(true); },
    can: function (name) { return Promise.resolve(can[name] === true); },
    me: function () { return Promise.resolve(me); },
    id: function () { return Promise.resolve(uid); },
    name: function () { return Promise.resolve(""); },
    avatarUrl: function () { return Promise.resolve(uid ? me.avatarUrl : null); },
    email: function () { return Promise.resolve(null); },
    profiles: function (ids) {
      var out = {};
      if (C.caps.user) {
        [].concat(ids == null ? [] : ids).forEach(function (id) { id = String(id); out[id] = profile(id); });
      }
      return Promise.resolve(freeze(out));
    },
    // hits always have a name, and no one here has one to give
    search: function () { return Promise.resolve(freeze([])); },
  });

  // -- artifact -----------------------------------------------------------
  // the page publishes itself: its whole page, or the files that changed,
  // as the artifact's newest version, which every open view reloads to
  function artifactError(code, message, more) {
    var e = new Error(message || code);
    e.name = "ArtifactError";
    e.code = code;
    if (more) ["live", "paths", "changed", "limit"].forEach(function (k) { if (more[k] !== undefined) e[k] = deepFreeze(more[k]); });
    return e;
  }
  function declared(name) {
    var c = C.caps[name];
    return c && typeof c === "object" ? c : null;
  }
  // {publish: false} on either name keeps the page from saving itself
  var mayPublish = [declared("artifact"), declared("self")].every(function (c) { return !c || c.publish !== false; });
  function bytes64(buf) {
    var u = new Uint8Array(buf), s = "";
    for (var i = 0; i < u.length; i += 0x8000) s += String.fromCharCode.apply(null, u.subarray(i, i + 0x8000));
    return btoa(s);
  }
  function blobBytes(b) {
    return (typeof b.arrayBuffer === "function" ? b.arrayBuffer() : new Response(b).arrayBuffer()).then(bytes64);
  }
  function isBlob(v) { return typeof Blob === "function" && v instanceof Blob; }
  function bare(t) { return String(t || "").split(";")[0].trim().toLowerCase(); }
  // one entry of a files publish, as magpie reads it
  function fileEntry(name, v) {
    if (v === null) return Promise.resolve(null);
    if (typeof v === "string") return Promise.resolve({ text: v });
    if (isBlob(v)) return blobBytes(v).then(function (b) { var e = { base64: b }; if (bare(v.type)) e.contentType = bare(v.type); return e; });
    if (v && typeof v === "object" && !Array.isArray(v)) {
      var e = {};
      if (Object.prototype.hasOwnProperty.call(v, "ifMatch")) {
        if (v.ifMatch !== null && typeof v.ifMatch !== "string") return Promise.reject(artifactError("invalid_content", name + ": ifMatch is a sha256 or null"));
        e.ifMatch = v.ifMatch;
      }
      if (v.delete === true) { e.delete = true; return Promise.resolve(e); }
      if (v.contentType !== undefined) e.contentType = String(v.contentType);
      if (typeof v.content === "string") { e.text = v.content; return Promise.resolve(e); }
      if (isBlob(v.content)) {
        return blobBytes(v.content).then(function (b) {
          e.base64 = b;
          if (e.contentType === undefined && bare(v.content.type)) e.contentType = bare(v.content.type);
          return e;
        });
      }
    }
    return Promise.reject(artifactError("invalid_content", name + ": a file is a string, a Blob, {content, contentType, ifMatch}, {delete: true} or null"));
  }
  function publish(what) {
    if (!mayPublish) return Promise.reject(artifactError("not_granted", "the artifact declares artifact with publish: false"));
    inflight++;
    return sendPublish(what).then(function (r) { settled(); return r; }, function (e) { settled(); throw e; });
  }
  function sendPublish(what) {
    if (typeof what === "string") {
      return post("publish", { t: C.token, base: version, html: what }, artifactError, "upstream_error").then(function (j) {
        // every open view, this one too, goes to it
        mine.add(+j.version);
        reload();
        return freeze({ version: j.version });
      }, lost);
    }
    if (!what || typeof what !== "object" || Array.isArray(what) || isBlob(what)) {
      return Promise.reject(artifactError("invalid_content", "publish takes the page's whole html, or its files by path"));
    }
    var names = Object.keys(what);
    if (!names.length) return Promise.reject(artifactError("invalid_content", "name the files that changed"));
    return Promise.all(names.map(function (n) { return fileEntry(n, what[n]); })).then(function (entries) {
      var files = {};
      names.forEach(function (n, i) { files[n] = entries[i]; });
      return post("publish", { t: C.token, base: version, files: files }, artifactError, "upstream_error");
    }).then(function (j) {
      // this view goes on, on the version it made; one that landed over
      // another's save reloads to bring theirs
      var n = +j.version;
      mine.add(n);
      version = Math.max(version, n);
      if (j.changed && j.changed.length) reload();
      var out = { version: j.version };
      if (j.changed) out.changed = j.changed;
      if (j.shas) out.shas = j.shas;
      return deepFreeze(out);
    }, lost);
  }
  // a conflict: another's version won, and the view goes to it
  function lost(e) {
    if (e && e.code === "conflict") reload();
    throw e;
  }
  var artifact = freeze({
    publish: publish,
    edit: function () { return Promise.reject(artifactError("not_granted", "a page here changes itself with publish")); },
    sync: function (fn) {
      if (!mayPublish) return Promise.reject(artifactError("not_granted", "the artifact declares artifact with publish: false"));
      return Promise.resolve().then(function () { return typeof fn === "function" ? fn() : undefined; }).then(function () {
        if (document.querySelector("artifact-sync, [artifact-sync]")) throw artifactError("invalid_content", "artifact-sync saves nothing: publish the page instead");
      });
    },
    metadata: function (refs) {
      var list = [].concat(refs == null ? [] : refs).map(String);
      var empty = function () {
        var out = {};
        list.forEach(function (r) { out[r] = freeze({ id: "", title: "", type: null }); });
        return freeze(out);
      };
      if (!list.length) return Promise.resolve(freeze({}));
      return post("meta", { t: C.token, refs: list }, artifactError, "upstream_error").then(function (j) {
        var out = {}, got = (j && j.artifacts) || {};
        list.forEach(function (r) {
          var e = got[r] || {};
          out[r] = freeze({ id: String(e.id || ""), title: String(e.title || ""), type: typeof e.type === "string" ? e.type : null });
        });
        return freeze(out);
      }, empty);
    },
  });
  if (served.artifact) {
    // claude.ai's elements for a sync no page here uses take no box
    var css = document.createElement("style");
    css.textContent = "artifact-sync,artifact-local{display:contents}";
    (document.head || document.documentElement).appendChild(css);
    connect();
  }

  // -- assets -------------------------------------------------------------
  // files the page uploads, kept with the artifact and served at
  // /_blob/<id> to every version of it
  function assetsError(code, message) {
    var e = new Error(message || code);
    e.name = "AssetsError";
    // the artifact gone, or the version: the state half of quota_or_state
    e.code = code === "revoked" ? "quota_or_state" : code;
    return e;
  }
  var assets = freeze({
    upload: function (blob, opts) {
      if (!isBlob(blob)) return Promise.reject(assetsError("invalid_request", "upload takes a Blob or a File"));
      if (!blob.size) return Promise.reject(assetsError("invalid_request", "the file is empty"));
      if (opts != null && (typeof opts !== "object" || opts.type !== undefined && typeof opts.type !== "string")) return Promise.reject(assetsError("invalid_request", "options is {type}, a media type"));
      var t = opts && opts.type !== undefined ? opts.type : blob.type;
      if (!t) return Promise.reject(assetsError("invalid_request", "the file has no type: pass {type}, e.g. image/png"));
      var url = C.api + "/upload?t=" + encodeURIComponent(C.token) + "&type=" + encodeURIComponent(t);
      // as text/plain, so it needs no preflight; its real type is in the url
      return fetch(url, { method: "POST", body: blob, headers: { "Content-Type": "text/plain" }, cache: "no-store" })
        .then(function (r) {
          return r.json().catch(function () { return { error: { code: r.status === 413 ? "too_large" : "upstream_error", message: "magpie answered " + r.status } }; });
        }, function (e) {
          throw assetsError("upstream_error", "magpie can't be reached: " + e.message);
        })
        .then(function (j) {
          if (j && j.error) throw assetsError(j.error.code || "upstream_error", j.error.message);
          return freeze({ id: j.id, url: j.url, sizeBytes: j.sizeBytes, contentType: j.contentType });
        });
    },
    list: function () {
      return post("assets", { t: C.token, op: "list" }, assetsError, "upstream_error").then(function (j) {
        return deepFreeze({ assets: j.assets || [], usage: j.usage });
      });
    },
    delete: function (ref) {
      if (typeof ref !== "string" || !ref) return Promise.reject(assetsError("invalid_request", "delete takes an asset's id or its url"));
      return post("assets", { t: C.token, op: "delete", ref: ref }, assetsError, "upstream_error").then(function (j) {
        return freeze({ deleted: !!j.deleted });
      });
    },
  });

  // -- sample -------------------------------------------------------------
  // the page asking a model: each round goes to <api>/sample and its
  // answer streams back as lines of JSON; a call with tools runs them
  // here, between rounds, and sends their results back
  var sampleLim = { prompt: 262144, images: 8, imageInput: 20000000, imageSide: 10000, imagePixels: 64e6, imageTarget: 1.2e6, imageOut: 5 << 20, tools: 32, rounds: 8, toolResult: 32768, toolTime: 150000, gcMost: 864e5 };
  var sampleTypes = ["image/jpeg", "image/png", "image/webp", "image/gif"];
  var sampleTiers = ["default", "complex", "quick"];
  var sampleImages = !!(C.caps.sample && C.caps.sample.images);
  function sampleErr(code, message, text, extra) {
    var e = { code: code, message: message || code };
    if (text) e.text = text;
    if (extra && extra.reason) e.reason = extra.reason;
    return freeze(e);
  }
  function isPlain(v) {
    if (!v || typeof v !== "object") return false;
    var p = Object.getPrototypeOf(v);
    return p === Object.prototype || p === null;
  }
  function sampleTurns(input) {
    var turns = [];
    if (typeof input === "string") {
      if (!input.trim()) throw sampleErr("invalid_request", "the input is empty");
      turns.push({ role: "user", content: input });
    } else if (Array.isArray(input)) {
      if (!input.length) throw sampleErr("invalid_request", "the input has no turns");
      input.forEach(function (t, i) {
        if (!isPlain(t) || (t.role !== "user" && t.role !== "assistant")) throw sampleErr("invalid_request", "turn " + i + " is {role: \"user\" | \"assistant\", content}");
        if (typeof t.content !== "string" || !t.content.trim()) throw sampleErr("invalid_request", "turn " + i + "'s content is a string with text in it");
        var last = turns[turns.length - 1];
        if (last && last.role === t.role) last.content += "\n\n" + t.content;
        else turns.push({ role: t.role, content: t.content });
      });
      if (turns[0].role !== "user" || turns[turns.length - 1].role !== "user") throw sampleErr("invalid_request", "the turns start and end on a user turn");
    } else {
      throw sampleErr("invalid_request", "the input is a string or a list of {role, content} turns");
    }
    var n = 0, enc = new TextEncoder();
    turns.forEach(function (t) { n += enc.encode(t.content).length; });
    if (n > sampleLim.prompt) throw sampleErr("prompt_too_large", "the input is " + Math.ceil(n / 1024) + " KiB; a call takes at most 256 KiB");
    return turns;
  }
  // the options, checked: what the round sends and what the page keeps
  function sampleOpts(o, verbJSON) {
    if (o == null) o = {};
    if (!isPlain(o)) throw sampleErr("invalid_request", "options is an object");
    var out = { body: { json: verbJSON }, tools: null, images: null };
    if (o.onText !== undefined && typeof o.onText !== "function") throw sampleErr("invalid_request", "onText is a function");
    out.onText = o.onText;
    if (o.signal !== undefined && !(typeof AbortSignal === "function" && o.signal instanceof AbortSignal)) throw sampleErr("invalid_request", "signal is an AbortSignal");
    out.signal = o.signal;
    if (o.modelTier !== undefined) {
      if (o.modelTier !== "inherit" && sampleTiers.indexOf(o.modelTier) < 0) throw sampleErr("invalid_request", "modelTier is \"default\", \"complex\" or \"quick\"");
      out.body.modelTier = o.modelTier === "inherit" ? "default" : o.modelTier;
    }
    if (o.model !== undefined) {
      if (typeof o.model !== "string") throw sampleErr("invalid_request", "model is a string");
      out.body.model = o.model;
    }
    if (o.effort !== undefined) {
      if (["low", "medium", "high"].indexOf(o.effort) >= 0) out.body.effort = o.effort;
      else console.warn("claude.use(\"sample\"): effort is low, medium or high; " + JSON.stringify(o.effort) + " is left out");
    }
    if (o.tools !== undefined) {
      if (!Array.isArray(o.tools)) throw sampleErr("invalid_request", "tools is a list");
      if (o.tools.length > sampleLim.tools) throw sampleErr("invalid_request", "a call offers at most " + sampleLim.tools + " tools");
      var names = {};
      out.tools = {};
      out.body.tools = o.tools.map(function (t, i) {
        if (!isPlain(t)) throw sampleErr("invalid_request", "tools[" + i + "] is {name, description, inputSchema?, execute}");
        if (typeof t.name !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(t.name) || names[t.name]) throw sampleErr("invalid_request", "tools[" + i + "]'s name is 1-128 of A-Z a-z 0-9 _ -, unique in the list");
        names[t.name] = true;
        if (typeof t.description !== "string" || !t.description.trim() || t.description.length > 1024) throw sampleErr("invalid_request", "tools[" + i + "] (" + t.name + ") needs a description of at most 1 KB");
        if (typeof t.execute !== "function") throw sampleErr("invalid_request", "tools[" + i + "] (" + t.name + ")'s execute is a function");
        var schema;
        if (t.inputSchema !== undefined) {
          var s;
          try { s = JSON.stringify(t.inputSchema); } catch (e) { s = ""; }
          if (!isPlain(t.inputSchema) || t.inputSchema.type !== "object" || !s || s.length > 4096) throw sampleErr("invalid_request", "tools[" + i + "] (" + t.name + ")'s inputSchema is a JSON Schema of type object, at most 4 KB");
          schema = t.inputSchema;
        }
        out.tools[t.name] = t.execute;
        return { name: t.name, description: t.description, inputSchema: schema };
      });
      if (!o.tools.length) { out.tools = null; delete out.body.tools; }
    }
    var cache = o.cache;
    if (cache !== undefined) {
      if (out.tools && cache !== false) throw sampleErr("invalid_request", "a call with tools isn't cached: leave cache out");
      if (cache === false) out.body.cache = { off: true };
      else if (cache === true) out.body.cache = {};
      else if (isPlain(cache)) {
        if (cache.gcTime !== undefined && !(typeof cache.gcTime === "number" && cache.gcTime >= 0)) throw sampleErr("invalid_request", "cache.gcTime is a number of milliseconds");
        if (cache.refresh !== undefined && typeof cache.refresh !== "boolean") throw sampleErr("invalid_request", "cache.refresh is true or false");
        out.body.cache = { gcTime: cache.gcTime === undefined ? 0 : Math.min(Math.round(cache.gcTime), sampleLim.gcMost), refresh: !!cache.refresh };
      } else throw sampleErr("invalid_request", "cache is true, false or {gcTime, refresh}");
    }
    if (out.tools) out.body.cache = { off: true };
    if (o.images !== undefined) {
      if (!Array.isArray(o.images)) throw sampleErr("invalid_request", "images is a list of Blobs");
      if (o.images.length) {
        if (!sampleImages) throw sampleErr("images_unavailable", "the page doesn't declare {sample: {images: true}}");
        if (o.images.length > sampleLim.images) throw sampleErr("image_rejected", "a call carries at most " + sampleLim.images + " images");
        o.images.forEach(function (b, i) {
          if (!isBlob(b)) throw sampleErr("invalid_request", "images[" + i + "] is a Blob");
          if (sampleTypes.indexOf(bare(b.type)) < 0) throw sampleErr("image_rejected", "images[" + i + "] is " + (b.type || "of no type") + "; an image is JPEG, PNG, WebP or GIF");
          if (!b.size || b.size > sampleLim.imageInput) throw sampleErr("image_rejected", "images[" + i + "] is " + (b.size ? "over 20 MB" : "empty"));
        });
        out.images = o.images.slice();
      }
    }
    return out;
  }
  function blobB64(b) {
    return new Promise(function (res, rej) {
      var r = new FileReader();
      r.onload = function () { var s = String(r.result); res(s.slice(s.indexOf(",") + 1)); };
      r.onerror = function () { rej(r.error); };
      r.readAsDataURL(b);
    });
  }
  // an image as a round sends it: read, checked, and downsized to about
  // 1.2 megapixels, turned the way the image says
  function sampleImage(b, i) {
    var t = bare(b.type);
    var fail = function (m) { return sampleErr("image_rejected", "images[" + i + "] " + m); };
    if (typeof createImageBitmap !== "function") return Promise.reject(fail("can't be read in this browser"));
    return createImageBitmap(b, { imageOrientation: "from-image" }).catch(function () { throw fail("isn't an image this browser reads"); }).then(function (bm) {
      var w = bm.width, h = bm.height;
      if (!w || !h) throw fail("is empty");
      if (w > sampleLim.imageSide || h > sampleLim.imageSide || w * h > sampleLim.imagePixels) throw fail("is " + w + "×" + h + "; an image is at most 10,000 px a side and 64 megapixels");
      var k = Math.min(1, Math.sqrt(sampleLim.imageTarget / (w * h)));
      if (k === 1 && b.size <= sampleLim.imageOut && t !== "image/gif") {
        bm.close && bm.close();
        return blobB64(b).then(function (d) { return { type: t, data: d }; });
      }
      var cw = Math.max(1, Math.round(w * k)), ch = Math.max(1, Math.round(h * k));
      var cv = document.createElement("canvas");
      cv.width = cw;
      cv.height = ch;
      cv.getContext("2d").drawImage(bm, 0, 0, cw, ch);
      bm.close && bm.close();
      var out = t === "image/jpeg" ? "image/jpeg" : t === "image/webp" ? "image/webp" : "image/png";
      var enc = function (type, q) { return new Promise(function (res) { cv.toBlob(res, type, q); }); };
      return enc(out, 0.85).then(function (o) {
        if (o && o.type === out && o.size <= sampleLim.imageOut) return o;
        return enc("image/jpeg", 0.85);
      }).then(function (o) {
        if (!o || o.size > sampleLim.imageOut) throw fail("is too large once downsized");
        return blobB64(o).then(function (d) { return { type: bare(o.type), data: d }; });
      });
    });
  }
  // sample.json's reading of a reply: the whole of it, else one fenced
  // block's body, else from the first { or [ to the last } or ]
  function sampleParse(text) {
    var t = text.trim();
    try { return { ok: true, v: JSON.parse(t) }; } catch (e) {}
    var fences = t.match(/```[A-Za-z0-9_-]*\s*\n([\s\S]*?)\n?```/g);
    if (fences && fences.length === 1) {
      var m = /```[A-Za-z0-9_-]*\s*\n([\s\S]*?)\n?```/.exec(fences[0]);
      try { return { ok: true, v: JSON.parse(m[1].trim()) }; } catch (e) {}
    }
    var i = t.search(/[{[]/), j = Math.max(t.lastIndexOf("}"), t.lastIndexOf("]"));
    if (i >= 0 && j > i) {
      try { return { ok: true, v: JSON.parse(t.slice(i, j + 1)) }; } catch (e) {}
    }
    return { ok: false };
  }
  // one round: its text to on(delta) as it comes; resolves its end
  function sampleRound(body, ctl, on) {
    return fetch(C.api + "/sample", { method: "POST", body: JSON.stringify(body), headers: { "Content-Type": "text/plain" }, cache: "no-store", signal: ctl.signal })
      .then(function (r) {
        if (!r.ok || !r.body || !/ndjson/.test(r.headers.get("Content-Type") || "")) {
          return r.json().catch(function () { return { error: { code: r.status === 429 ? "rate_limited" : "upstream_error", message: "magpie answered " + r.status } }; }).then(function (j) {
            var e = (j && j.error) || { code: "upstream_error", message: "magpie answered " + r.status };
            throw sampleErr(e.code || "upstream_error", e.message, "", e);
          });
        }
        var reader = r.body.getReader(), dec = new TextDecoder(), buf = "", end = null;
        function line(l) {
          if (!l.trim()) return;
          var m;
          try { m = JSON.parse(l); } catch (e) { return; }
          if (m.text) on(m.text);
          else if (m.end) end = m.end;
          else if (m.error) throw sampleErr(m.error.code || "upstream_error", m.error.message, "", m.error);
        }
        function pump() {
          return reader.read().then(function (x) {
            if (x.done) {
              buf += dec.decode();
              line(buf);
              if (!end) throw sampleErr("upstream_error", "the answer ended before it finished");
              return end;
            }
            buf += dec.decode(x.value, { stream: true });
            var k;
            while ((k = buf.indexOf("\n")) >= 0) {
              var l = buf.slice(0, k);
              buf = buf.slice(k + 1);
              line(l);
            }
            return pump();
          });
        }
        return pump();
      }, function (e) {
        if (ctl.signal.aborted) throw e;
        throw sampleErr("upstream_error", "magpie can't be reached: " + e.message);
      });
  }
  // a tool's result as the next round reads it
  function toolResult(id, v, err) {
    var s;
    if (err) s = "Error: " + (err && err.message ? err.message : String(err));
    else if (typeof v === "string") s = v;
    else {
      try { s = JSON.stringify(v === undefined ? null : v); } catch (e) { s = "Error: the tool's result isn't JSON: " + e.message; err = true; }
    }
    if (s.length > sampleLim.toolResult) { s = "Error: the tool's result is over 32 KB; return less"; err = true; }
    return { type: "tool_result", tool_use_id: id, content: s, is_error: !!err };
  }
  function runSample(input, o, verbJSON) {
    var turns, opts;
    try {
      turns = sampleTurns(input);
      opts = sampleOpts(o, verbJSON);
    } catch (e) {
      return Promise.reject(e);
    }
    if (opts.signal && opts.signal.aborted) return Promise.reject(sampleErr("cancelled", "the call was cancelled before it started"));
    var ctl = new AbortController(), settled = false, text = "", roundText = "";
    var stop = function () { ctl.abort(); };
    if (opts.signal) opts.signal.addEventListener("abort", stop);
    var said = function (delta) {
      if (settled || !delta) return;
      if (!roundText && text) delta = "\n\n" + delta;
      roundText += delta;
      text += delta;
      if (opts.onText) {
        try { opts.onText({ text: text, delta: delta }); } catch (e) { setTimeout(function () { throw e; }); }
      }
    };
    var finish = function () {
      settled = true;
      if (opts.signal) opts.signal.removeEventListener("abort", stop);
      ctl.abort();
    };
    var cancelled = function () { return sampleErr("cancelled", "the call was cancelled", text); };
    return Promise.resolve().then(function () {
      if (!opts.images) return null;
      return Promise.all(opts.images.map(sampleImage));
    }).then(function (ims) {
      if (ctl.signal.aborted) throw cancelled();
      var msgs = turns.slice(), round = 0;
      function next() {
        var body = Object.assign({ t: C.token, messages: msgs, last: round === sampleLim.rounds - 1 }, opts.body);
        if (ims) body.images = ims;
        roundText = "";
        return sampleRound(body, ctl, said).then(function (end) {
          var uses = (end.content || []).filter(function (b) { return b.type === "tool_use"; });
          if (end.stop !== "tool_use" || !opts.tools || !uses.length || body.last) return end;
          msgs = msgs.concat([{ role: "assistant", content: end.content }]);
          var tc = new AbortController(), timer = setTimeout(function () { tc.abort(); }, sampleLim.toolTime);
          var off = function () { tc.abort(); };
          ctl.signal.addEventListener("abort", off);
          return Promise.all(uses.map(function (u) {
            var fn = opts.tools[u.name];
            if (!fn) return toolResult(u.id, null, new Error("there is no tool named " + u.name));
            return new Promise(function (res) { res(fn(u.input || {}, { signal: tc.signal })); }).then(function (v) { return toolResult(u.id, v); }, function (e) { return toolResult(u.id, null, e || new Error("the tool failed")); });
          })).then(function (results) {
            clearTimeout(timer);
            ctl.signal.removeEventListener("abort", off);
            tc.abort();
            if (ctl.signal.aborted) throw cancelled();
            msgs = msgs.concat([{ role: "user", content: results }]);
            round++;
            return next();
          });
        });
      }
      return next();
    }).then(function (end) {
      var out = text;
      finish();
      if (end.stop === "refusal") throw sampleErr("refused", "the model declined to answer");
      if (!out.trim()) throw sampleErr("empty_completion", "the model's answer was empty");
      var res = { text: out, truncated: !!end.truncated, modelTierApplied: end.tier || "default" };
      if (end.model) res.modelApplied = end.model;
      if (!verbJSON) return freeze(res);
      if (res.truncated) throw sampleErr("invalid_json", "the answer was cut off before its JSON ended", out);
      var p = sampleParse(out);
      if (!p.ok) throw sampleErr("invalid_json", "the answer isn't JSON", out);
      return p.v;
    }, function (e) {
      var was = ctl.signal.aborted && !settled;
      finish();
      if (was || e && e.name === "AbortError") throw cancelled();
      if (e && e.code && !e.text && text) throw sampleErr(e.code, e.message, text, e);
      throw e && e.code ? e : sampleErr("upstream_error", e && e.message ? e.message : String(e), text);
    });
  }
  var sample = function (input, opts) { return runSample(input, opts, false); };
  sample.json = function (input, opts) { return runSample(input, opts, true); };
  sample.limits = function () {
    var l = { maxPromptBytes: sampleLim.prompt, tools: freeze({ maxCount: sampleLim.tools }), modelTiers: freeze(sampleTiers.slice()) };
    if (sampleImages) l.images = freeze({ maxCount: sampleLim.images, maxInputBytes: sampleLim.imageInput, mediaTypes: freeze(sampleTypes.slice()) });
    return Promise.resolve(freeze(l));
  };
  freeze(sample);

  // -- the viewer shell ---------------------------------------------------
  // In a tab, magpie frames the page in its viewer shell (shell.js), which
  // draws the comments over it. The page tells the shell where things
  // are; the shell tells it the comment mode and the threads' anchors.
  // Messages go to magpie's origin alone, and come from the parent at it.
  var SH = "artifact-shell", framed = !C.shared && !!C.origin && window.parent !== window;
  var shellUp = false, shellWait = [], cmode = false, cpins = [], cstyle = null, creq = 0, cwait = {};
  var askers = [], asking = {};
  function toShell(m) {
    if (!framed) return;
    m.magpie = SH;
    try { window.parent.postMessage(m, C.origin); } catch (e) {}
  }
  function whenShell() {
    if (shellUp) return Promise.resolve(true);
    if (!framed) return Promise.resolve(false);
    return new Promise(function (res) {
      shellWait.push(res);
      setTimeout(function () { res(shellUp); }, 3000);
    });
  }
  // an element's anchor path: from the nearest id, else from body, by
  // each element's place among its kind
  var idRe = /^[A-Za-z][\w-]*$/;
  function pathOf(node) {
    var parts = [];
    for (var e = node; e && e.nodeType === 1; e = e.parentElement) {
      if (e === document.documentElement) break;
      if (e === document.body) { parts.unshift("body"); break; }
      if (e.id && idRe.test(e.id) && document.querySelectorAll("#" + e.id).length === 1) { parts.unshift("#" + e.id); break; }
      var i = 1, s = e;
      while ((s = s.previousElementSibling)) if (s.tagName === e.tagName) i++;
      parts.unshift(e.tagName.toLowerCase() + ":nth-of-type(" + i + ")");
    }
    return parts.join(">") || "body";
  }
  function find(path) {
    try { return path ? document.querySelector(path) : null; } catch (e) { return null; }
  }
  function labelOf(e) {
    var w = e.getAttribute("aria-label") || e.getAttribute("alt") || e.getAttribute("title") || "";
    if (!w) w = (e.innerText || e.textContent || "").replace(/\s+/g, " ").trim();
    if (!w) w = e.tagName.toLowerCase();
    return w.length > 60 ? w.slice(0, 59) + "…" : w;
  }
  function quoteIn(e) {
    var sel = window.getSelection && window.getSelection();
    if (!sel || sel.isCollapsed || !sel.rangeCount) return "";
    var r = sel.getRangeAt(0);
    if (!e.contains(r.commonAncestorContainer) && !r.commonAncestorContainer.contains(e)) return "";
    var q = String(sel).replace(/\s+/g, " ").trim();
    return q.length > 500 ? q.slice(0, 499) + "…" : q;
  }
  function targetOf(e) {
    if (!e || e.nodeType !== 1) e = e && e.parentElement;
    if (!e || e.closest("[data-uncommentable]")) return null;
    return e.closest("[data-comment-target]") || e;
  }
  function anchorAt(e, cx, cy) {
    var r = e.getBoundingClientRect();
    var x = r.width ? (cx - r.left) / r.width : 0.5, y = r.height ? (cy - r.top) / r.height : 0.5;
    return { path: pathOf(e), x: Math.min(1, Math.max(0, x)), y: Math.min(1, Math.max(0, y)) };
  }
  // where each pin goes, in the frame's own coordinates
  var placing = false;
  function place() {
    if (placing) return;
    placing = true;
    requestAnimationFrame(function () {
      placing = false;
      var map = {};
      cpins.forEach(function (p) {
        var e = find(p.path), r = e && e.getBoundingClientRect();
        map[p.id] = r && (r.width || r.height) ? { x: r.left + p.x * r.width, y: r.top + p.y * r.height } : null;
      });
      toShell({ type: "placed", map: map });
    });
  }
  function setCMode(on) {
    cmode = on;
    if (on && !cstyle) {
      cstyle = document.createElement("style");
      cstyle.textContent = "*{cursor:crosshair!important}[data-uncommentable],[data-uncommentable] *{cursor:default!important}";
      (document.head || document.documentElement).appendChild(cstyle);
    } else if (!on && cstyle) {
      cstyle.remove();
      cstyle = null;
      toShell({ type: "hover", rect: null });
    }
  }
  if (framed) {
    window.addEventListener("message", function (e) {
      var d = e.data;
      if (e.source !== window.parent || e.origin !== C.origin || !d || d.magpie !== SH) return;
      switch (d.type) {
        case "hi":
          if (d.grants && typeof d.grants.dictation === "string") dgrant = d.grants.dictation;
          if (d.grants) mGrants(d.grants.mcp);
          shellUp = true;
          shellWait.splice(0).forEach(function (f) { f(true); });
          break;
        case "mode":
          setCMode(!!d.on);
          break;
        case "pins":
          cpins = Array.isArray(d.list) ? d.list.slice(0, 500) : [];
          place();
          break;
        case "reveal":
          var el = find(d.path);
          if (el && el.scrollIntoView) el.scrollIntoView({ block: "center", behavior: "smooth" });
          break;
        case "composed":
          if (cwait[d.req]) cwait[d.req](d);
          break;
        case "sent":
          if (cwait[d.req]) cwait[d.req](d);
          break;
        case "ask":
          askListeners(d);
          break;
        case "saved":
          if (swait[d.req]) swait[d.req](d);
          break;
        case "tts-audio":
          if (twait[d.req]) twait[d.req](d);
          break;
        case "tts-stop":
          if (reading) viewerStop();
          break;
        case "dict-state":
          dictState(d);
          break;
        case "dict-partial":
          if (dstate === "listening" || dstate === "finishing") demit("partial", freeze({ text: String(d.text || "") }));
          break;
        case "dict-final":
          dictFinal(String(d.text || ""));
          break;
        case "grants":
          if (d.grants && typeof d.grants.dictation === "string") dgrant = d.grants.dictation;
          if (d.grants) mGrants(d.grants.mcp);
          if (d.req && pwait[d.req]) pwait[d.req]();
          break;
        case "perms-closed":
          if (pwait[d.req]) pwait[d.req]();
          break;
        case "mcp-done":
          if (mwait[d.req]) mwait[d.req](d);
          break;
        case "abort":
          if (asking[d.id]) asking[d.id].abort();
          break;
      }
    });
    // comment mode: a click on the page is a comment there, not the page's
    var swallow = function (e) {
      if (!cmode || !e.isTrusted) return;
      e.preventDefault();
      e.stopImmediatePropagation();
    };
    ["pointerdown", "mousedown", "pointerup", "mouseup", "dblclick", "auxclick", "submit"].forEach(function (k) { window.addEventListener(k, swallow, true); });
    window.addEventListener("click", function (e) {
      if (!cmode || !e.isTrusted) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      var tgt = targetOf(e.target);
      if (!tgt) return;
      toShell({ type: "compose", anchor: anchorAt(tgt, e.clientX, e.clientY), at: { x: e.clientX, y: e.clientY }, label: labelOf(tgt), quote: quoteIn(tgt) });
    }, true);
    var hovered = null;
    window.addEventListener("mousemove", function (e) {
      if (!cmode) return;
      var tgt = targetOf(e.target);
      if (tgt === hovered) return;
      hovered = tgt;
      var r = tgt && tgt !== document.body && tgt.getBoundingClientRect();
      toShell({ type: "hover", rect: r ? { x: r.left, y: r.top, w: r.width, h: r.height } : null });
    }, true);
    window.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && (cmode || Object.keys(swait).length || dstate === "starting" || Object.keys(pwait).length || Object.keys(mwait).some(function (k) { return mwait[k].asks; }))) toShell({ type: "escape" });
    }, true);
    window.addEventListener("scroll", function () { if (cpins.length) place(); if (cmode && hovered) { hovered = null; toShell({ type: "hover", rect: null }); } }, true);
    window.addEventListener("resize", function () { if (cpins.length) place(); });
    var hello = function () {
      toShell({ type: "hello", follows: follows });
      if (typeof MutationObserver === "function") {
        new MutationObserver(function () { if (cpins.length) place(); }).observe(document.documentElement, { subtree: true, childList: true, attributes: true, characterData: true });
      }
    };
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", hello);
    else hello();
  }

  // -- comments -----------------------------------------------------------
  // the page writes threads, as the viewer; the shell shows them, so
  // nothing here reads them back
  function cErr(code, message) {
    var e = new Error(message || code);
    e.name = "CommentsError";
    e.code = code;
    return e;
  }
  var cdecl = C.caps.comments || {}, composerOnly = !!cdecl.composer_only;
  function ccall(op, body) {
    return post("comments", Object.assign({ t: C.token, op: op, version: version }, body), cErr, "unavailable");
  }
  function checkText(s) {
    if (typeof s !== "string" || !s.trim()) return Promise.reject(cErr("invalid", "a comment's text is a non-empty string"));
    if (new TextEncoder().encode(s).length > 4096) return Promise.reject(cErr("invalid", "a comment is at most 4 KiB as UTF-8"));
    if (/[\u0000-\u0008\u000b-\u001f\u007f]/.test(s)) return Promise.reject(cErr("invalid", "a comment has no control characters but newlines and tabs"));
    return null;
  }
  function checkAnchor(a) {
    return a && typeof a === "object" && typeof a.path === "string" && a.path && typeof a.x === "number" && typeof a.y === "number";
  }
  // where a composer the page opens points: like a click, at what the
  // viewer sees. An element (or range) out of view comes into it, and the
  // point is the middle of its visible part (a range's: under it)
  function shown(e, range) {
    var rectOf = function () { return (range || e).getBoundingClientRect(); };
    var r = rectOf();
    if (!(r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth) && e.scrollIntoView) {
      e.scrollIntoView({ block: "nearest", inline: "nearest" });
      r = rectOf();
    }
    var mid = function (a, b, max) { return (Math.max(a, 0) + Math.min(b, max)) / 2; };
    return { x: mid(r.left, r.right, innerWidth), y: range ? Math.min(Math.max(r.bottom, 0), innerHeight) : mid(r.top, r.bottom, innerHeight) };
  }
  function shellAsk(m) {
    return whenShell().then(function (up) {
      if (!up) throw cErr("unavailable", "comments open in magpie's viewer: open the page in its own tab");
      var req = ++creq;
      return new Promise(function (res) {
        cwait[req] = function (d) { delete cwait[req]; res(d); };
        m.req = req;
        toShell(m);
      });
    });
  }
  function askListeners(d) {
    var ctl = new AbortController(), i = 0, ls = askers.slice();
    asking[d.id] = ctl;
    var req = freeze({ text: String(d.text || ""), anchor: d.anchor == null ? null : String(d.anchor), label: d.label || undefined });
    function next() {
      if (i >= ls.length || ctl.signal.aborted) return Promise.resolve({ handled: false });
      var l = ls[i++];
      return Promise.resolve().then(function () { return l(req, { signal: ctl.signal }); }).then(function (a) {
        return a && a.handled ? a : next();
      }, next);
    }
    next().then(function (a) {
      delete asking[d.id];
      var reply = a && typeof a.reply === "string" ? a.reply.slice(0, 500) : undefined;
      toShell({ type: "answer", id: d.id, handled: !!(a && a.handled), reply: reply });
    });
  }
  var comments = {
    openComposer: function (target) {
      var e = target && (target.element || target.range && target.range.commonAncestorContainer);
      if (e && e.nodeType !== 1) e = e.parentElement;
      if (!e || !e.isConnected) return Promise.reject(cErr("invalid", "openComposer takes {element} or {range} in this document"));
      var tgt = targetOf(e);
      if (!tgt) return Promise.resolve(freeze({ opened: false }));
      var at = shown(e, target.range);
      var quote = target.range ? String(target.range).replace(/\s+/g, " ").trim().slice(0, 500) : "";
      return shellAsk({ type: "compose", anchor: anchorAt(tgt, at.x, at.y), at: at, label: labelOf(tgt), quote: quote }).then(function (d) {
        return freeze({ opened: !!d.opened });
      });
    },
    anchorFor: function (e) {
      if (!e || e.nodeType !== 1 || !e.isConnected) return Promise.reject(cErr("invalid", "anchorFor takes an element in this document"));
      return Promise.resolve(freeze({ path: pathOf(e), x: 0.5, y: 0.5 }));
    },
    onSendToClaude: function (l) {
      if (typeof l !== "function") throw new TypeError("onSendToClaude takes a function");
      askers.push(l);
      return function () { var i = askers.indexOf(l); if (i >= 0) askers.splice(i, 1); };
    },
  };
  if (!composerOnly) {
    comments.create = function (o) {
      var bad = checkText(o && o.text);
      if (bad) return bad;
      if (!checkAnchor(o.anchor)) return Promise.reject(cErr("invalid", "create takes the anchor anchorFor gave"));
      var e = find(o.anchor.path);
      return ccall("create", { anchor: { path: o.anchor.path, x: o.anchor.x, y: o.anchor.y }, label: e ? labelOf(e) : "", text: o.text }).then(function (j) {
        return freeze({ threadId: j.threadId, commentId: j.commentId });
      });
    };
    comments.reply = function (id, text) {
      var bad = checkText(text);
      if (bad) return bad;
      if (typeof id !== "string" || !id) return Promise.reject(cErr("invalid", "reply takes a threadId"));
      return ccall("reply", { thread: id, text: text }).then(function (j) { return freeze({ commentId: j.commentId }); });
    };
    // the viewer sends it from the shell's composer, written for them:
    // the page alone sends nothing to the agent
    comments.sendToClaude = function (o) {
      var bad = checkText(o && o.text);
      if (bad) return bad;
      var m = { type: "send", text: o.text };
      if (o.threadId) m.threadId = String(o.threadId);
      else if (checkAnchor(o.anchor)) {
        var e = find(o.anchor.path);
        m.anchor = { path: o.anchor.path, x: o.anchor.x, y: o.anchor.y };
        m.label = e ? labelOf(e) : "";
        if (e) m.at = shown(e);
      } else return Promise.reject(cErr("invalid", "sendToClaude takes {anchor, text} or {threadId, text}"));
      return shellAsk(m).then(function (d) {
        if (d.error) throw cErr(d.error, d.error === "consent_required" ? "the viewer didn't send it" : d.error === "claude_unavailable" ? "no agent can take it; nothing was posted" : "it can't be sent from here");
        return freeze({ threadId: d.threadId, commentId: d.commentId });
      });
    };
    comments.canSendToClaude = function () {
      return ccall("can", {}).then(function (j) { return j.can || "off"; }, function () { return "off"; });
    };
    comments.resolve = function (id, resolved) {
      return ccall("resolve", { thread: String(id), resolved: !!resolved }).then(function () {});
    };
    comments["delete"] = function (id) {
      return ccall("delete", { thread: String(id) }).then(function () {});
    };
  }
  if (cdecl.customAnchors) {
    comments.customAnchors = function () { return Promise.reject(cErr("capability_disabled", "custom anchors aren't served here yet: the shell anchors comments to elements")); };
  }
  freeze(comments);

  // -- downloads ----------------------------------------------------------
  // the page offers a file; the shell asks the viewer, at magpie's origin,
  // and saves it only when they accept. The page never downloads itself.
  var swait = {}, sreq = 0;
  function sErr(code, message) {
    var e = new Error(message || code);
    e.name = "DownloadsError";
    e.code = code;
    return e;
  }
  var dTypes = { gif: "image/gif", png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", webp: "image/webp", mp4: "video/mp4", webm: "video/webm", mov: "video/quicktime", txt: "text/plain;charset=utf-8", json: "application/json", md: "text/markdown;charset=utf-8",
    docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", pptx: "application/vnd.openxmlformats-officedocument.presentationml.presentation", epub: "application/epub+zip", csv: "text/csv;charset=utf-8", ttf: "font/ttf", html: "text/html;charset=utf-8", svg: "image/svg+xml", pdf: "application/pdf",
    xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", zip: "application/zip", fig: "application/octet-stream" };
  // the name as the viewer is shown it: no invisible characters or path,
  // one space for a run of them, at most 240 bytes of UTF-8
  function cleanName(n) {
    n = n.replace(/[\u0000-\u001f\u007f-\u009f­​-‏‪-‮⁠-⁯﻿]/g, "").replace(/[\/\\:*?"<>|]/g, "_").replace(/\s+/g, " ").trim().replace(/^\.+/, "");
    var enc = new TextEncoder();
    if (enc.encode(n).length > 240) {
      var dot = n.lastIndexOf("."), ext = dot > 0 ? n.slice(dot) : "", base = dot > 0 ? n.slice(0, dot) : n;
      var chars = Array.from(base);
      while (chars.length && enc.encode(chars.join("") + ext).length > 240) chars.pop();
      n = chars.join("").trim() + ext;
    }
    return n;
  }
  var downloads = freeze({
    save: function (o) {
      if (!o || typeof o !== "object") return Promise.reject(sErr("bad_request", "save takes {filename, data}"));
      if (o.request !== undefined) return Promise.reject(sErr("request_unknown", "no export was asked of this page"));
      if (typeof o.filename !== "string" || o.filename.length > 512) return Promise.reject(sErr("bad_request", "filename is a string of at most 512 characters"));
      var name = cleanName(o.filename), dot = name.lastIndexOf("."), ext = dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
      if (!ext || !dTypes[ext]) return Promise.reject(sErr("rejected_extension", "the extension is one of " + Object.keys(dTypes).join(" ")));
      if (ext === "mov") return Promise.reject(sErr("not_signed", "a mov is saved only with content credentials, which a page's own file has none of: offer mp4 or webm"));
      var d = o.data, blob;
      try {
        if (typeof d === "string") blob = new Blob([d], { type: dTypes[ext] });
        else if (typeof Blob === "function" && d instanceof Blob) blob = d.type === dTypes[ext] ? d : new Blob([d], { type: dTypes[ext] });
        else if (d instanceof ArrayBuffer) blob = new Blob([d.slice(0)], { type: dTypes[ext] });
        else if (ArrayBuffer.isView(d)) blob = new Blob([new Uint8Array(d.buffer, d.byteOffset, d.byteLength).slice()], { type: dTypes[ext] });
      } catch (e) {
        return Promise.reject(sErr("bad_request", "the data can't be read: " + e.message));
      }
      if (!blob || !blob.size) return Promise.reject(sErr("bad_request", "data is a non-empty string, Blob, ArrayBuffer or view"));
      if (blob.size > 64 << 20) return Promise.reject(sErr("too_large", "a file is at most 64 MiB"));
      return whenShell().then(function (up) {
        if (!up) throw sErr("unavailable", "files are saved from magpie's viewer: open the page in its own tab");
        var req = ++sreq;
        return new Promise(function (res, rej) {
          swait[req] = function (a) {
            delete swait[req];
            if (a.status === "saved") res(freeze({ status: "saved" }));
            else rej(sErr(a.error || "declined", a.error === "rate_limited" ? "another file is waiting for the viewer" : "the viewer didn't save it"));
          };
          toShell({ type: "save", req: req, filename: name, size: blob.size, blob: blob });
        });
      });
    },
  });

  // -- tts ----------------------------------------------------------------
  // The page's text read aloud: the shell, holding its own token, asks
  // magpie for each line's audio, and it plays here, in the frame the
  // viewer clicked in. The shell draws the Stop; Escape here stops too.
  var VOICES = ["buttery", "airy", "glassy", "rounded"];
  var twait = {}, treq = 0, tstate = "idle", reading = null, trate = 1, coolUntil = 0;
  var tl = { state: [], line: [], position: [] };
  function tErr(code, message, more) {
    var e = new Error(message || code);
    e.name = "TTSError";
    e.code = code;
    if (more) Object.keys(more).forEach(function (k) { e[k] = more[k]; });
    return e;
  }
  function emit(kind, v) {
    tl[kind].slice().forEach(function (f) { try { f(v); } catch (e) { setTimeout(function () { throw e; }); } });
  }
  function on(kind) {
    return function (fn) {
      if (typeof fn !== "function") throw tErr("invalid_argument", "pass a function");
      tl[kind].push(fn);
      return function () { var i = tl[kind].indexOf(fn); if (i >= 0) tl[kind].splice(i, 1); };
    };
  }
  function setState(st, reason) {
    if (st === tstate && !reason) return;
    tstate = st;
    toShell({ type: "tts-state", state: st });
    emit("state", freeze(reason ? { state: st, reason: reason } : { state: st }));
  }
  function clampRate(r, why) {
    if (r === undefined) return undefined;
    if (typeof r !== "number" || !isFinite(r)) throw tErr("invalid_argument", why + " is a number, 1 being normal");
    return Math.min(1.5, Math.max(0.7, r));
  }
  // a call's lines, checked: a string, a line, or up to 100 lines
  function linesOf(what, o) {
    o = o === undefined ? {} : o;
    if (!o || typeof o !== "object") throw tErr("invalid_argument", "options are an object");
    var list = typeof what === "string" || what && !Array.isArray(what) && typeof what === "object" ? [what] : what;
    if (!Array.isArray(list) || !list.length || list.length > 100) throw tErr("invalid_argument", "speak takes text, a line {text}, or 1 to 100 lines");
    return list.map(function (l) {
      if (typeof l === "string") l = { text: l };
      if (!l || typeof l !== "object" || typeof l.text !== "string") throw tErr("invalid_argument", "a line is {text, voice?, rate?, language?, id?}");
      var text = l.text.trim(), n = Array.from(text).length;
      if (!n || n > 2000) throw tErr("invalid_argument", "a line's text is 1 to 2,000 characters");
      var voice = l.voice !== undefined ? l.voice : o.voice !== undefined ? o.voice : "buttery";
      if (VOICES.indexOf(voice) < 0) throw tErr("invalid_argument", "voice is one of " + VOICES.join(", "));
      var lang = l.language !== undefined ? l.language : o.language;
      if (lang !== undefined && (typeof lang !== "string" || !/^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$/.test(lang))) throw tErr("invalid_argument", "language is a tag such as \"en\" or \"pt-BR\"");
      if (l.id !== undefined && (typeof l.id !== "string" || !l.id || l.id.length > 64)) throw tErr("invalid_argument", "a line's id is a string of 1 to 64 characters");
      var rate = clampRate(l.rate !== undefined ? l.rate : o.rate, "rate");
      return { text: text, voice: voice, language: lang, id: l.id, rate: rate };
    });
  }
  // the gesture: once the viewer has clicked or typed in the page; after
  // they stopped a reading, not for a few seconds
  function gestureGate() {
    var ua = navigator.userActivation;
    if (ua && !ua.hasBeenActive) return tErr("no_gesture", "tts speaks once the viewer has clicked or typed in the page");
    var wait = coolUntil - Date.now();
    if (wait > 0) return tErr("no_gesture", "the viewer just stopped the reading", { retryAfterMs: wait });
    return null;
  }
  // the viewer is dictating: nothing speaks over them
  function busyGate() {
    return dstate !== "idle" ? tErr("busy", "the viewer is dictating") : null;
  }
  function fetchLine(r, i) {
    var line = r.lines[i];
    if (!line || line.audio) return line && line.audio;
    var req = ++treq;
    line.audio = new Promise(function (res) {
      twait[req] = function (d) { delete twait[req]; res(d); };
      toShell({ type: "tts-line", req: req, text: line.text, voice: line.voice });
    });
    return line.audio;
  }
  // a call's promise: it resolves once its own lines are spoken
  function callOf(r, lines) {
    return new Promise(function (res, rej) {
      var c = { left: lines.length, ms: 0, res: res, rej: rej };
      lines.forEach(function (l) { l.call = c; r.lines.push(l); });
      r.calls.push(c);
    });
  }
  function endReading(r, reason, err) {
    if (reading !== r) return;
    reading = null;
    clearTimeout(r.pauseTimer);
    if (r.el) {
      r.el.pause();
      r.el.removeAttribute("src");
      r.el.load();
    }
    if (r.url) URL.revokeObjectURL(r.url);
    document.removeEventListener("visibilitychange", r.onHide);
    r.calls.forEach(function (c) {
      if (c.left > 0) c.rej(err || tErr("stopped", "the reading ended: " + reason, { reason: reason }));
    });
    setState("idle", reason);
  }
  function viewerStop() {
    coolUntil = Date.now() + 6000;
    endReading(reading, "viewer_stopped");
  }
  var failReasons = { rate_limited: 1, not_granted: 1, unavailable: 1, upstream_error: 1 };
  function playFrom(r, i) {
    if (reading !== r) return;
    if (i >= r.lines.length) return endReading(r, "completed");
    var line = r.lines[i];
    Promise.resolve(fetchLine(r, i)).then(function (d) {
      if (reading !== r) return;
      if (!d || d.error || !(d.blob instanceof Blob)) {
        var code = d && failReasons[d.error] ? d.error : "upstream_error";
        endReading(r, code, tErr(code, (d && d.message) || "the line couldn't be read aloud", d && d.retryAfterMs ? { retryAfterMs: d.retryAfterMs } : null));
        return;
      }
      if (r.url) URL.revokeObjectURL(r.url);
      r.url = URL.createObjectURL(d.blob);
      var a = r.el;
      a.src = r.url;
      a.playbackRate = line.rate || trate;
      a.onended = function () {
        if (reading !== r) return;
        var c = line.call;
        c.ms += Math.round((a.duration || a.currentTime || 0) * 1000);
        if (--c.left === 0) c.res(freeze({ playedMs: c.ms }));
        playFrom(r, i + 1);
      };
      a.ontimeupdate = function () {
        if (reading === r) emit("position", freeze({ ms: Math.round(a.currentTime * 1000), line: i }));
      };
      a.onerror = function () { endReading(r, "upstream_error", tErr("upstream_error", "the audio couldn't be played")); };
      var p = a.play();
      (p && p.then ? p : Promise.resolve()).then(function () {
        if (reading !== r) return;
        if (r.paused) { a.pause(); return; }
        setState("speaking");
        emit("line", freeze(line.id !== undefined ? { line: i, id: line.id } : { line: i }));
        fetchLine(r, i + 1);
      }, function (e) {
        if (reading !== r) return;
        if (e && e.name === "NotAllowedError") endReading(r, "interrupted", tErr("no_gesture", "the browser wants a click in the page before it plays sound"));
        else endReading(r, "interrupted", tErr("upstream_error", "the audio couldn't be played"));
      });
    });
  }
  function startReading(lines) {
    var r = { lines: [], calls: [], el: new Audio(), paused: false };
    r.el.preload = "auto";
    r.onHide = function () { if (document.visibilityState === "hidden") endReading(r, "hidden"); };
    document.addEventListener("visibilitychange", r.onHide);
    reading = r;
    var p = callOf(r, lines);
    setState("starting");
    whenShell().then(function (up) {
      if (reading !== r) return;
      if (!up) return endReading(r, "unavailable", tErr("unavailable", "the page reads aloud in magpie's viewer: open it in its own tab"));
      playFrom(r, 0);
    });
    return p;
  }
  var tts = freeze({
    speak: function (what, o) {
      var lines;
      try { lines = linesOf(what, o); } catch (e) { return Promise.reject(e); }
      var g = busyGate() || gestureGate();
      if (g) return Promise.reject(g);
      if (reading) endReading(reading, "replaced");
      return startReading(lines);
    },
    add: function (what, o) {
      var lines;
      try { lines = linesOf(what, o); } catch (e) { return Promise.reject(e); }
      if (!reading) {
        var g = busyGate() || gestureGate();
        if (g) return Promise.reject(g);
        return startReading(lines);
      }
      return callOf(reading, lines);
    },
    stop: function () {
      if (reading) endReading(reading, "stopped");
      return Promise.resolve();
    },
    pause: function () {
      var r = reading;
      if (!r || r.paused) return Promise.resolve();
      r.paused = true;
      r.el.pause();
      r.pauseTimer = setTimeout(function () { endReading(r, "time_limit"); }, 5 * 60 * 1000);
      setState("paused");
      return Promise.resolve();
    },
    resume: function () {
      var r = reading;
      if (!r || !r.paused) return Promise.resolve();
      r.paused = false;
      clearTimeout(r.pauseTimer);
      if (r.el.src && !r.el.ended) {
        var p = r.el.play();
        if (p && p.catch) p.catch(function () { endReading(r, "interrupted", tErr("no_gesture", "the browser wants a click in the page before it plays sound")); });
      }
      setState(r.el.src ? "speaking" : "starting");
      return Promise.resolve();
    },
    setRate: function (rate) {
      try { trate = clampRate(rate, "rate"); } catch (e) { return Promise.reject(e); }
      if (reading && reading.el) reading.el.playbackRate = trate;
      return Promise.resolve(trate);
    },
    active: function () { return Promise.resolve(tstate !== "idle"); },
    voices: function () { return Promise.resolve(freeze(VOICES.map(function (v) { return freeze({ id: v, default: v === "buttery" }); }))); },
    onState: on("state"),
    onLine: on("line"),
    onPosition: on("position"),
  });
  // an Escape nothing in the page claimed stops the reading
  window.addEventListener("keydown", function (e) {
    if (e.key !== "Escape" || !reading || e.defaultPrevented) return;
    var t = e.target;
    if (t && t.nodeType === 1 && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
    viewerStop();
  });

  // -- dictation ----------------------------------------------------------
  // The viewer's speech as text: the shell, at magpie's origin, asks the
  // viewer, holds the microphone, shows its own Listening and Stop, and
  // hands this page the words, never the sound. The state is the shell's;
  // this keeps the page's promises and listeners.
  var dstate = "idle", dgrant = "prompt", dstart = null, dnext = null, dstops = [], dlast = "";
  var dl = { state: [], partial: [], final: [] }, pwait = {}, preq = 0;
  var DREASONS = { stopped: 1, viewer_stopped: 1, hidden: 1, time_limit: 1, not_granted: 1, dismissed: 1, unavailable: 1, rate_limited: 1, upstream_error: 1 };
  function dErr(code, message, more) {
    var e = new Error(message || code);
    e.name = "DictationError";
    e.code = code;
    if (more) Object.keys(more).forEach(function (k) { e[k] = more[k]; });
    return e;
  }
  function demit(kind, v) {
    dl[kind].slice().forEach(function (f) { try { f(v); } catch (e) { setTimeout(function () { throw e; }); } });
  }
  function dOn(kind) {
    return function (fn) {
      if (typeof fn !== "function") throw dErr("invalid_argument", "pass a function");
      dl[kind].push(fn);
      var off = function () { var i = dl[kind].indexOf(fn); if (i >= 0) dl[kind].splice(i, 1); };
      // a listener added mid-session hears where it is, once
      if (kind === "state" && dstate !== "idle") {
        var now = dstate;
        setTimeout(function () { if (dl.state.indexOf(fn) >= 0 && dstate === now) fn(freeze({ state: now })); }, 0);
      }
      return off;
    };
  }
  // a settled piece, with the space that joins it to the last one; none
  // between CJK characters or before closing punctuation
  var CJK = /[⺀-鿿가-힯豈-﫿＀-￯　-〿]/;
  function dictFinal(text) {
    if (dstate !== "listening" && dstate !== "finishing") return;
    text = text.trim();
    if (!text) return;
    var a = dlast.slice(-1), b = text.charAt(0);
    if (dlast && !/\s/.test(a) && !CJK.test(a) && !CJK.test(b) && !/^[,.;:!?)\]}%'’"”]/.test(b)) text = " " + text;
    dlast = text;
    demit("partial", freeze({ text: "" }));
    demit("final", freeze({ text: text }));
  }
  function dictState(d) {
    var st = d.state;
    if (st !== "idle" && st !== "starting" && st !== "listening" && st !== "finishing") return;
    if (st !== "idle") {
      if (st === dstate) return;
      dstate = st;
      demit("state", freeze({ state: st }));
      if (st === "listening" && dstart) { var w = dstart; dstart = null; w.res(); }
      return;
    }
    if (dstate === "idle") return;
    var reason = DREASONS[d.reason] ? d.reason : "upstream_error";
    dstate = "idle";
    if (dstart) {
      var s = dstart;
      dstart = null;
      var code = reason === "stopped" || reason === "viewer_stopped" || reason === "hidden" || reason === "time_limit" ? "stopped" : reason;
      s.rej(dErr(code, d.message || "dictation ended: " + reason, { reason: reason }));
    }
    demit("state", freeze({ state: "idle", reason: reason }));
    dstops.splice(0).forEach(function (f) { f(); });
    if (dnext) { var n = dnext; dnext = null; begin(n); }
  }
  function begin(w) {
    if (reading) endReading(reading, "interrupted");
    dstart = w;
    dictState({ state: "starting" });
    whenShell().then(function (up) {
      if (dstart !== w) return;
      if (!up) return dictState({ state: "idle", reason: "unavailable", message: "the page takes dictation in magpie's viewer: open it in its own tab" });
      toShell({ type: "dict-start", language: w.lang });
    });
  }
  var dictation = freeze({
    start: function (o) {
      if (o !== undefined && (!o || typeof o !== "object")) return Promise.reject(dErr("invalid_argument", "options are an object"));
      var lang = o && o.language;
      if (lang !== undefined && (typeof lang !== "string" || !/^[a-z]{2}(-[A-Za-z]{2})?$/.test(lang))) return Promise.reject(dErr("invalid_argument", "language is a two-letter language, optionally with a region, such as \"es\" or \"es-MX\""));
      if (dstate === "listening") return Promise.resolve();
      if (dstate === "starting" && dstart) return dstart.p;
      if (dnext) return dnext.p;
      var ua = navigator.userActivation;
      if (ua && !ua.isActive) return Promise.reject(dErr("no_gesture", "call start() from the viewer's click or key press, before anything is awaited"));
      var w = { lang: lang };
      w.p = new Promise(function (res, rej) { w.res = res; w.rej = rej; });
      if (dstate === "finishing") dnext = w;
      else begin(w);
      return w.p;
    },
    stop: function () {
      if (dnext) { var n = dnext; dnext = null; n.rej(dErr("stopped", "stopped before it was listening", { reason: "stopped" })); }
      if (dstate === "idle") return Promise.resolve();
      var p = new Promise(function (res) { dstops.push(res); });
      if (dstate === "starting" && !shellUp) dictState({ state: "idle", reason: "stopped" });
      else toShell({ type: "dict-stop" });
      return p;
    },
    active: function () { return dstate !== "idle"; },
    onPartial: dOn("partial"),
    onFinal: dOn("final"),
    onState: dOn("state"),
  });
  // an Escape nothing in the page claimed stops listening, as the
  // shell's Stop does
  window.addEventListener("keydown", function (e) {
    if (e.key !== "Escape" || (dstate !== "listening" && dstate !== "finishing") || e.defaultPrevented) return;
    var t = e.target;
    if (t && t.nodeType === 1 && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
    toShell({ type: "dict-stop", viewer: true });
  });

  // -- MCP ----------------------------------------------------------------
  // The tools of the MCP servers in magpie's Library that the page's
  // manifest names. The shell, at magpie's origin, asks the viewer about
  // each server once and makes the calls with its own token; this keeps
  // the page's cache, its watches and their polling.
  var MAN = (C.caps.mcp && Array.isArray(C.caps.mcp.servers) ? C.caps.mcp.servers : []).filter(function (s) { return s && typeof s.server === "string"; });
  var mgrant = {}, mwait = {}, mreq = 0, mnotes = {}, mcache = {}, mflights = {}, mwatches = {}, mwatchN = 0;
  var MDENIALS = { not_in_manifest: 1, needs_reauth: 1, server_not_connected: 1, blocked_by_policy: 1, approval_required: 1 };
  var MREPLY = 130000, MWEEK = 604800000, MGC = 300000, MFLOOR = 30000, MWATCHES = 64, MINPUT = 1 << 20;
  function mErr(code, message, more) {
    var e = new Error(message || code);
    e.name = "McpError";
    e.code = code;
    if (more) ["server", "retryable", "retryAfterMs", "result"].forEach(function (k) { if (more[k] !== undefined) e[k] = more[k]; });
    if (e.retryAfterMs > 60000) e.retryAfterMs = 60000;
    return e;
  }
  function mGrants(g) {
    if (!g || typeof g !== "object" || Array.isArray(g)) return;
    var was = mgrant;
    mgrant = {};
    Object.keys(g).forEach(function (k) { if (g[k] === "granted" || g[k] === "denied") mgrant[k] = g[k]; });
    // a server the viewer stopped allowing answers nothing more from the cache
    Object.keys(was).concat(Object.keys(mgrant)).forEach(function (s) { if (mgrant[s] === "denied" && was[s] !== "denied") mDrop(s); });
  }
  function inManifest(server) {
    for (var i = 0; i < MAN.length; i++) if (MAN[i].server === server) return MAN[i];
    return null;
  }
  // one request to the shell; rejects with the shell's error, cancelled on
  // the signal, upstream_error when the shell stops answering
  function viaMcp(m, signal) {
    return whenShell().then(function (up) {
      if (!up) throw mErr("capability_disabled", "the page calls MCP tools in magpie's viewer: open it in its own tab");
      if (signal && signal.aborted) throw mErr("cancelled", "the call was cancelled", { server: m.server });
      var req = ++mreq;
      return new Promise(function (res, rej) {
        var timer = setTimeout(function () { end(); rej(mErr("upstream_error", "magpie's viewer didn't answer in " + MREPLY / 1000 + "s", { server: m.server })); }, MREPLY);
        var onAbort = function () { end(); toShell({ type: "mcp-cancel", req: req }); rej(mErr("cancelled", "the call was cancelled", { server: m.server })); };
        function end() {
          delete mwait[req];
          clearTimeout(timer);
          if (signal) signal.removeEventListener("abort", onAbort);
        }
        mwait[req] = function (d) {
          end();
          if (d.error) rej(mErr(String(d.error.code || "upstream_error"), String(d.error.message || ""), d.error));
          else res(d.result || {});
        };
        mwait[req].asks = m.op === "call" && m.ask && mgrant[m.server] !== "granted";
        if (signal) signal.addEventListener("abort", onAbort);
        m.type = "mcp";
        m.req = req;
        toShell(m);
      });
    });
  }
  // the input as plain JSON, or the error the call rejects with
  function plainErr(v, depth) {
    if (depth > 100) return "bad_request";
    if (v === null || typeof v === "string" || typeof v === "boolean") return "";
    if (typeof v === "number") return isFinite(v) ? "" : "bad_request";
    if (typeof v !== "object") return "bad_request";
    var proto = Object.getPrototypeOf(v);
    if (Array.isArray(v)) {
      if (proto !== Array.prototype) return "bad_request";
      for (var i = 0; i < v.length; i++) { var a = plainErr(v[i], depth + 1); if (a) return a; }
      return "";
    }
    if (proto !== Object.prototype && proto !== null) return "bad_request";
    var ks = Object.keys(v);
    if (ks.length === 1 && ks[0] === "$file") return "capability_disabled";
    for (var j = 0; j < ks.length; j++) {
      if (v[ks[j]] === undefined) continue;
      var b = plainErr(v[ks[j]], depth + 1);
      if (b) return b;
    }
    return "";
  }
  function inputOf(server, input) {
    if (input === undefined || input === null) return { input: undefined, id: "{}" };
    var bad = plainErr(input, 0);
    if (bad === "capability_disabled") return { err: mErr(bad, "file arguments aren't served here: send the bytes inline, as base64", { server: server }) };
    if (bad || typeof input !== "object" || Array.isArray(input)) return { err: mErr("bad_request", "input is a plain JSON object", { server: server }) };
    var s = JSON.stringify(input);
    if (new TextEncoder().encode(s).length > MINPUT) return { err: mErr("bad_request", "input is at most 1 MiB as JSON", { server: server }) };
    return { input: JSON.parse(s), id: canon(input) };
  }
  // order-insensitive JSON: one call however its members are ordered
  function canon(v) {
    if (v === null || typeof v !== "object") return JSON.stringify(v);
    if (Array.isArray(v)) return "[" + v.map(function (x) { return x === undefined ? "null" : canon(x); }).join(",") + "]";
    return "{" + Object.keys(v).filter(function (k) { return v[k] !== undefined; }).sort().map(function (k) { return JSON.stringify(k) + ":" + canon(v[k]); }).join(",") + "}";
  }
  function keyOf(server, tool, id) { return JSON.stringify([server, tool]) + id; }
  function noteOf(server, tool) { return mnotes[JSON.stringify([server, tool])]; }
  function note(server, tools) {
    (tools || []).forEach(function (t) {
      if (t && typeof t.name === "string" && t.annotations && typeof t.annotations === "object") mnotes[JSON.stringify([server, t.name])] = t.annotations;
    });
  }
  // how a call is cached: null for not at all; known: whether the tool's
  // hints are known yet (a call learns them)
  function policy(server, tool, c, watch) {
    if (c === false) return null;
    var n = noteOf(server, tool), ro = n ? n.readOnlyHint : undefined;
    if (ro === false) return null;
    if (c && typeof c === "object") {
      var gc = c.gcTime === undefined ? MGC : Math.min(Number(c.gcTime) || 0, MWEEK);
      if (!(gc > 0)) return null;
      var st = Math.min(Math.max(Number(c.staleTime) || 0, 0), gc);
      return { staleTime: st, gcTime: gc, refresh: !watch && !!c.refresh };
    }
    if (ro === true || watch) return { staleTime: 0, gcTime: MGC };
    return null;
  }
  // a result as the page gets it: its own copy, with payload, and the
  // cache's marker when it came from the cache
  function deliver(raw, cache) {
    var r = JSON.parse(raw);
    if (!r || typeof r !== "object" || Array.isArray(r)) return r;
    if (r.structuredContent !== undefined) r.payload = r.structuredContent;
    else {
      var first = Array.isArray(r.content) ? r.content.filter(function (b) { return b && b.type === "text" && typeof b.text === "string"; })[0] : null;
      if (first) { try { r.payload = JSON.parse(first.text); } catch (e) { r.payload = first.text; } }
    }
    if (cache) r.cache = cache;
    return r;
  }
  function mStore(key, server, tool, raw, p) {
    var was = mcache[key];
    if (was) clearTimeout(was.timer);
    var e = { server: server, tool: tool, raw: raw, at: Date.now(), staleTime: p.staleTime, gcTime: p.gcTime };
    e.timer = setTimeout(function () { if (mcache[key] === e) delete mcache[key]; }, p.gcTime);
    mcache[key] = e;
    feed(key, { type: "data", result: deliver(raw) });
  }
  function mDrop(server, tool, key) {
    Object.keys(mcache).forEach(function (k) {
      var e = mcache[k];
      if (server !== undefined && e.server !== server || tool !== undefined && e.tool !== tool || key !== undefined && k !== key) return;
      clearTimeout(mcache[k].timer);
      delete mcache[k];
    });
  }
  function fresh(key) {
    var e = mcache[key];
    return e && Date.now() - e.at < e.gcTime ? e : null;
  }
  // run a call, kept by the page's cache option (copt) as judged once
  // the tool's hints are known; share: one flight per identity at a time
  function run(server, tool, inp, key, copt, share, ask, signal, watch) {
    var shared = share && !signal;
    if (shared && mflights[key]) return mflights[key];
    var f = viaMcp({ op: "call", server: server, tool: tool, input: inp.input, ask: ask, watch: !!watch }, signal).then(function (j) {
      if (j.annotations) note(server, [{ name: tool, annotations: j.annotations }]);
      var r = j.result;
      if (r && typeof r === "object" && !Array.isArray(r)) delete r.cache;
      var raw = JSON.stringify(r === undefined ? null : r);
      // the hints are known now: a write is never kept
      var q = policy(server, tool, copt, watch);
      if (q) mStore(key, server, tool, raw, q);
      return raw;
    }, function (e) {
      if (MDENIALS[e.code]) mDrop(server);
      throw e;
    });
    if (shared) {
      mflights[key] = f;
      var clear = function () { if (mflights[key] === f) delete mflights[key]; };
      f.then(clear, clear);
    }
    return f;
  }
  function badArgs(server, tool) {
    if (typeof server !== "string" || !server || typeof tool !== "string" || !tool) return mErr("bad_request", "server and tool are strings", { server: typeof server === "string" ? server : undefined });
    return null;
  }
  function callTool(server, tool, input, o) {
    var bad = badArgs(server, tool);
    if (bad) return Promise.reject(bad);
    if (o !== undefined && (o === null || typeof o !== "object")) return Promise.reject(mErr("bad_request", "options are an object", { server: server }));
    o = o || {};
    if (o.cache !== undefined && o.cache !== false && (o.cache === null || typeof o.cache !== "object")) return Promise.reject(mErr("bad_request", "cache is false or an object", { server: server }));
    var signal = o.signal;
    if (signal !== undefined && !(signal && typeof signal.aborted === "boolean" && typeof signal.addEventListener === "function")) return Promise.reject(mErr("bad_request", "signal is an AbortSignal", { server: server }));
    if (signal && signal.aborted) return Promise.reject(mErr("cancelled", "the call was cancelled", { server: server }));
    var inp = inputOf(server, input);
    if (inp.err) return Promise.reject(inp.err);
    var key = keyOf(server, tool, inp.id);
    // p: how it is cached as far as the hints known yet tell
    var p = policy(server, tool, o.cache);
    if (p && !p.refresh) {
      var e = fresh(key);
      if (e && Date.now() - e.at < p.staleTime) return later(function () { return deliver(e.raw, { storedAt: e.at, revalidating: false }); });
    }
    var share = !!p && !p.refresh;
    // a call with a signal joins a flight already up, and leaves it alone
    var f = share && signal && mflights[key] ? race(mflights[key], signal, server) : run(server, tool, inp, key, o.cache, share, true, signal);
    return f.then(function (raw) { return deliver(raw); });
  }
  function race(f, signal, server) {
    return new Promise(function (res, rej) {
      var onAbort = function () { rej(mErr("cancelled", "the call was cancelled", { server: server })); };
      signal.addEventListener("abort", onAbort);
      f.then(function (v) { signal.removeEventListener("abort", onAbort); res(v); }, function (e) { signal.removeEventListener("abort", onAbort); rej(e); });
    });
  }

  // -- watches --
  function feed(key, ev) {
    var w = mwatches[key];
    if (!w) return;
    w.list.slice().forEach(function (x) { if (!x.gone) x.send(ev); });
  }
  function hear(handler) {
    return function (ev) {
      try { handler(ev); } catch (e) { setTimeout(function () { throw e; }); }
    };
  }
  function refetch(key) {
    var w = mwatches[key];
    if (!w || !w.list.length) return;
    var x = w.list[0];
    w.last = Date.now();
    run(x.server, x.tool, x.inp, key, x.copt, true, false, null, true).then(null, function (e) {
      feed(key, { type: "error", error: e });
    });
  }
  function poll(key) {
    var w = mwatches[key];
    if (!w) return;
    clearTimeout(w.timer);
    w.timer = null;
    var every = 0;
    w.list.forEach(function (x) { if (x.every && (!every || x.every < every)) every = x.every; });
    w.every = every;
    if (!every || document.hidden) return;
    w.timer = setTimeout(function () { refetch(key); poll(key); }, Math.max(every - (Date.now() - (w.last || 0)), 0) || every);
  }
  document.addEventListener("visibilitychange", function () {
    Object.keys(mwatches).forEach(function (key) {
      var w = mwatches[key];
      if (document.hidden) { clearTimeout(w.timer); w.timer = null; return; }
      // back: catch up on what the poll missed
      if (w.every && Date.now() - (w.last || 0) >= w.every) refetch(key);
      poll(key);
    });
  });
  function watchTool(server, tool, input, handler, o) {
    if (typeof handler !== "function") throw new TypeError("watchTool's handler is a function");
    var send = hear(handler), gone = false;
    var fail = function (e) { setTimeout(function () { if (!gone) send({ type: "error", error: e }); }, 0); return function () { gone = true; }; };
    var bad = badArgs(server, tool);
    if (bad) return fail(bad);
    if (o !== undefined && (o === null || typeof o !== "object")) return fail(mErr("bad_request", "options are an object", { server: server }));
    o = o || {};
    var n = noteOf(server, tool);
    if (n && n.readOnlyHint === false) return fail(mErr("bad_request", tool + " declares it changes things: watch reads only, and call an action once with callTool", { server: server }));
    var inp = inputOf(server, input);
    if (inp.err) return fail(inp.err);
    if (mwatchN >= MWATCHES) return fail(mErr("bad_request", "a page keeps at most " + MWATCHES + " watches: unsubscribe the ones it no longer shows", { server: server }));
    var key = keyOf(server, tool, inp.id);
    var w = mwatches[key] || (mwatches[key] = { list: [], timer: null, last: 0, every: 0 });
    for (var i = 0; i < w.list.length; i++) if (w.list[i].handler === handler) return fail(mErr("bad_request", "this handler already watches this call", { server: server }));
    var copt = o.cache && typeof o.cache === "object" ? { staleTime: o.cache.staleTime, gcTime: o.cache.gcTime } : undefined;
    var p = policy(server, tool, copt, true) || { staleTime: 0, gcTime: MGC };
    var every = Number(o.refetchInterval) > 0 ? Math.max(Number(o.refetchInterval), MFLOOR) : 0;
    var x = { server: server, tool: tool, inp: inp, copt: copt, handler: handler, every: every, send: function (ev) { if (!x.gone) send(ev); } };
    w.list.push(x);
    mwatchN++;
    setTimeout(function () {
      if (x.gone) return;
      var e = fresh(key), stale = !e || Date.now() - e.at >= p.staleTime;
      if (e) x.send({ type: "data", result: deliver(e.raw, { storedAt: e.at, revalidating: stale }) });
      if (stale) refetch(key);
      poll(key);
    }, 0);
    return function () {
      if (x.gone) return;
      x.gone = true;
      mwatchN--;
      w.list.splice(w.list.indexOf(x), 1);
      if (!w.list.length) { clearTimeout(w.timer); if (mwatches[key] === w) delete mwatches[key]; } else poll(key);
    };
  }
  function invalidate(server, tool, input) {
    if (server !== undefined && typeof server !== "string" || tool !== undefined && typeof tool !== "string") return Promise.reject(mErr("bad_request", "server and tool are strings"));
    if (tool !== undefined && server === undefined) return Promise.reject(mErr("bad_request", "a tool needs its server"));
    var key;
    if (input !== undefined) {
      if (server === undefined || tool === undefined) return Promise.reject(mErr("bad_request", "an input needs its server and tool"));
      var inp = inputOf(server, input);
      if (inp.err) return Promise.reject(inp.err);
      key = keyOf(server, tool, inp.id);
    }
    mDrop(server, tool, key);
    Object.keys(mwatches).forEach(function (k) {
      var x = mwatches[k].list[0];
      if (x && (server === undefined || x.server === server) && (tool === undefined || x.tool === tool) && (key === undefined || k === key)) refetch(k);
    });
    return later(function () {});
  }
  function listTools(server) {
    if (server !== undefined && typeof server !== "string") return Promise.reject(mErr("bad_request", "server is a string"));
    return viaMcp({ op: "list", server: server }).then(function (j) {
      var out = (Array.isArray(j.servers) ? j.servers : []).map(function (s) {
        var tools = (Array.isArray(s.tools) ? s.tools : []).map(function (t) {
          var x = { name: String(t.name), description: String(t.description || "") };
          if (t.annotations && typeof t.annotations === "object") x.annotations = t.annotations;
          return x;
        });
        note(s.server, tools);
        var st = s.authStatus === "connected" || s.authStatus === "needs_reauth" ? s.authStatus : "unknown";
        return { server: String(s.server), kind: "connector", authStatus: st, tools: tools };
      });
      return deepFreeze({ servers: out });
    });
  }
  function describeTool(server, tool) {
    var bad = badArgs(server, tool);
    if (bad) return Promise.reject(bad);
    return viaMcp({ op: "describe", server: server, tool: tool }).then(function (j) {
      note(server, [j]);
      return deepFreeze(j);
    });
  }
  var NOT_TOOLS = { then: 1, toJSON: 1 };
  function serverOf(name) {
    if (typeof name !== "string" || !name) return Promise.reject(mErr("bad_request", "name is a string"));
    return listTools(name).then(function (l) {
      var s = l.servers.filter(function (x) { return x.server === name; })[0];
      if (s && s.authStatus === "needs_reauth") throw mErr("needs_reauth", name + " needs signing in again in magpie's Library", { server: name });
      if (!s || !s.tools.length) throw mErr("server_not_connected", "no MCP server named " + name + " can be reached from this page", { server: name });
      var h = Object.create(null);
      s.tools.forEach(function (t) {
        if (NOT_TOOLS[t.name] || t.name in Object.prototype || !/^[A-Za-z0-9_.-]+$/.test(t.name)) return;
        h[t.name] = function (input, o) {
          return callTool(name, t.name, input, o).then(function (r) { return r && r.payload !== undefined ? r.payload : r; });
        };
      });
      return freeze(h);
    });
  }
  var mcp = freeze({ callTool: callTool, watchTool: watchTool, invalidate: invalidate, listTools: listTools, server: serverOf, describeTool: describeTool });

  // -- permissions --------------------------------------------------------
  // dictation and each MCP server ("mcp:<server>"; "mcp" for all of
  // them) are the viewer's to allow, once for each artifact, in the
  // shell; every other capability is granted with the page
  var asksViewer = served.dictation || served.mcp;
  function mState(server) {
    if (!served.mcp || !shellUp) return "unavailable";
    if (server === null) {
      if (!MAN.length) return "unavailable";
      var all = MAN.map(function (m) { return mgrant[m.server] || "prompt"; });
      return all.every(function (g) { return g === "granted"; }) ? "granted" : all.indexOf("prompt") >= 0 ? "prompt" : "denied";
    }
    return inManifest(server) ? mgrant[server] || "prompt" : "unavailable";
  }
  function states(names) {
    var out = {};
    var all = names || Object.keys(C.caps).concat(served.mcp ? MAN.map(function (m) { return "mcp:" + m.server; }) : []);
    all.forEach(function (n) {
      if (n === "mcp" || n.indexOf("mcp:") === 0) out[n] = mState(n === "mcp" ? null : n.slice(4));
      else if (served[n] && (n === "user" || C.caps[n] || (n === "artifact" || n === "self") && publishing)) out[n] = n === "dictation" ? (shellUp ? dgrant : "unavailable") : "granted";
      else if (names) out[n] = "unavailable";
      if (!names && out[n] === "unavailable") delete out[n];
    });
    return freeze(out);
  }
  function viaShell(type, more) {
    var req = ++preq;
    return new Promise(function (res) {
      pwait[req] = function () { delete pwait[req]; res(); };
      toShell(Object.assign({ type: type, req: req }, more || {}));
    });
  }
  var permissions = freeze({
    state: function (name) {
      var one = arguments.length > 0;
      return (asksViewer ? whenShell() : Promise.resolve()).then(function () {
        return one ? states([String(name)])[String(name)] : states(null);
      });
    },
    // one question at a time: dictation, then the MCP servers asked for
    request: function (names) {
      var list = names ? [].concat(names).map(String) : null;
      return (asksViewer ? whenShell() : Promise.resolve()).then(function () {
        var ask = served.dictation && shellUp && dgrant === "prompt" && (!list || list.indexOf("dictation") >= 0);
        return ask ? viaShell("dict-ask") : null;
      }).then(function () {
        if (!served.mcp || !shellUp) return;
        var want = MAN.filter(function (m) { return !list || list.indexOf("mcp") >= 0 || list.indexOf("mcp:" + m.server) >= 0; }).filter(function (m) { return !mgrant[m.server]; });
        if (want.length) return viaShell("mcp-ask", { servers: want.map(function (m) { return m.server; }) });
      }).then(function () { return states(list); });
    },
    manage: function () {
      if (!asksViewer) return Promise.reject(dbError("unavailable", "this page has nothing for the viewer to allow: what it may do is set when it is published"));
      return whenShell().then(function (up) {
        if (!up) throw dbError("unavailable", "the Permissions panel is in magpie's viewer: open the page in its own tab");
        return viaShell("perms", { serves: { dictation: !!served.dictation, mcp: !!served.mcp } });
      }).then(function () {});
    },
  });

  // -- claude.use ---------------------------------------------------------
  var spaces = { db: served.db ? db : null, user: served.user ? user : null, permissions: permissions, artifact: served.artifact ? artifact : null, self: served.self ? artifact : null, assets: served.assets ? assets : null, sample: served.sample ? sample : null, comments: served.comments ? comments : null, downloads: served.downloads ? downloads : null, tts: served.tts ? tts : null, dictation: served.dictation ? dictation : null, mcp: served.mcp ? mcp : null };
  var memo = {};
  function use(name) {
    name = String(name);
    if (!Object.prototype.hasOwnProperty.call(spaces, name) || !spaces[name]) return later(function () { return null; });
    return memo[name] || (memo[name] = later(function () { return spaces[name]; }));
  }
  Object.defineProperty(window, "claude", { value: freeze({ use: use }), writable: false, configurable: false, enumerable: true });
})
