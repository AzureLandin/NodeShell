package sftpservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type recordingSink struct {
	mu     sync.Mutex
	events []struct {
		Event   string
		Payload any
	}
}

func (r *recordingSink) Emit(event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, struct {
		Event   string
		Payload any
	}{Event: event, Payload: payload})
}

func (r *recordingSink) TaskEvents() []TransferTaskDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []TransferTaskDTO
	for _, e := range r.events {
		if e.Event == EventTransferTask {
			if dto, ok := e.Payload.(TransferTaskDTO); ok {
				out = append(out, dto)
			}
		}
	}
	return out
}

func TestTransferManagerConcurrencyAndFIFO(t *testing.T) {
	home := t.TempDir()
	fileA := filepath.Join(home, "fileA.txt")
	fileB := filepath.Join(home, "fileB.txt")
	fileC := filepath.Join(home, "fileC.txt")
	fileD := filepath.Join(home, "fileD.txt")
	_ = os.WriteFile(fileA, []byte("contentA"), 0600)
	_ = os.WriteFile(fileB, []byte("contentB"), 0600)
	_ = os.WriteFile(fileC, []byte("contentC"), 0600)
	_ = os.WriteFile(fileD, []byte("contentD"), 0600)

	sink := &recordingSink{}
	blocker := make(chan struct{})
	defer close(blocker)

	tm := NewTransferManager(TransferManagerDeps{
		Home: home,
		Sink: sink,
		Runner: func(ctx context.Context, task *Task, onProgress func(transferred, total int64, finalizing bool)) error {
			select {
			case <-blocker:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})

	// Enqueue 2 tasks for session s1, 1 task for session s2, 1 task for session s3, 1 task for session s4
	// Task 1: s1 -> should run (global 1, s1 1)
	// Task 2: s1 -> should queue (s1 already running)
	// Task 3: s2 -> should run (global 2, s2 1)
	// Task 4: s3 -> should run (global 3, s3 1)
	// Task 5: s4 -> should queue (global 3 reached)
	ids1, err := tm.EnqueueUpload("s1", "Session 1", "/remote", []string{fileA, fileB})
	if err != nil {
		t.Fatalf("EnqueueUpload s1 failed: %v", err)
	}
	if len(ids1) != 2 {
		t.Fatalf("expected 2 task IDs, got %d", len(ids1))
	}

	ids2, err := tm.EnqueueUpload("s2", "Session 2", "/remote", []string{fileC})
	if err != nil {
		t.Fatalf("EnqueueUpload s2 failed: %v", err)
	}
	ids3, err := tm.EnqueueUpload("s3", "Session 3", "/remote", []string{fileD})
	if err != nil {
		t.Fatalf("EnqueueUpload s3 failed: %v", err)
	}

	tm.mu.RLock()
	// Check s1 task 1 is running, task 2 is queued
	t1 := tm.tasks[ids1[0]]
	t2 := tm.tasks[ids1[1]]
	t3 := tm.tasks[ids2[0]]
	t4 := tm.tasks[ids3[0]]

	if t1.state != StateRunning {
		t.Errorf("expected task 1 to be running, got %s", t1.state)
	}
	if t2.state != StateQueued {
		t.Errorf("expected task 2 to be queued due to session limit, got %s", t2.state)
	}
	if t3.state != StateRunning {
		t.Errorf("expected task 3 to be running, got %s", t3.state)
	}
	if t4.state != StateRunning {
		t.Errorf("expected task 4 to be running, got %s", t4.state)
	}
	if tm.runningGlobal != 3 {
		t.Errorf("expected runningGlobal == 3, got %d", tm.runningGlobal)
	}
	tm.mu.RUnlock()

	// Enqueue 5th task from s4 - should be queued because global is 3
	fileE := filepath.Join(home, "fileE.txt")
	_ = os.WriteFile(fileE, []byte("contentE"), 0600)
	ids4, err := tm.EnqueueUpload("s4", "Session 4", "/remote", []string{fileE})
	if err != nil {
		t.Fatalf("EnqueueUpload s4 failed: %v", err)
	}
	tm.mu.RLock()
	t5 := tm.tasks[ids4[0]]
	if t5.state != StateQueued {
		t.Errorf("expected task 5 to be queued due to global limit, got %s", t5.state)
	}
	tm.mu.RUnlock()

	tm.Dispose()
}

func TestTransferManagerCancelQueuedAndRunning(t *testing.T) {
	home := t.TempDir()
	fileA := filepath.Join(home, "fileA.txt")
	fileB := filepath.Join(home, "fileB.txt")
	_ = os.WriteFile(fileA, []byte("contentA"), 0600)
	_ = os.WriteFile(fileB, []byte("contentB"), 0600)

	sink := &recordingSink{}
	blocker := make(chan struct{})
	defer close(blocker)

	tm := NewTransferManager(TransferManagerDeps{
		Home: home,
		Sink: sink,
		Runner: func(ctx context.Context, task *Task, onProgress func(transferred, total int64, finalizing bool)) error {
			select {
			case <-blocker:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})

	ids, err := tm.EnqueueUpload("s1", "Session 1", "/remote", []string{fileA, fileB})
	if err != nil {
		t.Fatalf("EnqueueUpload failed: %v", err)
	}

	task1ID := ids[0] // running
	task2ID := ids[1] // queued

	// Cancel queued task 2
	if err := tm.Cancel(task2ID); err != nil {
		t.Fatalf("Cancel task 2 failed: %v", err)
	}

	tm.mu.RLock()
	t2 := tm.tasks[task2ID]
	if t2.state != StateCancelled {
		t.Errorf("expected task 2 to be cancelled, got %s", t2.state)
	}
	tm.mu.RUnlock()

	// Cancel running task 1
	if err := tm.Cancel(task1ID); err != nil {
		t.Fatalf("Cancel task 1 failed: %v", err)
	}

	// Give a few ms for goroutine to handle ctx cancel
	var t1State TransferState
	for i := 0; i < 50; i++ {
		tm.mu.RLock()
		t1State = tm.tasks[task1ID].state
		tm.mu.RUnlock()
		if t1State == StateCancelled {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if t1State != StateCancelled {
		t.Errorf("expected task 1 to be cancelled, got %s", t1State)
	}

	tm.Dispose()
}

func TestTransferManagerRetryAndClear(t *testing.T) {
	home := t.TempDir()
	fileA := filepath.Join(home, "fileA.txt")
	_ = os.WriteFile(fileA, []byte("contentA"), 0600)

	sink := &recordingSink{}
	failedOnce := false
	tm := NewTransferManager(TransferManagerDeps{
		Home: home,
		Sink: sink,
		Runner: func(ctx context.Context, task *Task, onProgress func(transferred, total int64, finalizing bool)) error {
			if !failedOnce {
				failedOnce = true
				return errors.New("connection lost")
			}
			return nil
		},
	})

	ids, err := tm.EnqueueUpload("s1", "Session 1", "/remote", []string{fileA})
	if err != nil {
		t.Fatalf("EnqueueUpload failed: %v", err)
	}
	taskID := ids[0]

	// Wait for task to fail
	for i := 0; i < 50; i++ {
		tm.mu.RLock()
		st := tm.tasks[taskID].state
		tm.mu.RUnlock()
		if st == StateFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Now retry
	newID, err := tm.Retry(taskID)
	if err != nil {
		t.Fatalf("Retry failed: %v", err)
	}
	if newID == taskID {
		t.Error("expected new task ID on retry")
	}

	tm.mu.RLock()
	retryTask := tm.tasks[newID]
	if retryTask.retryOf != taskID {
		t.Errorf("expected retryOf to be %s, got %s", taskID, retryTask.retryOf)
	}
	if retryTask.name != "fileA.txt" {
		t.Errorf("expected name to be fileA.txt, got %s", retryTask.name)
	}
	tm.mu.RUnlock()

	// Clear the old failed task
	if err := tm.Clear(taskID); err != nil {
		t.Fatalf("Clear old task failed: %v", err)
	}

	tm.mu.RLock()
	if _, ok := tm.tasks[taskID]; ok {
		t.Error("expected old task to be deleted")
	}
	tm.mu.RUnlock()

	// Wait for retried task to succeed
	for i := 0; i < 50; i++ {
		tm.mu.RLock()
		st := tm.tasks[newID].state
		tm.mu.RUnlock()
		if st == StateSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := tm.ClearCompleted(); err != nil {
		t.Fatalf("ClearCompleted failed: %v", err)
	}

	tm.mu.RLock()
	if len(tm.tasks) != 0 {
		t.Errorf("expected all tasks cleared, got %d", len(tm.tasks))
	}
	tm.mu.RUnlock()

	tm.Dispose()
}

func TestTransferManagerDownloadEnqueue(t *testing.T) {
	home := t.TempDir()
	targetPath := filepath.Join(home, "downloaded.txt")

	sink := &recordingSink{}
	tm := NewTransferManager(TransferManagerDeps{
		Home: home,
		Sink: sink,
	})

	taskID, err := tm.EnqueueDownload("s1", "Session 1", "/remote/file.txt", targetPath)
	if err != nil {
		t.Fatalf("EnqueueDownload failed: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected non-empty task ID")
	}

	tasks := tm.GetTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task in snapshot, got %d", len(tasks))
	}
	if tasks[0].Direction != DirectionDownload {
		t.Errorf("expected download direction, got %s", tasks[0].Direction)
	}
	if tasks[0].Name != "file.txt" {
		t.Errorf("expected name file.txt, got %s", tasks[0].Name)
	}
	if tasks[0].RemotePath != "/remote/file.txt" {
		t.Errorf("expected RemotePath /remote/file.txt, got %s", tasks[0].RemotePath)
	}

	tm.Dispose()
}

func TestTransferManagerEventOrder(t *testing.T) {
	home := t.TempDir()
	fileA := filepath.Join(home, "fileA.txt")
	_ = os.WriteFile(fileA, []byte("content"), 0600)

	sink := &recordingSink{}
	tm := NewTransferManager(TransferManagerDeps{
		Home: home,
		Sink: sink,
		Runner: func(ctx context.Context, task *Task, onProgress func(transferred, total int64, finalizing bool)) error {
			onProgress(50, 100, false)
			onProgress(100, 100, true)
			return nil
		},
	})

	ids, err := tm.EnqueueUpload("s1", "Session 1", "/remote", []string{fileA})
	if err != nil {
		t.Fatalf("EnqueueUpload failed: %v", err)
	}
	taskID := ids[0]

	// Wait for task completion
	for i := 0; i < 50; i++ {
		tm.mu.RLock()
		st := tm.tasks[taskID].state
		tm.mu.RUnlock()
		if st == StateSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	events := sink.TaskEvents()
	var taskEvents []TransferTaskDTO
	for _, e := range events {
		if e.TaskID == taskID {
			taskEvents = append(taskEvents, e)
		}
	}

	if len(taskEvents) < 3 {
		t.Fatalf("expected at least 3 events (queued, running, succeeded), got %d: %+v", len(taskEvents), taskEvents)
	}

	if taskEvents[0].State != StateQueued {
		t.Errorf("first event must be queued, got %s", taskEvents[0].State)
	}
	if taskEvents[1].State != StateRunning {
		t.Errorf("second event must be running, got %s", taskEvents[1].State)
	}
	lastEvent := taskEvents[len(taskEvents)-1]
	if lastEvent.State != StateSucceeded {
		t.Errorf("final event must be succeeded, got %s", lastEvent.State)
	}

	// Ensure queued never appears after running
	seenRunning := false
	for _, e := range taskEvents {
		if e.State == StateRunning {
			seenRunning = true
		}
		if seenRunning && e.State == StateQueued {
			t.Errorf("found queued event after running event: %+v", e)
		}
	}

	tm.Dispose()
}

func TestTransferManagerFinalizingCannotCancel(t *testing.T) {
	home := t.TempDir()
	fileA := filepath.Join(home, "fileA.txt")
	_ = os.WriteFile(fileA, []byte("content"), 0600)

	sink := &recordingSink{}
	inFinalizing := make(chan struct{})
	finishFinalizing := make(chan struct{})

	tm := NewTransferManager(TransferManagerDeps{
		Home: home,
		Sink: sink,
		Runner: func(ctx context.Context, task *Task, onProgress func(transferred, total int64, finalizing bool)) error {
			onProgress(100, 100, true)
			close(inFinalizing)
			<-finishFinalizing
			return nil
		},
	})

	ids, err := tm.EnqueueUpload("s1", "Session 1", "/remote", []string{fileA})
	if err != nil {
		t.Fatalf("EnqueueUpload failed: %v", err)
	}
	taskID := ids[0]

	<-inFinalizing

	// Attempt cancel during finalizing
	if err := tm.Cancel(taskID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}

	tm.mu.RLock()
	st := tm.tasks[taskID].state
	tm.mu.RUnlock()
	if st != StateFinalizing {
		t.Errorf("expected task to stay in finalizing, got %s", st)
	}

	close(finishFinalizing)

	for i := 0; i < 50; i++ {
		tm.mu.RLock()
		st = tm.tasks[taskID].state
		tm.mu.RUnlock()
		if st == StateSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if st != StateSucceeded {
		t.Errorf("expected task to succeed after finalizing, got %s", st)
	}

	tm.Dispose()
}

func TestTransferManagerDisposeWait(t *testing.T) {
	home := t.TempDir()
	fileA := filepath.Join(home, "fileA.txt")
	_ = os.WriteFile(fileA, []byte("content"), 0600)

	runnerDone := make(chan struct{})
	sink := &recordingSink{}

	tm := NewTransferManager(TransferManagerDeps{
		Home: home,
		Sink: sink,
		Runner: func(ctx context.Context, task *Task, onProgress func(transferred, total int64, finalizing bool)) error {
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			close(runnerDone)
			return ctx.Err()
		},
	})

	_, err := tm.EnqueueUpload("s1", "Session 1", "/remote", []string{fileA})
	if err != nil {
		t.Fatalf("EnqueueUpload failed: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	tm.Dispose()

	// Runner must have finished by the time Dispose returned
	select {
	case <-runnerDone:
		// success
	default:
		t.Error("expected Dispose to wait for runner completion")
	}
}

func TestContextProgressReaderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	data := []byte("hello world")
	r := &contextProgressReader{
		ctx: ctx,
		r:   &mockReader{data: data},
	}

	buf := make([]byte, 10)
	_, err := r.Read(buf)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

type mockReader struct {
	data []byte
}

func (m *mockReader) Read(p []byte) (int, error) {
	n := copy(p, m.data)
	m.data = m.data[n:]
	return n, nil
}
