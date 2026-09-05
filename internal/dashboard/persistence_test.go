package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type failingRunner struct {
	started chan string
}

type burstProgressRunner struct{}

func (burstProgressRunner) Run(_ context.Context, _ *Job, stage func(string), progress func(ProgressUpdate), _ func(DiskSpaceUpdate), _ func(string)) (RunResult, error) {
	stage(stageHEVCPass1)
	for index := 1; index <= 100; index++ {
		progress(ProgressUpdate{ProgressPercent: float64(index) / 2, StageProgressPercent: float64(index), DurationSeconds: 100, ProcessedSeconds: float64(index), EncodeSpeed: 1})
	}
	return RunResult{Size: 2}, nil
}

func (r *failingRunner) Run(_ context.Context, job *Job, stage func(string), progress func(ProgressUpdate), _ func(DiskSpaceUpdate), logLine func(string)) (RunResult, error) {
	stage(stageHEVCPass1)
	progress(ProgressUpdate{ProgressPercent: 17, StageProgressPercent: 34, DurationSeconds: 90, ProcessedSeconds: 30, EncodeSpeed: 0.5})
	logLine("encoder failed")
	r.started <- job.ID
	return RunResult{}, errors.New("test encode failure")
}

func persistentManagerFixture(t *testing.T, mediaDir, stateDir string, runner JobRunner) *JobManager {
	t.Helper()
	roots, err := NewRootRegistry([]string{"Movies=" + mediaDir})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newPersistentJobManager(roots, runner, nil, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func findJob(t *testing.T, manager *JobManager, id string) *Job {
	t.Helper()
	for _, job := range manager.List() {
		if job.ID == id {
			return job
		}
	}
	t.Fatalf("job %s was not found", id)
	return nil
}

func waitForStartedID(t *testing.T, runner *controlledRunner, want string) {
	t.Helper()
	select {
	case got := <-runner.started:
		if got != want {
			t.Fatalf("started job %s; want %s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("job %s did not start", want)
	}
}

func TestCompletedFailedAndCancelledJobsSurviveRestart(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()

	completedRunner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, completedRunner)
	completed := submitTestJob(t, manager, mediaDir, "completed.mkv")
	waitForStartedID(t, completedRunner, completed.ID)
	completedRunner.release <- struct{}{}
	waitForState(t, manager, completed.ID, StateCompleted)
	manager.Close()

	failed := &failingRunner{started: make(chan string, 1)}
	manager = persistentManagerFixture(t, mediaDir, stateDir, failed)
	failedJob := submitTestJob(t, manager, mediaDir, "failed.mkv")
	select {
	case <-failed.started:
	case <-time.After(2 * time.Second):
		t.Fatal("failed job did not run")
	}
	waitForState(t, manager, failedJob.ID, StateFailed)
	manager.Close()

	cancelRunner := newControlledRunner()
	manager = persistentManagerFixture(t, mediaDir, stateDir, cancelRunner)
	cancelled := submitTestJob(t, manager, mediaDir, "cancelled.mkv")
	waitForStartedID(t, cancelRunner, cancelled.ID)
	if err := manager.Cancel(cancelled.ID); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, cancelled.ID, StateCancelled)
	manager.Close()

	restoredRunner := newControlledRunner()
	restored := persistentManagerFixture(t, mediaDir, stateDir, restoredRunner)
	defer restored.Close()
	for id, state := range map[string]JobState{completed.ID: StateCompleted, failedJob.ID: StateFailed, cancelled.ID: StateCancelled} {
		if got := findJob(t, restored, id).State; got != state {
			t.Fatalf("restored job %s state = %s; want %s", id, got, state)
		}
	}
	select {
	case id := <-restoredRunner.started:
		t.Fatalf("terminal history job %s was restarted", id)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestQueuedJobsAndOrderSurviveRestart(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	first := submitTestJob(t, manager, mediaDir, "running.mkv")
	waitForStartedID(t, runner, first.ID)
	second := submitTestJob(t, manager, mediaDir, "second.mkv")
	third := submitTestJob(t, manager, mediaDir, "third.mkv")
	manager.Close()

	restoredRunner := newControlledRunner()
	restored := persistentManagerFixture(t, mediaDir, stateDir, restoredRunner)
	defer restored.Close()
	if interrupted := findJob(t, restored, first.ID); interrupted.State != StateFailed || interrupted.Stage != "Interrupted by restart" {
		t.Fatalf("running job recovery = %#v", interrupted)
	}
	waitForStartedID(t, restoredRunner, second.ID)
	restoredRunner.release <- struct{}{}
	waitForState(t, restored, second.ID, StateCompleted)
	waitForStartedID(t, restoredRunner, third.ID)
}

func TestInterruptedJobPreservesProgressAndSafelyCleansOnlyPart(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	runner.emitProgress = true
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	job := submitTestJob(t, manager, mediaDir, "movie.mkv")
	waitForStartedID(t, runner, job.ID)
	source := filepath.Join(mediaDir, "movie.mkv")
	output := filepath.Join(mediaDir, "movie.shrunk.mkv")
	part := output + ".part"
	if err := os.WriteFile(output, []byte("completed output remains"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("temporary output"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.Close()

	restoredRunner := newControlledRunner()
	restored := persistentManagerFixture(t, mediaDir, stateDir, restoredRunner)
	defer restored.Close()
	interrupted := findJob(t, restored, job.ID)
	if interrupted.State != StateFailed || interrupted.Stage != "Interrupted by restart" || interrupted.Failure != interruptedByRestartMessage {
		t.Fatalf("interrupted job = %#v", interrupted)
	}
	if interrupted.ProgressPercent != 23 || interrupted.StageProgressPercent != 46 || interrupted.ProcessedSeconds != 46 || interrupted.ElapsedSeconds < 0 {
		t.Fatalf("interrupted progress was not preserved: %#v", interrupted)
	}
	if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected exact temporary output to be removed: %v", err)
	}
	for path, want := range map[string]string{source: "movie", output: "completed output remains"} {
		contents, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(contents), want) {
			t.Fatalf("recovery changed %s: %q, %v", path, contents, err)
		}
	}
	select {
	case id := <-restoredRunner.started:
		t.Fatalf("interrupted job %s resumed", id)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestNextIDSurvivesRestartAndIsNeverReused(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	first := submitTestJob(t, manager, mediaDir, "first.mkv")
	waitForStartedID(t, runner, first.ID)
	runner.release <- struct{}{}
	waitForState(t, manager, first.ID, StateCompleted)
	manager.Close()

	runner = newControlledRunner()
	manager = persistentManagerFixture(t, mediaDir, stateDir, runner)
	defer manager.Close()
	second := submitTestJob(t, manager, mediaDir, "second.mkv")
	firstID, _ := strconv.ParseUint(first.ID, 10, 64)
	secondID, _ := strconv.ParseUint(second.ID, 10, 64)
	if secondID != firstID+1 {
		t.Fatalf("next restored ID = %d; want %d", secondID, firstID+1)
	}
}

func TestAtomicStateReplacementAndPermissions(t *testing.T) {
	dir := t.TempDir()
	store := newJobStateStore(dir)
	first := jobStateSnapshot{revision: 1, state: persistedJobState{Version: jobStateVersion, NextID: 2, Jobs: []*Job{{ID: "1", State: StateCompleted, Logs: []string{}}}}}
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("jobs.json permissions = %o; want 600", info.Mode().Perm())
	}
	original, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.renameFile = func(string, string) error { return errors.New("injected rename failure") }
	second := jobStateSnapshot{revision: 2, state: persistedJobState{Version: jobStateVersion, NextID: 3, Jobs: []*Job{{ID: "2", State: StateFailed, Logs: []string{}}}}}
	if err := store.Save(second); err == nil {
		t.Fatal("Save succeeded despite injected atomic replacement failure")
	}
	after, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("failed replacement modified the previous valid state")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".jobs.json.tmp-*")); len(matches) != 0 {
		t.Fatalf("failed replacement left temporary files: %v", matches)
	}
}

func TestSubmissionPersistenceFailureDoesNotStartJob(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	defer manager.Close()
	manager.store.renameFile = func(string, string) error { return errors.New("injected rename failure") }
	writeTestFile(t, filepath.Join(mediaDir, "movie.mkv"))
	if _, err := manager.Submit("movies", "movie.mkv", "balanced", "mkv", false, 0); !errors.Is(err, ErrJobPersistence) {
		t.Fatalf("Submit() error = %v; want persistence error", err)
	}
	if jobs := manager.List(); len(jobs) != 0 {
		t.Fatalf("failed persistent submission remained in queue: %#v", jobs)
	}
	select {
	case id := <-runner.started:
		t.Fatalf("job %s started despite submission persistence failure", id)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestCorruptStateIsPreservedAndStartupContinues(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	statePath := filepath.Join(stateDir, "jobs.json")
	if err := os.WriteFile(statePath, []byte(`{"version":1,"jobs":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := persistentManagerFixture(t, mediaDir, stateDir, newControlledRunner())
	manager.Close()
	if jobs := manager.List(); len(jobs) != 0 {
		t.Fatalf("corrupt recovery restored jobs: %#v", jobs)
	}
	matches, err := filepath.Glob(statePath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt state backups = %v, %v; want one", matches, err)
	}
	contents, err := os.ReadFile(matches[0])
	if err != nil || !strings.Contains(string(contents), `"jobs":[`) {
		t.Fatalf("corrupt state was not preserved: %q, %v", contents, err)
	}
}

func TestRetentionKeepsNewestTerminalJobsAndAllQueuedJobs(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(mediaDir, "queued.mkv"))
	base := time.Now().Add(-time.Hour).UTC()
	jobs := make([]*Job, 0, terminalHistoryLimit+6)
	for index := 1; index <= terminalHistoryLimit+5; index++ {
		finished := base.Add(time.Duration(index) * time.Second)
		jobs = append(jobs, &Job{ID: strconv.Itoa(index), State: StateCompleted, Stage: "Completed", QueuedAt: base, FinishedAt: &finished, Logs: []string{}})
	}
	queuedID := strconv.Itoa(terminalHistoryLimit + 6)
	jobs = append(jobs, &Job{ID: queuedID, RootID: "movies", RootLabel: "Movies", Path: "queued.mkv", Filename: "queued.mkv", OutputPath: "queued.shrunk.mkv", OriginalSize: 4, Settings: JobSettings{Preset: "balanced", Quality: "good", Container: "mkv", TargetMB: 1}, State: StateQueued, Stage: "Waiting", QueuedAt: time.Now().UTC(), Logs: []string{}})
	store := newJobStateStore(stateDir)
	if err := store.Save(jobStateSnapshot{revision: 1, state: persistedJobState{Version: jobStateVersion, NextID: uint64(terminalHistoryLimit + 7), Jobs: jobs}}); err != nil {
		t.Fatal(err)
	}
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	defer manager.Close()
	waitForStartedID(t, runner, queuedID)
	restored := manager.List()
	terminalCount, activeCount := 0, 0
	for _, job := range restored {
		if isTerminalState(job.State) {
			terminalCount++
		} else {
			activeCount++
		}
	}
	if terminalCount != terminalHistoryLimit || activeCount != 1 {
		t.Fatalf("retained terminal/active counts = %d/%d; want %d/1", terminalCount, activeCount, terminalHistoryLimit)
	}
	foundOldest := false
	for _, job := range restored {
		if job.ID == "1" {
			foundOldest = true
		}
	}
	if foundOldest {
		t.Fatal("oldest terminal job was not pruned")
	}
}

func TestClearHistoryLeavesActiveJobsAndMediaUntouched(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	active := submitTestJob(t, manager, mediaDir, "active.mkv")
	waitForStartedID(t, runner, active.ID)
	queued := submitTestJob(t, manager, mediaDir, "queued.mkv")

	manager.mu.Lock()
	finished := time.Now().UTC()
	terminal := &Job{ID: "999", RootID: "movies", Path: "history.mkv", State: StateFailed, Stage: "Failed", FinishedAt: &finished, Logs: []string{}}
	manager.jobs = append(manager.jobs, terminal)
	manager.markPersistenceDirtyLocked()
	manager.mu.Unlock()
	manager.persistBestEffort("test setup", 0)

	removed, err := manager.ClearHistory()
	if err != nil || removed != 1 {
		t.Fatalf("ClearHistory() = %d, %v; want 1, nil", removed, err)
	}
	if findJob(t, manager, active.ID).State != StateRunning || findJob(t, manager, queued.ID).State != StateQueued {
		t.Fatal("ClearHistory changed active or queued jobs")
	}
	for _, name := range []string{"active.mkv", "queued.mkv"} {
		if _, err := os.Stat(filepath.Join(mediaDir, name)); err != nil {
			t.Fatalf("ClearHistory removed media %s: %v", name, err)
		}
	}
	manager.Close()

	restored := persistentManagerFixture(t, mediaDir, stateDir, newControlledRunner())
	defer restored.Close()
	for _, job := range restored.List() {
		if job.ID == terminal.ID {
			t.Fatal("cleared terminal history returned after restart")
		}
	}
}

func TestInvalidRecoveredQueuedJobsFailWithoutChangingMedia(t *testing.T) {
	tests := []struct {
		name       string
		removeSrc  bool
		makeOutput bool
	}{
		{name: "missing source", removeSrc: true},
		{name: "existing output", makeOutput: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mediaDir, stateDir := t.TempDir(), t.TempDir()
			source := filepath.Join(mediaDir, "movie.mkv")
			output := filepath.Join(mediaDir, "movie.shrunk.mkv")
			writeTestFile(t, source)
			if test.removeSrc {
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
			}
			if test.makeOutput {
				if err := os.WriteFile(output, []byte("existing final"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			job := &Job{ID: "7", RootID: "movies", RootLabel: "Movies", Path: "movie.mkv", Filename: "movie.mkv", OutputPath: "movie.shrunk.mkv", Settings: JobSettings{Preset: "balanced", Quality: "good", Container: "mkv", TargetMB: 1}, State: StateQueued, Stage: "Waiting", QueuedAt: time.Now().UTC(), Logs: []string{}}
			store := newJobStateStore(stateDir)
			if err := store.Save(jobStateSnapshot{revision: 1, state: persistedJobState{Version: jobStateVersion, NextID: 8, Jobs: []*Job{job}}}); err != nil {
				t.Fatal(err)
			}
			runner := newControlledRunner()
			manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
			defer manager.Close()
			restored := findJob(t, manager, "7")
			if restored.State != StateFailed || restored.Stage != "Failed to restore queued job" {
				t.Fatalf("invalid queued recovery = %#v", restored)
			}
			select {
			case id := <-runner.started:
				t.Fatalf("invalid queued job %s started", id)
			case <-time.After(30 * time.Millisecond):
			}
			if test.makeOutput {
				contents, err := os.ReadFile(output)
				if err != nil || string(contents) != "existing final" {
					t.Fatalf("existing final output changed: %q, %v", contents, err)
				}
			}
		})
	}
}

func TestInterruptedCleanupLeavesUnverifiedPartAlone(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(mediaDir, "movie.mkv"))
	unverifiedPart := filepath.Join(mediaDir, "movie.shrunk.mkv.part")
	if err := os.WriteFile(unverifiedPart, []byte("leave me"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{
		ID: "4", RootID: "movies", RootLabel: "Movies", Path: "movie.mkv", Filename: "movie.mkv",
		OutputPath: "different.shrunk.mkv", Settings: JobSettings{Container: "mkv", TargetMB: 1},
		State: StateRunning, Stage: stageHEVCPass1, QueuedAt: time.Now().UTC(), Logs: []string{},
	}
	store := newJobStateStore(stateDir)
	if err := store.Save(jobStateSnapshot{revision: 1, state: persistedJobState{Version: jobStateVersion, NextID: 5, Jobs: []*Job{job}}}); err != nil {
		t.Fatal(err)
	}
	manager := persistentManagerFixture(t, mediaDir, stateDir, newControlledRunner())
	defer manager.Close()
	restored := findJob(t, manager, "4")
	if restored.State != StateFailed || len(restored.Logs) == 0 || !strings.Contains(restored.Logs[len(restored.Logs)-1], "left in place") {
		t.Fatalf("unverified interrupted recovery = %#v", restored)
	}
	contents, err := os.ReadFile(unverifiedPart)
	if err != nil || string(contents) != "leave me" {
		t.Fatalf("unverified part was changed: %q, %v", contents, err)
	}
}

func TestPersistedStateDoesNotContainRuntimeAbsoluteOutputPath(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	runner := newControlledRunner()
	manager := persistentManagerFixture(t, mediaDir, stateDir, runner)
	job := submitTestJob(t, manager, mediaDir, "movie.mkv")
	waitForStartedID(t, runner, job.ID)
	manager.Close()
	data, err := os.ReadFile(filepath.Join(stateDir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), mediaDir) || strings.Contains(string(data), "outputAbs") {
		t.Fatalf("state exposed a runtime absolute path: %s", data)
	}
	var state persistedJobState
	if err := json.Unmarshal(data, &state); err != nil || state.Version != jobStateVersion {
		t.Fatalf("saved state is invalid: %#v, %v", state, err)
	}
}

func TestRequestedAndActualEncoderPersistAndLegacyDefaultsSafely(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	finished := time.Now().UTC()
	jobs := []*Job{
		{ID: "1", State: StateCompleted, Stage: "Completed", Settings: JobSettings{RequestedEncoder: "qsv"}, ActualEncoder: "qsv", QueuedAt: finished, FinishedAt: &finished, Logs: []string{}},
		{ID: "2", State: StateCompleted, Stage: "Completed", Settings: JobSettings{}, QueuedAt: finished, FinishedAt: &finished, Logs: []string{}},
	}
	store := newJobStateStore(stateDir)
	if err := store.Save(jobStateSnapshot{revision: 1, state: persistedJobState{Version: jobStateVersion, NextID: 3, Jobs: jobs}}); err != nil {
		t.Fatal(err)
	}
	manager := persistentManagerFixture(t, mediaDir, stateDir, newControlledRunner())
	defer manager.Close()
	if restored := findJob(t, manager, "1"); restored.Settings.RequestedEncoder != "qsv" || restored.ActualEncoder != "qsv" {
		t.Fatalf("hardware history lost backend fields: %#v", restored)
	}
	if legacy := findJob(t, manager, "2"); legacy.Settings.RequestedEncoder != "software" || legacy.ActualEncoder != "" {
		t.Fatalf("legacy encoder defaults = %#v", legacy)
	}
}

func TestLegacyJobDefaultsReplacementOff(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	finished := time.Now().UTC()
	legacy := []byte(`{"version":1,"next_id":2,"jobs":[{"id":"1","state":"completed","stage":"Completed","queued_at":"` + finished.Format(time.RFC3339Nano) + `","settings":{"preset":"balanced","quality":"good","container":"mkv","target_mb":1},"logs":[]}]}`)
	if err := os.WriteFile(filepath.Join(stateDir, "jobs.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := persistentManagerFixture(t, mediaDir, stateDir, newControlledRunner())
	defer manager.Close()
	job := findJob(t, manager, "1")
	if job.Settings.ReplaceOriginal || job.SourceReplaced || job.OriginalKept {
		t.Fatalf("legacy job enabled replacement fields: %#v", job)
	}
}

func TestInterruptedSameContainerReplacementCleansOnlyProvenArtifacts(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	source := filepath.Join(mediaDir, "movie.mkv")
	writeTestFile(t, source)
	transactionID := "7-recoverytest"
	backup := filepath.Join(mediaDir, ".movie.mkv.shrinkray-backup-"+transactionID)
	temporary := filepath.Join(mediaDir, ".movie.mkv.shrinkray-replace-"+transactionID+".part.mkv")
	if err := os.Link(source, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(mediaDir, ".movie.mkv.shrinkray-backup-unrelated")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "7", RootID: "movies", RootLabel: "Movies", Path: "movie.mkv", Filename: "movie.mkv", OutputPath: "movie.mkv", OriginalSize: 5, Settings: JobSettings{Container: "mkv", TargetMB: 1, ReplaceOriginal: true}, State: StateRunning, Stage: stageValidation, QueuedAt: time.Now().UTC(), Logs: []string{}, TransactionID: transactionID, OriginalKept: true}
	store := newJobStateStore(stateDir)
	if err := store.Save(jobStateSnapshot{revision: 1, state: persistedJobState{Version: jobStateVersion, NextID: 8, Jobs: []*Job{job}}}); err != nil {
		t.Fatal(err)
	}
	manager := persistentManagerFixture(t, mediaDir, stateDir, newControlledRunner())
	defer manager.Close()
	for _, removed := range []string{backup, temporary} {
		if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("proven redundant artifact remained: %s: %v", removed, err)
		}
	}
	if contents, err := os.ReadFile(unrelated); err != nil || string(contents) != "unrelated" {
		t.Fatalf("unrelated backup changed: %q, %v", contents, err)
	}
	restored := findJob(t, manager, "7")
	if !restored.OriginalKept || !strings.Contains(strings.Join(restored.Logs, "\n"), "original remained in place") {
		t.Fatalf("replacement recovery result = %#v", restored)
	}
}

func TestInterruptedReplacementRetainsAmbiguousBackup(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	source := filepath.Join(mediaDir, "movie.mkv")
	if err := os.WriteFile(source, []byte("new encode"), 0o600); err != nil {
		t.Fatal(err)
	}
	transactionID := "8-ambiguous"
	backup := filepath.Join(mediaDir, ".movie.mkv.shrinkray-backup-"+transactionID)
	if err := os.WriteFile(backup, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "8", RootID: "movies", RootLabel: "Movies", Path: "movie.mkv", Filename: "movie.mkv", OutputPath: "movie.mkv", Settings: JobSettings{Container: "mkv", TargetMB: 1, ReplaceOriginal: true}, State: StateRunning, QueuedAt: time.Now().UTC(), Logs: []string{}, TransactionID: transactionID}
	store := newJobStateStore(stateDir)
	if err := store.Save(jobStateSnapshot{revision: 1, state: persistedJobState{Version: jobStateVersion, NextID: 9, Jobs: []*Job{job}}}); err != nil {
		t.Fatal(err)
	}
	manager := persistentManagerFixture(t, mediaDir, stateDir, newControlledRunner())
	defer manager.Close()
	if contents, err := os.ReadFile(backup); err != nil || string(contents) != "original" {
		t.Fatalf("ambiguous backup was not retained: %q, %v", contents, err)
	}
	if logs := strings.Join(findJob(t, manager, "8").Logs, "\n"); !strings.Contains(logs, backup) || !strings.Contains(logs, "recovery attention required") {
		t.Fatalf("recovery log did not identify retained backup: %s", logs)
	}
}

func TestProgressPersistenceIsThrottled(t *testing.T) {
	mediaDir, stateDir := t.TempDir(), t.TempDir()
	manager := persistentManagerFixture(t, mediaDir, stateDir, burstProgressRunner{})
	writes := 0
	originalRename := manager.store.renameFile
	manager.store.renameFile = func(oldPath, newPath string) error {
		writes++
		return originalRename(oldPath, newPath)
	}
	job := submitTestJob(t, manager, mediaDir, "movie.mkv")
	waitForState(t, manager, job.ID, StateCompleted)
	manager.Close()
	if writes > 6 {
		t.Fatalf("100 progress packets caused %d state writes; want throttled writes", writes)
	}
}
