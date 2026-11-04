// A plugin whose page talks to a service its host part runs, as
// page-chat's (#5187): the service listens on options.port, else on
// $PAGESVC_PORT, and the plugin's load waits until it does, trying again
// while another program holds the port. It has no other part.
import { createServer } from "node:http"

const listen = (port) =>
  new Promise((resolve) => {
    const srv = createServer((_, res) => res.end("ok"))
    srv.once("error", () => resolve(null))
    srv.listen(port, "127.0.0.1", () => resolve(srv))
  })

export const PageService = async (_input, options) => {
  const port = Number(options?.port) > 0 ? Number(options.port) : Number(process.env.PAGESVC_PORT)
  for (;;) {
    if (await listen(port)) break
    await new Promise((r) => setTimeout(r, 200))
  }
  return {}
}
