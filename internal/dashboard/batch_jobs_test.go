package dashboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type mixedReplacementRunner struct{}

func (mixedReplacementRunner) Run(_ context.Context, job *Job, _ func(string), _ func(ProgressUpdate), _ func(DiskSpaceUpdate), _ func(string)) (RunResult, error) {
	if job.Filename == "fails.mkv" {
		return RunResult{}, errors.New("injected replacement failure")
	}
	return RunResult{Size: 2, SourceReplaced: true, FinalPath: job.OutputPath}, nil
}

type mixedDiskRunner struct{}

func (mixedDiskRunner) Run(_ context.Context, job *Job, _ func(string), _ func(ProgressUpdate), _ func(DiskSpaceUpdate), _ func(string)) (RunResult, error) {
	if job.Filename == "no-space.mkv" {
		return RunResult{}, errors.Join(errors.New("injected disk failure"), ErrInsufficientDiskSpace)
	}
	return RunResult{Size: 2, FinalPath: job.OutputPath}, nil
}

func writeSizedTestFile(t *testing.T, path string, size int64) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOneItemBatchHasBatchIdentity(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	writeTestFile(t, filepath.Join(dir, "only.mkv"))
	jobs, err := manager.SubmitBatch("movies", []string{"only.mkv"}, "balanced", "mkv", false, 0, false, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || !validBatchID(jobs[0].BatchID) || jobs[0].BatchIndex != 1 || jobs[0].BatchSize != 1 {
		t.Fatalf("one-item batch metadata = %#v", jobs)
	}
	<-runner.started
}

func TestBatchEntersExistingQueueInRequestOrderAndRunsSequentially(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	existing := submitTestJob(t, manager, dir, "existing.mkv")
	waitForStartedID(t, runner, existing.ID)
	paths := []string{"a.mkv", "folder/b.mkv", "c.mp4"}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	for index, path := range paths {
		writeSizedTestFile(t, filepath.Join(dir, filepath.FromSlash(path)), int64(index+2)*1024*1024)
	}
	jobs, err := manager.SubmitBatch("movies", paths, "exact", "mkv", true, 17, false, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != len(paths) {
		t.Fatalf("created %d jobs; want %d", len(jobs), len(paths))
	}
	for index, job := range jobs {
		if job.Path != paths[index] || job.BatchID != jobs[0].BatchID || job.BatchIndex != index+1 || job.BatchSize != len(paths) {
			t.Fatalf("batch job %d = %#v", index, job)
		}
		if job.Settings.TargetMB != 17 || !job.Settings.KeepAllAudio || job.Settings.RequestedEncoder != "auto" {
			t.Fatalf("shared settings were not applied independently: %#v", job.Settings)
		}
	}
	runner.release <- struct{}{}
	waitForState(t, manager, existing.ID, StateCompleted)
	for _, job := range jobs {
		waitForStartedID(t, runner, job.ID)
		runner.release <- struct{}{}
		waitForState(t, manager, job.ID, StateCompleted)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.maxRunning != 1 {
		t.Fatalf("batch ran %d jobs concurrently", runner.maxRunning)
	}
	want := []string{"existing.mkv", "a.mkv", "b.mkv", "c.mp4"}
	if strings.Join(runner.order, ",") != strings.Join(want, ",") {
		t.Fatalf("run order = %v; want %v", runner.order, want)
	}
}

func TestBatchPercentageTargetsAreCalculatedForEachSource(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	writeSizedTestFile(t, filepath.Join(dir, "small.mkv"), 10*1024*1024)
	writeSizedTestFile(t, filepath.Join(dir, "large.mkv"), 25*1024*1024)
	jobs, err := manager.SubmitBatch("movies", []string{"small.mkv", "large.mkv"}, "balanced", "mkv", false, 0, false, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Settings.TargetMB != 6 || jobs[1].Settings.TargetMB != 15 {
		t.Fatalf("per-source targets = %d, %d; want 6, 15", jobs[0].Settings.TargetMB, jobs[1].Settings.TargetMB)
	}
	<-runner.started
}

func TestBatchRejectsInvalidRequestsWithoutCreatingJobs(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string) []string
	}{
		{name: "empty", setup: func(*testing.T, string) []string { return nil }},
		{name: "too many", setup: func(*testing.T, string) []string { return make([]string, MaxBatchSize+1) }},
		{name: "duplicate", setup: func(t *testing.T, dir string) []string {
			writeTestFile(t, filepath.Join(dir, "movie.mkv"))
			return []string{"movie.mkv", "movie.mkv"}
		}},
		{name: "directory", setup: func(t *testing.T, dir string) []string {
			if err := os.Mkdir(filepath.Join(dir, "folder.mkv"), 0o700); err != nil {
				t.Fatal(err)
			}
			return []string{"folder.mkv"}
		}},
		{name: "outside root", setup: func(t *testing.T, dir string) []string {
			writeTestFile(t, filepath.Join(filepath.Dir(dir), "outside.mkv"))
			return []string{"../outside.mkv"}
		}},
		{name: "unsupported", setup: func(t *testing.T, dir string) []string {
			writeTestFile(t, filepath.Join(dir, "notes.txt"))
			return []string{"notes.txt"}
		}},
		{name: "existing output", setup: func(t *testing.T, dir string) []string {
			writeTestFile(t, filepath.Join(dir, "first.mkv"))
			writeTestFile(t, filepath.Join(dir, "second.mkv"))
			writeTestFile(t, filepath.Join(dir, "second.shrunk.mkv"))
			return []string{"first.mkv", "second.mkv"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, runner, dir := managerFixture(t)
			paths := test.setup(t, dir)
			if _, err := manager.SubmitBatch("movies", paths, "balanced", "mkv", false, 0, false, "auto"); err == nil {
				t.Fatal("invalid batch was accepted")
			}
			if jobs := manager.List(); len(jobs) != 0 {
				t.Fatalf("invalid batch created jobs: %#v", jobs)
			}
			select {
			case id := <-runner.started:
				t.Fatalf("invalid batch started job %s", id)
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}

func TestBatchConflictWithExistingReservationRejectsWholeBatch(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	existing := submitTestJob(t, manager, dir, "shared.mkv")
	waitForStartedID(t, runner, existing.ID)
	writeTestFile(t, filepath.Join(dir, "independent.mkv"))
	if _, err := manager.SubmitBatch("movies", []string{"independent.mkv", "shared.mkv"}, "balanced", "mkv", false, 0, false, "auto"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("reservation conflict error = %v; want ErrJobConflict", err)
	}
	if jobs := manager.List(); len(jobs) != 1 || jobs[0].ID != existing.ID {
		t.Fatalf("conflicting batch changed existing jobs: %#v", jobs)
	}
}

func TestInternalBatchOutputCollisionRejectsWholeBatch(t *testing.T) {
	manager, _, dir := managerFixture(t)
	writeTestFile(t, filepath.Join(dir, "movie.mkv"))
	writeTestFile(t, filepath.Join(dir, "movie.mp4"))
	if _, err := manager.SubmitBatch("movies", []string{"movie.mkv", "movie.mp4"}, "balanced", "mkv", false, 0, false, "auto"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("internal target collision error = %v; want ErrJobConflict", err)
	}
	if len(manager.List()) != 0 {
		t.Fatal("internal target collision partially created the batch")
	}
}

func TestBatchPersistenceFailureRollsBackJobsQueueReservationsAndIDs(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	defer manager.Close()
	writeTestFile(t, filepath.Join(mediaDir, "first.mkv"))
	writeTestFile(t, filepath.Join(mediaDir, "second.mkv"))
	originalRename := manager.store.renameFile
	manager.store.renameFile = func(string, string) error { return errors.New("injected rename failure") }
	if _, err := manager.SubmitBatch("movies", []string{"first.mkv", "second.mkv"}, "balanced", "mkv", false, 0, false, "auto"); !errors.Is(err, ErrJobPersistence) {
		t.Fatalf("SubmitBatch error = %v; want persistence error", err)
	}
	if len(manager.List()) != 0 {
		t.Fatal("failed batch persistence left job history")
	}
	manager.store.renameFile = originalRename
	job, err := manager.Submit("movies", "first.mkv", "balanced", "mkv", false, 0)
	if err != nil {
		t.Fatalf("rolled-back source reservation was not released: %v", err)
	}
	if job.ID != "1" {
		t.Fatalf("ID after rollback = %s; want 1", job.ID)
	}
	waitForStartedID(t, runner, job.ID)
}

func TestBatchPersistenceFailureLeavesExistingQueueUntouched(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	defer manager.Close()
	existing := submitTestJob(t, manager, mediaDir, "existing.mkv")
	waitForStartedID(t, runner, existing.ID)
	writeTestFile(t, filepath.Join(mediaDir, "first.mkv"))
	writeTestFile(t, filepath.Join(mediaDir, "second.mkv"))
	originalRename := manager.store.renameFile
	manager.store.renameFile = func(string, string) error { return errors.New("injected rename failure") }
	if _, err := manager.SubmitBatch("movies", []string{"first.mkv", "second.mkv"}, "balanced", "mkv", false, 0, false, "auto"); !errors.Is(err, ErrJobPersistence) {
		t.Fatalf("SubmitBatch error = %v; want persistence error", err)
	}
	if jobs := manager.List(); len(jobs) != 1 || jobs[0].ID != existing.ID || jobs[0].State != StateRunning {
		t.Fatalf("failed batch changed existing queue: %#v", jobs)
	}
	manager.store.renameFile = originalRename
	next, err := manager.Submit("movies", "first.mkv", "balanced", "mkv", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "2" {
		t.Fatalf("ID after rollback with existing queue = %s; want 2", next.ID)
	}
}

func TestCancelBatchLeavesCompletedMemberAndCancelsRunningAndQueued(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	for _, name := range []string{"first.mkv", "second.mkv", "third.mkv"} {
		writeTestFile(t, filepath.Join(dir, name))
	}
	jobs, err := manager.SubmitBatch("movies", []string{"first.mkv", "second.mkv", "third.mkv"}, "balanced", "mkv", false, 0, false, "auto")
	if err != nil {
		t.Fatal(err)
	}
	waitForStartedID(t, runner, jobs[0].ID)
	runner.release <- struct{}{}
	waitForState(t, manager, jobs[0].ID, StateCompleted)
	waitForStartedID(t, runner, jobs[1].ID)
	result, err := manager.CancelBatch(jobs[0].BatchID, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.QueuedCancelled != 1 || !result.RunningCancelled {
		t.Fatalf("CancelBatch result = %#v", result)
	}
	waitForState(t, manager, jobs[1].ID, StateCancelled)
	if findJob(t, manager, jobs[0].ID).State != StateCompleted || findJob(t, manager, jobs[2].ID).State != StateCancelled {
		t.Fatal("batch cancellation altered completed work or missed queued work")
	}
}

func TestCancelBatchCanLeaveRunningMemberAndCancelOnlyQueued(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	for _, name := range []string{"first.mkv", "second.mkv"} {
		writeTestFile(t, filepath.Join(dir, name))
	}
	jobs, err := manager.SubmitBatch("movies", []string{"first.mkv", "second.mkv"}, "balanced", "mkv", false, 0, false, "auto")
	if err != nil {
		t.Fatal(err)
	}
	waitForStartedID(t, runner, jobs[0].ID)
	result, err := manager.CancelBatch(jobs[0].BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.QueuedCancelled != 1 || result.RunningCancelled || findJob(t, manager, jobs[0].ID).State != StateRunning {
		t.Fatalf("queued-only cancellation result/state = %#v, %#v", result, findJob(t, manager, jobs[0].ID))
	}
}

func TestReplacementSettingsAndTargetsAreIndependentWithinBatch(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	writeTestFile(t, filepath.Join(dir, "first.mp4"))
	writeTestFile(t, filepath.Join(dir, "second.mp4"))
	jobs, err := manager.SubmitBatch("movies", []string{"first.mp4", "second.mp4"}, "balanced", "mkv", false, 0, true, "qsv")
	if err != nil {
		t.Fatal(err)
	}
	for index, job := range jobs {
		want := strings.TrimSuffix(job.Path, ".mp4") + ".mkv"
		if !job.Settings.ReplaceOriginal || job.Settings.RequestedEncoder != "qsv" || job.OutputPath != want || !job.OriginalKept || job.TransactionID == "" {
			t.Fatalf("replacement batch job %d = %#v", index, job)
		}
	}
	<-runner.started
	if _, err := manager.SubmitBatch("movies", []string{"first.mp4"}, "balanced", "mkv", false, 0, true, "auto"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("replacement reservation collision error = %v", err)
	}
}

func TestFailedReplacementMemberDoesNotStopOrUndoNextMember(t *testing.T) {
	dir := t.TempDir()
	roots, err := NewRootRegistry([]string{"Movies=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewJobManager(roots, mixedReplacementRunner{})
	defer manager.Close()
	for _, name := range []string{"fails.mkv", "succeeds.mkv"} {
		writeTestFile(t, filepath.Join(dir, name))
	}
	jobs, err := manager.SubmitBatch("movies", []string{"fails.mkv", "succeeds.mkv"}, "balanced", "mkv", false, 0, true, "auto")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, manager, jobs[0].ID, StateFailed)
	completed := waitForState(t, manager, jobs[1].ID, StateCompleted)
	if !failed.OriginalKept || failed.SourceReplaced || !completed.SourceReplaced {
		t.Fatalf("independent replacement results = failed %#v, completed %#v", failed, completed)
	}
	if _, err := os.Stat(filepath.Join(dir, "fails.mkv")); err != nil {
		t.Fatalf("failed replacement source was removed: %v", err)
	}
}

func TestDiskFailureAffectsOnlyItsBatchMember(t *testing.T) {
	dir := t.TempDir()
	roots, err := NewRootRegistry([]string{"Movies=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewJobManager(roots, mixedDiskRunner{})
	defer manager.Close()
	for _, name := range []string{"no-space.mkv", "continues.mkv"} {
		writeTestFile(t, filepath.Join(dir, name))
	}
	jobs, err := manager.SubmitBatch("movies", []string{"no-space.mkv", "continues.mkv"}, "balanced", "mkv", false, 0, false, "auto")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, manager, jobs[0].ID, StateFailed)
	completed := waitForState(t, manager, jobs[1].ID, StateCompleted)
	if failed.Stage != stageInsufficientDisk || completed.State != StateCompleted {
		t.Fatalf("disk failure did not remain isolated: failed %#v, completed %#v", failed, completed)
	}
	if _, err := os.Stat(filepath.Join(dir, "no-space.mkv")); err != nil {
		t.Fatalf("disk failure removed source: %v", err)
	}
}

func TestBatchIDsRemainMonotonic(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	for _, name := range []string{"one.mkv", "two.mkv", "three.mkv"} {
		writeTestFile(t, filepath.Join(dir, name))
	}
	jobs, err := manager.SubmitBatch("movies", []string{"one.mkv", "two.mkv"}, "balanced", "mkv", false, 0, false, "auto")
	if err != nil {
		t.Fatal(err)
	}
	third, err := manager.Submit("movies", "three.mkv", "balanced", "mkv", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, job := range append(jobs, third) {
		id, _ := strconv.Atoi(job.ID)
		if id != index+1 {
			t.Fatalf("job IDs are not monotonic: %#v", append(jobs, third))
		}
	}
	<-runner.started
}
