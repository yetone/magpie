// A plugin giving its providers their own icons and its key a title and a
// hint: the auth hook's icon (magpie's own field), else package.json's
// magpie.icon; an "api" method's label and placeholder.
const svg = "data:image/svg+xml;base64," + btoa('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="#fd0"/></svg>')

export const Lemon = async () => ({
  auth: {
    provider: "lemon",
    icon: svg,
    methods: [
      { type: "api", label: "Lemon API key (from lemon.example/keys)", placeholder: "sk-lemon-…" },
      { type: "oauth", label: "Browser", placeholder: "not a key's", authorize: async () => ({ url: "https://lemon.invalid", instructions: "", method: "auto", callback: async () => ({ type: "failed" }) }) },
    ],
  },
})

export const Lime = async () => ({
  auth: {
    provider: "lime",
    icon: "javascript:alert(1)", // refused: package.json's instead
    methods: [{ type: "api", label: "API key", placeholder: 42 }],
  },
})
