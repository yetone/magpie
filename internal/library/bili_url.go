package library

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// Zed has no plugin of bili's. Its README lists it with two routes: the
// launcher (bili zed), Linux only and leaving a provider on loopback
// direct, so magpie's gateway never passes through it; and, on every
// system, the /bili/ prefix its docs give for any client whose model
// address can be edited ("Adopting unlisted clients", docs/clients.md):
// language_models.openai_compatible.<provider>.api_url as
// http://127.0.0.1:8787/bili/<the address>, with bili's daemon (bili start)
// answering there. That is what magpie writes into Zed's magpie provider
// (agent.ZedThrough, which keeps it across picks and syncs and saves the
// key for it, Zed looking that up by the address). magpie keeps a bili
// start of its own running while such an agent is on (keepBiliServe, from
// KeepLinked), ended with magpie; one the user started is used as it is.

// biliOrigin is where bili start listens: 8787, the port bili keeps for
// it (its lanes take 18787 and up).
const biliOrigin = "http://127.0.0.1:8787"

// biliPrefix is what goes before the gateway's address.
const biliPrefix = biliOrigin + "/bili/"

// biliURLLane is an agent bili folds through its /bili/ prefix: whether
// its magpie provider goes through it, and putting it there (prefix
// biliPrefix) or back on the gateway ("").
type biliURLLane struct {
	on  func(a *agent.Agent) bool
	set func(a *agent.Agent, prefix string) error
}

// zedThrough is agent.ZedThrough; a var for tests, which save no key.
var zedThrough = agent.ZedThrough

// Hermes has a plugin of bili's (bili plugin install hermes), but it
// routes Hermes's traffic by setting HTTPS_PROXY in Hermes's process
// alone (hermes-plugin/__init__.py apply_env), and magpie's provider is at
// http://127.0.0.1, which that never takes: the plugin would add its
// tools while nothing folds. So Hermes goes through the /bili/ prefix as
// Zed does: agent.HermesThrough puts the base_url of magpie's entries, in
// every profile that has them, behind it, which a sync and a pick keep
// (Duckb7si on X).

var biliURLLanes = map[string]biliURLLane{
	"zed": {
		on:  func(a *agent.Agent) bool { return strings.HasPrefix(agent.ZedURL(a), biliPrefix) },
		set: func(a *agent.Agent, prefix string) error { return zedThrough(a, prefix) },
	},
	"hermes": {
		on:  func(a *agent.Agent) bool { return strings.HasPrefix(agent.HermesURL(a), biliPrefix) },
		set: agent.HermesThrough,
	},
	// dsh's own plugin for bili (dsh plugin add billion-context) rides proxy
	// settings of dsh's process, which magpie's http://127.0.0.1 route never
	// takes; its route's baseURL is editable, so it goes through the prefix
	// as Hermes does, in every profile (VantH_off on X).
	"dsh": {
		on:  func(a *agent.Agent) bool { return strings.HasPrefix(agent.DshURL(a), biliPrefix) },
		set: agent.DshThrough,
	},
}

// biliServing says bili start answers at biliOrigin: its health is
// {"ok":true}, which something else on the port doesn't say. A var for
// tests.
var biliServing = func() bool {
	c := &http.Client{Timeout: 2 * time.Second}
	res, err := c.Get(biliOrigin + "/__bili/health")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var h struct {
		OK bool `json:"ok"`
	}
	return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&h) == nil && h.OK
}

// biliServer is the bili start magpie runs: under ctx (KeepLinked's,
// which ends with magpie), stopped by cancel.
var biliServer struct {
	sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

func biliServeLog() string { return filepath.Join(settings.Dir(), "bili-start.log") }

// biliWait is how long a bili start just run has to answer.
var biliWait = 20 * time.Second

// biliServe has bili start answer at biliOrigin: one there already is
// used as it is, else magpie runs its own. A var for tests.
var biliServe = func(bin string) error {
	biliServer.Lock()
	defer biliServer.Unlock()
	if biliServing() {
		return nil
	}
	if biliServer.cancel != nil {
		// magpie's own, which stopped answering
		biliServer.cancel()
		biliServer.cancel = nil
	}
	parent := biliServer.ctx
	if parent == nil {
		parent = context.Background()
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return err
	}
	out, err := os.Create(biliServeLog())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	cmd := proc.CommandContext(ctx, bin, "start", "--port", "8787")
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		cancel()
		out.Close()
		return fmt.Errorf("bili start: %w", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		out.Close()
		close(done)
	}()
	biliServer.cancel = cancel
	for end := time.Now().Add(biliWait); time.Now().Before(end); {
		if biliServing() {
			return nil
		}
		select {
		case <-done:
			end = time.Time{}
		case <-time.After(250 * time.Millisecond):
		}
	}
	cancel()
	biliServer.cancel = nil
	said, _ := os.ReadFile(biliServeLog())
	why := lastLines(strings.TrimSpace(string(said)), 3)
	if why == "" {
		why = "nothing answered"
	}
	return fmt.Errorf("bili start didn't answer at %s: %s", biliOrigin, why)
}

// biliStop ends the bili start magpie runs, if it runs one. A var for
// tests.
var biliStop = func() {
	biliServer.Lock()
	defer biliServer.Unlock()
	if biliServer.cancel != nil {
		biliServer.cancel()
		biliServer.cancel = nil
	}
}

// biliURLOn is whether any agent here goes through bili's prefix.
func biliURLOn(agents []*agent.Agent) bool {
	for _, a := range biliAgents(agents) {
		if l, ok := biliURLLanes[a.ID]; ok && l.on(a) {
			return true
		}
	}
	return false
}

// setBiliURL puts an agent's magpie provider through bili's prefix, with
// bili start answering there first, or back on the gateway, and ends
// magpie's bili start when nothing goes through it any more.
func setBiliURL(a *agent.Agent, agents []*agent.Agent, bin string, on bool) (*BiliView, error) {
	b := newBackups()
	if err := b.keep(a.ID, a.Path); err != nil {
		return nil, err
	}
	l := biliURLLanes[a.ID]
	if on {
		if err := biliServe(bin); err != nil {
			return nil, err
		}
		if err := l.set(a, biliPrefix); err != nil {
			return nil, err
		}
	} else {
		if err := l.set(a, ""); err != nil {
			return nil, err
		}
		if !biliURLOn(agents) {
			biliStop()
		}
	}
	v := readBili(agents)
	v.Restart, v.Backup = []string{a.ID}, b.dir
	if b.dir != "" {
		pruneBackups()
	}
	return v, nil
}

// keepBiliServe, from KeepLinked, keeps bili start answering while an
// agent goes through its prefix: at magpie's start, and after it stopped.
// ctx is KeepLinked's, which ends magpie's bili start with magpie.
func keepBiliServe(ctx context.Context) {
	biliServer.Lock()
	if biliServer.ctx == nil {
		biliServer.ctx = ctx
	}
	biliServer.Unlock()
	if !biliURLOn(detected()) {
		return
	}
	bin := biliPath()
	if bin == "" || biliServing() {
		return
	}
	if err := biliServe(bin); err != nil {
		log.Println("library: billion-context:", err)
	}
}
