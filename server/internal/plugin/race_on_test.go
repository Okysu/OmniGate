//go:build race

package plugin

// raceDetector reports a -race build: the race detector slows the JavaScript
// runtime (and the host's JSON conversion) several times over, so timing
// budgets are relaxed in tests.
const raceDetector = true
