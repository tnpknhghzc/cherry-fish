//go:build linux

package hw

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func Memory() Info {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Info{}
	}
	defer f.Close()
	var in Info
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fs[1], 10, 64)
		switch fs[0] {
		case "MemTotal:":
			in.TotalRAM = v * 1024
		case "MemAvailable:":
			in.AvailRAM = v * 1024
		}
	}
	return in
}

func perfCores() int { return 0 }
