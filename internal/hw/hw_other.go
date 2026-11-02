//go:build !linux && !darwin && !windows

package hw

func Memory() Info   { return Info{} }
func perfCores() int { return 0 }
