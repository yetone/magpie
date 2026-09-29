package qoder

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

// Fixed vectors use RFC 4648 base64 with the client's alphabet substituted,
// then its outer-third swap. The JSON vector is CLIProxyAPI's worker fixture.
func TestCodecKnownAnswers(t *testing.T) {
	for _, tt := range []struct{ hex, encoded, wire string }{
		{"", "", ""},
		{"66", "D&$$", "$&$D"},
		{"666f", "DOq$", "$OqD"},
		{"666f6f", "DOWb", "bOWD"},
		{"666f6f626172", "DOWb#OgY", "gYWb#ODO"},
		{"00ff3f7f", "_vq!ef$$", "$$q!ef_v"},
		{"e4b8ade69687", "QG*tQ)PZ", "PZ*tQ)QG"},
		{"7b226d65737361676573223a5b5d7d", "mYKtDxj^#SJLNYByS..W", "ByS..Wj^#SJLNYmYKtDx"},
	} {
		t.Run(tt.hex, func(t *testing.T) {
			plain, _ := hex.DecodeString(tt.hex)
			if got := BodyEncode(plain); got != tt.encoded {
				t.Fatalf("encode %q, want %q", got, tt.encoded)
			}
			if got := BodyDecode(tt.encoded); !bytes.Equal(got, plain) {
				t.Fatalf("decode %x, want %x", got, plain)
			}
			if got := EncodeRequestBody(plain); got != tt.wire {
				t.Fatalf("wire %q, want %q", got, tt.wire)
			}
			if got := DecodeRequestBody(tt.wire); !bytes.Equal(got, plain) {
				t.Fatalf("wire decode %x, want %x", got, plain)
			}
		})
	}
}

func TestCosySignatureKnownAnswer(t *testing.T) {
	// Independently calculated MD5 of the five newline-separated fields.
	const path = "/api/v2/service/pro/sse/agent_chat_generation"
	if got := cosySignature("cGF5bG9hZA==", "c2VjcmV0", 1700000000, "ByS..Wj^#SJLNYmYKtDx", path); got != "da9dbed272847ea4664dd7ae8bf82575" {
		t.Fatalf("signature = %q", got)
	}
	if got := urlPathname(APIHost + ChatPath); got != path {
		t.Fatalf("signed path %q", got)
	}
}

func TestCosyMachineIdentity(t *testing.T) {
	u := &User{UID: "uid", Token: "jt", MachineID: "fixed-machine"}
	for i := 0; i < 2; i++ {
		h, err := BuildCosyHeaders(APIHost+ChatPath, u, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"Cosy-MachineId", "Cosy-MachineToken", "Cosy-ClientIp"} {
			if h[key] != "fixed-machine" {
				t.Fatalf("%s = %q", key, h[key])
			}
		}
		arch := map[string]string{"amd64": "x86_64", "386": "x86", "arm64": "aarch64"}[runtime.GOARCH]
		if arch == "" {
			arch = runtime.GOARCH
		}
		os := runtime.GOOS
		if os == "windows" {
			os = "win32"
		}
		if h["Cosy-MachineOS"] != arch+"_"+os {
			t.Fatalf("OS %q", h["Cosy-MachineOS"])
		}
	}
}

// TestCodecRoundTrip checks the fiddliest reverse-engineered piece: a request
// body survives encode-to-wire then decode-from-wire unchanged, over every
// length so the partial final 6-bit group, the "$" padding and the
// outer-thirds swap are all exercised.
func TestCodecRoundTrip(t *testing.T) {
	for n := 0; n < 200; n++ {
		plain := make([]byte, n)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		wire := EncodeRequestBody(plain)
		if got := DecodeRequestBody(wire); !bytes.Equal(got, plain) {
			t.Fatalf("round trip failed at len %d", n)
		}
		if len(wire)%4 != 0 {
			t.Fatalf("wire length %d not a multiple of 4", len(wire))
		}
	}
}

// TestCosyHeadersStructure checks the request envelope is well formed: the
// Authorization is "Bearer COSY.<payload>.<md5 hex>", the payload base64
// decodes to the documented JSON, and the token is never in the clear.
func TestCosyHeadersStructure(t *testing.T) {
	u := &User{UID: "uid-1", Name: "N", Email: "e@x", Token: "jt-abc", MachineID: "machine-test"}
	ts := int64(1700000000)
	h, err := BuildCosyHeaders(APIHost+ChatPath, u, `{"messages":[]}`, ts)
	if err != nil {
		t.Fatal(err)
	}
	auth := h["Authorization"]
	if !strings.HasPrefix(auth, "Bearer COSY.") {
		t.Fatalf("bad Authorization: %q", auth)
	}
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer COSY."), ".")
	if len(parts) != 2 || len(parts[1]) != 32 {
		t.Fatalf("expected payload.<md5hex>, got %q", auth)
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("payload not base64: %v", err)
	}
	var p map[string]any
	if json.Unmarshal(decoded, &p) != nil {
		t.Fatalf("payload not JSON: %q", decoded)
	}
	if p["version"] != "v1" || p["cosyVersion"] != "1.1.49" {
		t.Fatalf("unexpected payload fields: %v", p)
	}
	if strings.Contains(string(decoded), "jt-abc") {
		t.Fatal("token leaked into the payload")
	}
	if h["Cosy-User"] != "uid-1" {
		t.Fatalf("Cosy-User wrong: %q", h["Cosy-User"])
	}
	if h["Cosy-Date"] != "1700000000" {
		t.Fatalf("Cosy-Date wrong: %q", h["Cosy-Date"])
	}
}
