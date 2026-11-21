package plugin

import (
	"context"
	"fmt"
	"slices"
)

// Withdrawal is an official package magpie gives to nobody new: a user
// who has it keeps it, updated as before, and is told the risk and what
// to use instead; everyone else is turned away at Add.
type Withdrawal struct {
	// ID and Name are the provider the package serves
	ID   string `json:"id"`
	Name string `json:"name"`
	// Instead is the provider to use in its place, InsteadID its id, and
	// Package its plugin
	Instead   string `json:"instead"`
	InsteadID string `json:"insteadId"`
	Package   string `json:"package"`
	// Spec and Off are this magpie's entry of the withdrawn package, set
	// by WithdrawnKept: the page turns it off by its spec
	Spec string `json:"spec,omitempty"`
	Off  bool   `json:"off,omitempty"`
}

// withdrawn, by package name.
var withdrawn = map[string]Withdrawal{
	// Google suspends accounts it sees calling Code Assist through
	// Antigravity's own client, as this plugin does (the owner, 11-15:
	// 正在使用的继续正常，加个提示，新用户不让下载)
	"@magpie-community/opencode-antigravity-auth": {ID: "antigravity", Name: "Antigravity", Instead: "Antigravity (local agy CLI)", InsteadID: "agy-cli", Package: "@magpie-community/opencode-agy-cli-auth"},
}

// WithdrawnOf is the withdrawal of the package name, if it is withdrawn.
func WithdrawnOf(name string) (Withdrawal, bool) {
	w, ok := withdrawn[name]
	return w, ok
}

// WithdrawnKept are the withdrawn packages this magpie still has listed,
// on or off: their users, who are told the risk.
func WithdrawnKept() []Withdrawal {
	var out []Withdrawal
	for _, e := range Load().Plugins {
		w, ok := withdrawn[PackageName(e.Spec)]
		if !ok || slices.ContainsFunc(out, func(o Withdrawal) bool { return o.ID == w.ID }) {
			continue
		}
		w.Spec, w.Off = e.Spec, e.Off
		out = append(out, w)
	}
	return out
}

type keepWithdrawnKey struct{}

// KeepWithdrawn lets an Add under ctx install a withdrawn package: the
// move of the user's own accounts onto it, or keeping a moved user's
// plugin current.
func KeepWithdrawn(ctx context.Context) context.Context {
	return context.WithValue(ctx, keepWithdrawnKey{}, true)
}

// RefuseWithdrawn turns away an add of a withdrawn package that this
// magpie doesn't have listed already.
func RefuseWithdrawn(ctx context.Context, spec string) error {
	if IsPath(spec) || ctx.Value(keepWithdrawnKey{}) != nil {
		return nil
	}
	name := PackageName(spec)
	w, ok := withdrawn[name]
	if !ok {
		return nil
	}
	if slices.ContainsFunc(Load().Plugins, func(e Entry) bool { return PackageName(e.Spec) == name }) {
		return nil
	}
	return fmt.Errorf("%s is no longer installed: its vendor suspends accounts it sees used this way. Use %s instead: magpie plugin add %s", name, w.Instead, w.Package)
}
