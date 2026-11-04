// A plugin declaring switches for its provider (auth.options, magpie's own
// field, #2038) and reading them as they stand: its key prompt's check
// answers with what input.magpie.options says, read when it is asked.
export const Optco = async (input) => ({
  // its list goes by a switch, as WorkBuddy's "Free models only" does
  // (plugins#430): beta-model is listed only while beta is on
  config: async (cfg) => {
    cfg.provider ??= {}
    cfg.provider.optco ??= { name: "Optco", npm: "@ai-sdk/openai-compatible", api: "http://127.0.0.1:9/v1", models: { "base-model": { name: "Base" } } }
  },
  provider: {
    id: "optco",
    async models(provider) {
      const out = { ...provider.models }
      if (input.magpie?.option("optco", "beta")) out["beta-model"] = { ...out["base-model"], id: "beta-model", name: "Beta" }
      return out
    },
  },
  auth: {
    provider: "optco",
    options: [
      { key: "auto-claim", label: "  Claim rewards  ", description: "Claims the daily reward when usage is read.", default: true, descriptions: { zh: " 读取用量时领取每日奖励。 " }, filtersModels: true },
      // the words in other languages (plugins#590): what isn't a string,
      // an empty one and a language name over 16 are left out
      // placement "top": drawn under the accounts (#5269)
      { key: "beta", label: "Beta models", placement: "top", confirm: "Beta models may be withdrawn.", labels: { zh: " 测试版模型 ", "zh-TW": "", ja: 5, "a-language-name-too-long": "x", de: "Beta-Modelle" }, confirms: { zh: "测试版模型可能会下线。" } },
      { key: "beta", label: "the same key again" }, // dropped
      { key: "Bad Key", label: "not a key" }, // dropped
      { key: "nolabel" }, // dropped
      { key: "long", label: "L".repeat(200), description: "D".repeat(1000), default: "yes", filtersModels: "yes", placement: "sidebar", labels: "not an object", descriptions: { zh: "D".repeat(1000) }, confirms: Object.fromEntries(Array.from({ length: 20 }, (_, i) => ["l" + i, "c"])) }, // default and filtersModels are no boolean: off; a placement magpie doesn't know is none; the words capped, at most 16 languages
      ...Array.from({ length: 20 }, (_, i) => ({ key: "o" + i, label: "Option " + i })),
    ],
    methods: [
      {
        type: "api",
        label: "API key",
        prompts: [{ type: "text", key: "probe", message: "probe", validate: () => JSON.stringify([input.magpie?.options("optco"), input.magpie?.option("optco", "beta"), input.magpie?.option("optco", "nope")]) }],
      },
    ],
  },
})

// a provider whose plugin declares none
export const Plain = async () => ({ auth: { provider: "plain", options: "not a list", methods: [{ type: "api", label: "API key" }] } })
