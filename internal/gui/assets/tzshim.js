// Settings › Usage › Advanced › Time zone (JackyFill on X): a magpie set to
// a zone of its own shows every time in it, whatever zone the computer or
// the browser reading the page keeps. The server cuts its days there
// (usage.Zone), and the page, which reads the clock through Date and Intl
// in a thousand places, is put in the same zone here: the API serves this
// after window.bootPrefs in boot.js, only when the zone is set, so with
// none nothing changes. A page has no zone of its own to set, so Date's
// local getters and setters, its constructor from parts or a local
// string, its strings, toLocale* and Intl.DateTimeFormat with no timeZone
// given are made to answer in the zone. A plugin's page in a frame and a
// worker keep the browser's.
(function () {
  var Z = window.bootPrefs && window.bootPrefs.timeZone;
  if (!Z) return;
  var D = Date, P = D.prototype, ODTF = Intl.DateTimeFormat;
  var fmt;
  try {
    fmt = new ODTF("en-US", { timeZone: Z, hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", second: "numeric" });
  } catch (e) {
    return; // a zone this browser doesn't know: its own, as unset
  }
  var getTime = P.getTime, setTime = P.setTime, sysOffset = P.getTimezoneOffset;
  // off is how far Z's clock is ahead of UTC at t, in ms. Zones change
  // their offset on a quarter hour, so it is worked out once a quarter.
  var offs = new Map();
  function off(t) {
    var k = Math.floor(t / 9e5), o = offs.get(k);
    if (o === undefined) {
      var at = k * 9e5, p = {}, list;
      try {
        list = fmt.formatToParts(at);
      } catch (e) {
        return 0; // past the dates a Date can hold
      }
      for (var i = 0; i < list.length; i++) p[list[i].type] = +list[i].value;
      var w = new D(0);
      w.setUTCFullYear(p.year, p.month - 1, p.day);
      w.setUTCHours(p.hour % 24, p.minute, p.second, 0);
      o = getTime.call(w) - at;
      if (offs.size > 4096) offs.clear();
      offs.set(k, o);
    }
    return o;
  }
  // instant is the time Z's clock reads w (ms, as if UTC) at. Around a
  // change of clocks it is as a local Date has it: a time read twice is
  // the first, and a time skipped is read with the offset before the
  // change, which lands past it (02:30 is 03:30 when 02:00 skips to 03:00)
  function instant(w) {
    var oE = off(w - 864e5), oL = off(w + 864e5), tE = w - oE, tL = w - oL;
    if (oE === oL || off(tE) === oE) return tE;
    return off(tL) === oL ? tL : tE;
  }
  function def(name, fn) {
    Object.defineProperty(P, name, { value: fn, writable: true, configurable: true });
  }
  var gets = { getFullYear: "getUTCFullYear", getMonth: "getUTCMonth", getDate: "getUTCDate", getDay: "getUTCDay", getHours: "getUTCHours", getMinutes: "getUTCMinutes", getSeconds: "getUTCSeconds" };
  Object.keys(gets).forEach(function (name) {
    var utc = P[gets[name]];
    def(name, function () {
      var t = getTime.call(this);
      return t !== t ? NaN : utc.call(new D(t + off(t)));
    });
  });
  def("getTimezoneOffset", function () {
    var t = getTime.call(this);
    return t !== t ? NaN : -off(t) / 6e4;
  });
  var sets = { setFullYear: "setUTCFullYear", setMonth: "setUTCMonth", setDate: "setUTCDate", setHours: "setUTCHours", setMinutes: "setUTCMinutes", setSeconds: "setUTCSeconds" };
  Object.keys(sets).forEach(function (name) {
    var utc = P[sets[name]];
    def(name, function () {
      var t = getTime.call(this);
      if (t !== t && name !== "setFullYear") return NaN;
      var w = new D(t !== t ? 0 : t + off(t));
      var to = utc.apply(w, arguments);
      return setTime.call(this, to !== to ? NaN : instant(to));
    });
  });
  // a string with no zone of its own is a time of Z's, read as the
  // computer's by the engine and moved; a date alone is UTC's, as in the
  // engine
  var dateOnly = /^\s*[+-]?\d{4,6}(-\d\d){0,2}\s*$/;
  var zoned = /(\d\s*(Z|[+-]\d\d:?\d\d)\s*$)|\b(GMT|UTC|UT)\b|\b[ECMP][SD]T\b/i;
  function parse(s) {
    s = String(s);
    var t = D.parse(s);
    if (t !== t || dateOnly.test(s) || zoned.test(s)) return t;
    return instant(t - sysOffset.call(new D(t)) * 6e4);
  }
  function ZDate(y, m) {
    if (!new.target) return new ZDate().toString();
    var n = arguments.length, t;
    if (n === 0) t = D.now();
    else if (n === 1) t = typeof y === "string" ? parse(y) : y instanceof D ? getTime.call(y) : getTime.call(new D(y));
    else {
      var w = D.UTC.apply(null, arguments);
      t = w !== w ? NaN : instant(w);
    }
    return Reflect.construct(D, [t], new.target);
  }
  ZDate.prototype = P;
  ZDate.now = D.now;
  ZDate.UTC = D.UTC;
  ZDate.parse = parse;
  Object.defineProperty(P, "constructor", { value: ZDate, writable: true, configurable: true });
  window.Date = ZDate;

  var DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
  var MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  var two = function (n) { return (n < 10 ? "0" : "") + n; };
  function dateText(d) {
    var y = d.getFullYear();
    return DAYS[d.getDay()] + " " + MONTHS[d.getMonth()] + " " + two(d.getDate()) + " " + (y < 0 ? "-" + String(-y).padStart(6, "0") : String(y).padStart(4, "0"));
  }
  function timeText(d) {
    var o = -d.getTimezoneOffset(), a = Math.abs(o);
    return two(d.getHours()) + ":" + two(d.getMinutes()) + ":" + two(d.getSeconds()) + " GMT" + (o < 0 ? "-" : "+") + two(Math.floor(a / 60)) + two(a % 60) + " (" + Z + ")";
  }
  var valid = function (d) { var t = getTime.call(d); return t === t; };
  def("toString", function () { return valid(this) ? dateText(this) + " " + timeText(this) : "Invalid Date"; });
  def("toDateString", function () { return valid(this) ? dateText(this) : "Invalid Date"; });
  def("toTimeString", function () { return valid(this) ? timeText(this) : "Invalid Date"; });

  // options with no timeZone take Z
  function inZone(o) {
    if (o == null) return { timeZone: Z };
    o = Object(o);
    return o.timeZone !== undefined ? o : Object.assign({}, o, { timeZone: Z });
  }
  ["toLocaleString", "toLocaleDateString", "toLocaleTimeString"].forEach(function (name) {
    var native = P[name];
    def(name, function (locales, o) { return native.call(this, locales, inZone(o)); });
  });
  function DateTimeFormat(locales, o) {
    return new ODTF(locales, inZone(o));
  }
  DateTimeFormat.prototype = ODTF.prototype;
  DateTimeFormat.supportedLocalesOf = ODTF.supportedLocalesOf;
  Object.defineProperty(Intl, "DateTimeFormat", { value: DateTimeFormat, writable: true, configurable: true });
})();
