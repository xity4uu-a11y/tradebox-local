//go:build darwin

package power

import (
	"os/exec"
	"strconv"
	"strings"
)

func detectTotalRAMMB() uint64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	bytesVal, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return bytesVal / (1024 * 1024)
}
