// A plugin giving web search APIs (the search hook), which magpie lists
// beside Tavily and the rest. No network: each answers from its arguments.
export default async () => ({
  search: {
    fake: {
      name: "Fake Search",
      keysURL: "https://example.com/keys",
      async execute({ query, count, key, url }) {
        if (key === "spent") {
          const e = new Error("quota spent for " + key)
          e.status = 429
          e.retryAfter = 30
          throw e
        }
        if (key === "boom") throw new Error("it broke")
        if (key === "hang") await new Promise(() => {})
        if (key === "shape") return "not a list"
        return { results: [{ title: "T " + query, url: "https://example.com/" + count, content: "key " + key + " url " + url, date: "2026" }, null, { title: 5 }] }
      },
    },
    own: { name: "Own", keyOptional: true, url: true, execute: async () => [] },
    "Bad Id": { name: "x", execute: async () => [] },
    noexec: { name: "x" },
  },
})
