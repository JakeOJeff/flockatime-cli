// Package queue persists snapshots that failed to send and replays them
// oldest-first once the endpoint is reachable again.
package queue

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"snapshot-agent/internal/snapshot"
)

// MaxEntries caps the backlog; the oldest entries are dropped past it.
const MaxEntries = 5000

var bucketName = []byte("snapshots")

// Queue is a durable FIFO of undelivered snapshots. Keys are a big-endian
// counter, so bbolt's byte-ordered cursor walks them oldest-first for free.
type Queue struct {
	db *bolt.DB
}

// Open creates or opens the queue file at path.
func Open(path string) (*Queue, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening queue %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketName)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Queue{db: db}, nil
}

// Close releases the underlying database.
func (q *Queue) Close() error { return q.db.Close() }

// Push appends a snapshot, dropping the oldest entries if the queue is full.
// CapturedAt is stored as-is and never rewritten on flush, so a snapshot that
// waited out an outage still reports when it was actually taken.
func (q *Queue) Push(s snapshot.Snapshot) error {
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return q.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		if err := b.Put(key(seq), blob); err != nil {
			return err
		}
		// Keys are one unbroken run of sequence numbers --- Push only ever
		// appends and Ack only ever removes from the front --- so the depth
		// is the distance between the ends. Bucket stats would not do here:
		// inside a write transaction they do not see the Put above yet.
		for {
			k, _ := b.Cursor().First()
			if k == nil {
				break
			}
			if seq-binary.BigEndian.Uint64(k)+1 <= MaxEntries {
				break
			}
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// Drain returns up to n of the oldest snapshots without removing them. They
// stay on disk until Ack, so a crash mid-send replays rather than loses them.
func (q *Queue) Drain(n int) ([]snapshot.Snapshot, error) {
	var out []snapshot.Snapshot
	err := q.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketName).Cursor()
		for k, v := c.First(); k != nil && len(out) < n; k, v = c.Next() {
			var s snapshot.Snapshot
			if err := json.Unmarshal(v, &s); err != nil {
				continue // unreadable entry; Ack will retire it
			}
			out = append(out, s)
		}
		return nil
	})
	return out, err
}

// Ack removes the n oldest snapshots after a successful send.
func (q *Queue) Ack(n int) error {
	if n <= 0 {
		return nil
	}
	return q.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketName)
		c := b.Cursor()
		for i := 0; i < n; i++ {
			k, _ := c.First()
			if k == nil {
				return nil
			}
			if err := b.Delete(k); err != nil {
				return err
			}
			c = b.Cursor()
		}
		return nil
	})
}

// Len reports how many snapshots are waiting.
func (q *Queue) Len() (int, error) {
	var n int
	err := q.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucketName).Stats().KeyN
		return nil
	})
	return n, err
}

func key(seq uint64) []byte {
	k := make([]byte, 8)
	binary.BigEndian.PutUint64(k, seq)
	return k
}
