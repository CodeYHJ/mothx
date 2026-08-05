package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/startvibecoding/mothx/agent"
	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/session"
)

// Scheduler checks for due cron jobs and executes them via sub-agents.
type Scheduler struct {
	store              CronStore
	manager            *agent.AgentManager
	interval           time.Duration
	sessionDir         string
	quit               chan struct{}
	running            bool
	claims             map[string]struct{}
	completionObserver func(sessionID, response string, runErr error)
	lifecycleMu        sync.Mutex
	mu                 sync.Mutex
	loopWG             sync.WaitGroup
}

// A persisted running claim may outlive the process that created it. Keep the
// lease deliberately long so a slow legitimate run is not reclaimed during
// normal operation, while still allowing recovery after a dead process.
const runningLeaseTimeout = 24 * time.Hour

// SetCompletionObserver installs a callback for completed local cron runs.
// The callback is invoked after the job has produced its final response.
func (s *Scheduler) SetCompletionObserver(observer func(sessionID, response string, runErr error)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.completionObserver = observer
	s.mu.Unlock()
}

func (s *Scheduler) notifyCompletion(sessionID, response string, runErr error) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	observer := s.completionObserver
	s.mu.Unlock()
	if observer != nil {
		observer(sessionID, response, runErr)
	}
}

var a2aHTTPClient = &http.Client{Timeout: 30 * time.Second}

const maxA2AResponseBytes = 1 << 20

type dueJobClaimer interface {
	ClaimDue(id string, now time.Time) (bool, error)
}

// NewScheduler creates a new cron scheduler.
func NewScheduler(store CronStore, manager *agent.AgentManager, interval time.Duration) *Scheduler {
	return NewSchedulerWithSessionDir(store, manager, interval, "")
}

// NewSchedulerWithSessionDir creates a scheduler that can attach scheduled
// local runs to existing sessions by session ID.
func NewSchedulerWithSessionDir(store CronStore, manager *agent.AgentManager, interval time.Duration, sessionDir string) *Scheduler {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Scheduler{
		store:      store,
		manager:    manager,
		interval:   interval,
		sessionDir: sessionDir,
		quit:       make(chan struct{}),
		claims:     make(map[string]struct{}),
	}
}

// Start begins the scheduler loop.
func (s *Scheduler) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	quit := make(chan struct{})
	s.quit = quit
	s.loopWG.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.loopWG.Done()
		s.loop(quit)
	}()
}

// Stop stops the scheduler.
func (s *Scheduler) Stop() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	quit := s.quit
	close(quit)
	s.mu.Unlock()
	s.loopWG.Wait()
}

// IsRunning returns whether the scheduler is running.
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Scheduler) loop(quit <-chan struct{}) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Check immediately on start
	s.checkAndRun()

	for {
		select {
		case <-quit:
			return
		case <-ticker.C:
			s.checkAndRun()
		}
	}
}

// checkAndRun checks all enabled jobs and runs any that are due.
func (s *Scheduler) checkAndRun() {
	jobs, err := s.store.List()
	if err != nil {
		log.Printf("[cron] failed to list jobs: %v", err)
		return
	}

	now := time.Now()
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		if job.LastStatus == "running" && !s.isStaleRunning(job, now) {
			continue // Don't start a job that's already running
		}
		if s.isStaleRunning(job, now) || s.isDue(job, now) {
			claimed, release, err := s.claimJob(job.ID, now)
			if err != nil {
				log.Printf("[cron] claim job %s: %v", job.ID, err)
				continue
			}
			if claimed {
				go func() {
					defer release()
					s.executeJob(job)
				}()
			}
		}
	}
}

func (s *Scheduler) claimJob(id string, now time.Time) (bool, func(), error) {
	if claimer, ok := s.store.(dueJobClaimer); ok {
		claimed, err := claimer.ClaimDue(id, now)
		return claimed, func() {}, err
	}

	// In-memory stores cannot coordinate across processes, but retain the
	// previous single-scheduler behavior without allowing overlapping ticks.
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, claimed := s.claims[id]; claimed {
		return false, func() {}, nil
	}
	s.claims[id] = struct{}{}
	return true, func() {
		s.mu.Lock()
		delete(s.claims, id)
		s.mu.Unlock()
	}, nil
}

// isDue checks if a job should run now.
func (s *Scheduler) isDue(job CronJob, now time.Time) bool {
	// A computed NextRun is authoritative for periodic jobs, including their
	// first run. This prevents a newly-created @daily job from running now.
	if !job.NextRun.IsZero() {
		return !now.Before(job.NextRun)
	}
	// Legacy one-shot jobs have no NextRun and are due until their first claim.
	return job.LastRun.IsZero()
}

func (s *Scheduler) isStaleRunning(job CronJob, now time.Time) bool {
	return job.LastStatus == "running" && !job.LastRun.IsZero() &&
		now.Sub(job.LastRun) >= runningLeaseTimeout
}

// executeJob runs a cron job by spawning a sub-agent or sending to A2A server.
func (s *Scheduler) executeJob(job CronJob) {
	var lastErr error
	var response strings.Builder
	defer func() {
		s.notifyCompletion(job.SessionID, response.String(), lastErr)
	}()

	// A2A target mode: send task to remote A2A server
	if job.A2ATarget != "" {
		lastErr = s.executeA2AJob(job)
	} else {
		// Local agent mode
		multiAgentPrompt := false
		var sess *session.Manager
		workDir := job.WorkDir
		// Channel/API runs serialize writes to a bound session through the
		// shared runtime lock. Cron runs must use the same lock before opening
		// and appending to that session, otherwise both agents can write from
		// the same stale leaf and trigger ErrSessionModified.
		var releaseRuntime func()
		if job.SessionID != "" && s.sessionDir != "" {
			releaseRuntime = session.LockRuntime(s.sessionDir, job.SessionID)
			defer releaseRuntime()
			if opened, err := session.OpenByIDExact(s.sessionDir, job.SessionID); err == nil {
				sess = opened
				if workDir == "" {
					if header := opened.GetHeader(); header != nil && header.Cwd != "" {
						workDir = header.Cwd
					}
				}
			}
		}
		var runID string
		if sess != nil && job.SessionID != "" && s.sessionDir != "" {
			runID = "cron_" + session.GenerateID()
			startedAt := time.Now()
			data, _ := json.Marshal(map[string]string{
				"cronJobId":   job.ID,
				"cronJobName": job.Name,
			})
			if err := session.SaveSessionRun(s.sessionDir, session.SessionRun{
				ID: runID, SessionID: job.SessionID, WorkDir: workDir,
				Source: "cron", Model: "", Mode: job.Mode,
				Status: "running", StartedAt: startedAt, UpdatedAt: startedAt,
			}); err != nil {
				log.Printf("[cron] save run %s: %v", runID, err)
			}
			if _, err := session.SaveSessionRunEvent(s.sessionDir, session.SessionRunEvent{
				SessionID: job.SessionID, RunID: runID, EventType: "started",
				Source: "cron", Status: "running", Mode: job.Mode, Data: data,
			}); err != nil {
				log.Printf("[cron] save run start event %s: %v", runID, err)
			}
			defer func() {
				status := "completed"
				eventType := "finished"
				message := ""
				if lastErr != nil {
					status = "failed"
					eventType = "failed"
					message = lastErr.Error()
				}
				finishedAt := time.Now()
				if err := session.UpdateSessionRunStatus(s.sessionDir, runID, status, message, &finishedAt); err != nil {
					log.Printf("[cron] update run %s status: %v", runID, err)
				}
				var eventData json.RawMessage
				if message != "" {
					eventData, _ = json.Marshal(map[string]string{
						"cronJobId": job.ID, "cronJobName": job.Name, "error": message,
					})
				} else {
					eventData = data
				}
				if _, err := session.SaveSessionRunEvent(s.sessionDir, session.SessionRunEvent{
					SessionID: job.SessionID, RunID: runID, EventType: eventType,
					Source: "cron", Status: status, Mode: job.Mode, Data: eventData,
				}); err != nil {
					log.Printf("[cron] save run end event %s: %v", runID, err)
				}
			}()
		}
		if s.manager == nil {
			lastErr = fmt.Errorf("create agent: agent manager unavailable")
		} else {
			a, err := s.manager.Create(agent.AgentOptions{
				IsSubAgent: sess == nil,
				Mode:       job.Mode,
				WorkDir:    workDir,
				Session:    sess,
				MultiAgent: &multiAgentPrompt,
			})
			if err != nil {
				lastErr = fmt.Errorf("create agent: %w", err)
			} else {
				ch := a.Run(context.Background(), job.Prompt)
				for event := range ch {
					if event.Type == agentpkg.EventTextDelta {
						response.WriteString(event.TextDelta)
					}
					if event.Error != nil {
						lastErr = event.Error
					}
				}
				s.manager.Destroy(a.ID())
			}
		}
	}

	s.updateJob(job.ID, func(current *CronJob) {
		current.RunCount++
		if lastErr != nil {
			current.LastStatus = "failed"
			current.LastError = lastErr.Error()
		} else {
			current.LastStatus = "success"
			current.LastError = ""
		}

		// Compute next run from the latest stored schedule.
		next, isOneShot, err := ParseSchedule(current.Schedule, time.Now())
		if err != nil {
			isOneShot = true
		}
		if isOneShot || current.OneShot {
			current.Enabled = false
			current.NextRun = time.Time{}
		} else {
			current.NextRun = next
		}
	})
}

func (s *Scheduler) updateJob(id string, update func(*CronJob)) {
	current, err := s.store.Get(id)
	if err != nil {
		return
	}
	update(current)
	_ = s.store.Update(*current)
}

// executeA2AJob sends a task to a remote A2A server.
func (s *Scheduler) executeA2AJob(job CronJob) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":  "user",
				"parts": []map[string]string{{"type": "text", "text": job.Prompt}},
			},
		},
		"id": 1,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", job.A2ATarget+"/a2a", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if job.A2AToken != "" {
		req.Header.Set("Authorization", "Bearer "+job.A2AToken)
	}

	resp, err := a2aHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("a2a request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("a2a request: status %d", resp.StatusCode)
	}

	var result struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxA2AResponseBytes)).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.Error != nil {
		return fmt.Errorf("a2a error: %s", result.Error.Message)
	}
	return nil
}
