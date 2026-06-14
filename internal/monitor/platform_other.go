//go:build !windows

package monitor

// Stub implementations for non-Windows builds.
// Returns zero values — the monitor page simply shows 0 for system metrics.

func initPlatform()                     {}
func sampleCPU() float64                { return 0 }
func sampleMemory() (uint64, uint64)    { return 0, 0 }
func sampleDisk() (uint64, uint64)      { return 0, 0 }
func sampleUptime() uint64              { return 0 }
func sampleNetwork() (uint64, uint64)   { return 0, 0 }
