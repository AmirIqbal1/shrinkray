package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type controlledRunner struct {
	mu           sync.Mutex
	started      chan string
	release      chan struct{}
	running      int
	maxRunning   int
	order        []string
	emitProgress bool
}

type replacementResultRunner struct{}

func (replacementResultRunner) Run(context.Context, *Job, func(string), func(ProgressUpdate), func(DiskSpaceUpdate), func(string)) (RunResult, error) {
	return RunResult{Size: 40, SourceReplaced: true, FinalPath: "movie.mkv"}, nil
}

func newControlledRunner() *controlledRunner {
	return &controlledRunner{started: make(chan string, 10), release: make(chan struct{}, 10)}
}

func (r *controlledRunner) Run(ctx context.Context, job *Job, stage func(string), progress func(ProgressUpdate), disk func(DiskSpaceUpdate), log func(string)) (RunResult, error) {
	r.mu.Lock()
	r.running++
	if r.running > r.maxRunning {
		r.maxRunning = r.running
	}
	r.order = append(r.order, job.Filename)
	emitProgress := r.emitProgress
	r.mu.Unlock()
	if emitProgress {
		eta := 120.0
		stage(stageHEVCPass1)
		progress(ProgressUpdate{ProgressPercent: 23, StageProgressPercent: 46, DurationSeconds: 100, ProcessedSeconds: 46, ETASeconds: &eta, EncodeSpeed: 0.45, ETAIsEstimate: true})
	}
	r.started <- job.ID
	select {
	case <-ctx.Done():
		r.mu.Lock()
		r.running--
		r.mu.Unlock()
		return RunResult{}, ctx.Err()
	case <-r.release:
		r.mu.Lock()
		r.running--
		r.mu.Unlock()
		return RunResult{Size: 2}, nil
	}
}

func managerFixture(t *testing.T) (*JobManager, *controlledRunner, string) {
	t.Helper()
	dir := t.TempDir()
	roots, err := NewRootRegistry([]string{"Movies=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	runner := newControlledRunner()
	manager := NewJobManager(roots, runner)
	t.Cleanup(manager.Close)
	return manager, runner, dir
}

func multiRootManagerFixture(t *testing.T) (*JobManager, *controlledRunner, string, string) {
	t.Helper()
	movies, tv := makeRootDirectories(t)
	roots, err := NewRootRegistry([]string{"Movies=" + movies, "TV=" + tv})
	if err != nil {
		t.Fatal(err)
	}
	runner := newControlledRunner()
	manager := NewJobManager(roots, runner)
	t.Cleanup(manager.Close)
	return manager, runner, movies, tv
}

func submitTestJob(t *testing.T, manager *JobManager, dir, name string) *Job {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, name))
	job, err := manager.Submit("movies", name, "balanced", "mkv", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func waitForState(t *testing.T, manager *JobManager, id string, state JobState) *Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, job := range manager.List() {
			if job.ID == id && job.State == state {
				return job
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach state %s", id, state)
	return nil
}

func TestPresetSizeCalculations(t *testing.T) {
	size := int64(100 * 1024 * 1024)
	tests := []struct {
		preset  string
		exact   int64
		wantMB  int64
		quality string
	}{{"balanced", 0, 60, "good"}, {"smaller", 0, 40, "good"}, {"better", 0, 75, "best"}, {"exact", 23, 23, "good"}}
	for _, test := range tests {
		got, quality, err := CalculatePresetMB(size, test.preset, test.exact)
		if err != nil || got != test.wantMB || quality != test.quality {
			t.Errorf("CalculatePresetMB(%q) = %d, %q, %v; want %d, %q", test.preset, got, quality, err, test.wantMB, test.quality)
		}
	}
}

func TestQueuedJobLogsSerializeAsEmptyArray(t *testing.T) {
	manager, _, dir := managerFixture(t)
	job := submitTestJob(t, manager, dir, "movie.mkv")
	if job.Logs == nil {
		t.Fatal("newly queued job has a nil Logs slice")
	}

	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"logs":[]`)) {
		t.Fatalf("queued job JSON does not contain an empty logs array: %s", encoded)
	}
	if bytes.Contains(encoded, []byte(`"logs":null`)) {
		t.Fatalf("queued job JSON contains null logs: %s", encoded)
	}
}

func TestJobsEndpointSerializesEmptyQueueAsArray(t *testing.T) {
	roots, err := NewRootRegistry([]string{"Movies=" + t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(roots, "unused-shrinkray", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)

	request := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/jobs status = %d; want %d", response.Code, http.StatusOK)
	}

	var payload struct {
		Jobs []*Job `json:"jobs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Jobs == nil || len(payload.Jobs) != 0 {
		t.Fatalf("GET /api/jobs returned %#v; want a non-nil empty jobs array", payload.Jobs)
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"jobs":[]`)) {
		t.Fatalf("GET /api/jobs response = %s; want jobs:[]", response.Body.Bytes())
	}
}

func TestQueueOrderAndOnlyOneRunning(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	jobs := []*Job{
		submitTestJob(t, manager, dir, "one.mkv"),
		submitTestJob(t, manager, dir, "two.mkv"),
		submitTestJob(t, manager, dir, "three.mkv"),
	}
	for _, job := range jobs {
		select {
		case id := <-runner.started:
			if id != job.ID {
				t.Fatalf("started job %s; want %s", id, job.ID)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for job start")
		}
		runner.release <- struct{}{}
		waitForState(t, manager, job.ID, StateCompleted)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.maxRunning != 1 {
		t.Fatalf("maximum concurrent jobs = %d; want 1", runner.maxRunning)
	}
	want := []string{"one.mkv", "two.mkv", "three.mkv"}
	for i := range want {
		if runner.order[i] != want[i] {
			t.Fatalf("run order = %v; want %v", runner.order, want)
		}
	}
}

func TestJobsFromDifferentRootsShareOneQueue(t *testing.T) {
	manager, runner, movies, tv := multiRootManagerFixture(t)
	writeTestFile(t, filepath.Join(movies, "film.mkv"))
	writeTestFile(t, filepath.Join(tv, "episode.mkv"))
	movieJob, err := manager.Submit("movies", "film.mkv", "balanced", "mkv", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	tvJob, err := manager.Submit("tv", "episode.mkv", "smaller", "mkv", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if movieJob.RootID != "movies" || movieJob.RootLabel != "Movies" || tvJob.RootID != "tv" || tvJob.RootLabel != "TV" {
		t.Fatalf("jobs recorded incorrect roots: %#v, %#v", movieJob, tvJob)
	}

	for _, job := range []*Job{movieJob, tvJob} {
		select {
		case startedID := <-runner.started:
			if startedID != job.ID {
				t.Fatalf("started job %s; want %s", startedID, job.ID)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cross-root job")
		}
		runner.release <- struct{}{}
		waitForState(t, manager, job.ID, StateCompleted)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.maxRunning != 1 {
		t.Fatalf("maximum concurrent cross-root jobs = %d; want 1", runner.maxRunning)
	}
}

func TestOutputReservationsUseCanonicalPathsAcrossRoots(t *testing.T) {
	manager, _, movies, tv := multiRootManagerFixture(t)
	movieSource := filepath.Join(movies, "movie.mkv")
	writeTestFile(t, movieSource)
	writeTestFile(t, filepath.Join(tv, "movie.mkv"))
	if err := os.Symlink(movieSource, filepath.Join(movies, "alias.mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("movies", "movie.mkv", "balanced", "mkv", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("movies", "alias.mkv", "balanced", "mkv", false, 0); err == nil {
		t.Fatal("Submit accepted a second alias targeting the same canonical output")
	}
	if _, err := manager.Submit("tv", "movie.mkv", "balanced", "mkv", false, 0); err != nil {
		t.Fatalf("Submit rejected an independent output in another root: %v", err)
	}
}

func TestCancelQueuedJob(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	first := submitTestJob(t, manager, dir, "first.mkv")
	<-runner.started
	second := submitTestJob(t, manager, dir, "second.mkv")
	if err := manager.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, second.ID, StateCancelled)
	runner.release <- struct{}{}
	waitForState(t, manager, first.ID, StateCompleted)
	select {
	case id := <-runner.started:
		t.Fatalf("cancelled queued job %s started", id)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestRejectExistingOutput(t *testing.T) {
	manager, _, dir := managerFixture(t)
	writeTestFile(t, filepath.Join(dir, "movie.mkv"))
	writeTestFile(t, filepath.Join(dir, "movie.shrunk.mkv"))
	if _, err := manager.Submit("movies", "movie.mkv", "balanced", "mkv", false, 0); err == nil {
		t.Fatal("Submit accepted a job whose output exists")
	}
}

func TestReplacementDefaultsOffAndKeepsShrunkOutput(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	writeTestFile(t, filepath.Join(dir, "movie.mkv"))
	job, err := manager.Submit("movies", "movie.mkv", "balanced", "mkv", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if job.Settings.ReplaceOriginal || job.OutputPath != "movie.shrunk.mkv" || job.SourceReplaced {
		t.Fatalf("default job unexpectedly enabled replacement: %#v", job)
	}
	<-runner.started
}

func TestSameContainerReplacementReservesSource(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	writeTestFile(t, filepath.Join(dir, "movie.mkv"))
	job, err := manager.SubmitWithReplace("movies", "movie.mkv", "balanced", "mkv", false, 0, true, "software")
	if err != nil {
		t.Fatal(err)
	}
	if !job.Settings.ReplaceOriginal || job.OutputPath != "movie.mkv" || !job.OriginalKept || job.TransactionID == "" {
		t.Fatalf("replacement job fields = %#v", job)
	}
	<-runner.started
	if _, err := manager.SubmitWithReplace("movies", "movie.mkv", "balanced", "mkv", false, 0, true, "software"); err == nil {
		t.Fatal("second job reserved the same replacement source")
	}
}

func TestDifferentContainerReplacementUsesNewExtensionAndRefusesExistingTarget(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	writeTestFile(t, filepath.Join(dir, "movie.mp4"))
	job, err := manager.SubmitWithReplace("movies", "movie.mp4", "balanced", "mkv", false, 0, true, "software")
	if err != nil {
		t.Fatal(err)
	}
	if job.OutputPath != "movie.mkv" || job.FinalPath != "movie.mkv" {
		t.Fatalf("different-container target = %#v", job)
	}
	<-runner.started

	manager2, _, dir2 := managerFixture(t)
	writeTestFile(t, filepath.Join(dir2, "movie.mp4"))
	writeTestFile(t, filepath.Join(dir2, "movie.mkv"))
	if _, err := manager2.SubmitWithReplace("movies", "movie.mp4", "balanced", "mkv", false, 0, true, "software"); err == nil {
		t.Fatal("replacement accepted an existing different-container target")
	}
}

func TestCompletedReplacementRecordsFinalResult(t *testing.T) {
	dir := t.TempDir()
	roots, err := NewRootRegistry([]string{"Movies=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewJobManager(roots, replacementResultRunner{})
	t.Cleanup(manager.Close)
	if err := os.WriteFile(filepath.Join(dir, "movie.mkv"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := manager.SubmitWithReplace("movies", "movie.mkv", "balanced", "mkv", false, 0, true, "software")
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForState(t, manager, job.ID, StateCompleted)
	if !completed.SourceReplaced || completed.OriginalKept || completed.FinalPath != "movie.mkv" || completed.SavedPercent != 60 || completed.TransactionID != "" {
		t.Fatalf("completed replacement history = %#v", completed)
	}
}

func TestCancelRunningJob(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	runner.emitProgress = true
	job := submitTestJob(t, manager, dir, "movie.mkv")
	<-runner.started
	if err := manager.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitForState(t, manager, job.ID, StateCancelled)
	if cancelled.ProgressPercent != 23 || cancelled.StageProgressPercent != 46 || cancelled.ProcessedSeconds != 46 || cancelled.EncodeSpeed != 0.45 {
		t.Fatalf("cancelled job lost its last progress: %#v", cancelled)
	}
	if cancelled.ETASeconds == nil || *cancelled.ETASeconds != 120 || !cancelled.ETAIsEstimate {
		t.Fatalf("cancelled job lost its ETA state: %#v", cancelled)
	}
}

func TestCompletedJobIsAlwaysOneHundredPercent(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	job := submitTestJob(t, manager, dir, "complete.mkv")
	<-runner.started
	runner.release <- struct{}{}
	completed := waitForState(t, manager, job.ID, StateCompleted)
	if completed.ProgressPercent != 100 || completed.StageProgressPercent != 100 || completed.Stage != "Completed" {
		t.Fatalf("completed job progress = %#v; want 100%% and Completed", completed)
	}
}

func TestValidationStageHoldsAtNinetyNinePercent(t *testing.T) {
	eta := 20.0
	job := &Job{Stage: stageHEVCPass2, ProgressPercent: 75, StageProgressPercent: 51, ETASeconds: &eta, ETAIsEstimate: false}
	updateJobStage(job, stageValidation)
	if job.Stage != stageValidation || job.ProgressPercent != 99 || job.StageProgressPercent != 51 || job.ETASeconds != nil {
		t.Fatalf("validation transition = %#v", job)
	}
}

func TestLegacyJobWithoutProgressInformation(t *testing.T) {
	legacy := &Job{ID: "old", State: StateCompleted, Stage: "Completed", Logs: []string{}}
	cloned := cloneJob(legacy, time.Now())
	if cloned.ProgressPercent != 0 || cloned.DurationSeconds != 0 || cloned.ProcessedSeconds != 0 || cloned.ETASeconds != nil {
		t.Fatalf("legacy job gained misleading progress information: %#v", cloned)
	}
	if _, err := json.Marshal(cloned); err != nil {
		t.Fatalf("legacy job no longer serializes: %v", err)
	}
}

func TestSubmitRejectsDirectory(t *testing.T) {
	manager, _, dir := managerFixture(t)
	if err := os.Mkdir(filepath.Join(dir, "folder.mkv"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("movies", "folder.mkv", "balanced", "mkv", false, 0); err == nil {
		t.Fatal("Submit accepted a directory")
	}
}
