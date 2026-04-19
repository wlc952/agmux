//go:build !windows

package socketpath

import (
	"fmt"
	"os"
	"syscall"
)

func fileOwnerUID(info os.FileInfo) (int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unsupported file stat type")
	}
	return int(stat.Uid), nil
}

func currentUID() int {
	return os.Geteuid()
}
