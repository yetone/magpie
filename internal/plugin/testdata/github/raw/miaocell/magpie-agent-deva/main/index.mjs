// The Bun side of the plugin. Magpie's Agents page writes what was picked into
// ~/.deva/.magpie-agent.json; this applies it to Deva's own config.json.
//
// Deva keeps its providers in a JSON array and only reads that file when it
// starts. config.json is written even while Deva runs (and re-checked every
// tick, since Deva saves its in-memory copy over it), but secrets.json is not:
// Deva reads that one once and later saves the whole store from memory, so a
// key written while it runs would be invisible to it and clobbered by its next
// save. The key therefore waits for the exit.
//
// The provider's key is written at all because the gateway hands out the whole
// model catalogue to an unknown key and only the ticked ones to the key magpie
// issued — without it Deva's "add model" lists everything.
import fs from "node:fs"
import path from "node:path"
import { restore, selectionFrom, syncSelection } from "./lib/deva-config.mjs"
import { secretHolds } from "./lib/deva-secrets.mjs"
import {
  deleteProviderSecret,
  devaRunning,
  files,
  readJson,
  safeStorageKeyFromDisk,
  setProviderSecret,
  writeJson,
} from "./lib/deva-files.mjs"

const STARTED = Symbol.for("magpie.deva-agent.adapter.v1")
const POLL_MS = 2000

function start(input) {
  if (globalThis[STARTED]) return
  globalThis[STARTED] = true

  const log = (level, message) => {
    try {
      input?.client?.app?.log?.({ body: { service: "deva", level, message } })?.catch?.(() => {})
    } catch {
      /* Logging must never take the host down. */
    }
  }

  let lastMessage = ""
  const say = (message) => {
    if (message === lastMessage) return
    lastMessage = message
    log("info", message)
  }

  log("info", "deva: adapter started")

  // safeStorage key, read at most once on success.
  let storageKey

  const gatewayKey = async () => {
    if (storageKey !== undefined) return storageKey
    storageKey = await safeStorageKeyFromDisk()
    return storageKey
  }

  const tick = async () => {
    const f = files()
    if (!fs.existsSync(f.home)) return

    const picked = readJson(f.selection)
    if (!picked.ok && !picked.missing) return
    const selection = picked.ok ? selectionFrom(picked.value) : null

    const stateFile = readJson(f.state)
    const state = stateFile.ok && stateFile.value && typeof stateFile.value === "object" ? stateFile.value : null

    // Deva is written to even while it runs. It only reads config.json at start,
    // so nothing shows up until the user restarts it — but waiting for the exit
    // means never writing at all when Deva stays open, and Deva saves its own
    // in-memory copy over ours the moment anything is touched there. Writing
    // anyway and re-checking the file every tick is what keeps the two in step.
    if (!selection) {
      // Disconnected: put back what was there before the first pick.
      if (!state) return
      const config = readJson(f.config)
      if (!config.ok) return
      const next = restore(config.value, state)
      if (JSON.stringify(next) !== JSON.stringify(config.value)) {
        if (!fs.existsSync(f.backup)) writeJson(f.backup, config.value)
        writeJson(f.config, next)
      }
      // secrets.json is read once by Deva and then saved whole from memory, so
      // touching it while Deva runs would be undone by Deva's next save. Wait
      // for the exit, and keep the state file until the key is really gone.
      if (state.ownedSecret) {
        if (await devaRunning()) return say("Deva 正在运行，等它退出后清理网关密钥。")
        deleteProviderSecret(f.secrets, state.providerId)
      }
      fs.rmSync(f.state, { force: true })
      return say("已恢复 Deva 原来的模型设置。重开 Deva 才看得到。")
    }

    const config = readJson(f.config)
    if (!config.ok) return say("找不到 Deva 的 config.json，等 Deva 启动一次。")

    let result
    try {
      result = syncSelection(config.value, selection, state)
    } catch (error) {
      return say(`没有写入 Deva：${error?.message ?? error}`)
    }

    // Compared against what is on disk rather than against the last pick: Deva
    // rewrites this file from its own memory whenever it is used, which drops
    // what magpie put there. Noticing that and putting it back keeps the two
    // from drifting apart after the first sync.
    if (result.changed) {
      if (!fs.existsSync(f.backup)) writeJson(f.backup, config.value)
      writeJson(f.config, result.config)
    }

    // The key is what makes the gateway answer with only the ticked models.
    // Without it "add model" in Deva lists the whole catalogue.
    //
    // Deva reads secrets.json once and later saves the whole store from memory,
    // so a write here while it runs is invisible to it and gets clobbered by its
    // next save. The key therefore waits for the exit, unlike config.json.
    let nextState = result.state
    if (selection.gatewayKey) {
      const key = await gatewayKey()
      const stored = readProviderSecret(f.secrets, result.state.providerId)
      let held = Boolean(key) && secretHolds(stored, key, selection.gatewayKey)
      if (!held) {
        if (!key) {
          /* cannot encrypt on this platform: the key stays the user's to fill */
        } else if (await devaRunning()) {
          say("Deva 正在运行，网关密钥等它退出后写入。")
        } else if (setProviderSecret(f.secrets, result.state.providerId, selection.gatewayKey, key).written) {
          held = true
        }
      }
      if (held !== Boolean(nextState.ownedSecret)) nextState = { ...nextState, ownedSecret: held }
    }

    // `added` follows the ticked list even when nothing in config.json moved:
    // bookkeeping left stale is how a model that should have been taken back
    // ends up staying in Deva forever.
    if (JSON.stringify(nextState) !== JSON.stringify(stateFile.value)) writeJson(f.state, nextState)

    say(
      result.changed
        ? `已把 ${selection.models.length} 个模型写进 Deva。重开 Deva 后在它的模型列表里选一个用。`
        : `Deva 里已是勾选的 ${selection.models.length} 个模型。`
    )
  }

  const loop = () => {
    tick().catch((error) => log("error", `deva: ${error?.message ?? error}`))
  }

  loop()
  setInterval(loop, POLL_MS).unref?.()
}

export default async function devaAgent(input) {
  start(input)
  return {}
}
