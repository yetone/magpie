// The page runs in the system's WebKit: on macOS 12 that can be Safari
// 15.0's (#220), older than what the rest of the code was written against.
// Loaded before every other script, this fills in the few built-ins they
// use that came later, each only where it is missing. The syntax the
// scripts may use is held by tests/old-webkit.test.cjs.

// Safari 15.4
if (!Array.prototype.at) {
  Object.defineProperty(Array.prototype, "at", {
    configurable: true, writable: true,
    value(i) { i = Math.trunc(i) || 0; if (i < 0) i += this.length; return this[i]; },
  });
}
if (!Array.prototype.findLast) {
  Object.defineProperty(Array.prototype, "findLast", {
    configurable: true, writable: true,
    value(f, self) { for (let i = this.length - 1; i >= 0; i--) if (f.call(self, this[i], i, this)) return this[i]; },
  });
}
if (!Array.prototype.findLastIndex) {
  Object.defineProperty(Array.prototype, "findLastIndex", {
    configurable: true, writable: true,
    value(f, self) { for (let i = this.length - 1; i >= 0; i--) if (f.call(self, this[i], i, this)) return i; return -1; },
  });
}
if (!Object.hasOwn) {
  Object.defineProperty(Object, "hasOwn", {
    configurable: true, writable: true,
    value: (o, k) => Object.prototype.hasOwnProperty.call(o, k),
  });
}
// the page only copies plain data (what the API sent), so JSON is enough
if (!window.structuredClone) {
  window.structuredClone = (v) => (v === undefined ? v : JSON.parse(JSON.stringify(v)));
}
