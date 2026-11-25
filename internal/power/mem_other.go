//go:build !windows && !linux && !darwin

package power

func detectTotalRAMMB() uint64 {
	return 0
}
