package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// requestAnalystStrata uses the same plan validation and KRAKEN contract as
// existing providers. No project writes are delegated to STRATA.
func (c *controller) requestAnalystStrata(ctx context.Context, req analystPlanInput) (analystPlan, error) {
	base, err := strataAPIURL(req.StrataURL)
	if err != nil {
		return analystPlan{}, err
	}
	// STRATA stays loaded after planning. A model load/unload is always manual.
	if err := requireStrataLoaded(ctx, base); err != nil {
		return analystPlan{}, err
	}
	prompt := "ОРИГИНАЛЬНОЕ ЗАДАНИЕ (только анализ, не выполнять):\n" + req.Task +
		"\n\nРЕАЛЬНЫЕ ФАЙЛЫ ПРОЕКТА (read-only inventory; используй их для точного write_scope):\n" + analystProjectInventory(req.Root)
	raw, err := json.Marshal(map[string]any{
		"model": req.AnalystModel, "stream": false, "temperature": 0.15,
		"max_tokens": 4096, "reasoning_effort": "none",
		"response_format": map[string]string{"type": "json_object"},
		"messages":        []map[string]string{{"role": "system", "content": analystSystem}, {"role": "user", "content": prompt}},
	})
	if err != nil {
		return analystPlan{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return analystPlan{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("DEXSTER_STRATA_API_KEY"); key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 4 * time.Minute}).Do(hreq)
	if err != nil {
		return analystPlan{}, fmt.Errorf("STRATA Analyst request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 96*1024))
	if err != nil {
		return analystPlan{}, err
	}
	if resp.StatusCode != 200 {
		return analystPlan{}, fmt.Errorf("STRATA Analyst HTTP %d: %.250s", resp.StatusCode, string(data))
	}
	var answer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return analystPlan{}, fmt.Errorf("STRATA Analyst JSON response: %w", err)
	}
	if len(answer.Choices) == 0 {
		return analystPlan{}, errors.New("STRATA Analyst returned no choices")
	}
	ch := answer.Choices[0]
	if ch.FinishReason == "length" || strings.TrimSpace(ch.Message.Content) == "" {
		return analystPlan{}, errors.New("STRATA Analyst: incomplete JSON plan")
	}
	var plan analystPlan
	if err := json.Unmarshal([]byte(ch.Message.Content), &plan); err != nil {
		return analystPlan{}, fmt.Errorf("STRATA Analyst: bad plan JSON: %w", err)
	}
	if err := validateAnalystPlan(&plan); err != nil {
		return analystPlan{}, err
	}
	if err := ensureNewGoModuleScope(req, &plan); err != nil {
		return analystPlan{}, err
	}
	plan.ID = planKey(req)
	plan.AnalystModel = req.AnalystModel
	return plan, nil
}
