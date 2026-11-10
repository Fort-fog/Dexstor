package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAIResponse uses the OpenAI Responses API. Each call is stateless: KRAKEN
// and the Go controller retain the authoritative state, not the API server.
func (c *Client) openAIResponse(model, prompt string) (Generation, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return Generation{}, errors.New("dexster: OPENAI_API_KEY is not configured")
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com/v1"
	}
	body, err := json.Marshal(map[string]any{
		"model": model,
		"input": prompt + "\n\nReturn exactly one tool action as a JSON object with string fields name, path, content, source, old, new. No explanations, no markdown. Valid actions: list_files, read_file, replace_in_file, write_file, make_dir, copy_file, move_file, delete_path, declare_done. replace_in_file uses path plus exact old/new strings. copy_file/move_file use source plus path as destination. For unused fields return empty strings.",
		"store": false,
	})
	if err != nil {
		return Generation{}, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return Generation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Generation{}, fmt.Errorf("dexster: OpenAI request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Never echo arbitrary provider bodies; they can contain sensitive values.
		return Generation{}, fmt.Errorf("dexster: OpenAI request failed (HTTP %d)", resp.StatusCode)
	}
	var out struct {
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return Generation{}, fmt.Errorf("dexster: invalid OpenAI response: %w", err)
	}
	result := Generation{PromptEvalCount: out.Usage.InputTokens, EvalCount: out.Usage.OutputTokens, DoneReason: out.Status}
	for _, item := range out.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				result.Text += content.Text
			}
		}
	}
	if out.Status != "completed" || strings.TrimSpace(result.Text) == "" {
		return result, fmt.Errorf("dexster: OpenAI response not completed (status=%q)", out.Status)
	}
	return result, nil
}
