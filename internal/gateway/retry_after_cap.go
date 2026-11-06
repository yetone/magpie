package gateway

import (
	"net/http"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// retryCapped bounds the Retry-After an agent is told, when Settings'
// RetryAfterCap is set (#5371, tlerbao): a plan's quota running out rests
// its account inside magpie until the plan's reset, hours away, and that
// horizon went to the agent as its wait. An agent with its own fallback
// chain (OpenCode clamps one wait at 900s and asks the same model three
// times) then sat about 45 minutes before it moved on.
//
// It is the one place every answer leaves the gateway: magpie's own
// turn-aways, a vendor's Retry-After passed on (keepRetry) and the
// limits' 429s alike. What magpie reads to rest an account (the held
// try's header, restAfter) is read before here, so the account rests
// exactly as long either way. Unset, nothing changes.
func retryCapped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&retryCapWriter{ResponseWriter: w}, r)
	})
}

type retryCapWriter struct {
	http.ResponseWriter
	wrote bool
}

func (c *retryCapWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		if code >= 400 {
			capRetryAfter(c.ResponseWriter.Header(), time.Now())
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *retryCapWriter) Write(b []byte) (int, error) {
	c.wrote = true // an implicit 200
	return c.ResponseWriter.Write(b)
}

func (c *retryCapWriter) Flush() { _ = http.NewResponseController(c.ResponseWriter).Flush() }

// Unwrap lets http.ResponseController and the websocket's Accept reach
// the connection's own writer (deadlines, Hijack).
func (c *retryCapWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// capRetryAfter writes h's Retry-After, in seconds or as a date, down to
// RetryAfterCap when it says to wait longer. A shorter one, or none, is
// left as it is: the cap is never a floor.
func capRetryAfter(h http.Header, now time.Time) {
	v := h.Get("Retry-After")
	if v == "" {
		return
	}
	most := settings.Shared().RetryAfterCap
	if most == nil || *most <= 0 {
		return
	}
	var wait time.Duration
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		wait = time.Duration(n * float64(time.Second))
	} else if t, err := http.ParseTime(v); err == nil {
		wait = t.Sub(now)
	} else {
		return
	}
	if wait > time.Duration(*most)*time.Second {
		h.Set("Retry-After", strconv.Itoa(*most))
	}
}
