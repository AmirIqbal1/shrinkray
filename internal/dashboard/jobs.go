package dashboard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type JobState string

const (
	StateQueued    JobState = "queued"
	StateRunning   JobState = "running"
	StateCompleted JobState = "completed"
	StateFailed    JobState = "failed"
	StateCancelled JobState = "cancelled"
)

type JobSettings struct {
	Preset       string `json:"preset"`
	Quality      string `json:"quality"`
	Container    string `json:"container"`
	KeepAllAudio bool   `json:"keep_all_audio"`
	TargetMB     int64  `json:"target_mb"`
}

type Job struct {
	ID             string      `json:"id"`
	Path           string      `json:"path"`
	Filename       string      `json:"filename"`
	OutputPath     string      `json:"output_path"`
	OriginalSize   int64       `json:"original_size"`
	Settings       JobSettings `json:"settings"`
	State          JobState    `json:"state"`
	Stage          string      `json:"stage"`
	QueuedAt       time.Time   `json:"queued_at"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	FinishedAt     *time.Time  `json:"finished_at,omitempty"`
	ElapsedSeconds int64       `json:"elapsed_seconds"`
	Logs           []string    `json:"logs"`
	ResultSize     int64       `json:"result_size,omitempty"`
	SavedPercent   float64     `json:"saved_percent,omitempty"`
	Failure        string      `json:"failure,omitempty"`

	cancel context.CancelFunc
}

type RunResult struct {
	Size int64
}

type JobRunner interface {
	Run(context.Context, *Job, func(string), func(string)) (RunResult, error)
}

type CLIRunner struct {
	root         *SafeRoot
	shrinkrayBin string
}

func NewCLIRunner(root *SafeRoot, shrinkrayBin string) *CLIRunner {
	return &CLIRunner{root: root, shrinkrayBin: shrinkrayBin}
}

func (r *CLIRunner) Run(ctx context.Context, job *Job, stage func(string), logLine func(string)) (RunResult, error) {
	stage("Inspecting")
	source, _, err := r.root.ResolveVideo(job.Path)
	if err != nil {
		return RunResult{}, errors.New("source movie is no longer available")
	}
	output := outputPath(source, job.Settings.Container)
	if _, err := os.Lstat(output); err == nil {
		return RunResult{}, errors.New("intended output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RunResult{}, errors.New("could not inspect intended output")
	}

	args := []string{source, "--size", strconv.FormatInt(job.Settings.TargetMB, 10), "--quality", job.Settings.Quality, "--container", job.Settings.Container}
	if job.Settings.KeepAllAudio {
		args = append(args, "--keep-all-audio")
	}
	cmd := exec.Command(r.shrinkrayBin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	reader, writer, err := os.Pipe()
	if err != nil {
		return RunResult{}, errors.New("could not capture shrinkray output")
	}
	defer reader.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		writer.Close()
		return RunResult{}, errors.New("could not start shrinkray")
	}
	writer.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.ReplaceAll(scanner.Text(), r.root.Path(), "[movie root]")
		logLine(line)
		if parsed := stageFromLog(line); parsed != "" {
			stage(parsed)
		}
	}
	if scanner.Err() != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	waitErr := cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return RunResult{}, context.Canceled
	}
	if scanner.Err() != nil {
		return RunResult{}, errors.New("shrinkray produced an unreadable log line")
	}
	if waitErr != nil {
		return RunResult{}, errors.New("shrinkray exited unsuccessfully")
	}
	info, err := os.Stat(output)
	if err != nil || !info.Mode().IsRegular() {
		return RunResult{}, errors.New("shrinkray did not create the expected output")
	}
	return RunResult{Size: info.Size()}, nil
}

func stageFromLog(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "pass 1 of 2"):
		return "HEVC pass 1 of 2"
	case strings.Contains(lower, "pass 2 of 2"):
		return "HEVC pass 2 of 2"
	case strings.Contains(lower, "encoding with av1"):
		return "AV1 encoding"
	case strings.Contains(lower, "validating output"):
		return "Validating output"
	default:
		return ""
	}
}

type JobManager struct {
	mu         sync.Mutex
	cond       *sync.Cond
	root       *SafeRoot
	runner     JobRunner
	jobs       []*Job
	pending    []*Job
	reserved   map[string]bool
	closed     bool
	nextID     uint64
	workerDone chan struct{}
	closeOnce  sync.Once
}

func NewJobManager(root *SafeRoot, runner JobRunner) *JobManager {
	m := &JobManager{root: root, runner: runner, reserved: make(map[string]bool), workerDone: make(chan struct{})}
	m.cond = sync.NewCond(&m.mu)
	go m.worker()
	return m
}

func CalculatePresetMB(size int64, preset string, exact int64) (int64, string, error) {
	var percent int64
	quality := "good"
	switch preset {
	case "balanced":
		percent = 60
	case "smaller":
		percent = 40
	case "better":
		percent, quality = 75, "best"
	case "exact":
		if exact <= 0 {
			return 0, "", errors.New("exact size must be a positive whole number of MB")
		}
		return exact, quality, nil
	default:
		return 0, "", errors.New("unknown preset")
	}
	bytes := (size*percent + 99) / 100
	mb := (bytes + 1048575) / 1048576
	if mb < 1 {
		mb = 1
	}
	return mb, quality, nil
}

func (m *JobManager) Submit(path, preset, container string, keepAllAudio bool, exactMB int64) (*Job, error) {
	source, clean, err := m.root.ResolveVideo(path)
	if err != nil {
		return nil, ErrInvalidPath
	}
	if container != "mkv" && container != "mp4" {
		return nil, errors.New("container must be mkv or mp4")
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, ErrInvalidPath
	}
	target, quality, err := CalculatePresetMB(info.Size(), preset, exactMB)
	if err != nil {
		return nil, err
	}
	output := outputPath(source, container)
	if _, err := os.Lstat(output); err == nil {
		return nil, errors.New("intended output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("could not inspect intended output")
	}
	relOutput, err := filepathRelSlash(m.root.Path(), output)
	if err != nil {
		return nil, errors.New("invalid intended output")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("job queue is shutting down")
	}
	if m.reserved[output] {
		return nil, errors.New("a job already targets that output")
	}
	if _, err := os.Lstat(output); err == nil {
		return nil, errors.New("intended output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("could not inspect intended output")
	}
	m.nextID++
	job := &Job{
		ID: strconv.FormatUint(m.nextID, 10), Path: clean, Filename: filepathBase(clean), OutputPath: relOutput,
		OriginalSize: info.Size(), Settings: JobSettings{Preset: preset, Quality: quality, Container: container, KeepAllAudio: keepAllAudio, TargetMB: target},
		State: StateQueued, Stage: "Waiting", QueuedAt: time.Now().UTC(), Logs: []string{},
	}
	m.jobs = append(m.jobs, job)
	m.pending = append(m.pending, job)
	m.reserved[output] = true
	m.cond.Signal()
	return cloneJob(job, time.Now()), nil
}

func filepathRelSlash(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return filepath.ToSlash(rel), nil
}

func filepathBase(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	return parts[len(parts)-1]
}

func (m *JobManager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	result := make([]*Job, 0, len(m.jobs))
	for i := len(m.jobs) - 1; i >= 0; i-- {
		result = append(result, cloneJob(m.jobs[i], now))
	}
	return result
}

func (m *JobManager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.ID != id {
			continue
		}
		switch job.State {
		case StateQueued:
			now := time.Now().UTC()
			job.State, job.Stage, job.FinishedAt = StateCancelled, "Cancelled", &now
			delete(m.reserved, outputPathFromJob(m.root.Path(), job))
		case StateRunning:
			if job.cancel != nil {
				job.cancel()
			}
		default:
			return errors.New("job can no longer be cancelled")
		}
		return nil
	}
	return errors.New("job not found")
}

func (m *JobManager) worker() {
	defer close(m.workerDone)
	for {
		m.mu.Lock()
		for len(m.pending) == 0 && !m.closed {
			m.cond.Wait()
		}
		if m.closed {
			m.mu.Unlock()
			return
		}
		job := m.pending[0]
		m.pending = m.pending[1:]
		if job.State == StateCancelled {
			m.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		now := time.Now().UTC()
		job.State, job.Stage, job.StartedAt, job.cancel = StateRunning, "Inspecting", &now, cancel
		m.mu.Unlock()

		result, err := m.runner.Run(ctx, cloneJob(job, time.Now()), func(stage string) {
			m.mu.Lock()
			if job.State == StateRunning {
				job.Stage = stage
			}
			m.mu.Unlock()
		}, func(line string) {
			m.mu.Lock()
			job.Logs = append(job.Logs, line)
			if len(job.Logs) > 30 {
				job.Logs = append([]string(nil), job.Logs[len(job.Logs)-30:]...)
			}
			m.mu.Unlock()
		})
		wasCancelled := ctx.Err() != nil
		cancel()

		m.mu.Lock()
		finished := time.Now().UTC()
		job.FinishedAt, job.cancel = &finished, nil
		if errors.Is(err, context.Canceled) || wasCancelled {
			job.State, job.Stage = StateCancelled, "Cancelled"
		} else if err != nil {
			job.State, job.Stage, job.Failure = StateFailed, "Failed", err.Error()
		} else {
			job.State, job.Stage, job.ResultSize = StateCompleted, "Completed", result.Size
			if job.OriginalSize > 0 {
				job.SavedPercent = (1 - float64(result.Size)/float64(job.OriginalSize)) * 100
			}
		}
		delete(m.reserved, outputPathFromJob(m.root.Path(), job))
		m.mu.Unlock()
	}
}

func outputPathFromJob(root string, job *Job) string {
	return filepath.Join(root, filepath.FromSlash(job.OutputPath))
}

func cloneJob(job *Job, now time.Time) *Job {
	copy := *job
	copy.cancel = nil
	copy.Logs = append(make([]string, 0, len(job.Logs)), job.Logs...)
	if job.StartedAt != nil {
		end := now
		if job.FinishedAt != nil {
			end = *job.FinishedAt
		}
		copy.ElapsedSeconds = int64(end.Sub(*job.StartedAt).Seconds())
	}
	return &copy
}

func (m *JobManager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		now := time.Now().UTC()
		for _, job := range m.jobs {
			switch job.State {
			case StateQueued:
				job.State, job.Stage, job.FinishedAt = StateCancelled, "Cancelled", &now
			case StateRunning:
				if job.cancel != nil {
					job.cancel()
				}
			}
		}
		m.cond.Broadcast()
		m.mu.Unlock()
		<-m.workerDone
	})
}

func (s JobSettings) String() string {
	audio := "first audio track"
	if s.KeepAllAudio {
		audio = "all audio tracks"
	}
	return fmt.Sprintf("%s, %s, %s, %s", s.Preset, s.Quality, strings.ToUpper(s.Container), audio)
}
