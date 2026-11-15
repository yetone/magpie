// A plugin that runs its vendor's CLI and installs it itself (magpie's
// auth.cli): the "CLI" is a file in HOME. HOME/cli-mode says how the
// install goes: "fail" throws, "hang" waits until magpie gives it up
// (and writes HOME/cli-aborted then), anything else installs it.
import { existsSync, readFileSync, writeFileSync } from "node:fs"
import { homedir } from "node:os"
import { join } from "node:path"

const home = () => process.env.HOME || process.env.USERPROFILE || homedir()
const bin = () => join(home(), "fake-cli")
const mode = () => { try { return readFileSync(join(home(), "cli-mode"), "utf8").trim() } catch { return "" } }

export const CLIInstallPlugin = async () => ({
  auth: {
    provider: "cliinstall",
    cli: {
      name: "Fake CLI",
      manual: "curl -fsSL https://fake.invalid/install.sh | bash",
      installed: async () => existsSync(bin()),
      install: async ({ signal } = {}) => {
        const m = mode()
        if (m === "fail") throw new Error("the download was refused")
        if (m === "hang") {
          await new Promise((resolve) => signal?.addEventListener("abort", () => {
            writeFileSync(join(home(), "cli-aborted"), "1")
            resolve()
          }))
          throw new Error("aborted")
        }
        writeFileSync(bin(), "#!/bin/sh\n")
      },
    },
    methods: [{
      type: "oauth", label: "Browser",
      authorize: async () => existsSync(bin())
        ? { url: "https://cliinstall.invalid/login", method: "code", instructions: "Paste the code", callback: async () => ({ type: "failed" }) }
        : { url: "", method: "code", callback: async () => ({ type: "failed", error: "Fake CLI not found" }) },
    }],
  },
})
