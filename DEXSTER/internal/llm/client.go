// Package llm provides a minimal Ollama-compatible model client and the
// TOOL_CALL protocol parser used by the DEXSTER agent.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Client talks to an Ollama-compatible /api/generate endpoint.
// A provider retry is safe here: the Go controller executes no tools until
// it has received and parsed a COMPLETE generation.
type Client struct {
	BaseURL    string
	Provider   string // ollama (default) or openai
	APIKey     string // only kept in process memory, never serialized
	HTTP       *http.Client
	NumCtx     int
	MaxRetries int                      // extra Ollama attempts after transient transport/provider errors
	RetryDelay time.Duration            // first delay, doubled for each retry
	Trace      func(event, text string) // optional live text; never used to execute tools
}

type generateRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Stream    bool           `json:"stream"`
	Options   map[string]int `json:"options,omitempty"`
	KeepAlive any            `json:"keep_alive,omitempty"`
	Think     *bool          `json:"think,omitempty"`
	Format    any            `json:"format,omitempty"`
}

type generateResponse struct {
	Response        string `json:"response"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	Thinking        string `json:"thinking"`
}

// Generation contains real counters reported by Ollama for one successful
// request. Failed attempts are NOT counted as completed LLM calls.
type Generation struct {
	Text            string
	PromptEvalCount int
	EvalCount       int
	DoneReason      string
	ThinkingChars   int
}

// ContextLimitError is a permanent Ollama refusal, not a transient network
// error. The controller may compact the prompt once, but MUST NOT blindly
// resend the same oversized request.
type ContextLimitError struct {
	Requested int
	Capacity  int
}

func (e *ContextLimitError) Error() string {
	if e.Requested > 0 && e.Capacity > 0 {
		return fmt.Sprintf("dexster: Ollama context exceeded: %d requested tokens, %d available; compact prompt or increase Ollama num_ctx", e.Requested, e.Capacity)
	}
	return "dexster: Ollama context exceeded; compact prompt or increase Ollama num_ctx"
}

var ollamaContextCounts = regexp.MustCompile(`(?i)request\s*\((\d+)\s+tokens\).*?(?:context size|context window)\s*\((\d+)\s+tokens\)`)

func contextLimitError(status int, response []byte) error {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
		return nil
	}
	msg := string(response)
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "exceed_context_size_error") &&
		!strings.Contains(lower, "exceeds the available context size") &&
		!strings.Contains(lower, "context length exceeded") &&
		!strings.Contains(lower, "context window exceeded") {
		return nil
	}
	ret := &ContextLimitError{}
	if m := ollamaContextCounts.FindStringSubmatch(msg); len(m) == 3 {
		ret.Requested, _ = strconv.Atoi(m[1])
		ret.Capacity, _ = strconv.Atoi(m[2])
	}
	return ret
}

func New(baseURL string) *Client {
	client := &http.Client{Timeout: 5 * time.Minute}
	if socket := os.Getenv("DEXSTER_OLLAMA_SOCKET"); socket != "" {
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}
	}
	return &Client{BaseURL: baseURL, HTTP: client, MaxRetries: 2, RetryDelay: 250 * time.Millisecond}
}

func (c *Client) Generate(model, prompt string) (string, error) {
	result, err := c.GenerateWithStats(model, prompt)
	return result.Text, err
}

func (c *Client) GenerateWithStats(model, prompt string) (Generation, error) {
	if c.Provider == "llamacpp" || c.Provider == "unsloth" || c.Provider == "strata" {
		if c.Trace != nil {
			c.Trace("start", "")
		}
		result, err := c.localChatCompletion(model, prompt)
		if c.Trace != nil {
			if err != nil {
				c.Trace("error", err.Error())
			} else {
				c.Trace("chunk", result.Text)
				c.Trace("done", "")
			}
		}
		return result, err
	}
	if c.Provider == "openai" {
		if c.Trace != nil {
			c.Trace("start", "")
		}
		result, err := c.openAIResponse(model, prompt)
		if c.Trace != nil {
			if err != nil {
				c.Trace("error", err.Error())
			} else {
				c.Trace("chunk", result.Text)
				c.Trace("done", "")
			}
		}
		return result, err
	}
	options := map[string]int{}
	if c.NumCtx > 0 {
		options["num_ctx"] = c.NumCtx
	}
	noThinking := false
	body, err := json.Marshal(generateRequest{Model: model, Prompt: prompt, Stream: c.Trace != nil, Options: options, Think: &noThinking, Format: toolCallSchema()})
	if err != nil {
		return Generation{}, fmt.Errorf("dexster: encode request: %w", err)
	}

	retries := c.MaxRetries
	if retries < 0 {
		retries = 0
	}
	if retries > 4 {
		retries = 4
	} // cap automatic requests; never retry indefinitely
	pause := c.RetryDelay
	if pause <= 0 {
		pause = 250 * time.Millisecond
	}
	for attempt := 0; attempt <= retries; attempt++ {
		var out generateResponse
		if c.Trace != nil {
			c.Trace("start", "") // resets previous incomplete stream before a retry
			err = c.postGenerateStream(body, &out)
		} else {
			err = c.postGenerate(body, &out)
		}
		if err == nil {
			result := Generation{Text: out.Response, PromptEvalCount: out.PromptEvalCount, EvalCount: out.EvalCount, DoneReason: out.DoneReason, ThinkingChars: len(out.Thinking)}
			if !out.Done {
				if c.Trace != nil {
					c.Trace("error", "incomplete generation")
				}
				return result, fmt.Errorf("dexster: Ollama returned incomplete generation (done=false, done_reason=%q)", out.DoneReason)
			}
			if out.DoneReason == "length" {
				if c.Trace != nil {
					c.Trace("error", "generation truncated")
				}
				return result, errors.New("dexster: Ollama truncated generation (done_reason=length)")
			}
			if c.Trace != nil {
				c.Trace("done", "")
			}
			return result, nil
		}
		var retryable *transientProviderError
		if !errors.As(err, &retryable) || attempt == retries {
			if c.Trace != nil {
				c.Trace("error", err.Error())
			}
			return Generation{}, err
		}
		if c.Trace != nil {
			c.Trace("retry", "temporary Ollama connection error")
		}
		// This is ONLY an API retry. No parsed tool call has been executed.
		fmt.Fprintf(os.Stderr, "[DEXSTER] Ollama transient request error; retry %d/%d (no tool executed)\n", attempt+1, retries)
		time.Sleep(pause)
		pause *= 2
	}
	return Generation{}, err
}

// Unload does not automatically retry; keep_alive=0 has no tool side effects,
// but a context rotation should report failure explicitly to the controller.
func (c *Client) Unload(model string) error {
	if c.Provider == "llamacpp" {
		return c.unloadLlamaCpp(model)
	}
	if c.Provider == "strata" {
		return c.unloadStrata(model)
	}
	if c.Provider == "openai" || c.Provider == "unsloth" {
		return nil
	}
	body, err := json.Marshal(generateRequest{Model: model, Prompt: "", Stream: false, KeepAlive: 0})
	if err != nil {
		return err
	}
	var out generateResponse
	return c.postGenerate(body, &out)
}

type transientProviderError struct{ cause error }

func (e *transientProviderError) Error() string { return e.cause.Error() }
func (e *transientProviderError) Unwrap() error { return e.cause }

func temporaryNetworkError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return !errors.Is(err, context.Canceled)
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func (c *Client) postGenerate(body []byte, out *generateResponse) error {
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dexster: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("dexster: model request failed: %w", err)
		if temporaryNetworkError(err) {
			return &transientProviderError{wrapped}
		}
		return wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if limit := contextLimitError(resp.StatusCode, errBody); limit != nil {
			return limit
		}
		// Avoid echoing raw error bodies; they can contain request/user data.
		wrapped := fmt.Errorf("dexster: Ollama HTTP status %s", resp.Status)
		if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			return &transientProviderError{wrapped}
		}
		return wrapped
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		wrapped := fmt.Errorf("dexster: decode model response: %w", err)
		if temporaryNetworkError(err) {
			return &transientProviderError{wrapped}
		}
		return wrapped
	}
	return nil
}

// toolCallSchema constrains a single action at the Ollama generation layer.
// The Go router still validates action, path, and content independently.
func toolCallSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    map[string]any{"type": "string", "enum": []string{"list_files", "read_file", "replace_in_file", "write_file", "make_dir", "copy_file", "move_file", "delete_path", "declare_done"}},
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
			"source":  map[string]any{"type": "string"},
			"old":     map[string]any{"type": "string"},
			"new":     map[string]any{"type": "string"},
		},
		"required":             []string{"name", "path", "content", "source"},
		"additionalProperties": false,
	}
}

// postGenerateStream consumes Ollama's streaming NDJSON; the web UI can show
// tokens immediately, but the agent executes NO tool until done=true is seen.
// Incomplete responses are discarded by the caller before its safe retry.
func (c *Client) postGenerateStream(body []byte, out *generateResponse) error {
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dexster: build stream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("dexster: model stream failed: %w", err)
		if temporaryNetworkError(err) {
			return &transientProviderError{wrapped}
		}
		return wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if limit := contextLimitError(resp.StatusCode, b); limit != nil {
			return limit
		}
		wrapped := fmt.Errorf("dexster: Ollama HTTP status %s", resp.Status)
		if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			return &transientProviderError{wrapped}
		}
		return wrapped
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
	var answer strings.Builder
	for {
		var chunk struct {
			generateResponse
			Error string `json:"error"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			wrapped := fmt.Errorf("dexster: incomplete Ollama stream: %w", err)
			if temporaryNetworkError(err) || errors.Is(err, io.ErrUnexpectedEOF) {
				return &transientProviderError{wrapped}
			}
			return wrapped
		}
		if chunk.Error != "" {
			return fmt.Errorf("dexster: Ollama stream reported failure")
		}
		if answer.Len()+len(chunk.Response) > 16<<20 {
			return fmt.Errorf("dexster: Ollama streaming response exceeds 16 MiB")
		}
		answer.WriteString(chunk.Response)
		if c.Trace != nil && chunk.Response != "" {
			c.Trace("chunk", chunk.Response)
		}
		if chunk.Done {
			*out = chunk.generateResponse
			out.Response = answer.String()
			return nil
		}
	}
}
