// Package scheduler runs ScheduledTask entries on a cron schedule.
//
// Load order: on Start(), the scheduler reads all enabled tasks from storage
// and registers them with robfig/cron. CRUD handlers call Reload() to refresh
// in-memory registrations without restarting the server. Each task's execution
// delegates to the Runner callback supplied at construction time — the runner
// knows how to call bootimus's storage / WOL / Redfish layers.
package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"bootimus/internal/models"
	"bootimus/internal/storage"

	"github.com/robfig/cron/v3"
)

// Runner executes a single scheduled task. It returns the status string
// ("ok", "partial", "failed", etc.) and an optional error message for logs.
// The caller records the outcome via storage.RecordScheduledTaskRun.
type Runner func(ctx context.Context, task *models.ScheduledTask) (status string, errMsg string)

// Scheduler wraps robfig/cron + the storage-backed task list.
type Scheduler struct {
	store  storage.Storage
	runner Runner
	cron   *cron.Cron
	mu     sync.Mutex
	// Registered cron entries keyed by task ID — lets Reload remove stale
	// entries when tasks are deleted or disabled without recreating the whole cron.
	entries map[uint]cron.EntryID
}

func New(store storage.Storage, runner Runner) *Scheduler {
	return &Scheduler{
		store:   store,
		runner:  runner,
		cron:    cron.New(),
		entries: make(map[uint]cron.EntryID),
	}
}

// Start loads tasks and begins the cron loop. Call Reload after any task CRUD.
func (s *Scheduler) Start() {
	s.cron.Start()
	if err := s.Reload(); err != nil {
		log.Printf("scheduler: initial load failed: %v", err)
	}
}

// Stop drains in-flight runs and stops the cron.
func (s *Scheduler) Stop() {
	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done()
	}
}

// Reload syncs the in-memory schedule with storage. Disabled or deleted
// tasks are removed; new or changed tasks are registered with updated specs.
func (s *Scheduler) Reload() error {
	if s.store == nil {
		return nil
	}
	tasks, err := s.store.ListScheduledTasks()
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Build set of live IDs so we can prune stale entries.
	live := make(map[uint]bool, len(tasks))
	for _, t := range tasks {
		if t.Enabled {
			live[t.ID] = true
		}
	}
	for id, entryID := range s.entries {
		if !live[id] {
			s.cron.Remove(entryID)
			delete(s.entries, id)
		}
	}

	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		// If already registered, remove and re-add to pick up any spec changes.
		if existing, ok := s.entries[t.ID]; ok {
			s.cron.Remove(existing)
			delete(s.entries, t.ID)
		}
		task := *t // capture by value so each closure has its own task
		entryID, err := s.cron.AddFunc(t.CronExpr, func() {
			s.runTask(task)
		})
		if err != nil {
			log.Printf("scheduler: invalid cron %q for task %d (%s): %v", t.CronExpr, t.ID, t.Name, err)
			continue
		}
		s.entries[t.ID] = entryID
	}
	log.Printf("scheduler: %d active task(s) loaded", len(s.entries))
	return nil
}

// RunNow fires a task immediately, outside the cron schedule. Used by the
// "Run now" button in the UI.
func (s *Scheduler) RunNow(id uint) error {
	t, err := s.store.GetScheduledTask(id)
	if err != nil {
		return err
	}
	go s.runTask(*t)
	return nil
}

func (s *Scheduler) runTask(t models.ScheduledTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	status, errMsg := s.runner(ctx, &t)
	if err := s.store.RecordScheduledTaskRun(t.ID, status, errMsg); err != nil {
		log.Printf("scheduler: failed to record run for task %d: %v", t.ID, err)
	}
	log.Printf("scheduler: task %d (%s) → %s%s", t.ID, t.Name, status, func() string {
		if errMsg != "" {
			return ": " + errMsg
		}
		return ""
	}())
}
