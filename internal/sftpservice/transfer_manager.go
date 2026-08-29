package sftpservice

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"nodeshell/internal/apperror"
	"nodeshell/internal/localpathguard"
)

// EventTransferTask is the Wails event name for task state updates.
const EventTransferTask = "transfer:task"

// TransferDirection indicates whether the file is being uploaded or downloaded.
type TransferDirection string

const (
	DirectionUpload   TransferDirection = "upload"
	DirectionDownload TransferDirection = "download"
)

// TransferState indicates the lifecycle state of a transfer task.
type TransferState string

const (
	StateQueued     TransferState = "queued"
	StateRunning    TransferState = "running"
	StateFinalizing TransferState = "finalizing"
	StateSucceeded  TransferState = "succeeded"
	StateFailed     TransferState = "failed"
	StateCancelled  TransferState = "cancelled"
)

// TransferTaskDTO is the public shape transmitted across IPC to the frontend.
type TransferTaskDTO struct {
	TaskID       string            `json:"taskId"`
	SessionID    string            `json:"sessionId"`
	SessionTitle string            `json:"sessionTitle"`
	Direction    TransferDirection `json:"direction"`
	Name         string            `json:"name"`
	RemotePath   string            `json:"remotePath"`
	Transferred  int64             `json:"transferred"`
	Total        int64             `json:"total"`
	State        TransferState     `json:"state"`
	Error        string            `json:"error,omitempty"`
	CreatedAt    int64             `json:"createdAt"`
	StartedAt    int64             `json:"startedAt,omitempty"`
	FinishedAt   int64             `json:"finishedAt,omitempty"`
	RetryOf      string            `json:"retryOf,omitempty"`
}

// Task represents an internal transfer task item managed by TransferManager.
type Task struct {
	id           string
	sessionID    string
	sessionTitle string
	direction    TransferDirection
	name         string
	remoteDir    string // for upload: target remote directory
	remotePath   string // for download: source remote file; for upload: remoteDir/name
	localPath    string // for upload: source file; for download: target local file
	transferred  int64
	total        int64
	state        TransferState
	err          string
	createdAt    int64
	startedAt    int64
	finishedAt   int64
	retryOf      string

	cancel context.CancelFunc
}

func (t *Task) toDTO() TransferTaskDTO {
	return TransferTaskDTO{
		TaskID:       t.id,
		SessionID:    t.sessionID,
		SessionTitle: t.sessionTitle,
		Direction:    t.direction,
		Name:         t.name,
		RemotePath:   t.remotePath,
		Transferred:  t.transferred,
		Total:        t.total,
		State:        t.state,
		Error:        t.err,
		CreatedAt:    t.createdAt,
		StartedAt:    t.startedAt,
		FinishedAt:   t.finishedAt,
		RetryOf:      t.retryOf,
	}
}

// TransferRunner is a custom transfer execution function (used for testing or custom engines).
type TransferRunner func(ctx context.Context, task *Task, onProgress func(transferred, total int64, finalizing bool)) error

// TransferManagerDeps wires dependencies for TransferManager.
type TransferManagerDeps struct {
	SFTP   *Service
	Sink   EventSink
	Home   string
	UUID   func() string
	Runner TransferRunner
}

// TransferManager manages multi-file transfers with global and per-session concurrency limits.
type TransferManager struct {
	mu             sync.RWMutex
	wg             sync.WaitGroup
	sftp           *Service
	sink           EventSink
	home           string
	uuid           func() string
	runner         TransferRunner
	tasks          map[string]*Task
	queue          []string // FIFO task IDs
	runningGlobal  int
	runningSession map[string]int
	closed         bool
}

type scheduledLaunch struct {
	task *Task
	ctx  context.Context
	dto  TransferTaskDTO
}

const (
	MaxGlobalConcurrentTransfers  = 3
	MaxSessionConcurrentTransfers = 1
)

// NewTransferManager constructs a TransferManager.
func NewTransferManager(deps TransferManagerDeps) *TransferManager {
	sink := deps.Sink
	if sink == nil {
		sink = nopSink{}
	}
	u := deps.UUID
	if u == nil {
		u = uuid.NewString
	}
	return &TransferManager{
		sftp:           deps.SFTP,
		sink:           sink,
		home:           deps.Home,
		uuid:           u,
		runner:         deps.Runner,
		tasks:          make(map[string]*Task),
		queue:          make([]string, 0),
		runningSession: make(map[string]int),
	}
}

// GetTasks returns a snapshot of all tracked transfer tasks.
func (m *TransferManager) GetTasks() []TransferTaskDTO {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]TransferTaskDTO, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t.toDTO())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// EnqueueUpload validates local files and enqueues individual upload tasks.
func (m *TransferManager) EnqueueUpload(sessionID, sessionTitle, remoteDir string, localPaths []string) ([]string, error) {
	if len(localPaths) == 0 {
		return nil, nil
	}
	type validFile struct {
		safePath string
		name     string
		size     int64
	}
	var valid []validFile
	for _, p := range localPaths {
		if p == "" {
			continue
		}
		safe, err := localpathguard.ResolveExisting(p, m.home)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(safe)
		if err != nil {
			return nil, errf(apperror.Unknown, "Failed to inspect the local file")
		}
		if info.IsDir() {
			continue
		}
		valid = append(valid, validFile{
			safePath: safe,
			name:     filepath.Base(safe),
			size:     info.Size(),
		})
	}
	if len(valid) == 0 {
		return nil, nil
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errf(apperror.Unknown, "Transfer manager is closed")
	}

	var taskIDs []string
	var dtosToEmit []TransferTaskDTO
	now := time.Now().UnixMilli()

	for _, v := range valid {
		taskID := m.uuid()
		t := &Task{
			id:           taskID,
			sessionID:    sessionID,
			sessionTitle: sessionTitle,
			direction:    DirectionUpload,
			name:         v.name,
			remoteDir:    remoteDir,
			remotePath:   path.Join(remoteDir, v.name),
			localPath:    v.safePath,
			transferred:  0,
			total:        v.size,
			state:        StateQueued,
			createdAt:    now,
		}
		m.tasks[taskID] = t
		m.queue = append(m.queue, taskID)
		taskIDs = append(taskIDs, taskID)
		dtosToEmit = append(dtosToEmit, t.toDTO())
	}
	m.mu.Unlock()

	for _, dto := range dtosToEmit {
		m.sink.Emit(EventTransferTask, dto)
	}

	m.schedule()
	return taskIDs, nil
}

// EnqueueDownload validates target path and enqueues a single download task.
func (m *TransferManager) EnqueueDownload(sessionID, sessionTitle, remotePath, localPath string) (string, error) {
	safeTarget, err := localpathguard.ResolveTarget(localPath, m.home)
	if err != nil {
		return "", err
	}
	name := path.Base(remotePath)
	if name == "" || name == "/" || name == "." {
		return "", errf(apperror.Unknown, "Download remote path is invalid")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", errf(apperror.Unknown, "Transfer manager is closed")
	}

	now := time.Now().UnixMilli()
	taskID := m.uuid()
	t := &Task{
		id:           taskID,
		sessionID:    sessionID,
		sessionTitle: sessionTitle,
		direction:    DirectionDownload,
		name:         name,
		remotePath:   remotePath,
		localPath:    safeTarget,
		transferred:  0,
		total:        0, // will be discovered on start
		state:        StateQueued,
		createdAt:    now,
	}
	m.tasks[taskID] = t
	m.queue = append(m.queue, taskID)
	dto := t.toDTO()
	m.mu.Unlock()

	m.sink.Emit(EventTransferTask, dto)
	m.schedule()
	return taskID, nil
}

// Cancel cancels a queued or in-flight transfer task idempotently.
func (m *TransferManager) Cancel(taskID string) error {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return errf(apperror.Unknown, "Transfer task not found")
	}

	if task.state == StateQueued {
		for i, qid := range m.queue {
			if qid == taskID {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		task.state = StateCancelled
		task.finishedAt = time.Now().UnixMilli()
		dto := task.toDTO()
		m.mu.Unlock()

		m.sink.Emit(EventTransferTask, dto)
		m.schedule()
		return nil
	}

	if task.state == StateRunning {
		if task.cancel != nil {
			task.cancel()
		}
	}
	m.mu.Unlock()
	return nil
}

// Retry re-enqueues a failed or cancelled task with a new TaskID and inherited parameters.
func (m *TransferManager) Retry(taskID string) (string, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", errf(apperror.Unknown, "Transfer manager is closed")
	}
	oldTask, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return "", errf(apperror.Unknown, "Transfer task not found")
	}
	if oldTask.state != StateFailed && oldTask.state != StateCancelled {
		m.mu.Unlock()
		return "", errf(apperror.Unknown, "Only failed or cancelled tasks can be retried")
	}

	now := time.Now().UnixMilli()
	newID := m.uuid()
	newTask := &Task{
		id:           newID,
		sessionID:    oldTask.sessionID,
		sessionTitle: oldTask.sessionTitle,
		direction:    oldTask.direction,
		name:         oldTask.name,
		remoteDir:    oldTask.remoteDir,
		remotePath:   oldTask.remotePath,
		localPath:    oldTask.localPath,
		transferred:  0,
		total:        oldTask.total,
		state:        StateQueued,
		createdAt:    now,
		retryOf:      oldTask.id,
	}
	m.tasks[newID] = newTask
	m.queue = append(m.queue, newID)
	dto := newTask.toDTO()
	m.mu.Unlock()

	m.sink.Emit(EventTransferTask, dto)
	m.schedule()
	return newID, nil
}

// Clear removes a single finished task from the history.
func (m *TransferManager) Clear(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return nil
	}
	if task.state == StateQueued || task.state == StateRunning || task.state == StateFinalizing {
		return errf(apperror.Unknown, "Cannot clear an active transfer task")
	}
	delete(m.tasks, taskID)
	return nil
}

// ClearCompleted removes all succeeded, failed, or cancelled tasks from history.
func (m *TransferManager) ClearCompleted() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, t := range m.tasks {
		if t.state == StateSucceeded || t.state == StateFailed || t.state == StateCancelled {
			delete(m.tasks, id)
		}
	}
	return nil
}

// Dispose shuts down the transfer manager, cancels in-flight transfers, and flushes queues.
func (m *TransferManager) Dispose() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	now := time.Now().UnixMilli()
	var dtosToEmit []TransferTaskDTO

	for _, qid := range m.queue {
		if t, ok := m.tasks[qid]; ok {
			t.state = StateCancelled
			t.finishedAt = now
			dtosToEmit = append(dtosToEmit, t.toDTO())
		}
	}
	m.queue = nil

	for _, t := range m.tasks {
		if t.state == StateRunning {
			if t.cancel != nil {
				t.cancel()
			}
		}
	}
	m.mu.Unlock()

	for _, dto := range dtosToEmit {
		m.sink.Emit(EventTransferTask, dto)
	}

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

func (m *TransferManager) schedule() {
	m.mu.Lock()
	launches := m.scheduleLocked()
	m.mu.Unlock()

	for _, l := range launches {
		m.sink.Emit(EventTransferTask, l.dto)
		m.wg.Add(1)
		go func(t *Task, c context.Context) {
			defer m.wg.Done()
			m.runTask(t, c)
		}(l.task, l.ctx)
	}
}

// scheduleLocked picks runnable tasks from the queue respecting concurrency caps.
func (m *TransferManager) scheduleLocked() []scheduledLaunch {
	if m.closed {
		return nil
	}
	var launches []scheduledLaunch
	for m.runningGlobal < MaxGlobalConcurrentTransfers && len(m.queue) > 0 {
		pickedIndex := -1
		for i, taskID := range m.queue {
			task, ok := m.tasks[taskID]
			if !ok {
				continue
			}
			if m.runningSession[task.sessionID] < MaxSessionConcurrentTransfers {
				pickedIndex = i
				break
			}
		}
		if pickedIndex == -1 {
			break
		}

		taskID := m.queue[pickedIndex]
		m.queue = append(m.queue[:pickedIndex], m.queue[pickedIndex+1:]...)
		task := m.tasks[taskID]

		task.state = StateRunning
		task.startedAt = time.Now().UnixMilli()
		m.runningGlobal++
		m.runningSession[task.sessionID]++

		ctx, cancel := context.WithCancel(context.Background())
		task.cancel = cancel

		launches = append(launches, scheduledLaunch{
			task: task,
			ctx:  ctx,
			dto:  task.toDTO(),
		})
	}
	return launches
}

// runTask executes a transfer and handles completion, cancellation, and errors.
func (m *TransferManager) runTask(task *Task, ctx context.Context) {
	var runErr error

	onProgress := func(transferred, total int64, finalizing bool) {
		m.mu.Lock()
		task.transferred = transferred
		if total > 0 {
			task.total = total
		}
		if finalizing && task.state == StateRunning {
			task.state = StateFinalizing
		}
		dto := task.toDTO()
		closed := m.closed
		m.mu.Unlock()

		if !closed {
			m.sink.Emit(EventTransferTask, dto)
		}
	}

	if m.runner != nil {
		runErr = m.runner(ctx, task, onProgress)
	} else if m.sftp == nil {
		runErr = errf(apperror.Unknown, "SFTP service is not initialized")
	} else if task.direction == DirectionUpload {
		runErr = m.sftp.UploadWithContext(ctx, task.sessionID, task.remoteDir, task.localPath, onProgress)
	} else {
		runErr = m.sftp.DownloadWithContext(ctx, task.sessionID, task.remotePath, task.localPath, onProgress)
	}

	m.mu.Lock()
	now := time.Now().UnixMilli()
	task.finishedAt = now

	if runErr == nil {
		task.state = StateSucceeded
		if task.total > 0 {
			task.transferred = task.total
		}
	} else if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		task.state = StateCancelled
	} else if ctx.Err() != nil && task.state != StateFinalizing {
		task.state = StateCancelled
	} else {
		task.state = StateFailed
		task.err = runErr.Error()
	}

	m.runningGlobal--
	m.runningSession[task.sessionID]--
	if m.runningSession[task.sessionID] <= 0 {
		delete(m.runningSession, task.sessionID)
	}

	dto := task.toDTO()
	closed := m.closed
	m.mu.Unlock()

	if !closed {
		m.sink.Emit(EventTransferTask, dto)
		m.schedule()
	}
}
