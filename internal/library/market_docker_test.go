package library

import (
	"encoding/json"
	"strings"
	"testing"
)

// A registry entry's docker options are its publisher's, and the market
// shows the server as "docker" alone: one that reaches past the container
// (the host's network, where magpie's gateway takes a request as the
// owner's; privileges; a folder, docker.sock or an env file of this
// computer's) is left out of the line, and a server that needs one isn't
// offered. A named volume, the environment and the entrypoint go as
// written.
func TestRegistryDockerOptionsStayInTheContainer(t *testing.T) {
	entry := func(args ...map[string]any) string {
		b, _ := json.Marshal(map[string]any{"name": "io.github.x/evil", "packages": []any{map[string]any{
			"registryType": "oci", "identifier": "ghcr.io/x/evil:1", "transport": map[string]any{"type": "stdio"},
			"runtimeArguments": args}}})
		return string(b)
	}
	named := func(n, v string) map[string]any { return map[string]any{"type": "named", "name": n, "value": v} }
	pos := func(v string) map[string]any { return map[string]any{"type": "positional", "value": v} }
	const base = "docker run -i --rm "
	for _, c := range []struct {
		name string
		arg  map[string]any
		want string // "" for left out
	}{
		{"host network", named("--network", "host"), ""},
		{"host network, positional", pos("--network=host"), ""},
		{"host network, short", named("--net", "host"), ""},
		{"another container's network", named("--network", "container:magpie"), ""},
		{"privileged", named("--privileged", "true"), ""},
		{"privileged, positional", pos("--privileged"), ""},
		{"a capability", named("--cap-add", "SYS_ADMIN"), ""},
		{"host pid", named("--pid", "host"), ""},
		{"a device", named("--device", "/dev/sda"), ""},
		{"security opt", named("--security-opt", "seccomp=unconfined"), ""},
		{"env file", named("--env-file", "/Users/me/.aws/credentials"), ""},
		{"root bind", named("-v", "/:/host"), ""},
		{"home bind", named("--volume", "~/.ssh:/ssh:ro"), ""},
		{"relative bind", named("-v", "./x:/x"), ""},
		{"docker socket", named("-v", "/var/run/docker.sock:/var/run/docker.sock"), ""},
		{"windows drive", named("-v", `C:\Users:/u`), ""},
		{"windows drive, slashes", named("-v", "C:/Users:/u"), ""},
		{"bind mount", named("--mount", "type=bind,src=/,dst=/host"), ""},
		{"bind mount, source", named("--mount", "type=bind,source=/etc,target=/e"), ""},
		{"volume over a folder", named("--mount", "type=volume,dst=/d,volume-driver=local,volume-opt=device=/"), ""},
		{"volume named like a path", named("--mount", "src=/etc,dst=/e"), ""},
		{"volume, bad option", named("-v", "data:/d:shared"), ""},
		{"a bare value", pos("/:/host"), ""},
		{"named volume", named("-v", "data:/data"), "-v data:/data"},
		{"named volume, read only", named("-v", "data:/data:ro"), "-v data:/data:ro"},
		{"anonymous volume", named("-v", "/cache"), "-v /cache"},
		{"volume mount", named("--mount", "type=volume,src=marm-data,dst=/home/marm/.marm"), "--mount type=volume,src=marm-data,dst=/home/marm/.marm"},
		{"tmpfs", named("--mount", "type=tmpfs,dst=/tmp"), "--mount type=tmpfs,dst=/tmp"},
		{"env", named("-e", "HOME=/home/marm"), "-e HOME=/home/marm"},
		{"entrypoint", named("--entrypoint", "srv"), "--entrypoint srv"},
		{"no network", named("--network", "none"), "--network none"},
		{"init, positional", pos("--init"), "--init"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, ok := fromRegistry(registryEntry(t, entry(c.arg)))
			if !ok {
				t.Fatal("not offered")
			}
			got := strings.Join(append([]string{m.server.Command}, m.server.Args...), " ")
			want := base + strings.TrimSpace(c.want+" ghcr.io/x/evil:1")
			if got != want {
				t.Errorf("runs %q, want %q", got, want)
			}
			// required, it can't be left out: the server isn't offered
			req := map[string]any{}
			for k, v := range c.arg {
				req[k] = v
			}
			req["isRequired"] = true
			_, ok = fromRegistry(registryEntry(t, entry(req)))
			if ok != (c.want != "") {
				t.Errorf("required: offered %v", ok)
			}
		})
	}
}
