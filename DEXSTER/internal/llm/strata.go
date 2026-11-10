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

// unloadStrata releases the STRATA engine after DEXSTER checkpoint/VERIFIED.
// Its HTTP service stays up and can auto-load on the next request.
func (c *Client) unloadStrata(model string) error {
	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" {
		return fmt.Errorf("dexster: invalid STRATA API URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("dexster: STRATA unload requires loopback API")
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("dexster: STRATA unload requires model ID")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1") + "/v1/unload"
	data, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(data))
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
		return fmt.Errorf("dexster: STRATA unload request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("dexster: STRATA unload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
