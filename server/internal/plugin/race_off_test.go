//go:build !race

package plugin

// raceDetector reports a -race build (see race_on_test.go).
const raceDetector = false
