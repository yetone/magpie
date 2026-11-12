package provider

import "strings"

// WithScheme is a typed address with its scheme made certain: one that is
// there already, http or https in any letter case, stays (its scheme in
// lower case) and is never given a second; one with none gets def. A
// scheme saved twice ("http://http://host", from before this was
// checked) is one again. Other schemes are left as they are. The rest of
// the address, its path and trailing slash, is not touched.
func WithScheme(raw, def string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	scheme, rest := splitScheme(raw)
	if scheme == "" {
		return def + "://" + raw
	}
	// the repeats: "http://https://host" is host, at the last scheme said
	for {
		inner, more := splitScheme(rest)
		if inner != "http" && inner != "https" || scheme != "http" && scheme != "https" {
			break
		}
		scheme, rest = inner, more
	}
	return scheme + "://" + rest
}

// splitScheme cuts a leading "http://" or "https://" (any case) or another
// "scheme://" off s; scheme is lower case, "" when s has none.
func splitScheme(s string) (scheme, rest string) {
	i := strings.Index(s, "://")
	if i <= 0 {
		return "", s
	}
	for _, c := range s[:i] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			return "", s
		}
	}
	return strings.ToLower(s[:i]), s[i+3:]
}
