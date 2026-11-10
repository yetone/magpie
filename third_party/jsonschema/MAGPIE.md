# jsonschema v6.0.3, as magpie builds it

github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 (Apache-2.0, LICENSE here),
its non-test files as released, with one change: the drafts' metaschemas
are compiled the first time a schema is (`Compiler.Compile` calls
`ensureDrafts`), not in `init`. Upstream's init cost every magpie launch
~1.7 MB and ~21,000 allocations though most compile no schema.

Changed files: draft.go (init -> loadDrafts behind sync.Once),
compiler.go (Compile ensures the drafts; loadDrafts compiles through the
unexported compile). go.mod's replace points here.

To take a new upstream version: copy its non-test files over these and
make the same change again; upstream's own tests (`go test .` in the
module, GOWORK=off) pass on it.
