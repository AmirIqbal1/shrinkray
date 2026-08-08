package dashboard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
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
	Preset           string `json:"preset"`
	Quality          string `json:"quality"`
	Container        string `json:"container"`
	KeepAllAudio     bool   `json:"keep_all_audio"`
	TargetMB         int64  `json:"target_mb"`
	RequestedEncoder string `json:"requested_encoder,omitempty"`
}

type Job struct {
	ID                   string      `json:"id"`
	RootID               string      `json:"root_id"`
	RootLabel            string      `json:"root_label"`
	Path                 string      `json:"path"`
	Filename             string      `json:"filename"`
	OutputPath           string      `json:"output_path"`
	OriginalSize         int64       `json:"original_size"`
	Settings             JobSettings `json:"settings"`
	State                JobState    `json:"state"`
	Stage                string      `json:"stage"`
	QueuedAt             time.Time   `json:"queued_at"`
	StartedAt            *time.Time  `json:"started_at,omitempty"`
	FinishedAt           *time.Time  `json:"finished_at,omitempty"`
	ElapsedSeconds       int64       `json:"elapsed_seconds"`
	ProgressPercent      float64     `json:"progress_percent"`
	StageProgressPercent float64     `json:"stage_progress_percent"`
	DurationSeconds      float64     `json:"duration_seconds"`
	ProcessedSeconds     float64     `json:"processed_seconds"`
	ETASeconds           *float64    `json:"eta_seconds"`
	EncodeSpeed          float64     `json:"encode_speed"`
	ETAIsEstimate        bool        `json:"eta_is_estimate"`
	DiskAvailableBytes   uint64      `json:"disk_available_bytes,omitempty"`
	DiskRequiredBytes    uint64      `json:"disk_required_bytes,omitempty"`
	DiskSafetyReserve    uint64      `json:"disk_safety_reserve_bytes,omitempty"`
	DiskSpaceWarning     bool        `json:"disk_space_warning,omitempty"`
	Logs                 []string    `json:"logs"`
	ResultSize           int64       `json:"result_size,omitempty"`
	SavedPercent         float64     `json:"saved_percent,omitempty"`
	Failure              string      `json:"failure,omitempty"`
	ActualEncoder        string      `json:"actual_encoder,omitempty"`

	cancel              context.CancelFunc
	outputAbs           string
	lastProgressPersist time.Time
}

type RunResult struct {
	Size int64
}

type JobRunner interface {
	Run(context.Context, *Job, func(string), func(ProgressUpdate), func(DiskSpaceUpdate), func(string)) (RunResult, error)
}

type CLIRunner struct {
	roots         *RootRegistry
	shrinkrayBin  string
	diskSpace     DiskSpaceChecker
	probeDuration func(context.Context, string) (float64, error)
}

func NewCLIRunner(roots *RootRegistry, shrinkrayBin string) *CLIRunner {
	return newCLIRunnerWithDiskSpace(roots, shrinkrayBin, FilesystemDiskSpaceChecker{})
}

func newCLIRunnerWithDiskSpace(roots *RootRegistry, shrinkrayBin string, checker DiskSpaceChecker) *CLIRunner {
	return &CLIRunner{roots: roots, shrinkrayBin: shrinkrayBin, diskSpace: checker, probeDuration: probeSourceDuration}
}

func (r *CLIRunner) Run(ctx context.Context, job *Job, stage func(string), progress func(ProgressUpdate), disk func(DiskSpaceUpdate), logLine func(string)) (RunResult, error) {
	stage("Inspecting")
	mediaRoot, err := r.roots.Get(job.RootID)
	if err != nil {
		return RunResult{}, errors.New("configured media root is no longer available")
	}
	source, _, err := mediaRoot.Root.ResolveVideo(job.Path)
	if err != nil {
		return RunResult{}, errors.New("source movie is no longer available")
	}
	output := outputPath(source, job.Settings.Container)
	if job.outputAbs != "" && output != job.outputAbs {
		return RunResult{}, errors.New("intended output path changed")
	}
	if _, err := os.Lstat(output); err == nil {
		return RunResult{}, errors.New("intended output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RunResult{}, errors.New("could not inspect intended output")
	}
	targetBytes := TargetBytesFromMB(job.Settings.TargetMB)
	checkDisk := func() (DiskSpaceStatus, error) {
		status, checkErr := r.diskSpace.Check(filepath.Dir(output), targetBytes)
		if checkErr == nil {
			disk(diskSpaceUpdate(status))
		}
		return status, checkErr
	}
	status, err := checkDisk()
	if err != nil {
		return RunResult{}, errors.New("could not inspect free space on the destination filesystem")
	}
	if !status.Sufficient {
		stage(stageInsufficientDisk)
		return RunResult{}, insufficientDiskError(status)
	}

	duration, err := r.probeDuration(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return RunResult{}, context.Canceled
		}
		return RunResult{}, errors.New("ffprobe could not read the source duration")
	}
	progress(ProgressUpdate{DurationSeconds: duration})

	requestedEncoder := job.Settings.RequestedEncoder
	if requestedEncoder == "" {
		requestedEncoder = "software"
	}
	args := []string{source, "--size", strconv.FormatInt(job.Settings.TargetMB, 10), "--quality", job.Settings.Quality, "--container", job.Settings.Container, "--encoder", requestedEncoder, "--machine-progress"}
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
	status, err = checkDisk()
	if err != nil {
		writer.Close()
		return RunResult{}, errors.New("could not recheck free space on the destination filesystem")
	}
	if !status.Sufficient {
		writer.Close()
		stage(stageInsufficientDisk)
		return RunResult{}, insufficientDiskError(status)
	}
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
	progressParser := newProgressParser()
	currentStage := "Inspecting"
	criticalDisk := false
	diskCheckWarningLogged := false
	for scanner.Scan() {
		line := r.roots.Redact(scanner.Text())
		if record, complete, machineLine := progressParser.Consume(line); machineLine {
			if complete {
				progress(progressFromRecord(currentStage, duration, record))
				status, checkErr := checkDisk()
				if checkErr != nil {
					if !diskCheckWarningLogged {
						logLine("!!  Could not refresh destination disk space while encoding.")
						diskCheckWarningLogged = true
					}
				} else if status.AvailableBytes < CriticalFreeSpaceBytes && !criticalDisk {
					criticalDisk = true
					currentStage = stageCriticalDisk
					stage(stageCriticalDisk)
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				}
			}
			continue
		}
		if isRepetitiveMempolicyWarning(line) {
			continue
		}
		logLine(line)
		if parsed := stageFromLog(line); parsed != "" {
			currentStage = parsed
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
	if criticalDisk {
		return RunResult{}, criticalDiskError()
	}
	if waitErr != nil {
		if currentStage == stageCriticalDisk {
			return RunResult{}, criticalDiskError()
		}
		return RunResult{}, errors.New("shrinkray exited unsuccessfully")
	}
	info, err := os.Stat(output)
	if err != nil || !info.Mode().IsRegular() {
		return RunResult{}, errors.New("shrinkray did not create the expected output")
	}
	return RunResult{Size: info.Size()}, nil
}

func insufficientDiskError(status DiskSpaceStatus) error {
	return fmt.Errorf("Not enough free disk space. Available: %s. Required: %s. Shrinkray will not start this encode: %w", FormatBytes(status.AvailableBytes), FormatBytes(status.RequiredBytes), ErrInsufficientDiskSpace)
}

func criticalDiskError() error {
	return fmt.Errorf("Critical low disk space. Encoding stopped before the destination filesystem was exhausted: %w", ErrCriticalDiskSpace)
}

func probeSourceDuration(ctx context.Context, source string) (float64, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", source).Output()
	if err != nil {
		return 0, err
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return 0, errors.New("invalid source duration")
	}
	return duration, nil
}

func stageFromLog(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "pass 1 of 2"):
		return stageHEVCPass1
	case strings.Contains(lower, "pass 2 of 2"):
		return stageHEVCPass2
	case strings.Contains(lower, "encoding with av1"):
		return stageAV1
	case strings.Contains(lower, "hevc hardware") && strings.Contains(lower, "intel qsv"):
		return stageHardwareQSV
	case strings.Contains(lower, "hevc hardware") && strings.Contains(lower, "vaapi"):
		return stageHardwareVAAPI
	case strings.Contains(lower, "hevc hardware") && strings.Contains(lower, "nvidia nvenc"):
		return stageHardwareNVENC
	case strings.Contains(lower, "validating output"):
		return stageValidation
	case strings.Contains(lower, "insufficient disk space"), strings.Contains(lower, "not enough free disk space"):
		return stageInsufficientDisk
	case strings.Contains(lower, "critical low disk space"):
		return stageCriticalDisk
	default:
		return ""
	}
}

type JobManager struct {
	mu                  sync.Mutex
	cond                *sync.Cond
	roots               *RootRegistry
	runner              JobRunner
	diskSpace           DiskSpaceChecker
	store               *jobStateStore
	jobs                []*Job
	pending             []*Job
	reserved            map[string]bool
	closed              bool
	nextID              uint64
	persistenceRevision uint64
	workerDone          chan struct{}
	closeOnce           sync.Once
}

func NewJobManager(roots *RootRegistry, runner JobRunner) *JobManager {
	return newJobManagerWithDiskSpace(roots, runner, nil)
}

func newJobManagerWithDiskSpace(roots *RootRegistry, runner JobRunner, checker DiskSpaceChecker) *JobManager {
	m := &JobManager{roots: roots, runner: runner, diskSpace: checker, reserved: make(map[string]bool), workerDone: make(chan struct{})}
	m.cond = sync.NewCond(&m.mu)
	go m.worker()
	return m
}

func newPersistentJobManager(roots *RootRegistry, runner JobRunner, checker DiskSpaceChecker, stateDir string) (*JobManager, error) {
	store := newJobStateStore(stateDir)
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	m := &JobManager{roots: roots, runner: runner, diskSpace: checker, store: store, reserved: make(map[string]bool), workerDone: make(chan struct{})}
	m.cond = sync.NewCond(&m.mu)
	if m.restore(state) {
		m.markPersistenceDirtyLocked()
		if err := store.Save(m.persistenceSnapshotLocked()); err != nil {
			log.Printf("ERROR: could not persist recovered job history: %v", err)
		}
	}
	go m.worker()
	return m, nil
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

func (m *JobManager) Submit(rootID, path, preset, container string, keepAllAudio bool, exactMB int64, requestedEncoders ...string) (*Job, error) {
	requestedEncoder := "auto"
	if len(requestedEncoders) > 0 && requestedEncoders[0] != "" {
		requestedEncoder = requestedEncoders[0]
	}
	if !validRequestedEncoder(requestedEncoder) {
		return nil, errors.New("encoder must be auto, software, qsv, vaapi, or nvenc")
	}
	mediaRoot, err := m.roots.Get(rootID)
	if err != nil {
		return nil, ErrUnknownRoot
	}
	source, clean, err := mediaRoot.Root.ResolveVideo(path)
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
	relOutput, err := filepathRelSlash(mediaRoot.Root.Path(), output)
	if err != nil {
		return nil, errors.New("invalid intended output")
	}
	var initialDisk DiskSpaceStatus
	if m.diskSpace != nil {
		initialDisk, _ = m.diskSpace.Check(filepath.Dir(output), TargetBytesFromMB(target))
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("job queue is shutting down")
	}
	if m.reserved[output] {
		m.mu.Unlock()
		return nil, errors.New("a job already targets that output")
	}
	if _, err := os.Lstat(output); err == nil {
		m.mu.Unlock()
		return nil, errors.New("intended output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		m.mu.Unlock()
		return nil, errors.New("could not inspect intended output")
	}
	if m.nextID == math.MaxUint64 {
		m.mu.Unlock()
		return nil, errors.New("job ID space is exhausted")
	}
	m.nextID++
	job := &Job{
		ID: strconv.FormatUint(m.nextID, 10), RootID: mediaRoot.ID, RootLabel: mediaRoot.Label,
		Path: clean, Filename: filepathBase(clean), OutputPath: relOutput,
		OriginalSize: info.Size(), Settings: JobSettings{Preset: preset, Quality: quality, Container: container, KeepAllAudio: keepAllAudio, TargetMB: target, RequestedEncoder: requestedEncoder},
		State: StateQueued, Stage: "Waiting", QueuedAt: time.Now().UTC(), Logs: []string{}, outputAbs: output,
		DiskAvailableBytes: initialDisk.AvailableBytes, DiskRequiredBytes: initialDisk.RequiredBytes,
		DiskSafetyReserve: initialDisk.ReserveBytes, DiskSpaceWarning: initialDisk.Warning,
	}
	m.jobs = append(m.jobs, job)
	m.reserved[output] = true
	m.markPersistenceDirtyLocked()
	if m.store != nil {
		if err := m.store.Save(m.persistenceSnapshotLocked()); err != nil {
			m.jobs = m.jobs[:len(m.jobs)-1]
			delete(m.reserved, output)
			m.mu.Unlock()
			log.Printf("ERROR: could not persist submitted job: %v", err)
			return nil, fmt.Errorf("could not persist the submitted job: %w", ErrJobPersistence)
		}
	}
	m.pending = append(m.pending, job)
	m.cond.Signal()
	result := cloneJob(job, time.Now())
	m.mu.Unlock()
	return result, nil
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
	for _, job := range m.jobs {
		if job.ID != id {
			continue
		}
		switch job.State {
		case StateQueued:
			now := time.Now().UTC()
			job.State, job.Stage, job.FinishedAt = StateCancelled, "Cancelled", &now
			delete(m.reserved, job.outputAbs)
			m.removePendingLocked(job)
			pruneTerminalHistory(&m.jobs)
			m.markPersistenceDirtyLocked()
			snapshot := m.persistenceSnapshotLocked()
			m.mu.Unlock()
			if m.store != nil {
				if err := m.store.Save(snapshot); err != nil {
					log.Printf("ERROR: could not persist queued job cancellation: %v", err)
					return fmt.Errorf("job was cancelled, but its history could not be persisted: %w", ErrJobPersistence)
				}
			}
			return nil
		case StateRunning:
			if job.cancel != nil {
				job.cancel()
			}
			m.mu.Unlock()
			return nil
		default:
			m.mu.Unlock()
			return errors.New("job can no longer be cancelled")
		}
	}
	m.mu.Unlock()
	return errors.New("job not found")
}

func (m *JobManager) removePendingLocked(target *Job) {
	for index, job := range m.pending {
		if job == target {
			m.pending = append(m.pending[:index], m.pending[index+1:]...)
			return
		}
	}
}

func (m *JobManager) ClearHistory() (int, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, errors.New("job queue is shutting down")
	}
	kept := make([]*Job, 0, len(m.jobs))
	removed := 0
	for _, job := range m.jobs {
		if isTerminalState(job.State) {
			removed++
			continue
		}
		kept = append(kept, job)
	}
	if removed == 0 {
		m.mu.Unlock()
		return 0, nil
	}
	m.jobs = kept
	m.markPersistenceDirtyLocked()
	snapshot := m.persistenceSnapshotLocked()
	m.mu.Unlock()
	if m.store != nil {
		if err := m.store.Save(snapshot); err != nil {
			log.Printf("ERROR: could not persist cleared job history: %v", err)
			return removed, fmt.Errorf("history was cleared in memory, but the change could not be persisted: %w", ErrJobPersistence)
		}
	}
	return removed, nil
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
		job.FinishedAt = nil
		job.lastProgressPersist = now
		m.markPersistenceDirtyLocked()
		startSnapshot := m.persistenceSnapshotLocked()
		m.mu.Unlock()
		if m.store != nil {
			if err := m.store.Save(startSnapshot); err != nil {
				log.Printf("ERROR: could not persist running job transition: %v", err)
				cancel()
				m.mu.Lock()
				finished := time.Now().UTC()
				job.State, job.Stage, job.Failure, job.FinishedAt, job.cancel = StateFailed, "Persistence error", "Shrinkray could not safely record that this job started, so the encode was not launched.", &finished, nil
				delete(m.reserved, job.outputAbs)
				pruneTerminalHistory(&m.jobs)
				m.markPersistenceDirtyLocked()
				m.mu.Unlock()
				m.persistBestEffort("recording a persistence failure", 2)
				continue
			}
		}

		result, err := m.runner.Run(ctx, cloneJob(job, time.Now()), func(stage string) {
			persist := false
			m.mu.Lock()
			if job.State == StateRunning {
				previous := job.Stage
				updateJobStage(job, stage)
				if actual := actualEncoderFromStage(stage); actual != "" {
					job.ActualEncoder = actual
				}
				if previous != job.Stage {
					job.lastProgressPersist = time.Now()
					m.markPersistenceDirtyLocked()
					persist = true
				}
			}
			m.mu.Unlock()
			if persist {
				m.persistBestEffort("a stage change", 0)
			}
		}, func(update ProgressUpdate) {
			persist := false
			m.mu.Lock()
			if job.State == StateRunning {
				job.ProgressPercent = update.ProgressPercent
				job.StageProgressPercent = update.StageProgressPercent
				job.DurationSeconds = update.DurationSeconds
				job.ProcessedSeconds = update.ProcessedSeconds
				job.ETASeconds = update.ETASeconds
				job.EncodeSpeed = update.EncodeSpeed
				job.ETAIsEstimate = update.ETAIsEstimate
				now := time.Now()
				if now.Sub(job.lastProgressPersist) >= progressPersistInterval {
					job.lastProgressPersist = now
					m.markPersistenceDirtyLocked()
					persist = true
				}
			}
			m.mu.Unlock()
			if persist {
				m.persistBestEffort("a progress checkpoint", 0)
			}
		}, func(update DiskSpaceUpdate) {
			persist := false
			m.mu.Lock()
			if job.State == StateRunning {
				job.DiskAvailableBytes = update.AvailableBytes
				job.DiskRequiredBytes = update.RequiredBytes
				job.DiskSafetyReserve = update.ReserveBytes
				job.DiskSpaceWarning = update.Warning
				now := time.Now()
				if now.Sub(job.lastProgressPersist) >= progressPersistInterval {
					job.lastProgressPersist = now
					m.markPersistenceDirtyLocked()
					persist = true
				}
			}
			m.mu.Unlock()
			if persist {
				m.persistBestEffort("a disk-space checkpoint", 0)
			}
		}, func(line string) {
			persist := false
			m.mu.Lock()
			job.Logs = append(job.Logs, line)
			if len(job.Logs) > 30 {
				job.Logs = append([]string(nil), job.Logs[len(job.Logs)-30:]...)
			}
			now := time.Now()
			if job.State == StateRunning && now.Sub(job.lastProgressPersist) >= progressPersistInterval {
				job.lastProgressPersist = now
				m.markPersistenceDirtyLocked()
				persist = true
			}
			m.mu.Unlock()
			if persist {
				m.persistBestEffort("a log checkpoint", 0)
			}
		})
		wasCancelled := ctx.Err() != nil
		cancel()

		m.mu.Lock()
		if m.closed {
			job.cancel = nil
			m.markPersistenceDirtyLocked()
			m.mu.Unlock()
			m.persistBestEffort("shutdown", 2)
			continue
		}
		finished := time.Now().UTC()
		job.FinishedAt, job.cancel = &finished, nil
		if errors.Is(err, context.Canceled) || wasCancelled {
			job.State, job.Stage = StateCancelled, "Cancelled"
		} else if err != nil {
			job.State, job.Failure = StateFailed, err.Error()
			switch {
			case errors.Is(err, ErrInsufficientDiskSpace):
				job.Stage = stageInsufficientDisk
			case errors.Is(err, ErrCriticalDiskSpace):
				job.Stage = stageCriticalDisk
			default:
				job.Stage = "Failed"
			}
		} else {
			job.State, job.Stage, job.ResultSize = StateCompleted, "Completed", result.Size
			job.ProgressPercent, job.StageProgressPercent = 100, 100
			job.ProcessedSeconds = job.DurationSeconds
			job.ETASeconds, job.ETAIsEstimate = nil, false
			if job.OriginalSize > 0 {
				job.SavedPercent = (1 - float64(result.Size)/float64(job.OriginalSize)) * 100
			}
		}
		delete(m.reserved, job.outputAbs)
		pruneTerminalHistory(&m.jobs)
		m.markPersistenceDirtyLocked()
		m.mu.Unlock()
		m.persistBestEffort("a terminal job transition", 2)
	}
}

func updateJobStage(job *Job, stage string) {
	previous := job.Stage
	job.Stage = stage
	if previous == stage {
		return
	}
	switch stage {
	case stageHEVCPass1, stageAV1, stageHardwareQSV, stageHardwareVAAPI, stageHardwareNVENC:
		job.ProgressPercent = 0
		job.StageProgressPercent = 0
		job.ProcessedSeconds = 0
		job.EncodeSpeed = 0
		job.ETASeconds, job.ETAIsEstimate = nil, false
	case stageHEVCPass2:
		job.ProgressPercent = 50
		job.StageProgressPercent = 0
		job.ProcessedSeconds = 0
		job.EncodeSpeed = 0
		job.ETASeconds, job.ETAIsEstimate = nil, false
	case stageValidation:
		job.ProgressPercent = 99
		job.ETASeconds, job.ETAIsEstimate = nil, false
	}
}

func actualEncoderFromStage(stage string) string {
	switch stage {
	case stageHEVCPass1, stageHEVCPass2:
		return "software"
	case stageHardwareQSV:
		return "qsv"
	case stageHardwareVAAPI:
		return "vaapi"
	case stageHardwareNVENC:
		return "nvenc"
	default:
		return ""
	}
}

func cloneJob(job *Job, now time.Time) *Job {
	copy := *job
	copy.cancel = nil
	if job.ETASeconds != nil {
		eta := *job.ETASeconds
		copy.ETASeconds = &eta
	}
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
		for _, job := range m.jobs {
			if job.State == StateRunning && job.cancel != nil {
				job.cancel()
			}
		}
		m.markPersistenceDirtyLocked()
		shutdownSnapshot := m.persistenceSnapshotLocked()
		m.cond.Broadcast()
		m.mu.Unlock()
		if m.store != nil {
			if err := m.store.Save(shutdownSnapshot); err != nil {
				log.Printf("ERROR: could not persist job state before shutdown: %v", err)
			}
		}
		<-m.workerDone
		m.persistBestEffort("final shutdown", 2)
	})
}

func (s JobSettings) String() string {
	audio := "first audio track"
	if s.KeepAllAudio {
		audio = "all audio tracks"
	}
	encoder := s.RequestedEncoder
	if encoder == "" {
		encoder = "software"
	}
	return fmt.Sprintf("%s, %s, %s, %s, encoder %s", s.Preset, s.Quality, strings.ToUpper(s.Container), audio, encoder)
}
