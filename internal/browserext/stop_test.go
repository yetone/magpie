package browserext

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A user who presses Cancel on Chrome's "is debugging this browser" bar
// while browser_type's typing goes on after its call has stopped that
// typing too: the timer that keeps the tab attached for the typing never
// attaches the debugger again, and not one key more goes into the page.
func TestCancelStopsTypingThatOutlivedItsCall(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	dir, err := filepath.Abs("ext")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "--input-type=module", "-e", cancelStopsTypingJS, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const cancelStopsTypingJS = `
import { pathToFileURL } from "node:url";
import path from "node:path";
const dir = process.argv[1];
const attached = new Set();
let attaches = 0, onDetach;
const keys = [];
globalThis.chrome = {
  debugger: {
    attach: async ({ tabId }) => { attaches++; attached.add(tabId); },
    detach: async ({ tabId }) => { attached.delete(tabId); },
    sendCommand: async (to, method, params) => {
      if (!attached.has(to.tabId)) throw new Error("Debugger is not attached to the tab with id: " + to.tabId + ".");
      if (method === "Input.dispatchKeyEvent" && params.text) keys.push(params.text);
      return {};
    },
    onEvent: { addListener() {} },
    onDetach: { addListener(f) { onDetach = f; } },
  },
  tabs: { get: async () => ({ url: "https://example.com/" }) },
};
const cdp = await import(pathToFileURL(path.join(dir, "cdp.js")).href);
const human = await import(pathToFileURL(path.join(dir, "human.js")).href);
const fail = (m) => { console.error(m); process.exit(1); };
const wait = async (ok) => { for (let i = 0; i < 400 && !ok(); i++) await new Promise((r) => setTimeout(r, 25)); if (!ok()) fail("timed out"); };

// typing still going on holds its tab
await cdp.attach(1);
const t = cdp.state(1);
const typing = human.type(t, "a line typed at a person's pace", {}).then(() => "finished", (e) => String(e.message || e));
await wait(() => keys.length >= 4);
if (!cdp.hold(1)) fail("hold let go of a tab typing goes on in");

// the user presses Cancel: Chrome detaches and says why
attached.delete(1);
onDetach({ tabId: 1 }, "canceled_by_user");
const at = keys.length;
if (cdp.hold(1)) fail("hold went on after the user's Cancel");
if (attaches !== 1 || attached.has(1)) fail("the debugger was attached again after the user's Cancel: " + attaches + " attaches");
const end = await typing;
if (end === "finished") fail("the typing finished after the user's Cancel");
if (keys.length !== at) fail((keys.length - at) + " keys went in after the user's Cancel");

// a typing that sees the stop first sends nothing more, even attached
t.stoppedByUser = false;
await cdp.attach(1);
const second = human.type(t, "another line typed here", {}).then(() => "finished", (e) => String(e.message || e));
await wait(() => keys.length >= at + 3);
t.stoppedByUser = true;
await new Promise((r) => setTimeout(r, 400));
const n = keys.length;
const end2 = await second;
if (end2 === "finished" || !/stopped/.test(end2)) fail("the typing went on with the stop set: " + end2);
if (keys.length !== n) fail("keys went in with the stop set");
process.exit(0);
`
