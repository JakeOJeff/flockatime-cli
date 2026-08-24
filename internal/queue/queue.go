// Package queue persists snapshots that failed to send and replays them
// oldest-first once the endpoint is reachable again.
package queue

import (
	"errors"

	"snapshot-agent/internal/snapshot"
)

var errNotImplemented = errors.New("queue: not implemented yet")

// maxEntries caps the backlog; the oldest entries are dropped past it.
const maxEntries = 5000

// Queue is a durable FIFO of undelivered snapshots.
type Queue struct {
	path string
}

// Open creates or opens the queue file at path.
func Open(path string) (*Queue, error) { return nil, errNotImplemented }

// Close releases the underlying database.
func (q *Queue) Close() error { return errNotImplemented }

// Push appends a snapshot, dropping the oldest entry if the queue is full.
// The snapshot keeps its original CapturedAt --- flushing never rewrites it.
func (q *Queue) Push(s snapshot.Snapshot) error { return errNotImplemented }

// Drain returns up to n of the oldest snapshots without removing them.
func (q *Queue) Drain(n int) ([]snapshot.Snapshot, error) { return nil, errNotImplemented }

// Ack removes the n oldest snapshots after a successful send.
func (q *Queue) Ack(n int) error { return errNotImplemented }
