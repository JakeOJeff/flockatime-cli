// Package transport posts snapshot batches to the collection endpoint.
package transport

import (
	"errors"

	"snapshot-agent/internal/snapshot"
)

var errNotImplemented = errors.New("transport: not implemented yet")

// Client posts to {Endpoint}/v1/snapshots with a bearer token.
type Client struct {
	Endpoint string
	APIKey   string
}

// Send posts a batch of snapshots. A batch may hold more than one because the
// offline queue flushes a backlog in a single request.
func (c *Client) Send(batch []snapshot.Snapshot) error {
	// TODO: marshal batch, POST with Authorization: Bearer, treat 2xx as
	// success and anything else as retryable.
	return errNotImplemented
}
