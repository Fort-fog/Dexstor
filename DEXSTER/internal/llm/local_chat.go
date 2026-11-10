package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// strataStructuredOutputFailed recognizes STRATA's documented structured
// generation failure, not an arbitrary HTTP 502 or transport error.
func strataStructuredOutputFailed(status int, body []byte) bool {
	if status != http.StatusBadGateway {
		return false
	}
	var response struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	return response.Error.Type == "structured_output_failed" || response.Error.Code == "structured_output_failed"
}

// localChatCompletion uses the OpenAI-compatible Chat Completions protocol
// provided by llama.cpp and Unsloth inference servers. The model's reasoning
// is never treated as an agent tool call.
func (c *Client) localChatCompletion(model, prompt string) (Generation, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return Generation{}, errors.New("dexster: local API URL is empty")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") && !strings.HasPrefix(base, "http://localhost:") && !strings.HasPrefix(base, "https://") {
		return Generation{}, errors.New("dexster: local API URL must use localhost or HTTPS")
	}
	// Thinking models can exhaust a short completion budget before producing
	// message.content. Retry once ONLY for reasoning-only truncation, before
	// the controller can execute any tool.
	structuredRetries := 0
	for attempt, maxTokens := range []int{4096, 8192} {
		for {
			messages := []map[string]string{{"role": "user", "content": prompt}}
			if c.Provider == "strata" && structuredRetries > 0 {
				// Keep the original task and the identical strict JSON schema.
				// Correct only generation formatting; never repeat an executed tool.
				messages = append(messages, map[string]string{
					"role":    "user",
					"content": "Your previous response could not be parsed as JSON. Return exactly one complete JSON object satisfying the supplied response_format schema. Do not include markdown or reasoning text outside the JSON object.",
				})
			}
			request := map[string]any{
				"model":      model,
				"stream":     false,
				"max_tokens": maxTokens,
				"messages":   messages,
			}
			// llama.cpp natively supports schema-constrained chat completions.
			// Use the SAME action schema already used by the working Ollama path,
			// without changing Ollama, the controller, or tool execution semantics.
			// This is deliberately llama.cpp-only; Unsloth inference API support
			// for response_format varies by server version.
			if c.Provider == "llamacpp" {
				request["response_format"] = map[string]any{
					"type":   "json_schema",
					"schema": toolCallSchema(),
				}
			}
			if c.Provider == "strata" {
				// STRATA uses the OpenAI JSON Schema envelope, unlike llama.cpp.
				// No native tools: DEXSTER still parses and verifies every command.
				request["response_format"] = map[string]any{
					"type": "json_schema",
					"json_schema": map[string]any{
						"name": "dexster_tool_call", "strict": true,
						"schema": toolCallSchema(),
					},
				}
				effort := strings.TrimSpace(strings.ToLower(os.Getenv("DEXSTER_STRATA_REASONING_EFFORT")))
				if effort == "" {
					effort = "low"
				}
				switch effort {
				case "none", "low", "medium", "high":
					request["reasoning_effort"] = effort
				default:
					return Generation{}, fmt.Errorf("dexster: invalid STRATA reasoning effort %q", effort)
				}
			}
			// Unsloth confirms this model-specific chat-template control for GLM-4.7-Flash.
			// Do not force it on other models or providers.
			if c.Provider == "llamacpp" && strings.Contains(strings.ToLower(model), "glm-4.7-flash") {
				request["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
			}
			body, err := json.Marshal(request)
			if err != nil {
				return Generation{}, err
			}
			req, err := http.NewRequest(http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
			if err != nil {
				return Generation{}, err
			}
			req.Header.Set("Content-Type", "application/json")
			if c.APIKey != "" {
				req.Header.Set("Authorization", "Bearer "+c.APIKey)
			}
			resp, err := c.HTTP.Do(req)
			if err != nil {
				return Generation{}, fmt.Errorf("dexster: %s API request: %w", c.Provider, err)
			}
			if resp.StatusCode != http.StatusOK {
				if c.Provider == "strata" {
					message, _ := io.ReadAll(io.LimitReader(resp.Body, 1400))
					resp.Body.Close()
					if strataStructuredOutputFailed(resp.StatusCode, message) {
						if structuredRetries < 2 {
							structuredRetries++
							fmt.Fprintf(os.Stderr, "[DEXSTER] STRATA structured_output_failed; retry %d/2 (no tool executed)\n", structuredRetries)
							continue
						}
						return Generation{}, fmt.Errorf("dexster: STRATA structured_output_failed after %d retries (no tool executed): HTTP %d: %s", structuredRetries, resp.StatusCode, strings.TrimSpace(string(message)))
					}
					return Generation{}, fmt.Errorf("dexster: STRATA API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
				}
				resp.Body.Close()
				return Generation{}, fmt.Errorf("dexster: %s API HTTP %d", c.Provider, resp.StatusCode)
			}
			var out struct {
				Choices []struct {
					FinishReason string `json:"finish_reason"`
					Message      struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
					} `json:"message"`
				} `json:"choices"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out)
			resp.Body.Close()
			if err != nil {
				return Generation{}, fmt.Errorf("dexster: invalid %s response: %w", c.Provider, err)
			}
			if len(out.Choices) == 0 {
				return Generation{}, fmt.Errorf("dexster: %s returned no choices", c.Provider)
			}
			ch := out.Choices[0]
			if strings.TrimSpace(ch.Message.Content) != "" {
				if ch.FinishReason == "length" {
					return Generation{}, fmt.Errorf("dexster: %s truncated completion (finish_reason=length, max_tokens=%d); refusing incomplete tool call", c.Provider, maxTokens)
				}
				return Generation{Text: ch.Message.Content, PromptEvalCount: out.Usage.PromptTokens, EvalCount: out.Usage.CompletionTokens, DoneReason: ch.FinishReason, ThinkingChars: len(ch.Message.ReasoningContent)}, nil
			}
			reasoningOnly := strings.TrimSpace(ch.Message.ReasoningContent) != ""
			if reasoningOnly && ch.FinishReason == "length" && attempt == 0 {
				break
			}
			return Generation{}, fmt.Errorf("dexster: %s returned empty content (finish_reason=%q, reasoning_chars=%d, completion_tokens=%d, max_tokens=%d); no tool executed", c.Provider, ch.FinishReason, len(ch.Message.ReasoningContent), out.Usage.CompletionTokens, maxTokens)
		}
	}
	return Generation{}, fmt.Errorf("dexster: %s failed to produce final content", c.Provider)
}
