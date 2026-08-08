package dashboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type sequenceDiskChecker struct {
	mu       sync.Mutex
	statuses []DiskSpaceStatus
	calls    int
	paths    []string
}

func (c *sequenceDiskChecker) Check(path string, _ uint64) (DiskSpaceStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, path)
	index := c.calls
	c.calls++
	if index >= len(c.statuses) {
		index = len(c.statuses) - 1
	}
	return c.statuses[index], nil
}

func sufficientTestDisk() DiskSpaceStatus {
	return CalculateDiskSpace(1024*1024, gibibytes(20), gibibytes(40))
}

func insufficientTestDisk() DiskSpaceStatus {
	return CalculateDiskSpace(1024*1024, gibibytes(1), gibibytes(40))
}

func criticalTestDisk() DiskSpaceStatus {
	return CalculateDiskSpace(1024*1024, CriticalFreeSpaceBytes-1, gibibytes(40))
}

func runnerDiskFixture(t *testing.T, script string, checker DiskSpaceChecker) (*CLIRunner, *Job, string, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(source, []byte("original movie"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, err := NewRootRegistry([]string{"Movies=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	shrinkrayBin := filepath.Join(t.TempDir(), "fake-shrinkray")
	if err := os.WriteFile(shrinkrayBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := newCLIRunnerWithDiskSpace(roots, shrinkrayBin, checker)
	runner.probeDuration = func(context.Context, string) (float64, error) { return 10, nil }
	output := filepath.Join(dir, "movie.shrunk.mkv")
	job := &Job{
		RootID: "movies", Path: "movie.mkv", Filename: "movie.mkv", outputAbs: output,
		Settings: JobSettings{TargetMB: 1, Quality: "good", Container: "mkv"},
	}
	return runner, job, source, output
}

func runTestCLI(runner *CLIRunner, job *Job, stage func(string)) (RunResult, error) {
	return runner.Run(context.Background(), job, stage, func(ProgressUpdate) {}, func(DiskSpaceUpdate) {}, func(string) {})
}

func TestDashboardPreflightInsufficientSpaceNeverStartsCLI(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	script := "#!/usr/bin/env bash\ntouch '" + marker + "'\n"
	checker := &sequenceDiskChecker{statuses: []DiskSpaceStatus{insufficientTestDisk()}}
	runner, job, source, output := runnerDiskFixture(t, script, checker)
	lastStage := ""
	_, err := runTestCLI(runner, job, func(stage string) { lastStage = stage })
	if !errors.Is(err, ErrInsufficientDiskSpace) || lastStage != stageInsufficientDisk {
		t.Fatalf("Run() error/stage = %v, %q; want insufficient disk", err, lastStage)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("CLI started despite insufficient disk space")
	}
	if _, err := os.Stat(output + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight failure left a temporary output")
	}
	contents, err := os.ReadFile(source)
	if err != nil || string(contents) != "original movie" {
		t.Fatalf("source changed after preflight failure: %q, %v", contents, err)
	}
}

func TestDashboardQueueRechecksSpaceImmediatelyBeforeCLIStart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	script := "#!/usr/bin/env bash\ntouch '" + marker + "'\n"
	checker := &sequenceDiskChecker{statuses: []DiskSpaceStatus{sufficientTestDisk(), insufficientTestDisk()}}
	runner, job, _, _ := runnerDiskFixture(t, script, checker)
	_, err := runTestCLI(runner, job, func(string) {})
	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("Run() error = %v; want authoritative recheck failure", err)
	}
	if checker.calls != 2 {
		t.Fatalf("disk checks = %d; want initial and immediate pre-start checks", checker.calls)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("CLI started after queued space became insufficient")
	}
}

func TestDashboardEnoughSpaceRunsCLI(t *testing.T) {
	script := `#!/usr/bin/env bash
set -euo pipefail
input="$1"
printf '==> Encoding with HEVC (pass 1 of 2)...\n'
printf 'out_time_us=10000000\nspeed=1.0x\nprogress=end\n'
cp -- "$input" "${input%.*}.shrunk.mkv"
`
	checker := &sequenceDiskChecker{statuses: []DiskSpaceStatus{sufficientTestDisk()}}
	runner, job, _, output := runnerDiskFixture(t, script, checker)
	result, err := runTestCLI(runner, job, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Size == 0 {
		t.Fatal("successful CLI run returned an empty result")
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("successful CLI run did not create output: %v", err)
	}
}

func TestCriticalDiskSpaceStopsEncodeAndKeepsOriginal(t *testing.T) {
	script := `#!/usr/bin/env bash
set -u
input="$1"
part="${input%.*}.shrunk.mkv.part"
cleanup() { rm -f -- "$part"; exit 70; }
trap cleanup TERM INT
: > "$part"
printf '==> Encoding with HEVC (pass 1 of 2)...\n'
printf 'out_time_us=1000000\nspeed=0.5x\nprogress=continue\n'
while :; do :; done
`
	checker := &sequenceDiskChecker{statuses: []DiskSpaceStatus{sufficientTestDisk(), sufficientTestDisk(), criticalTestDisk()}}
	runner, job, source, output := runnerDiskFixture(t, script, checker)
	lastStage := ""
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := runner.Run(ctx, job, func(stage string) { lastStage = stage }, func(ProgressUpdate) {}, func(DiskSpaceUpdate) {}, func(string) {})
	if !errors.Is(err, ErrCriticalDiskSpace) || lastStage != stageCriticalDisk {
		t.Fatalf("critical Run() error/stage = %v, %q", err, lastStage)
	}
	if contents, readErr := os.ReadFile(source); readErr != nil || string(contents) != "original movie" {
		t.Fatalf("critical abort changed source: %q, %v", contents, readErr)
	}
	for _, path := range []string{output, output + ".part"} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("critical abort left %s", path)
		}
	}
}

type insufficientRunner struct{}

func (insufficientRunner) Run(_ context.Context, _ *Job, stage func(string), _ func(ProgressUpdate), disk func(DiskSpaceUpdate), _ func(string)) (RunResult, error) {
	status := insufficientTestDisk()
	disk(diskSpaceUpdate(status))
	stage(stageInsufficientDisk)
	return RunResult{}, ErrInsufficientDiskSpace
}

func TestDashboardJobUsesInsufficientDiskFailureStage(t *testing.T) {
	dir := t.TempDir()
	roots, err := NewRootRegistry([]string{"Movies=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewJobManager(roots, insufficientRunner{})
	t.Cleanup(manager.Close)
	job := submitTestJob(t, manager, dir, "low-space.mkv")
	failed := waitForState(t, manager, job.ID, StateFailed)
	if failed.Stage != stageInsufficientDisk || failed.Failure == "" {
		t.Fatalf("failed low-space job = %#v", failed)
	}
}

func TestQueuedJobExposesSubmissionDiskInformation(t *testing.T) {
	dir := t.TempDir()
	roots, err := NewRootRegistry([]string{"Movies=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	runner := newControlledRunner()
	status := sufficientTestDisk()
	checker := &sequenceDiskChecker{statuses: []DiskSpaceStatus{status}}
	manager := newJobManagerWithDiskSpace(roots, runner, checker)
	t.Cleanup(manager.Close)
	job := submitTestJob(t, manager, dir, "queued-space.mkv")
	if job.DiskAvailableBytes != status.AvailableBytes || job.DiskRequiredBytes != status.RequiredBytes || job.DiskSafetyReserve != status.ReserveBytes {
		t.Fatalf("queued disk fields = %#v; want %#v", job, status)
	}
}
