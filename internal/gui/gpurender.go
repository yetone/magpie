package gui

// Settings › General › Advanced › GPU rendering, Linux's app alone: how
// webkit_linux.go set WebKitGTK to draw at this run's start, and what it
// would pick with the setting left alone.

// webkitRunInfo is what this run decided.
type webkitRunInfo struct {
	Auto bool // what magpie picks here on its own
	GPU  bool // whether this run composites on the GPU
	// User are the variables the user set themselves, NAME=VALUE, which
	// take the choice over
	User []string
}

// webkitRun is set by webkitDefaults as the app starts; nil in magpie
// web and on the other systems, which have no row.
var webkitRun *webkitRunInfo

// gpuJSON is the row's facts: Auto, the default; Now, this run's (the
// setting takes effect at the next start); Env, the user's variables.
type gpuJSON struct {
	Auto bool     `json:"auto"`
	Now  bool     `json:"now"`
	Env  []string `json:"env,omitempty"`
}

func gpuRow() *gpuJSON {
	if webkitRun == nil {
		return nil
	}
	return &gpuJSON{Auto: webkitRun.Auto, Now: webkitRun.GPU, Env: webkitRun.User}
}
