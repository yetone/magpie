package claudetui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guardStub stands in for Bun's FetchSession: what each request was sent
// with is printed, a JSON line a request.
const guardStub = `if (!globalThis.Bun) globalThis.Bun = {};
Bun.FetchSession = class {
  async fetch(input, init) {
    console.log(JSON.stringify({ body: typeof init.body === "string" ? init.body : null, keys: Object.keys(init) }));
    return new Response("", { headers: { "content-type": "text/plain" } });
  }
};
`

// guardMain makes requests as Claude Code does, the bridge's guard file
// rewritten between them as a turn's safeguards change.
const guardMain = `const fs = require("fs");
const guard = process.argv[process.argv.length - 1];
const url = "https://api.anthropic.com/v1/messages?beta=true";
const headers = { "x-claude-code-request-class": "main" };
const body = (sg) => JSON.stringify({ model: "m", messages: [{ role: "user", content: "hi" }], safeguards: sg, stream: true });
(async () => {
  const s = new Bun.FetchSession();
  const ask = (u, b) => s.fetch(u, { signal: undefined, method: "POST", headers, body: b });
  // the run's own: sent as Claude Code made it
  await ask(url, body([{ c: "a" }]));
  // a turn's new ones, put in its next request in Claude Code's place
  fs.writeFileSync(guard, JSON.stringify({ safeguards: [{ c: "b", x: "<&>", n: [1, 2] }] }));
  await ask(url, body([{ c: "a" }]));
  // what Claude Code builds from the variable they now are: as it is
  await ask(url, body(JSON.parse(process.env.CLAUDE_CODE_EXTRA_BODY).safeguards));
  // a request without safeguards, another endpoint, a body not a string
  await ask(url, JSON.stringify({ model: "m", messages: [] }));
  await ask("https://api.anthropic.com/v1/messages/count_tokens", body([{ c: "a" }]));
  await s.fetch(url, { method: "POST", headers, body: new TextEncoder().encode(body([{ c: "a" }])) });
  console.log(JSON.stringify({ env: process.env.CLAUDE_CODE_EXTRA_BODY, guardEnv: process.env.MAGPIE_CLAUDE_GUARD || null }));
})();
`

// #4804: Claude Code reads CLAUDE_CODE_EXTRA_BODY from its environment as
// it builds each request, and nothing outside it can change that once it
// runs. tap.js puts the guard file's safeguards in each request to
// /v1/messages in their place, the body otherwise byte for byte Claude
// Code's, and sets the variable to them for the requests after; a request
// it has nothing to replace in goes as it was. Run with Bun, Claude
// Code's runtime, and Node, whichever are here.
func TestTapPutsTheGuardFilesSafeguards(t *testing.T) {
	ran := false
	for _, rt := range []string{"bun", "node"} {
		path, err := exec.LookPath(rt)
		if err != nil {
			continue
		}
		ran = true
		t.Run(rt, func(t *testing.T) {
			dir := t.TempDir()
			for name, js := range map[string]string{"stub.js": guardStub, "main.js": guardMain, "tap.js": string(tapJS)} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(js), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			guard := filepath.Join(dir, "guard.json")
			start := `{"safeguards":[{"c":"a"}]}`
			if err := os.WriteFile(guard, []byte(start), 0o600); err != nil {
				t.Fatal(err)
			}
			pre := "--preload"
			if rt == "node" {
				pre = "-r"
			}
			cmd := exec.Command(path, pre, filepath.Join(dir, "stub.js"), pre, filepath.Join(dir, "tap.js"), filepath.Join(dir, "main.js"), guard)
			cmd.Env = append(os.Environ(), tapEnv+"="+filepath.Join(dir, "tap.jsonl"), GuardEnv+"="+guard, "CLAUDE_CODE_EXTRA_BODY="+start)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			var got []map[string]any
			for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				var m map[string]any
				if json.Unmarshal([]byte(l), &m) != nil {
					t.Fatalf("not a line of the stub's: %q\n%s", l, out)
				}
				got = append(got, m)
			}
			body := func(sg string) string {
				return `{"model":"m","messages":[{"role":"user","content":"hi"}],"safeguards":` + sg + `,"stream":true}`
			}
			now := `[{"c":"b","x":"<&>","n":[1,2]}]`
			want := []any{body(`[{"c":"a"}]`), body(now), body(now), `{"model":"m","messages":[]}`, body(`[{"c":"a"}]`), nil}
			if len(got) != len(want)+1 {
				t.Fatalf("told %s", out)
			}
			for i, w := range want {
				if got[i]["body"] != w {
					t.Fatalf("request %d was sent\n%v\nwant\n%v", i, got[i]["body"], w)
				}
				if k, _ := json.Marshal(got[i]["keys"]); i < 5 && string(k) != `["signal","method","headers","body"]` {
					t.Fatalf("request %d's init is %s", i, k)
				}
			}
			end := got[len(got)-1]
			if end["env"] != `{"safeguards":[{"c":"b","x":"<&>","n":[1,2]}]}` || end["guardEnv"] != nil {
				t.Fatalf("Claude Code's environment after: %v", end)
			}
			if _, err := os.Stat(guard + ".live"); err != nil {
				t.Fatalf("the tap never said it puts them in: %v", err)
			}
		})
	}
	if !ran {
		t.Skip("neither Bun nor Node is here to run tap.js")
	}
}
