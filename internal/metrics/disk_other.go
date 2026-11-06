//go:build !linux

package metrics

import "fmt"

func readRootDisk() (Disk, error) {
	return Disk{}, fmt.Errorf("disk metrics require Linux")
}
