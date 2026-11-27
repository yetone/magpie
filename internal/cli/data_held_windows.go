package cli

import (
	"context"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// On Windows a folder can't be renamed while a process has a file in it
// open, so a move of magpie's folders asks what runs from them first
// (#7812).
func init() {
	appdir.HeldBy = func() ([]appdir.Proc, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ks, err := proc.Family(ctx)
		if err != nil {
			return nil, err
		}
		ps := make([]appdir.Proc, len(ks))
		for i, k := range ks {
			ps[i] = appdir.Proc{PID: k.PID, PPID: k.PPID, Args: k.Args}
		}
		return ps, nil
	}
}
