package metrics

import (
	"fmt"
	"syscall"
)

func readRootDisk() (Disk, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return Disk{}, err
	}
	blockSize := stat.Frsize
	if blockSize <= 0 {
		blockSize = stat.Bsize
	}
	if blockSize <= 0 {
		return Disk{}, fmt.Errorf("invalid filesystem block size")
	}
	return diskUsage(uint64(blockSize), stat.Blocks, stat.Bfree, stat.Bavail)
}

func diskUsage(blockSize, total, free, available uint64) (Disk, error) {
	if blockSize == 0 || total == 0 || total > maxSafeInteger/blockSize ||
		free > total || available > free {
		return Disk{}, fmt.Errorf("invalid filesystem block counts")
	}
	return Disk{
		TotalBytes:     total * blockSize,
		UsedBytes:      (total - free) * blockSize,
		AvailableBytes: available * blockSize,
	}, nil
}
