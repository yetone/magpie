// A D1 database stood in for by node:sqlite, for the site's tests: the
// prepared statement's bind, run, first and all, and the database's batch.
import { DatabaseSync } from "node:sqlite";

export function d1() {
  const db = new DatabaseSync(":memory:");
  const stmt = (sql, args = []) => ({
    bind: (...a) => stmt(sql, a),
    run: async () => {
      const r = db.prepare(sql).run(...args);
      return { success: true, meta: { changes: Number(r.changes) } };
    },
    first: async () => db.prepare(sql).get(...args) ?? null,
    all: async () => ({ results: db.prepare(sql).all(...args) }),
    async exec() {
      return /^\s*(SELECT|PRAGMA|.*RETURNING)/is.test(sql) ? { results: db.prepare(sql).all(...args) } : this.run();
    },
  });
  return {
    raw: db,
    prepare: (sql) => stmt(sql),
    async batch(list) {
      const out = [];
      for (const s of list) out.push(await s.exec());
      return out;
    },
  };
}
