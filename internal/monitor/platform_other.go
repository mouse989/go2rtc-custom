//go:build !windows && !linux

package monitor

// Stub implementations for non-Windows builds.
// Returns zero values — the monitor page simply shows 0 for system metrics.

func initPlatform()                     {}
func sampleCPU() float64                { return 0 }
func sampleMemory() (uint64, uint64)    { return 0, 0 }
func sampleDisks() []DiskInfo           { return nil }
func sampleUptime() uint64              { return 0 }
func sampleNetwork() (uint64, uint64)   { return 0, 0 }
