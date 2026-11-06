package sessions

import (
	"encoding/json"
	"strconv"
	"strings"
	"unsafe"

	"github.com/tidwall/gjson"
)

// ccUsageFields is a Claude Code message's usage object, as json.Unmarshal
// reads it.
type ccUsageFields struct {
	Input      int              `json:"input_tokens"`
	Output     int              `json:"output_tokens"`
	CacheRead  int              `json:"cache_read_input_tokens"`
	CacheWrite int              `json:"cache_creation_input_tokens"`
	Creation   *ccCacheCreation `json:"cache_creation"`
}

// ccDecodeUsage is json.Unmarshal(b, u) for a reply's usage object, read
// in place: json.Unmarshal made a decoder of its own for each reply's
// few numbers (160 MB of a first start's read of a real history). What
// it can't be sure of reading as json.Unmarshal does (a key spelt in
// another case, a number that isn't a plain integer, a value of another
// type, a repeated cache_creation) is left to json.Unmarshal.
func ccDecodeUsage(b []byte, u *ccUsageFields) error {
	if v, ok := ccUsageInPlace(b); ok {
		*u = v
		return nil
	}
	return json.Unmarshal(b, u)
}

func ccUsageInPlace(b []byte) (ccUsageFields, bool) {
	var u ccUsageFields
	if len(b) == 0 || b[0] != '{' || !gjson.ValidBytes(b) {
		return u, false
	}
	ok, creations := true, 0
	var creation ccCacheCreation
	gjson.Parse(unsafe.String(&b[0], len(b))).ForEach(func(k, v gjson.Result) bool {
		key := k.Raw
		if len(key) < 2 || strings.IndexByte(key, '\\') >= 0 {
			ok = false
			return false
		}
		key = key[1 : len(key)-1]
		var dst *int
		switch key {
		case "input_tokens":
			dst = &u.Input
		case "output_tokens":
			dst = &u.Output
		case "cache_read_input_tokens":
			dst = &u.CacheRead
		case "cache_creation_input_tokens":
			dst = &u.CacheWrite
		case "cache_creation":
			if creations++; creations > 1 {
				ok = false
				return false
			}
			switch v.Type {
			case gjson.Null:
				return true
			case gjson.JSON:
				if !v.IsObject() {
					ok = false
					return false
				}
			default:
				ok = false
				return false
			}
			v.ForEach(func(k, v gjson.Result) bool {
				key := k.Raw
				if len(key) < 2 || strings.IndexByte(key, '\\') >= 0 {
					ok = false
					return false
				}
				key = key[1 : len(key)-1]
				var dst *int
				switch key {
				case "ephemeral_5m_input_tokens":
					dst = &creation.Ephemeral5m
				case "ephemeral_1h_input_tokens":
					dst = &creation.Ephemeral1h
				default:
					if strings.EqualFold(key, "ephemeral_5m_input_tokens") || strings.EqualFold(key, "ephemeral_1h_input_tokens") {
						ok = false
					}
					return ok
				}
				ok = ccUsageInt(v, dst)
				return ok
			})
			if ok {
				u.Creation = &creation
			}
			return ok
		default:
			for _, known := range [...]string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "cache_creation"} {
				if strings.EqualFold(key, known) {
					ok = false
					return false
				}
			}
			return true
		}
		ok = ccUsageInt(v, dst)
		return ok
	})
	return u, ok
}

// ccUsageInt sets *dst to v when v is a plain integer, leaves it for null,
// and is false for anything else.
func ccUsageInt(v gjson.Result, dst *int) bool {
	switch v.Type {
	case gjson.Null:
		return true
	case gjson.Number:
		n, err := strconv.Atoi(v.Raw)
		if err != nil {
			return false
		}
		*dst = n
		return true
	}
	return false
}
