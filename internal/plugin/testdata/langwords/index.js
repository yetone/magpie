// A plugin giving its sign-in words in other languages, magpie's own
// fields beside OpenCode's English ones: a method's labels, a question's
// messages, a choice's labels and hints ({"zh": "…"}). Words that aren't
// strings, empty ones and a list are left out; Pear gives none.
export const Quince = async () => ({
  auth: {
    provider: "quince",
    methods: [
      {
        type: "oauth",
        label: "Quince sign-in",
        labels: { zh: "Quince 登录", "zh-TW": "Quince 登入", ja: 42, de: "  " },
        prompts: [{
          type: "select", key: "way", message: "Choose a sign-in method",
          messages: { zh: "选择登录方式", ja: "サインイン方法を選択" },
          options: [
            { label: "Builder ID", value: "builderid", hint: "AWS sign-in", labels: { zh: "Builder ID（个人）" }, hints: { zh: "AWS 登录" } },
            { label: "Sign-in page", value: "portal", labels: ["not", "a", "map"] },
          ],
        }],
        authorize: async () => ({ url: "https://quince.invalid", instructions: "", method: "auto", callback: async () => ({ type: "failed" }) }),
      },
      { type: "api", label: "Quince API key", labels: { zh: "Quince API Key" } },
    ],
  },
})

export const Pear = async () => ({
  auth: {
    provider: "pear",
    methods: [{ type: "api", label: "Pear API key" }],
  },
})
