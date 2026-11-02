//go:build race

package server_test

// raceEnabled scales down the load tests, which the race detector slows down a lot.
const raceEnabled = true
