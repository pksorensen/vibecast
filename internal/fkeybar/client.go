package fkeybar

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/types"
)

// Client communicates with the vibecast control socket.
type Client struct {
	sockPath string
	client   *http.Client
}

// NewClient creates a new control socket client.
func NewClient() *Client {
	sockPath := filepath.Join(session.VibecastDir(), "control.sock")
	return &Client{
		sockPath: sockPath,
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", sockPath)
				},
			},
			Timeout: 5 * time.Second,
		},
	}
}

// StatusResponse holds the response from GET /status.
type StatusResponse struct {
	StreamID string `json:"streamId"`
	URL      string `json:"url"`
	PinCode  string `json:"pinCode"`
	Viewers  int    `json:"viewers"`
	Uptime   string `json:"uptime"`
	Phase    string `json:"phase"`
}

// GetStatus fetches current status from the control socket.
func (c *Client) GetStatus() (*StatusResponse, error) {
	resp, err := c.client.Get("http://localhost/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetPanes fetches current pane list.
func (c *Client) GetPanes() ([]types.PaneStatus, error) {
	resp, err := c.client.Get("http://localhost/panes")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var panes []types.PaneStatus
	if err := json.NewDecoder(resp.Body).Decode(&panes); err != nil {
		return nil, err
	}
	return panes, nil
}

// PostFKey sends an F-key action to the control socket.
func (c *Client) PostFKey(key string) error {
	req, err := http.NewRequest("POST", "http://localhost/fkey?key="+key, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PostStartStream sends a start-stream request.
func (c *Client) PostStartStream(promptSharing, shareProjectInfo bool) error {
	body := fmt.Sprintf(`{"promptSharing":%v,"shareProjectInfo":%v}`, promptSharing, shareProjectInfo)
	req, err := http.NewRequest("POST", "http://localhost/start-stream", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

