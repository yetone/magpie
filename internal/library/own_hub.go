package library

import (
	"context"

	"github.com/yetone/magpie/internal/provider"
)

// A Remote magpie added with a key that may use all of it is the user's
// own hub (starballl on X: a magpie on a VM that the Mac and the Linux
// machine use, with MCP servers and skills set up once there). Both of
// its editor's Library switches, "Add its MCP servers to the Library" and
// "Use its skills and prompts", were off when it was added, so each
// machine was set up again by hand. Now they start on for such a key:
// one held to a budget, models, accounts or MCP servers, read-only,
// issued or expiring is a part of someone's magpie given to this one,
// and its switches still start off.

// AddedRemote turns on the Library switches of the Remote magpie id just
// added that the user left alone (mcp, shared), when every key it uses
// may use all of the magpie it reaches (provider.RemoteKeyFull) and that
// magpie is not this one. It says which it turned on; it turns nothing
// off, and changes no switch when the key can't be judged. Run it after
// the provider's first model list, which tells a magpie that is this one
// (SelfAnswered). With mcp on, the hub is asked for its router at once;
// with shared on, the caller reads its Library (UseRemoteLibrary).
func AddedRemote(ctx context.Context, id string, mcp, shared bool) (mcpOn, sharedOn bool) {
	if !mcp && !shared {
		return false, false
	}
	p, err := provider.Find(id)
	if err != nil || !p.IsRemoteMagpie() || p.Account != nil || p.Off || p.SelfAnswered() {
		return false, false
	}
	if !provider.RemoteKeyFull(ctx, *p) {
		return false, false
	}
	if mcp && !RemoteMCPOn()[id] {
		if _, err := SetRemoteMCP(id, true); err == nil {
			mcpOn = true
			provider.AskRemoteHub(ctx, id)
		}
	}
	if shared && !BorrowedOn()[id] {
		if _, err := SetBorrow(id, true); err == nil {
			sharedOn = true
		}
	}
	return mcpOn, sharedOn
}
