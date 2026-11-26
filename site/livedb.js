// A shared artifact's document store, for the Durable Object that holds
// one shared artifact: a port of magpie's internal/artifact/db.go (paths,
// limits, Get, Run, Batch, Acquire, totals), caps.go's db rules
// (ParseDBConfig, compileRules, levels, can) and the page-facing answers
// of internal/gateway/artifacts_api.go (artifactDB), so the same page
// runtime (internal/artifact/runtime.js, post("db", …)) reaches it.
//
// Values are kept as Go keeps them: a document's body is the bytes Go's
// json.Marshal makes of it (keys sorted, <>& and U+2028/9 escaped), so
// sizes, totals and the ordering of objects in a query match; strings are
// ordered by code point, as Go orders UTF-8 bytes; a time is Go's
// RFC3339Nano in UTC.

// -- limits (db.go) ---------------------------------------------------------

const MAX_DOC_BYTES = 256 << 10;
const MAX_DOC_DEPTH = 32;
const MAX_SEGMENTS = 16;
const MAX_SEG_BYTES = 200;
const MAX_PATH_BYTES = 1000;
const MAX_FILTERS = 10;
const MAX_IN_VALUES = 30;
const MAX_LIMIT = 1000;
const MAX_BATCH = 500;
const MAX_RULES = 64;
const MIN_LEASE = 1000;
const MAX_LEASE = 10 * 60 * 1000;
const DEFAULT_LEASE = 30 * 1000;
// a call is at most this (artifactAPIBody): a 256 KiB document and the
// JSON around it
const MAX_CALL_BYTES = 1 << 20;

export const LIMITS = Object.freeze({
  maxDocBytes: MAX_DOC_BYTES, maxDocDepth: MAX_DOC_DEPTH, maxSegments: MAX_SEGMENTS,
  maxSegBytes: MAX_SEG_BYTES, maxPathBytes: MAX_PATH_BYTES, maxFilters: MAX_FILTERS,
  maxInValues: MAX_IN_VALUES, maxLimit: MAX_LIMIT, maxBatch: MAX_BATCH, maxRules: MAX_RULES,
  maxDocs: 25000, maxDataBytes: 256 << 20, maxCallBytes: MAX_CALL_BYTES,
});

// DBError is a store call's failure, by the code claude.ai's runtime gives
// a page (invalid_argument, quota_exceeded, unavailable, …).
export class DBError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

const invalid = (m) => new DBError("invalid_argument", m);
const quota = (m) => new DBError("quota_exceeded", m);
const unavailable = (e) => (e instanceof DBError ? e : new DBError("unavailable", String((e && e.message) || e)));

// -- Go's strings and JSON --------------------------------------------------

// wellFormed is s with each lone surrogate made U+FFFD, as Go's decoder
// makes "\ud800" and every invalid byte.
const wellFormed = typeof String.prototype.toWellFormed === "function"
  ? (s) => s.toWellFormed()
  : (s) => s.replace(/[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/g, "�");

// utf8Len is the bytes s takes in UTF-8 (Go's len(s)).
function utf8Len(s) {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length && (s.charCodeAt(i + 1) & 0xfc00) === 0xdc00) {
      n += 4;
      i++;
    } else n += 3;
  }
  return n;
}

// cmpStr orders two strings as Go's strings.Compare orders their UTF-8
// bytes: by code point, not by UTF-16 unit.
function cmpStr(a, b) {
  if (a === b) return 0;
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) {
    let x = a.charCodeAt(i), y = b.charCodeAt(i);
    if (x !== y) {
      // a surrogate is part of a code point above every BMP one
      x = x >= 0xe000 ? x - 0x800 : x >= 0xd800 ? x + 0x2000 : x;
      y = y >= 0xe000 ? y - 0x800 : y >= 0xd800 ? y + 0x2000 : y;
      return x < y ? -1 : 1;
    }
  }
  return a.length < b.length ? -1 : 1;
}

// goQuote is Go's %q of s (strconv.Quote), for error messages.
function goQuote(s) {
  let out = '"';
  for (const ch of wellFormed(String(s))) {
    const c = ch.codePointAt(0);
    if (ch === '"' || ch === "\\") out += "\\" + ch;
    else if (c >= 0x20 && c < 0x7f) out += ch;
    else if (c === 7) out += "\\a";
    else if (c === 8) out += "\\b";
    else if (c === 12) out += "\\f";
    else if (c === 10) out += "\\n";
    else if (c === 13) out += "\\r";
    else if (c === 9) out += "\\t";
    else if (c === 11) out += "\\v";
    else if (c < 0x20 || c === 0x7f) out += "\\x" + c.toString(16).padStart(2, "0");
    else if (c < 0xa0 || c === 0xad || (c >= 0x2028 && c <= 0x202e) || (c >= 0x2060 && c <= 0x206f) || c === 0xfeff) out += "\\u" + c.toString(16).padStart(4, "0");
    else out += ch;
  }
  return out + '"';
}

const hasOwn = (o, k) => Object.prototype.hasOwnProperty.call(o, k);
const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
// put sets o[k] as an own field, "__proto__" included
const put = (o, k, v) => Object.defineProperty(o, k, { value: v, enumerable: true, writable: true, configurable: true });

class Unsupported extends Error {}

function goString(s) {
  return JSON.stringify(wellFormed(s)).replace(/[<>&\u2028\u2029]/g, (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"));
}

// goJSON is the text Go's json.Marshal makes of a decoded JSON value.
function goJSON(v) {
  if (v === null) return "null";
  switch (typeof v) {
    case "boolean":
      return v ? "true" : "false";
    case "number":
      if (!Number.isFinite(v)) throw new Unsupported("json: unsupported value: " + (Number.isNaN(v) ? "NaN" : v > 0 ? "+Inf" : "-Inf"));
      return Object.is(v, -0) ? "-0" : JSON.stringify(v);
    case "string":
      return goString(v);
    case "object":
      if (Array.isArray(v)) return "[" + v.map(goJSON).join(",") + "]";
      return "{" + Object.keys(v).sort(cmpStr).map((k) => goString(k) + ":" + goJSON(v[k])).join(",") + "}";
  }
  throw new Unsupported("json: unsupported type: " + typeof v);
}

// clean is a decoded JSON value as Go decodes it: strings well formed.
function clean(v) {
  if (typeof v === "string") return wellFormed(v);
  if (Array.isArray(v)) return v.map(clean);
  if (isObj(v)) {
    const o = {};
    for (const k of Object.keys(v)) put(o, wellFormed(k), clean(v[k]));
    return o;
  }
  return v;
}

// goTime is a time in Unix ms as Go marshals it (UTC, RFC3339Nano):
// trailing zeros of the fraction dropped, and the fraction when it is 0.
function goTime(ms) {
  return new Date(ms).toISOString().replace(/\.?0+Z$/, "Z");
}

// jsonKind names a value as Go's UnmarshalTypeError does.
function jsonKind(v) {
  if (typeof v === "number") return "number " + String(v);
  if (typeof v === "boolean") return "bool";
  if (Array.isArray(v)) return "array";
  return typeof v;
}

// fields are obj's fields named in names (lower case), matched as Go's
// encoding/json matches them: case-insensitively, the last one winning.
function fields(obj, names) {
  const out = {};
  for (const k of Object.keys(obj)) {
    const l = k.replace(/[A-Z]/g, (c) => c.toLowerCase());
    if (names.includes(l)) out[l] = obj[k];
  }
  return out;
}

// -- paths ------------------------------------------------------------------

// checkSegment says why s can't be a path segment, or "".
function checkSegment(s) {
  if (s === "") return "a path has no empty segment";
  if (s === "." || s === "..") return goQuote(s) + " can't be a segment";
  if (utf8Len(s) > MAX_SEG_BYTES) return `a segment is at most ${MAX_SEG_BYTES} bytes`;
  if (!/^[A-Za-z0-9_\-.~:@+]*$/.test(s)) return `segment ${goQuote(s)}: segments use letters, digits and _ - . ~ : @ + only`;
  return "";
}

// checkPath is a store path's segments; doc says whether it names a
// document (an even number of segments) or a collection (odd). It throws
// an invalid_argument DBError.
export function checkPath(p, doc) {
  if (typeof p !== "string") p = "";
  if (utf8Len(p) > MAX_PATH_BYTES) throw invalid(`a path is at most ${MAX_PATH_BYTES} bytes`);
  const segs = p === "" ? [] : p.split("/");
  if (segs.length === 0 || segs.length > MAX_SEGMENTS) throw invalid(`a path has 1 to ${MAX_SEGMENTS} segments; ${goQuote(p)} has ${segs.length}`);
  for (const s of segs) {
    const why = checkSegment(s);
    if (why) throw invalid(why);
  }
  if (doc && segs.length % 2 !== 0) throw invalid(`${goQuote(p)} has ${segs.length} segments, so it is a collection: a document's path has an even number`);
  if (!doc && segs.length % 2 === 0) throw invalid(`${goQuote(p)} has ${segs.length} segments, so it is a document: a collection's path has an odd number`);
  return segs;
}

// -- rules (caps.go) --------------------------------------------------------

export const LEVEL = Object.freeze({ view: 0, interact: 1, admin: 2, owner: 3, self: 4 });
const UNSET = -1;
const levelOf = (l) => (hasOwn(LEVEL, l) ? LEVEL[l] : undefined);

const BAD_DECL = 'it is {} or {"rules": [{"path": "votes", "read": "view", "write": "owner"}]}, where read and write are levels: view, interact, admin, owner or self';

// splitPath is a rule path's segments; "" is the root, with none.
function splitPath(p) {
  p = p.replace(/^\/+|\/+$/g, "");
  return p === "" ? [] : p.split("/");
}

// decodeDBConfig is decl as Go's json.Unmarshal reads it into DBConfig.
function decodeDBConfig(decl) {
  if (decl === null || decl === undefined) return [];
  if (!isObj(decl)) throw new Error(BAD_DECL);
  const { rules } = fields(decl, ["rules"]);
  if (rules === null || rules === undefined) return [];
  if (!Array.isArray(rules)) throw new Error(BAD_DECL);
  return rules.map((r) => {
    if (r === null) return { path: "", read: "", write: "" };
    if (!isObj(r)) throw new Error(BAD_DECL);
    const f = fields(r, ["path", "read", "write"]);
    const out = {};
    for (const k of ["path", "read", "write"]) {
      const v = f[k];
      if (v !== null && v !== undefined && typeof v !== "string") throw new Error(BAD_DECL);
      out[k] = v == null ? "" : wellFormed(v);
    }
    return out;
  });
}

// parseRules reads and checks the db capability's declaration ({} or
// {rules: […]}) as ParseDBConfig does, and compiles it; it throws an Error
// with ParseDBConfig's message.
export function parseRules(decl) {
  const rules = decodeDBConfig(decl);
  if (rules.length > MAX_RULES) throw new Error(`${rules.length} rules; the most is ${MAX_RULES}`);
  const seen = new Set();
  const selfPrefix = new Set();
  rules.forEach((r, i) => {
    const segs = splitPath(r.path);
    for (let j = 0; j < segs.length; j++) {
      if (segs[j] === "{self}") {
        if (j !== segs.length - 1 || j === 0) throw new Error(`rule ${i}: {self} is the last segment of a path below a prefix`);
        continue;
      }
      const why = checkSegment(segs[j]);
      if (why) throw new Error(`rule ${i}: ${why}`);
    }
    if (segs.length > MAX_SEGMENTS) throw new Error(`rule ${i}: a path has at most ${MAX_SEGMENTS} segments`);
    const key = segs.join("/");
    if (seen.has(key)) throw new Error(`rule ${i}: a second rule for ${goQuote(r.path)}`);
    seen.add(key);
    if (segs.length > 0 && segs[segs.length - 1] === "{self}") selfPrefix.add(segs.slice(0, -1).join("/"));
    for (const l of [r.read, r.write]) {
      if (l !== "" && levelOf(l) === undefined) throw new Error(`rule ${i}: ${goQuote(l)} isn't a level; they are view, interact, admin, owner and self`);
    }
  });
  rules.forEach((r, i) => {
    const segs = splitPath(r.path);
    const key = segs.join("/");
    const self = segs.length > 0 && segs[segs.length - 1] === "{self}";
    const usesSelf = r.read === "self" || r.write === "self";
    if (usesSelf && (self || !selfPrefix.has(key))) throw new Error(`rule ${i}: self goes only on the rule at the prefix of a {self} rule`);
    if (r.read === "self" && r.write !== "self") throw new Error(`rule ${i}: read self is valid only with write self`);
    if (selfPrefix.has(key) && (r.read === "" || r.write === "")) throw new Error(`rule ${i}: the rule at a {self} rule's prefix sets both read and write`);
    if (r.read !== "" && r.write !== "" && LEVEL[r.write] < LEVEL[r.read]) throw new Error(`rule ${i}: writing implies reading, so write is never below read`);
  });
  return compileRules(rules);
}

// compileRules: the declared rules and the defaults every declaration
// starts from (everyone admitted reads, interact writes, and each viewer's
// data/users/<id>/ is theirs), shallow first so a deeper rule overrides.
function compileRules(rules) {
  const out = [];
  const declared = new Set();
  for (const r of rules) {
    const segs = splitPath(r.path);
    out.push({
      segs,
      read: r.read !== "" ? LEVEL[r.read] : UNSET,
      write: r.write !== "" ? LEVEL[r.write] : UNSET,
      self: segs.length > 0 && segs[segs.length - 1] === "{self}",
    });
    declared.add(segs.join("/"));
  }
  if (!declared.has("data/users")) out.push({ segs: ["data", "users"], read: LEVEL.self, write: LEVEL.self, self: false });
  if (!declared.has("data/users/{self}")) out.push({ segs: ["data", "users", "{self}"], read: UNSET, write: UNSET, self: true });
  out.sort((a, b) => a.segs.length - b.segs.length); // stable
  return Object.freeze({ rules: out });
}

const DEFAULT_RULES = compileRules([]);

// levels is the least level reading and writing segs take for viewer id.
function levels(rs, segs, id) {
  let read = LEVEL.view, write = LEVEL.interact;
  // what was in force above a {self} rule's prefix, for the own subtree
  const above = new Map();
  for (const r of rs.rules) {
    if (r.segs.length > segs.length) continue;
    let match = true;
    for (let i = 0; i < r.segs.length; i++) {
      const s = r.segs[i];
      if (s === "{self}") {
        if (!(id !== "" && segs[i] === id)) match = false;
        break;
      }
      if (segs[i] !== s) {
        match = false;
        break;
      }
    }
    if (!match) continue;
    if (r.self) {
      // the viewer's own subtree: the {self} rule's levels, unset ones
      // from above its prefix, or the prefix's where it gives more
      const prefix = r.segs.slice(0, -1).join("/");
      const base = above.get(prefix) || { read, write };
      const sr = r.read === UNSET ? base.read : r.read;
      const sw = r.write === UNSET ? base.write : r.write;
      read = Math.min(read, sr);
      write = Math.min(write, sw);
      continue;
    }
    above.set(r.segs.join("/"), { read, write });
    if (r.read !== UNSET) read = r.read;
    if (r.write !== UNSET) write = r.write;
    if (write < read) write = read;
  }
  return { read, write };
}

// can is whether viewer v reads (write false) or writes the path segs.
function can(rs, v, segs, write) {
  if (v.agent) return true;
  const l = levels(rs || DEFAULT_RULES, segs, viewerID(v));
  const need = write ? l.write : l.read;
  return need !== LEVEL.self && Number(v.level) >= need;
}

const viewerID = (v) => (typeof v.id === "string" ? v.id : v.id == null ? "" : String(v.id));

// canRead is whether viewer may read the document at path, or, for a
// collection's path, some document in it: one at the viewer's own id, at
// an id a rule names, or at any other id. A path that isn't one is read
// by nobody.
export function canRead(rules, viewer, path) {
  if (typeof path !== "string" || path === "") return false;
  const segs = path.split("/");
  let doc;
  try {
    checkPath(path, segs.length % 2 === 0);
    doc = segs.length % 2 === 0;
  } catch {
    return false;
  }
  const rs = rules || DEFAULT_RULES;
  if (doc) return can(rs, viewer, segs, false);
  const ids = new Set([""]); // "": an id no rule names
  const id = viewerID(viewer);
  if (id !== "") ids.add(id);
  for (const r of rs.rules) {
    if (r.segs.length === segs.length + 1 && r.segs[segs.length] !== "{self}") ids.add(r.segs[segs.length]);
  }
  for (const x of ids) if (can(rs, viewer, [...segs, x], false)) return true;
  return false;
}

// -- values (db.go) ---------------------------------------------------------

// kind orders JSON values as Firestore does: null, booleans, numbers,
// strings, arrays, objects.
function kind(v) {
  if (v === null || v === undefined) return 0;
  switch (typeof v) {
    case "boolean":
      return 1;
    case "number":
      return 2;
    case "string":
      return 3;
  }
  return Array.isArray(v) ? 4 : 5;
}

// compare orders two JSON values: by kind, then by value.
function compare(a, b) {
  const ka = kind(a), kb = kind(b);
  if (ka !== kb) return ka - kb;
  switch (ka) {
    case 1:
      return a === b ? 0 : a ? 1 : -1;
    case 2:
      return a < b ? -1 : a > b ? 1 : 0;
    case 3:
      return cmpStr(a, b);
    case 4: {
      for (let i = 0; i < a.length && i < b.length; i++) {
        const c = compare(a[i], b[i]);
        if (c !== 0) return c;
      }
      return a.length - b.length;
    }
    case 5:
      return cmpStr(goJSON(a), goJSON(b));
  }
  return 0;
}

const FILTER_OPS = ["==", "!=", "<", "<=", ">", ">=", "in", "not-in", "array-contains"];

function checkFilter(f) {
  if (f.field === "") throw invalid("a filter names a field");
  if (!FILTER_OPS.includes(f.op)) throw invalid(`${goQuote(f.op)} isn't an operator; they are ${FILTER_OPS.join(" ")}`);
  if ((f.op === "in" || f.op === "not-in") && (!Array.isArray(f.value) || f.value.length > MAX_IN_VALUES)) {
    throw invalid(`${f.op} takes an array of at most ${MAX_IN_VALUES} values`);
  }
}

function matches(data, where) {
  for (const f of where) {
    const has = hasOwn(data, f.field);
    const v = has ? data[f.field] : undefined;
    switch (f.op) {
      case "==":
        if (!has || compare(v, f.value) !== 0) return false;
        break;
      case "!=":
        if (!has || compare(v, f.value) === 0) return false;
        break;
      case "<":
      case "<=":
      case ">":
      case ">=": {
        if (!has || kind(v) !== kind(f.value)) return false;
        const c = compare(v, f.value);
        if ((f.op === "<" && c >= 0) || (f.op === "<=" && c > 0) || (f.op === ">" && c <= 0) || (f.op === ">=" && c < 0)) return false;
        break;
      }
      case "in":
      case "not-in": {
        let isIn = false;
        for (const x of f.value) isIn = isIn || (has && compare(v, x) === 0);
        if (!has || isIn !== (f.op === "in")) return false;
        break;
      }
      case "array-contains": {
        if (!Array.isArray(v)) return false;
        if (!v.some((x) => compare(x, f.value) === 0)) return false;
        break;
      }
    }
  }
  return true;
}

function depth(v) {
  if (v !== null && typeof v === "object") {
    let d = 0;
    for (const e of Array.isArray(v) ? v : Object.values(v)) d = Math.max(d, depth(e));
    return d + 1;
  }
  return 0;
}

// checkBody is data's body as stored, and its bytes, or throws why data
// can't be a document's body.
function checkBody(data) {
  if (!isObj(data)) throw invalid("a document's body is a JSON object");
  if (depth(data) > MAX_DOC_DEPTH) throw invalid(`a document is at most ${MAX_DOC_DEPTH} levels deep`);
  let text;
  try {
    text = goJSON(data);
  } catch (e) {
    if (e instanceof Unsupported) throw invalid("the body isn't plain JSON: " + e.message);
    throw e;
  }
  const bytes = utf8Len(text);
  if (bytes > MAX_DOC_BYTES) throw invalid(`the document is ${bytes} bytes; the most is ${MAX_DOC_BYTES} (256 KiB)`);
  return { text, bytes };
}

// merge puts patch into base, nested objects recursively; anything else,
// arrays included, replaces the field.
function merge(base, patch) {
  const out = {};
  for (const k of Object.keys(base)) put(out, k, base[k]);
  if (patch) {
    for (const k of Object.keys(patch)) {
      const v = patch[k];
      if (isObj(v) && hasOwn(out, k) && isObj(out[k])) put(out, k, merge(out[k], v));
      else put(out, k, v);
    }
  }
  return out;
}

const collOf = (p) => p.slice(0, p.lastIndexOf("/"));
const idOf = (p) => p.slice(p.lastIndexOf("/") + 1);

class Unreadable extends Error {}

function readDoc(path, body, version, updated) {
  let data;
  try {
    data = JSON.parse(body);
  } catch (e) {
    throw new Unreadable(`${path} doesn't read: ${e.message}`);
  }
  return { path, id: idOf(path), data, version: Number(version), updated: goTime(Number(updated)) };
}

// -- the call (artifacts_api.go) --------------------------------------------

class TransformError extends Error {}

const CALL_FIELDS = ["op", "path", "data", "collection", "where", "orderby", "desc", "limit", "holder", "ttlms"];
const CALL_NAMES = { orderby: "orderBy", ttlms: "ttlMs" };

// decodeCall is the call as Go's json.Unmarshal reads it into artifactCall,
// or throws a TransformError for a value of the wrong type.
function decodeCall(c) {
  if (c === null || c === undefined) c = {};
  if (!isObj(c)) throw new TransformError(`json: cannot unmarshal ${jsonKind(c)} into Go value of type gateway.artifactCall`);
  const f = fields(c, CALL_FIELDS);
  const bad = (name, v, type) => new TransformError(`json: cannot unmarshal ${jsonKind(v)} into Go struct field artifactCall.${name} of type ${type}`);
  const str = (k) => {
    const v = f[k];
    if (v == null) return "";
    if (typeof v !== "string") throw bad(CALL_NAMES[k] || k, v, "string");
    return wellFormed(v);
  };
  const int = (k, type) => {
    const v = f[k];
    if (v == null) return 0;
    if (typeof v !== "number" || !Number.isInteger(v) || Math.abs(v) >= 2 ** 63) throw bad(CALL_NAMES[k] || k, v, type);
    return v;
  };
  const out = {
    op: str("op"), path: str("path"), collection: str("collection"), orderBy: str("orderby"), holder: str("holder"),
    limit: int("limit", "int"), ttlMs: int("ttlms", "int64"), desc: false, data: null, where: [],
  };
  if (f.desc != null) {
    if (typeof f.desc !== "boolean") throw bad("desc", f.desc, "bool");
    out.desc = f.desc;
  }
  if (f.data != null) {
    if (!isObj(f.data)) throw bad("data", f.data, "map[string]interface {}");
    out.data = clean(f.data);
  }
  if (f.where != null) {
    if (!Array.isArray(f.where)) throw bad("where", f.where, "[]artifact.Filter");
    out.where = f.where.map((w) => {
      if (w === null) return { field: "", op: "", value: null };
      if (!isObj(w)) throw bad("where", w, "artifact.Filter");
      const g = fields(w, ["field", "op", "value"]);
      for (const k of ["field", "op"]) {
        if (g[k] != null && typeof g[k] !== "string") throw new TransformError(`json: cannot unmarshal ${jsonKind(g[k])} into Go struct field Filter.where.${k} of type string`);
      }
      return { field: g.field == null ? "" : wellFormed(g.field), op: g.op == null ? "" : wellFormed(g.op), value: g.value === undefined ? null : clean(g.value) };
    });
  }
  return out;
}

const errorBody = (code, message) => ({ error: { code, message } });

// readCall is a POST's body as artifactDB reads it: {c} to give call(), or
// {status, body} when it is too big or isn't JSON.
export function readCall(body) {
  const bytes = typeof body === "string" ? utf8Len(body) : body.byteLength;
  if (bytes > MAX_CALL_BYTES) return { status: 413, body: errorBody("invalid_argument", "a call is at most 1 MiB") };
  const text = typeof body === "string" ? body : new TextDecoder().decode(body);
  try {
    return { c: JSON.parse(text) };
  } catch (e) {
    return { status: 400, body: errorBody("transform_error", "the call isn't JSON: " + e.message) };
  }
}

// -- the store --------------------------------------------------------------

export class Store {
  // sql is a Durable Object's SqlStorage; transactionSync, when given,
  // runs a function as one transaction (ctx.storage.transactionSync).
  constructor(sql, transactionSync) {
    this.sql = sql;
    this.transactionSync = transactionSync || ((fn) => fn());
    // how many documents, and bytes of them, the store holds at most; a
    // test lowers them
    this.maxDocs = LIMITS.maxDocs;
    this.maxDataBytes = LIMITS.maxDataBytes;
    sql.exec(`CREATE TABLE IF NOT EXISTS docs (
      path TEXT PRIMARY KEY,
      coll TEXT NOT NULL,
      body TEXT NOT NULL,
      version INTEGER NOT NULL,
      updated INTEGER NOT NULL,
      lease_holder TEXT NOT NULL DEFAULT '',
      lease_until INTEGER NOT NULL DEFAULT 0
    )`);
    sql.exec(`CREATE INDEX IF NOT EXISTS docs_coll ON docs(coll)`);
    sql.exec(`CREATE TABLE IF NOT EXISTS totals (k INTEGER PRIMARY KEY CHECK (k = 0), docs INTEGER NOT NULL, bytes INTEGER NOT NULL)`);
    sql.exec(`INSERT OR IGNORE INTO totals VALUES (0, 0, 0)`);
  }

  rows(q, ...b) {
    try {
      return this.sql.exec(q, ...b).toArray();
    } catch (e) {
      throw unavailable(e);
    }
  }

  run(q, ...b) {
    try {
      this.sql.exec(q, ...b);
    } catch (e) {
      throw unavailable(e);
    }
  }

  tx(fn) {
    try {
      return this.transactionSync(fn);
    } catch (e) {
      if (e instanceof DBError || e instanceof Unreadable) throw e;
      throw unavailable(e);
    }
  }

  // totals is what the store holds, kept beside it so a write never reads
  // every document to know.
  totals() {
    const r = this.rows(`SELECT docs, bytes FROM totals`)[0];
    return { docs: Number(r.docs), bytes: Number(r.bytes) };
  }

  // call answers one page call (artifactCall) as artifactDB does: {status,
  // body, changed}, changed the document paths written.
  call(viewer, rules, c, now = Date.now()) {
    let changed = [];
    try {
      c = decodeCall(c);
      let body = {};
      switch (c.op) {
        case "get":
          body = { doc: this.get(viewer, rules, c.path) };
          break;
        case "query":
          body = { docs: this.query(viewer, rules, c) };
          break;
        case "set":
        case "update":
        case "delete":
          changed = this.write(viewer, rules, [{ op: c.op, path: c.path, data: c.data }], now, false);
          break;
        case "acquire": {
          const r = this.acquire(viewer, rules, c.path, c.holder, c.ttlMs, c.data, now);
          body = { lease: r.lease };
          changed = r.changed;
          break;
        }
        default:
          throw new DBError("capability_removed", `${goQuote(c.op)} isn't a store call`);
      }
      return { status: 200, body, changed };
    } catch (e) {
      if (e instanceof TransformError) return { status: 400, body: errorBody("transform_error", "the call isn't JSON: " + e.message), changed: [] };
      if (e instanceof DBError) return { status: 400, body: errorBody(e.code, e.message), changed: [] };
      return { status: 500, body: errorBody("unavailable", String((e && e.message) || e)), changed: [] };
    }
  }

  // get is the document at p, null when there is none or the viewer
  // can't see it: the two are the same to the viewer.
  get(viewer, rules, p) {
    const segs = checkPath(p, true);
    if (!can(rules, viewer, segs, false)) return null;
    const r = this.rows(`SELECT body, version, updated FROM docs WHERE path = ?`, p)[0];
    return r ? readDoc(p, r.body, r.version, r.updated) : null;
  }

  // query is the documents q matches that the viewer can see, in its order.
  query(viewer, rules, q) {
    const segs = checkPath(q.collection, false);
    const where = q.where || [];
    if (where.length > MAX_FILTERS) throw invalid(`a query has at most ${MAX_FILTERS} filters`);
    for (const f of where) checkFilter(f);
    const limit = q.limit || 0;
    if (limit < 0 || limit > MAX_LIMIT) throw invalid(`a limit is 1 to ${MAX_LIMIT}`);
    const out = [];
    for (const r of this.rows(`SELECT path, body, version, updated FROM docs WHERE coll = ? ORDER BY path`, q.collection)) {
      if (!can(rules, viewer, [...segs, idOf(r.path)], false)) continue;
      let d;
      try {
        d = readDoc(r.path, r.body, r.version, r.updated);
      } catch {
        continue;
      }
      if (isObj(d.data) && matches(d.data, where)) out.push(d);
    }
    const by = q.orderBy || "";
    if (by !== "") {
      const less = (x, y) => {
        const aok = hasOwn(x.data, by), bok = hasOwn(y.data, by);
        // a document missing the field sorts last, either way
        if (!aok || !bok) return aok && !bok;
        return q.desc ? compare(y.data[by], x.data[by]) < 0 : compare(x.data[by], y.data[by]) < 0;
      };
      out.sort((x, y) => (less(x, y) ? -1 : less(y, x) ? 1 : 0));
    }
    return limit > 0 && out.length > limit ? out.slice(0, limit) : out;
  }

  // batch makes the writes ({op: set|update|delete, path, data}) together,
  // or none of them, answering as call does.
  batch(viewer, rules, ops, now = Date.now()) {
    try {
      const changed = this.write(viewer, rules, (ops || []).map((o) => ({ op: o.op, path: o.path, data: o.data == null ? null : clean(o.data) })), now, false);
      return { status: 200, body: {}, changed };
    } catch (e) {
      if (e instanceof DBError) return { status: 400, body: errorBody(e.code, e.message), changed: [] };
      return { status: 500, body: errorBody("unavailable", String((e && e.message) || e)), changed: [] };
    }
  }

  // write is Batch: it checks every write, then makes them in one
  // transaction, reading and changing an overlay first so a refusal part
  // way writes nothing even without one. seeding lifts the 500-write cap
  // and keeps each op's version and updated when it has them.
  write(viewer, rules, ops, now, seeding) {
    if (ops.length === 0) return [];
    if (!seeding && ops.length > MAX_BATCH) throw invalid(`a batch has at most ${MAX_BATCH} writes`);
    const ready = [];
    for (const o of ops) {
      const segs = checkPath(o.path, true);
      if (!can(rules, viewer, segs, true)) throw invalid(`this viewer can't write ${o.path}`);
      const p = { op: o.op, path: o.path, data: o.data, version: o.version, updated: o.updated };
      switch (o.op) {
        case "set":
          p.body = checkBody(o.data);
          break;
        case "update":
          if (!isObj(o.data)) throw invalid("an update's body is a JSON object");
          break;
        case "delete":
          break;
        default:
          throw invalid(`${goQuote(o.op)} isn't a write; they are set, update and delete`);
      }
      ready.push(p);
    }
    return this.tx(() => {
      let { docs, bytes: size } = this.totals();
      // path → {exists, body, bytes, version}, as the writes so far leave it
      const seen = new Map();
      const read = (path) => {
        if (seen.has(path)) return seen.get(path);
        const r = this.rows(`SELECT body, version FROM docs WHERE path = ?`, path)[0];
        return r ? { exists: true, body: r.body, bytes: utf8Len(r.body), version: Number(r.version) } : { exists: false, body: "", bytes: 0, version: 0 };
      };
      const writes = [];
      const paths = [];
      for (const p of ready) {
        const old = read(p.path);
        if (p.op === "delete") {
          if (!old.exists) continue;
          writes.push({ del: true, path: p.path });
          docs -= 1;
          size -= old.bytes;
          seen.set(p.path, { exists: false, body: "", bytes: 0, version: 0 });
          paths.push(p.path);
          continue;
        }
        let body = p.body;
        if (p.op === "update") {
          if (!old.exists) throw invalid(`update needs ${p.path} to exist; set makes it`);
          let base;
          try {
            base = JSON.parse(old.body);
          } catch {
            base = {};
          }
          body = checkBody(merge(isObj(base) ? base : {}, p.data));
        }
        if (!old.exists && docs >= this.maxDocs) throw quota(`the artifact's database holds ${this.maxDocs} documents, the most it may`);
        const grown = size - old.bytes + body.bytes;
        if (grown > this.maxDataBytes && body.bytes > old.bytes) throw quota(`the artifact's database would hold over ${Math.floor(this.maxDataBytes / 1048576)} MiB, the most it may`);
        if (!old.exists) docs++;
        size = grown;
        let version = old.version + 1;
        let updated = now;
        if (seeding) {
          if (Number.isSafeInteger(p.version) && p.version > 0) version = p.version;
          if (typeof p.updated === "number" && Number.isFinite(p.updated)) updated = Math.trunc(p.updated);
          else if (typeof p.updated === "string" && !Number.isNaN(Date.parse(p.updated))) updated = Date.parse(p.updated);
        }
        writes.push({ path: p.path, body: body.text, version, updated });
        seen.set(p.path, { exists: true, body: body.text, bytes: body.bytes, version });
        paths.push(p.path);
      }
      for (const w of writes) {
        if (w.del) this.run(`DELETE FROM docs WHERE path = ?`, w.path);
        else {
          this.run(`INSERT INTO docs(path, coll, body, version, updated) VALUES(?, ?, ?, ?, ?)
            ON CONFLICT(path) DO UPDATE SET body = excluded.body, version = excluded.version, updated = excluded.updated`,
          w.path, collOf(w.path), w.body, w.version, w.updated);
        }
      }
      this.run(`UPDATE totals SET docs = ?, bytes = ?`, docs, size);
      return paths;
    });
  }

  // acquire takes a short lease on the document at p for holder, when no
  // one else holds it, merging data into the document then.
  acquire(viewer, rules, p, holder, ttlMs, data, now) {
    const segs = checkPath(p, true);
    if (typeof holder !== "string" || holder === "" || utf8Len(holder) > 200) throw invalid("acquire names its holder, at most 200 bytes");
    if (!can(rules, viewer, segs, true)) throw invalid(`this viewer can't write ${p}`);
    // time.Duration(ttlMs) * time.Millisecond, as Go makes it (wrapping)
    let ns = BigInt.asIntN(64, BigInt(Math.trunc(Number(ttlMs) || 0)) * 1000000n);
    if (ns <= 0n) ns = BigInt(DEFAULT_LEASE) * 1000000n;
    const lo = BigInt(MIN_LEASE) * 1000000n, hi = BigInt(MAX_LEASE) * 1000000n;
    ns = ns < lo ? lo : ns > hi ? hi : ns;
    const ttl = Number(ns / 1000000n);
    return this.tx(() => {
      const r = this.rows(`SELECT body, version, lease_holder, lease_until FROM docs WHERE path = ?`, p)[0];
      const exists = !!r;
      const body = exists ? r.body : "";
      const version = exists ? Number(r.version) : 0;
      if (exists && r.lease_holder !== "" && r.lease_holder !== holder && Number(r.lease_until) > now) {
        return { lease: { acquired: false, expiresAt: goTime(Number(r.lease_until)) }, changed: [] };
      }
      let base = {};
      if (exists) {
        try {
          const b = JSON.parse(body);
          if (isObj(b)) base = b;
        } catch {
          // Go keeps the empty map
        }
      }
      const nb = checkBody(merge(base, data));
      let { docs, bytes: size } = this.totals();
      const oldBytes = utf8Len(body);
      if (!exists && docs >= this.maxDocs) throw quota(`the artifact's database holds ${this.maxDocs} documents, the most it may`);
      const grown = size - oldBytes + nb.bytes;
      if (grown > this.maxDataBytes && nb.bytes > oldBytes) throw quota(`the artifact's database would hold over ${Math.floor(this.maxDataBytes / 1048576)} MiB, the most it may`);
      if (!exists) docs++;
      this.run(`UPDATE totals SET docs = ?, bytes = ?`, docs, grown);
      const exp = now + ttl;
      this.run(`INSERT INTO docs(path, coll, body, version, updated, lease_holder, lease_until) VALUES(?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(path) DO UPDATE SET body = excluded.body, version = excluded.version, updated = excluded.updated,
        lease_holder = excluded.lease_holder, lease_until = excluded.lease_until`,
      p, collOf(p), nb.text, version + 1, now, holder, exp);
      return { lease: { acquired: true, version: version + 1, expiresAt: goTime(exp), holder }, changed: [p] };
    });
  }

  // stats is how much the store holds (Stats).
  stats() {
    return this.totals();
  }

  // dump is every document, for an export.
  dump() {
    return this.rows(`SELECT path, body, version, updated FROM docs ORDER BY path`).map((r) => {
      const d = readDoc(r.path, r.body, r.version, r.updated);
      return { path: d.path, data: d.data, version: d.version, updated: d.updated };
    });
  }

  // seed writes docs ([{path, data, version?, updated?}]) as the agent, all
  // or none: it throws the first one's DBError, having written nothing. A
  // doc's version and updated (Go's time text or Unix ms), when given,
  // are kept, so a dump seeds back as it was.
  seed(docs, now = Date.now()) {
    const ops = (docs || []).map((d) => ({ op: "set", path: d && d.path, data: d && d.data != null ? clean(d.data) : null, version: d && d.version, updated: d && d.updated }));
    return this.write({ agent: true, id: "", level: LEVEL.owner }, DEFAULT_RULES, ops, now, true);
  }
}
