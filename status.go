package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// agentStatus is what a running `run` last said about itself, so `doctor` (a
// separate process) can tell a healthy agent from a dead or half-working one.
// Counts and times only: no paths, no project contents.
type agentStatus struct {
	PID            int    `json:"pid"`
	Version        string `json:"version"`
	UpdatedAt      int64  `json:"updated_at"`
	Active         bool   `json:"active"`
	LastCaptureAt  int64  `json:"last_capture_at,omitempty"`
	PollSeconds    int    `json:"poll_seconds"`
	CadenceOn      bool   `json:"cadence_on"`
	CadenceFiles   int    `json:"cadence_files"`
	CadenceProjs   int    `json:"cadence_projects"`
	LastPollAt     int64  `json:"last_poll_at,omitempty"`
	LastPollMs     int64  `json:"last_poll_ms"`
	FingerprintedN int    `json:"fingerprinted_files"`
	Stopped        bool   `json:"stopped,omitempty"` // set on a clean shutdown
}

func statusPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".flockatime", "status.json"), nil
}

// writeStatus replaces the status file atomically. Failure is ignored: the
// status is a diagnostic, never a reason to stop capturing.
func writeStatus(s agentStatus) {
	p, err := statusPath()
	if err != nil {
		return
	}
	s.PID, s.Version, s.UpdatedAt = os.Getpid(), agentVersion, time.Now().Unix()
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, p)
	}
}

func readStatus() (*agentStatus, error) {
	p, err := statusPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s agentStatus
	return &s, json.Unmarshal(b, &s)
}

// status gathers the run loop's current state for writeStatus.
func (c *capturer) status(active bool, polling bool, lastCapture time.Time, pollSeconds int) agentStatus {
	s := agentStatus{
		Active:         active,
		PollSeconds:    pollSeconds,
		CadenceOn:      polling,
		FingerprintedN: c.fpFiles,
	}
	if !lastCapture.IsZero() {
		s.LastCaptureAt = lastCapture.Unix()
	}
	if c.poller != nil {
		ps := c.poller.Status()
		s.CadenceFiles, s.CadenceProjs = ps.Files, ps.Projects
		s.LastPollMs = ps.LastPollTook.Milliseconds()
		if !ps.LastPoll.IsZero() {
			s.LastPollAt = ps.LastPoll.Unix()
		}
	}
	return s
}
