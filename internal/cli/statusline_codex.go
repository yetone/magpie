package cli

// magpie statusline --codex: Codex's status line, magpie's (lyj1115 on X:
// the current account's quota, or the relay's balance, under Codex's chat
// box). Codex (0.171.0-alpha.12 on, behind [features]
// tui_extensions_statusline) draws its footer from a stdio MCP server
// whose tool advertises a statusline entrypoint
// (_meta["openai/ui"].entrypoints, its bundled create-statusline skill):
// it reads that resource about once a second, with the thread's state in
// params._meta["com.openai/codex-statusline-state"], and draws the UI tree
// the read returns; null clears the footer, and an error or a read past
// its one second stops the reads until the provider is picked again. This
// is that server, newline-delimited JSON-RPC on stdin and stdout. Each
// read asks the gateway on this computer what it already holds: the
// thread's latest route (Codex sends its thread id as session_id) and the
// accounts as last read (/v1/magpie/quotas?kept=1), never a vendor. The
// Agents page writes it into config.toml (internal/agent/codex_statusline.go).
//
// Its tool is listed to the model like any MCP tool, so its name and
// words name neither magpie nor a vendor.

import (
	"bufio"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	// CodexStatusServer and CodexStatusTool are the server's name in
	// config.toml's [mcp_servers] and its tool's, as status_line_provider
	// names them
	CodexStatusServer = "quota_line"
	CodexStatusTool   = "statusline"
	codexStatusURI    = "ui://quota-line/footer"
	// codexStatusState is where Codex puts the thread's state in a read
	codexStatusState = "com.openai/codex-statusline-state"
)

// codexStatusWait bounds each ask of the gateway: Codex gives a read one
// second in all, and one past it stops the footer.
const codexStatusWait = 400 * time.Millisecond

// codexStatusKeep is how long the accounts read are kept between reads:
// Codex reads again a second after each one.
const codexStatusKeep = 5 * time.Second

// codexStatus answers Codex's reads from the gateway at base.
type codexStatus struct {
	base   string
	client *http.Client
	now    func() time.Time

	mu       sync.Mutex
	quotas   []provider.Quota
	quotasAt time.Time
}

func newCodexStatus(base string) *codexStatus {
	return &codexStatus{base: base, client: &http.Client{Timeout: codexStatusWait}, now: time.Now}
}

// serve answers requests from in on out until in ends.
func (c *codexStatus) serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": nil}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			if strings.TrimSpace(sc.Text()) == "" {
				continue
			}
			reply["error"] = map[string]any{"code": -32700, "message": "Parse error"}
		} else if len(req.ID) == 0 || string(req.ID) == "null" {
			continue // a notification
		} else {
			reply["id"] = req.ID
			if res, ok := c.respond(req.Method, req.Params); ok {
				reply["result"] = res
			} else {
				reply["error"] = map[string]any{"code": -32601, "message": "Method not found"}
			}
		}
		if err := enc.Encode(reply); err != nil {
			return err
		}
	}
	return sc.Err()
}

var codexStatusToolDef = map[string]any{
	"name":        CodexStatusTool,
	"title":       "Quota",
	"description": "The remaining quota or balance of the account this thread uses, as shown in the footer.",
	"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	"_meta": map[string]any{"openai/ui": map[string]any{
		"entrypoints": []any{map[string]any{"type": "statusline", "resourceUri": codexStatusURI}},
	}},
}

func (c *codexStatus) respond(method string, params json.RawMessage) (any, bool) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]any{"name": CodexStatusServer, "version": "1"},
		}, true
	case "ping":
		return map[string]any{}, true
	case "tools/list":
		return map[string]any{"tools": []any{codexStatusToolDef}}, true
	case "tools/call":
		// the model may call it: it is told the line the footer shows
		var p struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		}
		_ = json.Unmarshal(params, &p)
		line := strings.Join(c.cells(threadOf(p.Meta)), " · ")
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": cmp.Or(line, "No quota is known.")}}}, true
	case "resources/list":
		return map[string]any{"resources": []any{map[string]any{"name": "footer", "uri": codexStatusURI, "mimeType": "application/json"}}}, true
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, true
	case "resources/read":
		var p struct {
			URI  string                     `json:"uri"`
			Meta map[string]json.RawMessage `json:"_meta"`
		}
		_ = json.Unmarshal(params, &p)
		tree, _ := json.Marshal(codexStatusTree(c.cells(threadOf(p.Meta))))
		return map[string]any{"contents": []any{map[string]any{"uri": codexStatusURI, "mimeType": "application/json", "text": string(tree)}}}, true
	}
	return nil, false
}

// threadOf is the thread id in a read's state, "" when there is none.
func threadOf(meta map[string]json.RawMessage) string {
	var st struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(meta[codexStatusState], &st)
	return strings.TrimSpace(st.Thread.ID)
}

// codexStatusTree is the footer: the model, then the account, or null
// (nothing shown) when the gateway told nothing.
func codexStatusTree(cells []string) any {
	if len(cells) == 0 {
		return nil
	}
	var kids []any
	for i, s := range cells {
		color := "status-limit"
		if i == 0 && strings.HasPrefix(s, "→ ") {
			color = "status-model"
		}
		kids = append(kids, map[string]any{"type": "text", "props": map[string]any{"color": color}, "children": []any{s}})
	}
	return map[string]any{"type": "row", "props": map[string]any{"gap": 2, "wrap": "wrap"}, "children": kids}
}

// cells are the footer's parts for thread: "→ provider/model" when the
// gateway has its route, then the account it went to.
func (c *codexStatus) cells(thread string) []string {
	var wg sync.WaitGroup
	model := ""
	if thread != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var route struct {
				Route *struct {
					Asked string `json:"asked"`
					Model string `json:"model"`
				} `json:"route"`
			}
			if getJSON(c.client, c.base+"/v1/magpie/route?session="+url.QueryEscape(thread), &route) && route.Route != nil {
				model = cmp.Or(route.Route.Model, route.Route.Asked)
			}
		}()
	}
	qs, ok := c.accounts()
	wg.Wait()
	if !ok {
		return nil
	}
	var out []string
	if model != "" {
		out = append(out, "→ "+model)
	}
	now := c.now()
	for _, q := range provider.CodexStatuslineCards(model, qs) {
		out = append(out, provider.CodexStatuslineCard(q, now))
	}
	return out
}

// accounts are the gateway's kept quotas, read again once codexStatusKeep
// has gone by; ok is false when the gateway can't be asked.
func (c *codexStatus) accounts() ([]provider.Quota, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.quotasAt.IsZero() && c.now().Sub(c.quotasAt) < codexStatusKeep {
		return c.quotas, true
	}
	var quotas struct {
		Data []provider.Quota `json:"data"`
	}
	if !getJSON(c.client, c.base+"/v1/magpie/quotas?kept=1", &quotas) {
		c.quotasAt = time.Time{}
		return nil, false
	}
	c.quotas, c.quotasAt = quotas.Data, c.now()
	return c.quotas, true
}
