// A plugin whose sign-ins end in ways magpie must follow (#5016,
// plugins#658): a wait on a file a desktop app writes, one that ignores
// magpie giving up and one that heeds it, and authorize results that fail
// with no callback to call.
import { existsSync, writeFileSync } from "node:fs"
import { join } from "node:path"

const home = () => process.env.HOME
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const mark = (name, text = "") => writeFileSync(join(home(), name), text)

// waits for $HOME/go as AStudio's plugin waits for its app's session
// file, every 50ms, for a minute at most
async function wait(signal) {
  mark("waiting")
  for (let i = 0; i < 1200; i++) {
    if (signal?.aborted) {
      mark("aborted")
      return { type: "failed", error: "aborted" }
    }
    if (existsSync(join(home(), "go"))) {
      mark("returned")
      return { type: "success", key: "tok-late", metadata: { uid: "late" } }
    }
    await sleep(50)
  }
  return { type: "failed", error: "no session" }
}

export const SignInEndPlugin = async () => ({
  auth: {
    provider: "signinend",
    methods: [
      // 0: waits on its own, deaf to magpie giving up
      { type: "oauth", label: "deaf", authorize: async () => ({ url: "https://signinend.invalid/", instructions: "", method: "auto", callback: async () => wait() }) },
      // 1: waits on its own, and stops when given magpie's signal
      {
        type: "oauth",
        label: "heeds",
        authorize: async () => ({ url: "https://signinend.invalid/", instructions: "", method: "auto", callback: async (_code, o) => wait(o?.signal) }),
      },
      // 2: an auto sign-in that fails before it starts, as AStudio's
      // banned account does
      { type: "oauth", label: "banned", authorize: async () => ({ type: "failed", error: "this AStudio account is banned" }) },
      // 3: a code sign-in that fails before it starts
      { type: "oauth", label: "code failed", authorize: async () => ({ type: "failed", error: "Agnes Code signs in on Windows only", method: "code" }) },
      // 4: a result with nothing to finish it with
      { type: "oauth", label: "no callback", authorize: async () => ({ url: "https://signinend.invalid/", method: "auto" }) },
      // 5: a key sign-in that fails with why
      { type: "api", label: "key failed", authorize: async () => ({ type: "failed", error: "that key is revoked" }) },
      // 6: a code sign-in whose callback fails with why
      { type: "oauth", label: "code", authorize: async () => ({ url: "https://signinend.invalid/", method: "code", callback: async () => ({ type: "failed", error: "that code expired" }) }) },
      // 7: polls its vendor, whose answer never comes
      {
        type: "oauth",
        label: "polls",
        authorize: async () => ({
          url: "https://signinend.invalid/",
          method: "auto",
          callback: async () => {
            try {
              await fetch(process.env.FAKE_BASE + "/poll", { headers: { "X-Fake-Run": process.env.FAKE_RUN ?? "" } })
              return { type: "success", key: "tok-polled" }
            } catch (e) {
              mark("fetch-aborted", e?.name ?? String(e))
              return { type: "failed", error: String(e) }
            }
          },
        }),
      },
      // 8: an auto sign-in with no refresh token, the field left out, as
      // codearts' CodeArts Agent sign-in was (plugins#998)
      { type: "oauth", label: "no refresh", authorize: async () => ({ url: "", method: "auto", callback: async () => ({ type: "success", access: "acc-only", expires: 4102444800000, accountId: "norefresh" }) }) },
      // 9: a success with nothing in it to keep
      { type: "oauth", label: "nothing", authorize: async () => ({ url: "", method: "auto", callback: async () => ({ type: "success" }) }) },
    ],
  },
})
