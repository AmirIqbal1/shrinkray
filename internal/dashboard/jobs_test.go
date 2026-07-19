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
	mu         sync.Mutex
	started    chan string
	release    chan struct{}
	running    int
	maxRunning int
	order      []string
}

func newControlledRunner() *controlledRunner {
	return &controlledRunner{started: make(chan string, 10), release: make(chan struct{}, 10)}
}

func (r *controlledRunner) Run(ctx context.Context, job *Job, stage func(string), log func(string)) (RunResult, error) {
	r.mu.Lock()
	r.running++
	if r.running > r.maxRunning {
		r.maxRunning = r.running
	}
	r.order = append(r.order, job.Filename)
	r.mu.Unlock()
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
	root, dir := makeTestRoot(t)
	runner := newControlledRunner()
	manager := NewJobManager(root, runner)
	t.Cleanup(manager.Close)
	return manager, runner, dir
}

func submitTestJob(t *testing.T, manager *JobManager, dir, name string) *Job {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, name))
	job, err := manager.Submit(name, "balanced", "mkv", false, 0)
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
	server, err := NewServer(t.TempDir(), "unused-shrinkray", t.TempDir(), "test")
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
	if _, err := manager.Submit("movie.mkv", "balanced", "mkv", false, 0); err == nil {
		t.Fatal("Submit accepted a job whose output exists")
	}
}

func TestCancelRunningJob(t *testing.T) {
	manager, runner, dir := managerFixture(t)
	job := submitTestJob(t, manager, dir, "movie.mkv")
	<-runner.started
	if err := manager.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	waitForState(t, manager, job.ID, StateCancelled)
}

func TestSubmitRejectsDirectory(t *testing.T) {
	manager, _, dir := managerFixture(t)
	if err := os.Mkdir(filepath.Join(dir, "folder.mkv"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("folder.mkv", "balanced", "mkv", false, 0); err == nil {
		t.Fatal("Submit accepted a directory")
	}
}
