//go:build windows

package socketpath

import "os"

func fileOwnerUID(_ os.FileInfo) (int, error) {
	return currentUID(), nil
}

func currentUID() int {
	return 0
}
