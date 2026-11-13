// A plugin that takes accounts from a file (magpie's auth.import): each
// line "who:refresh" is one account; "bad" lines are refused.
export const ImporterPlugin = async () => ({
  auth: {
    provider: "importer",
    importFrom: "Choose the Importer app's accounts file",
    importFroms: { zh: "选择 Importer 的账号文件" },
    methods: [{ type: "oauth", label: "Browser", authorize: async () => ({ url: "https://importer.invalid", method: "code", callback: async () => ({ type: "failed" }) }) }],
    import: async (texts) => texts.flatMap((t) => t.split("\n").filter(Boolean).map((line) => {
      const [who, refresh] = line.split(":")
      if (who === "bad") return { type: "failed", error: "Importer refused it", accountId: "bad" }
      return { type: "success", refresh, access: "a-" + refresh, expires: Date.now() + 3600e3, accountId: who }
    })),
  },
})
