package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	jobStateVersion         = 1
	terminalHistoryLimit    = 250
	progressPersistInterval = 3 * time.Second
)

const interruptedByRestartMessage = "Shrinkray restarted while this encode was running. The source file was left untouched. Start a new job to retry the encode."

const interruptedReplacementAttentionMessage = "Shrinkray restarted during a safe replacement transaction. Ambiguous files were left in place; inspect the recovery log before retrying."

var ErrJobPersistence = errors.New("job persistence failed")

type persistedJobState struct {
	Version int    `json:"version"`
	NextID  uint64 `json:"next_id"`
	Jobs    []*Job `json:"jobs"`
}

type jobStateSnapshot struct {
	revision uint64
	state    persistedJobState
}

type jobStateStore struct {
	mu           sync.Mutex
	directory    string
	path         string
	renameFile   func(string, string) error
	now          func() time.Time
	lastRevision uint64
	disabled     bool
}

func newJobStateStore(directory string) *jobStateStore {
	return &jobStateStore{
		directory:  directory,
		path:       filepath.Join(directory, "jobs.json"),
		renameFile: os.Rename,
		now:        time.Now,
	}
}

func (s *jobStateStore) Load() (persistedJobState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return persistedJobState{}, fmt.Errorf("create job state directory: %w", err)
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyPersistedJobState(), nil
	}
	if err != nil {
		return persistedJobState{}, fmt.Errorf("read job state: %w", err)
	}

	var state persistedJobState
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&state); err != nil {
		return s.preserveCorruptLocked(fmt.Errorf("decode job state: %w", err))
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return s.preserveCorruptLocked(errors.New("job state contains trailing data"))
	}
	if state.Version != jobStateVersion {
		return s.preserveCorruptLocked(fmt.Errorf("unsupported job state version %d", state.Version))
	}
	if state.Jobs == nil {
		state.Jobs = []*Job{}
	}
	return state, nil
}

func emptyPersistedJobState() persistedJobState {
	return persistedJobState{Version: jobStateVersion, NextID: 1, Jobs: []*Job{}}
}

func (s *jobStateStore) preserveCorruptLocked(reason error) (persistedJobState, error) {
	stamp := s.now().UTC().Format("20060102-150405")
	backup := s.path + ".corrupt-" + stamp
	for suffix := 1; ; suffix++ {
		if _, err := os.Lstat(backup); errors.Is(err, os.ErrNotExist) {
			break
		}
		backup = fmt.Sprintf("%s.corrupt-%s-%d", s.path, stamp, suffix)
	}
	if err := s.renameFile(s.path, backup); err != nil {
		s.disabled = true
		log.Printf("WARNING: job history is corrupt (%v) and could not be preserved: %v; persistence is disabled for this run", reason, err)
		return emptyPersistedJobState(), nil
	}
	log.Printf("WARNING: job history is corrupt (%v); preserved it as %s and started with empty history", reason, filepath.Base(backup))
	return emptyPersistedJobState(), nil
}

func (s *jobStateStore) Save(snapshot jobStateSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled {
		return errors.New("job persistence is disabled because corrupt state could not be preserved")
	}
	if snapshot.revision <= s.lastRevision {
		return nil
	}
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("create job state directory: %w", err)
	}
	temporary, err := os.CreateTemp(s.directory, ".jobs.json.tmp-")
	if err != nil {
		return fmt.Errorf("create temporary job state: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		_ = temporary.Close()
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary job state permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot.state); err != nil {
		return fmt.Errorf("encode job state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary job state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary job state: %w", err)
	}
	if err := s.renameFile(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace job state: %w", err)
	}
	keepTemporary = true
	if err := os.Chmod(s.path, 0o600); err != nil {
		// The temporary file was already mode 0600 before the atomic rename. A
		// post-rename chmod failure must not report that persistence failed after
		// the new state is already visible on disk.
		log.Printf("WARNING: could not re-apply job state permissions after replacement: %v", err)
	}
	if directory, err := os.Open(s.directory); err == nil {
		if syncErr := directory.Sync(); syncErr != nil {
			log.Printf("WARNING: could not sync job state directory: %v", syncErr)
		}
		_ = directory.Close()
	}
	s.lastRevision = snapshot.revision
	return nil
}

func (m *JobManager) markPersistenceDirtyLocked() {
	if m.store == nil {
		return
	}
	if m.persistenceRevision < math.MaxUint64 {
		m.persistenceRevision++
	}
}

func (m *JobManager) persistenceSnapshotLocked() jobStateSnapshot {
	now := time.Now()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, cloneJob(job, now))
	}
	nextID := m.nextID
	if nextID < math.MaxUint64 {
		nextID++
	}
	if nextID == 0 {
		nextID = 1
	}
	return jobStateSnapshot{
		revision: m.persistenceRevision,
		state:    persistedJobState{Version: jobStateVersion, NextID: nextID, Jobs: jobs},
	}
}

func (m *JobManager) persistNow() error {
	if m.store == nil {
		return nil
	}
	m.mu.Lock()
	snapshot := m.persistenceSnapshotLocked()
	m.mu.Unlock()
	return m.store.Save(snapshot)
}

func (m *JobManager) persistBestEffort(reason string, retries int) {
	var err error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
		}
		if err = m.persistNow(); err == nil {
			return
		}
	}
	log.Printf("ERROR: could not persist job history after %s: %v", reason, err)
}

func (m *JobManager) restore(state persistedJobState) bool {
	recoveryTime := time.Now().UTC()
	changed := false
	maxID := uint64(0)
	if state.NextID > 0 {
		m.nextID = state.NextID - 1
	}
	for _, restored := range state.Jobs {
		if restored == nil || strings.TrimSpace(restored.ID) == "" {
			changed = true
			continue
		}
		job := cloneJob(restored, recoveryTime)
		job.cancel = nil
		job.outputAbs = ""
		job.reservationKeys = nil
		job.lastProgressPersist = time.Time{}
		if job.Settings.RequestedEncoder == "" {
			job.Settings.RequestedEncoder = "software"
			changed = true
		}
		if job.Settings.ReplaceOriginal && job.TransactionID == "" && job.State == StateQueued {
			job.TransactionID = newTransactionID(job.ID)
			changed = true
		}
		if job.Logs == nil {
			job.Logs = []string{}
		}
		if len(job.Logs) > 30 {
			job.Logs = append([]string(nil), job.Logs[len(job.Logs)-30:]...)
			changed = true
		}
		if parsed, err := strconv.ParseUint(job.ID, 10, 64); err == nil && parsed > maxID {
			maxID = parsed
		}

		switch job.State {
		case StateCompleted, StateFailed, StateCancelled:
			if job.Settings.ReplaceOriginal && !job.SourceReplaced && job.TransactionID != "" {
				m.recoverReplacementArtifacts(job)
				changed = true
			}
		case StateRunning:
			m.recoverInterruptedJob(job, recoveryTime)
			changed = true
		case StateQueued:
			if err := m.restoreQueuedJob(job, recoveryTime); err != nil {
				markRestoredJobFailed(job, recoveryTime, err.Error())
				changed = true
			}
		default:
			markRestoredJobFailed(job, recoveryTime, "The saved job had an unrecognized state and was not started.")
			changed = true
		}
		m.jobs = append(m.jobs, job)
		if job.State == StateQueued {
			m.pending = append(m.pending, job)
			reservePaths(m.reserved, job.reservationKeys)
		}
	}
	if maxID > m.nextID {
		m.nextID = maxID
	}
	if pruneTerminalHistory(&m.jobs) > 0 {
		changed = true
	}
	return changed
}

func (m *JobManager) restoreQueuedJob(job *Job, recoveryTime time.Time) error {
	if job.Settings.ReplaceOriginal && !validTransactionID(job.TransactionID) {
		return errors.New("Queued replacement job has an invalid transaction identifier.")
	}
	output, err := m.expectedOutput(job)
	if err != nil {
		return fmt.Errorf("Queued job could not be restored: %w", err)
	}
	mediaRoot, _ := m.roots.Get(job.RootID)
	source, _, _ := mediaRoot.Root.ResolveVideo(job.Path)
	keys := jobReservationKeys(source, output)
	if reservedPath(m.reserved, keys) {
		return errors.New("Queued job could not be restored because another queued job targets the same output.")
	}
	if output != source {
		if _, err := os.Lstat(output); err == nil {
			return errors.New("Queued job was not restarted because its intended output already exists.")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("Queued job was not restarted because its intended output could not be inspected.")
		}
	}
	job.outputAbs = output
	job.reservationKeys = keys
	job.State = StateQueued
	job.Stage = "Waiting"
	job.StartedAt = nil
	job.FinishedAt = nil
	job.ElapsedSeconds = 0
	job.RootLabel = currentRootLabel(m.roots, job.RootID, job.RootLabel)
	_ = recoveryTime
	return nil
}

func (m *JobManager) expectedOutput(job *Job) (string, error) {
	mediaRoot, err := m.roots.Get(job.RootID)
	if err != nil {
		return "", errors.New("configured media root is no longer available")
	}
	if job.Settings.Container != "mkv" && job.Settings.Container != "mp4" {
		return "", errors.New("saved output container is invalid")
	}
	source, clean, err := mediaRoot.Root.ResolveVideo(job.Path)
	if err != nil {
		return "", errors.New("source movie is no longer available")
	}
	if clean != filepath.ToSlash(filepath.Clean(job.Path)) {
		return "", errors.New("saved source path is invalid")
	}
	output := intendedOutputPath(source, job.Settings.Container, job.Settings.ReplaceOriginal)
	relativeOutput, err := filepathRelSlash(mediaRoot.Root.Path(), output)
	if err != nil || relativeOutput != job.OutputPath {
		return "", errors.New("saved output path does not match the expected Shrinkray output")
	}
	return output, nil
}

func currentRootLabel(roots *RootRegistry, rootID, fallback string) string {
	if root, err := roots.Get(rootID); err == nil {
		return root.Label
	}
	return fallback
}

func (m *JobManager) recoverInterruptedJob(job *Job, recoveryTime time.Time) {
	job.State = StateFailed
	job.Stage = "Interrupted by restart"
	job.Failure = interruptedByRestartMessage
	job.FinishedAt = &recoveryTime
	job.ETASeconds = nil
	job.ETAIsEstimate = false
	if job.Settings.ReplaceOriginal {
		m.recoverReplacementArtifacts(job)
		if job.OriginalKept {
			job.Failure = "Shrinkray restarted during replacement recovery. The original was kept; inspect the recovery log before retrying."
		} else {
			job.Failure = interruptedReplacementAttentionMessage
		}
		return
	}
	if output, err := m.expectedOutput(job); err == nil {
		part := output + ".part"
		if cleanupErr := removeInterruptedPart(m.roots, job.RootID, output, part); cleanupErr != nil {
			appendRecoveryLog(job, "!!  Could not safely remove interrupted temporary output: "+cleanupErr.Error())
		}
	} else {
		appendRecoveryLog(job, "!!  Interrupted temporary output was left in place because its path could not be verified: "+err.Error())
	}
}

func (m *JobManager) recoverReplacementArtifacts(job *Job) {
	source, err := m.savedSourcePath(job)
	if err != nil {
		appendRecoveryLog(job, "!!  Replacement recovery attention required; saved source path could not be verified: "+err.Error())
		return
	}
	output := intendedOutputPath(source, job.Settings.Container, true)
	if !validTransactionID(job.TransactionID) {
		appendRecoveryLog(job, "!!  Replacement recovery attention required; the saved job has no valid transaction identifier.")
		return
	}
	temporary := filepath.Join(filepath.Dir(source), "."+filepath.Base(source)+".shrinkray-replace-"+job.TransactionID+".part."+job.Settings.Container)
	backup := filepath.Join(filepath.Dir(source), "."+filepath.Base(source)+".shrinkray-backup-"+job.TransactionID)

	backupInfo, backupErr := os.Lstat(backup)
	if backupErr == nil {
		if !backupInfo.Mode().IsRegular() || backupInfo.Mode()&os.ModeSymlink != 0 {
			appendRecoveryLog(job, "!!  Replacement recovery attention required; backup is not a regular file: "+backup)
			return
		}
		sourceInfo, sourceErr := os.Lstat(source)
		if sourceErr == nil && sourceInfo.Mode().IsRegular() && sourceInfo.Mode()&os.ModeSymlink == 0 && os.SameFile(sourceInfo, backupInfo) {
			if err := os.Remove(backup); err != nil {
				appendRecoveryLog(job, "!!  Could not remove proven-redundant replacement backup: "+backup+": "+err.Error())
			} else {
				appendRecoveryLog(job, "==> Recovered an interrupted transaction; the original remained in place and its redundant hard-link backup was removed.")
			}
			job.OriginalKept = true
			removeExactReplacementTemp(job, source, temporary)
			clearRecoveredTransactionIfClean(job, temporary, backup)
			return
		}
		appendRecoveryLog(job, "!!  Replacement recovery attention required; recoverable original backup was left at: "+backup)
		return
	}
	if !errors.Is(backupErr, os.ErrNotExist) {
		appendRecoveryLog(job, "!!  Replacement recovery attention required; backup could not be inspected: "+backup+": "+backupErr.Error())
		return
	}

	if sourceInfo, sourceErr := os.Lstat(source); sourceErr == nil && sourceInfo.Mode().IsRegular() && sourceInfo.Mode()&os.ModeSymlink == 0 {
		if output == source {
			if sourceInfo.Size() != job.OriginalSize {
				appendRecoveryLog(job, "!!  Replacement recovery attention required; the source path exists but its size does not prove whether replacement completed: "+source)
				return
			}
			job.OriginalKept = true
		} else {
			job.OriginalKept = true
			if _, finalErr := os.Lstat(output); finalErr == nil {
				appendRecoveryLog(job, "!!  Replacement recovery attention required; both source and expected final target were left in place: "+output)
				return
			}
		}
		removeExactReplacementTemp(job, source, temporary)
		clearRecoveredTransactionIfClean(job, temporary, backup)
		return
	}
	if output != source {
		if finalInfo, finalErr := os.Lstat(output); finalErr == nil && finalInfo.Mode().IsRegular() && finalInfo.Mode()&os.ModeSymlink == 0 {
			job.FinalPath = job.OutputPath
			appendRecoveryLog(job, "!!  Replacement recovery attention required; source is absent and the final target was left in place: "+output)
			return
		}
	}
	appendRecoveryLog(job, "!!  Replacement recovery attention required; Shrinkray could not prove a safe completed outcome.")
}

func (m *JobManager) savedSourcePath(job *Job) (string, error) {
	mediaRoot, err := m.roots.Get(job.RootID)
	if err != nil {
		return "", errors.New("configured media root is unavailable")
	}
	if filepath.IsAbs(job.Path) || containsDotDot(job.Path) {
		return "", errors.New("saved source path is invalid")
	}
	clean := filepath.Clean(job.Path)
	if clean == "." || filepath.ToSlash(clean) != filepath.ToSlash(job.Path) {
		return "", errors.New("saved source path is invalid")
	}
	parent, err := filepath.EvalSymlinks(filepath.Join(mediaRoot.Root.Path(), filepath.Dir(clean)))
	if err != nil {
		return "", errors.New("saved source directory is unavailable")
	}
	if _, err := filepathRelSlash(mediaRoot.Root.Path(), parent); err != nil {
		return "", errors.New("saved source directory is outside the configured media root")
	}
	return filepath.Join(parent, filepath.Base(clean)), nil
}

func removeExactReplacementTemp(job *Job, source, temporary string) {
	info, err := os.Lstat(temporary)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		appendRecoveryLog(job, "!!  Interrupted replacement temporary output was left in place because it was not a regular Shrinkray-owned file: "+temporary)
		return
	}
	expected := filepath.Join(filepath.Dir(source), "."+filepath.Base(source)+".shrinkray-replace-"+job.TransactionID+".part."+job.Settings.Container)
	if temporary != expected {
		appendRecoveryLog(job, "!!  Interrupted replacement temporary output was left in place because its path could not be verified: "+temporary)
		return
	}
	if err := os.Remove(temporary); err != nil {
		appendRecoveryLog(job, "!!  Could not remove interrupted replacement temporary output: "+temporary+": "+err.Error())
	}
}

func clearRecoveredTransactionIfClean(job *Job, temporary, backup string) {
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		return
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return
	}
	job.TransactionID = ""
}

func removeInterruptedPart(roots *RootRegistry, rootID, output, part string) error {
	mediaRoot, err := roots.Get(rootID)
	if err != nil {
		return errors.New("configured media root is unavailable")
	}
	if part != output+".part" || part == output || strings.TrimSuffix(part, ".part") != output {
		return errors.New("temporary path does not exactly match the expected output")
	}
	relative, err := filepath.Rel(mediaRoot.Root.Path(), part)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("temporary path is outside the configured media root")
	}
	info, err := os.Lstat(part)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect temporary output: %w", err)
	}
	if info.IsDir() {
		return errors.New("temporary output path is a directory")
	}
	if err := os.Remove(part); err != nil {
		return fmt.Errorf("remove temporary output: %w", err)
	}
	return nil
}

func appendRecoveryLog(job *Job, line string) {
	job.Logs = append(job.Logs, line)
	if len(job.Logs) > 30 {
		job.Logs = append([]string(nil), job.Logs[len(job.Logs)-30:]...)
	}
}

func markRestoredJobFailed(job *Job, recoveryTime time.Time, message string) {
	job.State = StateFailed
	job.Stage = "Failed to restore queued job"
	job.Failure = message
	job.FinishedAt = &recoveryTime
	job.ETASeconds = nil
	job.ETAIsEstimate = false
	job.outputAbs = ""
}

func isTerminalState(state JobState) bool {
	return state == StateCompleted || state == StateFailed || state == StateCancelled
}

func pruneTerminalHistory(jobs *[]*Job) int {
	terminal := make([]int, 0)
	for index, job := range *jobs {
		if isTerminalState(job.State) {
			terminal = append(terminal, index)
		}
	}
	if len(terminal) <= terminalHistoryLimit {
		return 0
	}
	sort.SliceStable(terminal, func(i, j int) bool {
		return terminalJobTime((*jobs)[terminal[i]]).After(terminalJobTime((*jobs)[terminal[j]]))
	})
	keep := make(map[int]bool, terminalHistoryLimit)
	for _, index := range terminal[:terminalHistoryLimit] {
		keep[index] = true
	}
	filtered := make([]*Job, 0, len(*jobs)-(len(terminal)-terminalHistoryLimit))
	for index, job := range *jobs {
		if !isTerminalState(job.State) || keep[index] {
			filtered = append(filtered, job)
		}
	}
	removed := len(*jobs) - len(filtered)
	*jobs = filtered
	return removed
}

func terminalJobTime(job *Job) time.Time {
	if job.FinishedAt != nil {
		return *job.FinishedAt
	}
	if job.StartedAt != nil {
		return *job.StartedAt
	}
	return job.QueuedAt
}
