//go:build race

package ecmaregex

// raceEnabled says the tests run under the race detector, whose sync.Pool drops a share of what is put back on purpose,
// so a count of allocations means nothing there.
const raceEnabled = true
