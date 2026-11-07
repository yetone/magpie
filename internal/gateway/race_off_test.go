//go:build !race

package gateway

const raceOn = false

// oldBodyLimit is the request body limit magpie had before #118: a body
// past it is read whole (race_on_test.go says why -race sends less).
const oldBodyLimit = 64 << 20
