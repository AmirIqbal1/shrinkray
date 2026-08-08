package dashboard

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func gibibytes(value uint64) uint64 { return value * 1024 * 1024 * 1024 }

func TestDiskSpaceEnoughPermitsEncode(t *testing.T) {
	status := CalculateDiskSpace(gibibytes(1), gibibytes(20), gibibytes(40))
	if !status.Sufficient {
		t.Fatalf("space status = %#v; want sufficient", status)
	}
}

func TestDiskSpaceInsufficientRejectsEncode(t *testing.T) {
	status := CalculateDiskSpace(gibibytes(2), gibibytes(3), gibibytes(40))
	if status.Sufficient {
		t.Fatalf("space status = %#v; want insufficient", status)
	}
}

func TestDiskSpaceExactlyAtThresholdIsSufficient(t *testing.T) {
	status := CalculateDiskSpace(gibibytes(1), gibibytes(1)+EncoderWorkingAllowanceBytes+gibibytes(2), gibibytes(10))
	if !status.Sufficient || status.AvailableBytes != status.RequiredBytes {
		t.Fatalf("threshold status = %#v; want exact sufficient threshold", status)
	}
}

func TestSafetyReserveUsesMinimumTwoGiB(t *testing.T) {
	status := CalculateDiskSpace(0, gibibytes(10), gibibytes(10))
	if status.ReserveBytes != MinimumSafetyReserveBytes {
		t.Fatalf("reserve = %d; want %d", status.ReserveBytes, MinimumSafetyReserveBytes)
	}
}

func TestSafetyReserveUsesTenPercentOnLargeFilesystem(t *testing.T) {
	status := CalculateDiskSpace(0, gibibytes(100), gibibytes(100))
	if status.ReserveBytes != gibibytes(10) {
		t.Fatalf("reserve = %d; want %d", status.ReserveBytes, gibibytes(10))
	}
}

func TestRequirementIncludesWorkingAllowanceButNotSourceSize(t *testing.T) {
	target := gibibytes(3)
	status := CalculateDiskSpace(target, gibibytes(20), gibibytes(10))
	want := target + EncoderWorkingAllowanceBytes + MinimumSafetyReserveBytes
	if status.RequiredBytes != want {
		t.Fatalf("required = %d; want target + 512 MiB + reserve = %d", status.RequiredBytes, want)
	}
	largeSourceSize := gibibytes(500)
	if status.RequiredBytes >= largeSourceSize {
		t.Fatal("source size appears to have been included in disk requirement")
	}
}

func TestLargeFilesystemCalculationSaturatesWithoutOverflow(t *testing.T) {
	status := CalculateDiskSpace(math.MaxUint64, math.MaxUint64, math.MaxUint64)
	if status.RequiredBytes != math.MaxUint64 || !status.Sufficient {
		t.Fatalf("large filesystem status = %#v; want saturated sufficient values", status)
	}
	if got := saturatingMultiply(math.MaxUint64, 4096); got != math.MaxUint64 {
		t.Fatalf("saturatingMultiply overflowed to %d", got)
	}
	if got := TargetBytesFromMB(math.MaxInt64); got != math.MaxUint64 {
		t.Fatalf("large target conversion = %d; want saturation", got)
	}
}

func TestDiskSpaceWarningNearSafetyLimit(t *testing.T) {
	status := CalculateDiskSpace(gibibytes(1), gibibytes(3)+EncoderWorkingAllowanceBytes, gibibytes(10))
	if !status.Sufficient || !status.Warning {
		t.Fatalf("near-limit status = %#v; want sufficient with warning", status)
	}
}

func TestFilesystemCheckerUsesDestinationPath(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "media", "movies")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := (FilesystemDiskSpaceChecker{}).Check(destination, 1)
	if err != nil {
		t.Fatal(err)
	}
	if status.AvailableBytes == 0 || status.CapacityBytes == 0 || status.RequiredBytes == 0 {
		t.Fatalf("destination filesystem status = %#v", status)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[uint64]string{
		823 * 1024 * 1024:             "823 MB",
		47 * 1024 * 1024 * 102:        "4.7 GB",
		12 * 1024 * 1024 * 1024 * 102: "1.2 TB",
	}
	for bytes, want := range tests {
		if got := FormatBytes(bytes); got != want {
			t.Errorf("FormatBytes(%d) = %q; want %q", bytes, got, want)
		}
	}
}
