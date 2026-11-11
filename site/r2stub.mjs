// An R2 bucket binding for the tests: what files.js asks of R2 (get with
// range and onlyIf, head, put with sha256, list with prefix, delimiter
// and include, delete), kept in memory. put refuses a body whose SHA-256
// isn't the one given, as R2 does. fail makes every call throw.
import { createHash } from "node:crypto";

export function bucket() {
  const objs = new Map();
  const b = {
    objs,
    fail: false,
    puts: 0,
    check() {
      if (b.fail) throw new Error("R2 is down");
    },
    view(key, o, body, range) {
      return {
        key,
        size: o.bytes.length,
        httpEtag: `"${o.sha256.slice(0, 8)}"`,
        customMetadata: o.customMetadata,
        writeHttpMetadata(h) {
          if (o.httpMetadata.contentType) h.set("Content-Type", o.httpMetadata.contentType);
          if (o.httpMetadata.contentDisposition) h.set("Content-Disposition", o.httpMetadata.contentDisposition);
        },
        ...(body === undefined ? {} : { body, range }),
      };
    },
    async head(key) {
      b.check();
      const o = objs.get(key);
      return o ? b.view(key, o) : null;
    },
    async get(key, opts = {}) {
      b.check();
      const o = objs.get(key);
      if (!o) return null;
      const h = opts.onlyIf instanceof Headers ? opts.onlyIf : new Headers();
      if (h.get("If-None-Match") === `"${o.sha256.slice(0, 8)}"`) return b.view(key, o);
      const rh = opts.range instanceof Headers ? opts.range.get("Range") : null;
      if (!rh) return b.view(key, o, new Blob([o.bytes]).stream(), undefined);
      const m = rh.match(/^bytes=(\d*)-(\d*)$/);
      if (!m || (m[1] === "" && m[2] === "")) throw new Error("bad range");
      let range, start, end;
      if (m[1] === "") {
        range = { offset: undefined, length: undefined, suffix: +m[2] };
        start = Math.max(0, o.bytes.length - +m[2]);
        end = o.bytes.length;
      } else {
        start = +m[1];
        if (start >= o.bytes.length) throw new Error("range not satisfiable");
        end = m[2] === "" ? o.bytes.length : Math.min(o.bytes.length, +m[2] + 1);
        range = { offset: start, length: end - start, suffix: undefined };
      }
      return b.view(key, o, new Blob([o.bytes.slice(start, end)]).stream(), range);
    },
    async put(key, body, opts = {}) {
      b.check();
      b.puts++;
      const bytes = new Uint8Array(await new Response(body).arrayBuffer());
      const sha256 = createHash("sha256").update(bytes).digest("hex");
      if (opts.sha256 && opts.sha256 !== sha256) throw new Error(`put: The SHA-256 checksum you specified did not match what we received`);
      objs.set(key, { bytes, sha256, httpMetadata: opts.httpMetadata || {}, customMetadata: opts.customMetadata || {} });
      return b.view(key, objs.get(key));
    },
    async list(opts = {}) {
      b.check();
      const prefix = opts.prefix || "";
      const keys = [...objs.keys()].filter((k) => k.startsWith(prefix)).sort();
      if (opts.delimiter) {
        const pre = new Set(keys.map((k) => k.slice(prefix.length)).filter((k) => k.includes(opts.delimiter)).map((k) => prefix + k.split(opts.delimiter)[0] + opts.delimiter));
        return { objects: [], delimitedPrefixes: [...pre], truncated: false };
      }
      return { objects: keys.map((k) => b.view(k, objs.get(k))), delimitedPrefixes: [], truncated: false };
    },
    async delete(keys) {
      b.check();
      for (const k of [].concat(keys)) objs.delete(k);
    },
  };
  return b;
}

export function sha(s) {
  return createHash("sha256").update(s).digest("hex");
}

// hold puts body at tag/name, as the cron would have
export async function hold(b, tag, name, body) {
  await b.put(`${tag}/${name}`, body, { sha256: sha(body), httpMetadata: { contentType: "application/octet-stream" }, customMetadata: { sha256: sha(body) } });
}
