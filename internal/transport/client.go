// Package transport posts snapshot batches to the collection endpoint.
package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"snapshot-agent/internal/snapshot"
)

// Client posts to {Endpoint}/v1/snapshots with a bearer token.
type Client struct {
	Endpoint string
	APIKey   string
	HTTP     *http.Client
}

// New returns a client with a sane timeout.
func New(endpoint, apiKey string) *Client {
	return &Client{
		Endpoint: endpoint,
		APIKey:   apiKey,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Send posts a batch of snapshots. The payload is always a JSON array: the
// offline queue can flush a backlog and the current tick in one request.
func (c *Client) Send(batch []snapshot.Snapshot) error {
	if len(batch) == 0 {
		return nil
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("encoding batch: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.Endpoint+"/v1/snapshots", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("endpoint returned %s", resp.Status)
	}
	return nil
}

// Reachable probes the endpoint for `doctor`. Any HTTP answer counts: a 404 or
// a 401 still proves something is listening and routable.
func (c *Client) Reachable() error {
	req, err := http.NewRequest(http.MethodGet, c.Endpoint+"/v1/snapshots", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}
