package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// unloadLlamaCpp is the llama.cpp equivalent of Ollama keep_alive=0.
// The existing DEXSTER rotation has already checkpointed state and KRAKEN
// before invoking this method. Router autoloads the model on the next request.
func (c *Client) unloadLlamaCpp(model string) error {
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" {
		return fmt.Errorf("dexster: invalid local llama.cpp API URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("dexster: llama.cpp unload requires loopback API")
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("dexster: llama.cpp unload requires model ID")
	}
	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1")
	u.Path = path + "/models/unload"
	payload, err := json.Marshal(struct {
		Model string `json:"model"`
	}{Model: model})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("dexster: llama.cpp unload request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("dexster: llama.cpp unload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
