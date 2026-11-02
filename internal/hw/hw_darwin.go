//go:build darwin

package hw

import (
	"os/exec"
	"strconv"
	"strings"
)

func sysctl(name string) uint64 {
	out, err := exec.Command("sysctl", "-n", name).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	return v
}

func Memory() Info {
	t := sysctl("hw.memsize")
	return Info{TotalRAM: t, AvailRAM: t / 2}
}

// perfCores — число «производительных» ядер Apple Silicon.
func perfCores() int { return int(sysctl("hw.perflevel0.physicalcpu")) }
