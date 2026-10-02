package provider

// PLUGIN-SERVED (see AGENTS.md): Devin ("devin") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-devin-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/devin) and raise the
// mover's min in internal/provider/migrate_side.go.

// Which of Devin's models take images, as Devin tells its CLI: `devin models
// list` leaves it out, and models.dev doesn't know Devin's own ids (swe-2,
// gpt-6-1-sol), so agents were told a model that takes images doesn't and
// dropped them (#417). The CLI's picker reads GetCliModelConfigs, whose
// ClientModelConfig gives each model's supports_images.

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"runtime"
	"time"
)

var devinImagesClient = &http.Client{Timeout: 15 * time.Second}

const devinModelConfigsRPC = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"

// devinImagesAt is whether each of Devin's model ids takes images, for the
// account signed in in home ("" for the CLI's own); nil when Devin can't be
// asked. A var so tests can stand in for it.
var devinImagesAt = func(ctx context.Context, home string) map[string]bool {
	key, server, err := DevinAuthAt(home)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// the metadata the gateway's chats send (gateway/devin.go), which the
	// CLI's own requests carry: with the CLI's ide and extension names
	// alone Devin lists one model
	meta := devinPB{}.str(1, "devin-cli").str(2, devinUsageVersion).str(3, key).str(4, "en").
		str(5, runtime.GOOS).str(7, devinUsageVersion).str(12, "chisel").str(28, "chisel")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+devinModelConfigsRPC, bytes.NewReader(devinPB{}.bytes(1, meta)))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	res, err := devinImagesClient.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil
	}
	return parseDevinModelConfigs(b)
}

// parseDevinModelConfigs reads GetCliModelConfigsResponse: its
// client_model_configs (1), each a ClientModelConfig with its model_uid (22)
// and supports_images (5), a bool proto3 leaves out when false — as Devin
// leaves it out for GLM, DeepSeek, Inkling and Nemotron.
func parseDevinModelConfigs(b []byte) map[string]bool {
	out := map[string]bool{}
	for _, f := range devinFields(b) {
		if f.num != 1 || f.wire != 2 {
			continue
		}
		uid, images := "", false
		for _, g := range devinFields(f.data) {
			switch {
			case g.num == 22 && g.wire == 2:
				uid = string(g.data)
			case g.num == 5 && g.wire == 0:
				images = g.n != 0
			}
		}
		if uid != "" {
			out[uid] = images
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// withDevinImages is families with what Devin said of each variant's images
// on it; a variant Devin didn't name is left unsaid.
func withDevinImages(families []DevinFamily, images map[string]bool) []DevinFamily {
	if len(images) == 0 {
		return families
	}
	for i := range families {
		for j, m := range families[i].Models {
			if v, ok := images[m.ID]; ok {
				families[i].Models[j].ImageInput = &v
			}
		}
	}
	return families
}

// images is whether the family takes images, as its variants Devin named
// all say; nil when they say different things or none was named.
func (f DevinFamily) images() *bool {
	var said *bool
	for _, m := range f.Models {
		if m.ImageInput == nil {
			continue
		}
		if said != nil && *said != *m.ImageInput {
			return nil
		}
		v := *m.ImageInput
		said = &v
	}
	return said
}

// devinPB is a protobuf message being written.
type devinPB []byte

func (b devinPB) bytes(num int, v []byte) devinPB {
	b = binary.AppendUvarint(b, uint64(num<<3|2))
	return append(binary.AppendUvarint(b, uint64(len(v))), v...)
}

func (b devinPB) str(num int, v string) devinPB { return b.bytes(num, []byte(v)) }

type devinField struct {
	num, wire int
	n         uint64
	data      []byte
}

// devinFields reads a message's fields; a malformed tail is dropped.
func devinFields(b []byte) []devinField {
	var out []devinField
	for len(b) > 0 {
		key, k := binary.Uvarint(b)
		if k <= 0 {
			break
		}
		b = b[k:]
		f := devinField{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, k := binary.Uvarint(b)
			if k <= 0 {
				return out
			}
			f.n, b = v, b[k:]
		case 1:
			if len(b) < 8 {
				return out
			}
			b = b[8:]
		case 2:
			l, k := binary.Uvarint(b)
			if k <= 0 || uint64(len(b)-k) < l {
				return out
			}
			f.data, b = b[k:k+int(l)], b[k+int(l):]
		case 5:
			if len(b) < 4 {
				return out
			}
			b = b[4:]
		default:
			return out
		}
		out = append(out, f)
	}
	return out
}
