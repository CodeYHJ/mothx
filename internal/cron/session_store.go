package cron

import (
	"fmt"
	"time"
)

// sessionScopedStore constrains a shared CronStore to one session. The
// scheduler and API use this adapter so callers cannot read or mutate another
// session's jobs by guessing an ID.
type sessionScopedStore struct {
	base      CronStore
	sessionID string
	workDir   string
}

func NewSessionScopedStore(base CronStore, sessionID string) CronStore {
	return NewSessionScopedStoreWithWorkDir(base, sessionID, "")
}

func NewSessionScopedStoreWithWorkDir(base CronStore, sessionID, workDir string) CronStore {
	return &sessionScopedStore{base: base, sessionID: sessionID, workDir: workDir}
}

func (s *sessionScopedStore) List() ([]CronJob, error) {
	jobs, err := s.base.List()
	if err != nil {
		return nil, err
	}
	filtered := make([]CronJob, 0, len(jobs))
	for _, job := range jobs {
		if job.SessionID == s.sessionID {
			filtered = append(filtered, job)
		}
	}
	return filtered, nil
}

func (s *sessionScopedStore) Get(id string) (*CronJob, error) {
	job, err := s.base.Get(id)
	if err != nil {
		return nil, err
	}
	if job.SessionID != s.sessionID {
		return nil, fmt.Errorf("cron job %q not found", id)
	}
	return job, nil
}

func (s *sessionScopedStore) Create(job CronJob) (*CronJob, error) {
	job.SessionID = s.sessionID
	if job.WorkDir == "" {
		job.WorkDir = s.workDir
	}
	return s.base.Create(job)
}

func (s *sessionScopedStore) Update(job CronJob) error {
	if job.SessionID != s.sessionID {
		return fmt.Errorf("cron job %q not found", job.ID)
	}
	return s.base.Update(job)
}

func (s *sessionScopedStore) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	return s.base.Delete(id)
}

func (s *sessionScopedStore) ClaimDue(id string, now time.Time) (bool, error) {
	job, err := s.Get(id)
	if err != nil {
		return false, err
	}
	if claimer, ok := s.base.(dueJobClaimer); ok {
		return claimer.ClaimDue(job.ID, now)
	}
	return false, nil
}
