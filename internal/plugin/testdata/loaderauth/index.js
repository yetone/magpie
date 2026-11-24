// A plugin whose loader reads its account's sign-in once, as agy-cli's
// does (an account's home is in its sign-in's metadata): its options carry
// the key it read, and its fetch answers, with no vendor, with the home it
// read and how many times the loader ran.
let loads = 0

export const LoaderAuthPlugin = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.loaderco = {
      name: "LoaderCo",
      npm: "@ai-sdk/openai-compatible",
      api: "https://loader.invalid/v1",
      models: { "lo-1": { name: "Lo One", limit: { context: 1000, output: 100 } } },
    }
  },
  auth: {
    provider: "loaderco",
    methods: [{ type: "api", label: "API key" }],
    loader: async (getAuth) => {
      loads++
      const a = await getAuth()
      const home = a?.metadata?.home ?? ""
      return {
        baseURL: "https://loader.invalid/v1",
        apiKey: "made-from-" + (a?.key ?? ""),
        async fetch() {
          return new Response(JSON.stringify({ home, loads }), { headers: { "content-type": "application/json" } })
        },
      }
    },
  },
})
