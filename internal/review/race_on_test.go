//go:build race

package review

// raceEnabled: the race detector slows code about tenfold, so time limits
// are scaled and the scaling test is skipped (CI runs it without -race).
const raceEnabled = true
