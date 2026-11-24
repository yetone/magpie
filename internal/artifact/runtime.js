// The page runtime (docs/subsystems/artifacts.md, "Page runtime"): the
// window.claude.use(name) a page written for claude.ai's artifact runtime
// (contract 0.2.99) calls, served by magpie. It goes into the page's
// <head> ahead of the page's own scripts, with its config: the API it
// calls, the token that names this artifact's page, the capabilities the
// version declares and the viewer, who on this computer is the artifact's
// owner.
(function (C) {
  "use strict";
  if (window.claude && window.claude.use) return;
  // a shared copy (usemagpie.ai) serves no store and knows no viewer:
  // every capability resolves null there, as it does for a page's
  // visitor that claude.ai can't serve
  var served = C.shared ? { permissions: true } : { db: !!C.caps.db, user: true, permissions: true };
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
  function call(op, body) {
    body = Object.assign({ t: C.token, op: op }, body);
    var text;
    try {
      text = JSON.stringify(body);
    } catch (e) {
      return Promise.reject(dbError("transform_error", "the arguments aren't JSON: " + e.message));
    }
    return fetch(C.api + "/db", { method: "POST", body: text, headers: { "Content-Type": "text/plain" }, cache: "no-store" })
      .then(function (r) {
        return r.json().catch(function () { return { error: { code: "unavailable", message: "magpie answered " + r.status } }; });
      }, function (e) {
        throw dbError("unavailable", "magpie can't be reached: " + e.message);
      })
      .then(function (j) {
        if (j && j.error) throw dbError(j.error.code || "unavailable", j.error.message);
        return j;
      });
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
  // can see is made, and every 30 s while the stream is down
  var listeners = new Set(), source = null, poll = null;
  function heard(paths) {
    listeners.forEach(function (l) { if (!paths || l.touches(paths)) l.refresh(); });
  }
  function connect() {
    if (source || !listeners.size || typeof EventSource !== "function") return;
    source = new EventSource(C.api + "/events?t=" + encodeURIComponent(C.token));
    source.onmessage = function (e) {
      var m;
      try { m = JSON.parse(e.data); } catch (_) { return; }
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
    if (listeners.size) return;
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

  // -- permissions --------------------------------------------------------
  function states(names) {
    var out = {};
    (names || Object.keys(C.caps)).forEach(function (n) {
      if (served[n] && (n === "user" || C.caps[n])) out[n] = "granted";
      else if (names) out[n] = "unavailable";
    });
    return freeze(out);
  }
  var permissions = freeze({
    state: function (name) {
      return Promise.resolve(arguments.length ? states([String(name)])[String(name)] : states(null));
    },
    request: function (names) { return Promise.resolve(states(names ? [].concat(names).map(String) : null)); },
    manage: function () { return Promise.reject(dbError("unavailable", "magpie's pages have no Permissions panel: what a page may do is set when it is published")); },
  });

  // -- claude.use ---------------------------------------------------------
  var spaces = { db: served.db ? db : null, user: served.user ? user : null, permissions: permissions };
  var memo = {};
  function use(name) {
    name = String(name);
    if (!Object.prototype.hasOwnProperty.call(spaces, name) || !spaces[name]) return later(function () { return null; });
    return memo[name] || (memo[name] = later(function () { return spaces[name]; }));
  }
  Object.defineProperty(window, "claude", { value: freeze({ use: use }), writable: false, configurable: false, enumerable: true });
})
