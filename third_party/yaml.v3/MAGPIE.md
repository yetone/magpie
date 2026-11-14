# yaml.v3 v3.0.1, as magpie builds it

gopkg.in/yaml.v3 v3.0.1 (MIT and Apache-2.0, LICENSE and NOTICE here),
its non-test files as released (gofmt run over them), with one change: the emitter's event
queue starts again once every event in it is written
(`yaml_emitter_emit` in emitterc.go). Upstream only appends to it, so an
encode held every event of the document until it ended: ~24 MB of
allocations when magpie writes dsh's model routes, on the first start of
each version and on every catalog change. go.yaml.in/yaml/v3 v3.0.5 has
the same queue.

go.mod here names the module only: upstream's requires gopkg.in/check.v1
for its tests, which are not copied. go.mod's replace points here.

To take a new upstream version: copy its non-test files over these and
make the same change again; upstream's own tests (`go test .` in its
module) pass on it.
