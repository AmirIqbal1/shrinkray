package dashboard

import (
	"errors"
	"fmt"
	"math"
	"syscall"
)

const (
	EncoderWorkingAllowanceBytes uint64 = 512 * 1024 * 1024
	MinimumSafetyReserveBytes    uint64 = 2 * 1024 * 1024 * 1024
	CriticalFreeSpaceBytes       uint64 = 512 * 1024 * 1024
)

var (
	ErrInsufficientDiskSpace = errors.New("insufficient disk space")
	ErrCriticalDiskSpace     = errors.New("critical low disk space")
)

type DiskSpaceStatus struct {
	AvailableBytes uint64
	CapacityBytes  uint64
	RequiredBytes  uint64
	ReserveBytes   uint64
	Sufficient     bool
	Warning        bool
}

type DiskSpaceUpdate struct {
	AvailableBytes uint64
	RequiredBytes  uint64
	ReserveBytes   uint64
	Warning        bool
}

type DiskSpaceChecker interface {
	Check(string, uint64) (DiskSpaceStatus, error)
}

type FilesystemDiskSpaceChecker struct{}

func (FilesystemDiskSpaceChecker) Check(directory string, targetBytes uint64) (DiskSpaceStatus, error) {
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(directory, &filesystem); err != nil {
		return DiskSpaceStatus{}, fmt.Errorf("inspect destination filesystem: %w", err)
	}
	blockSize := uint64(filesystem.Bsize)
	available := saturatingMultiply(filesystem.Bavail, blockSize)
	capacity := saturatingMultiply(filesystem.Blocks, blockSize)
	return CalculateDiskSpace(targetBytes, available, capacity), nil
}

func CalculateDiskSpace(targetBytes, availableBytes, capacityBytes uint64) DiskSpaceStatus {
	reserve := capacityBytes / 10
	if reserve < MinimumSafetyReserveBytes {
		reserve = MinimumSafetyReserveBytes
	}
	required := saturatingAdd(targetBytes, EncoderWorkingAllowanceBytes, reserve)
	warningThreshold := saturatingAdd(required, CriticalFreeSpaceBytes)
	return DiskSpaceStatus{
		AvailableBytes: availableBytes,
		CapacityBytes:  capacityBytes,
		RequiredBytes:  required,
		ReserveBytes:   reserve,
		Sufficient:     availableBytes >= required,
		Warning:        availableBytes < warningThreshold,
	}
}

func TargetBytesFromMB(megabytes int64) uint64 {
	if megabytes <= 0 {
		return 0
	}
	return saturatingMultiply(uint64(megabytes), 1024*1024)
}

func saturatingAdd(values ...uint64) uint64 {
	var total uint64
	for _, value := range values {
		if math.MaxUint64-total < value {
			return math.MaxUint64
		}
		total += value
	}
	return total
}

func saturatingMultiply(left, right uint64) uint64 {
	if left == 0 || right == 0 {
		return 0
	}
	if left > math.MaxUint64/right {
		return math.MaxUint64
	}
	return left * right
}

func FormatBytes(bytes uint64) string {
	const unit = uint64(1024)
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	value := float64(bytes)
	index := -1
	for value >= 1024 && index < len(units)-1 {
		value /= 1024
		index++
	}
	if value >= 100 || index < 1 {
		return fmt.Sprintf("%.0f %s", value, units[index])
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

func diskSpaceUpdate(status DiskSpaceStatus) DiskSpaceUpdate {
	return DiskSpaceUpdate{
		AvailableBytes: status.AvailableBytes,
		RequiredBytes:  status.RequiredBytes,
		ReserveBytes:   status.ReserveBytes,
		Warning:        status.Warning,
	}
}
