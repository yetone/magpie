//go:build race

package gateway

// raceOn says the tests run under -race, where sync.Pool lets a quarter of
// what is put in it go, so a test of what a pool saves is skipped.
const raceOn = true

// oldBodyLimit is 64 MB, the request body limit magpie had before #118,
// in the plain run. Under -race it is 20 MB: the race detector makes a
// scan of the 64 MB bodies take 25 to 30 times as long, and the five
// tests that send one took ~170s of the gateway's race run on an M5 Pro
// and pushed a macOS CI shard past its 20 minutes (#5361, #5320). 20 MB
// still passes every cut the request path makes below 64 MB (bodyKept's
// 4 MB, the 8 MB and 16 MB reads), so the race run takes the same
// branches; the plain Test step sends the full 64 MB, one byte either
// side of it too.
const oldBodyLimit = 20 << 20
