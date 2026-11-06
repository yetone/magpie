package claudecode

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// When KeepCurrent first looks, and how often after that; vars for tests.
var (
	keepFirst = time.Minute
	keepEvery = 6 * time.Hour
)

// keep is what KeepCurrent reads of the machine; tests stand in for it.
var keep = struct {
	own    func() string
	latest func(context.Context) (Release, error)
}{
	own:    func() string { return proc.FindTool("claude") },
	latest: Latest,
}

// KeepCurrent keeps the Claude Code magpie downloaded at Claude Code's
// newest release while magpie serves: a minute after it starts and every six
// hours after. Every run magpie starts sets DISABLE_AUTOUPDATER, so nothing
// else updates it, and a container's download (in /config/cache, on the
// volume) would otherwise stay at the version first installed, image
// update after image update (alextjy on Discord). It does nothing when
// magpie downloaded none, when the machine has a Claude Code of its own
// (that one is run, and updates itself), or when DISABLE_AUTOUPDATER is set
// for magpie: Claude Code's own way of holding a version.
func KeepCurrent(ctx context.Context) {
	t := time.NewTimer(keepFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if said, err := keepOnce(ctx); err != nil {
			log.Printf("Claude Code: keeping magpie's download current: %v", err)
		} else if said != "" {
			log.Print(said)
		}
		t.Reset(keepEvery)
	}
}

// keepOnce updates magpie's download when a newer release is out, and says
// what it did ("" for nothing). The version an earlier look replaced goes
// first: no run has been started on it since then.
func keepOnce(ctx context.Context) (string, error) {
	path, v := Downloaded()
	if path == "" || keep.own() != "" || os.Getenv("DISABLE_AUTOUPDATER") != "" {
		return "", nil
	}
	prune(v)
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	r, err := keep.latest(lctx)
	cancel()
	if err != nil {
		return "", err
	}
	if !newer(r.Version, v) {
		return "", nil
	}
	// no bound on the whole: a slow download still moving finishes, and
	// one that stops sending is ended by downloadStall (#5572). The old
	// version stays until the next look: a run being started now may have
	// been handed its path
	if _, err := install(ctx, r, nil); err != nil {
		return "", fmt.Errorf("Claude Code %s: %w", r.Version, err)
	}
	return fmt.Sprintf("Claude Code: magpie's download updated from %s to %s", v, r.Version), nil
}
