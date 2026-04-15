package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// openclawBackend implements Backend by spawning `openclaw agent -p <prompt>
// --output-format stream-json --yes` and reading streaming NDJSON events from
// stdout — similar to the opencode backend.
type openclawBackend struct {
	cfg Config
}

func (b *openclawBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "openclaw"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("openclaw executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 20 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)

	args := []string{"agent", "--output-format", "stream-json", "--yes"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--session", opts.ResumeSessionID)
	}
	args = append(args, "-p", prompt)

	cmdPath, cmdArgs, err := wrapCommandWithSandbox(execPath, args, opts)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("wrap openclaw with sandbox: %w", err)
	}
	cmd := exec.CommandContext(runCtx, cmdPath, cmdArgs...)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openclaw stdout pipe: %w", err)
	}
	cmd.Stderr = newLogWriter(b.cfg.Logger, "[openclaw:stderr] ")

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start openclaw: %w", err)
	}

	b.cfg.Logger.Info("openclaw started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		scanResult := b.processEvents(stdout, msgCh)

		// Wait for process exit.
		exitErr := cmd.Wait()
		duration := time.Since(startTime)

		if runCtx.Err() == context.DeadlineExceeded {
			scanResult.status = "timeout"
			scanResult.errMsg = fmt.Sprintf("openclaw timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			scanResult.status = "aborted"
			scanResult.errMsg = "execution cancelled"
		} else if exitErr != nil && scanResult.status == "completed" {
			scanResult.status = "failed"
			scanResult.errMsg = fmt.Sprintf("openclaw exited with error: %v", exitErr)
		}

		b.cfg.Logger.Info("openclaw finished", "pid", cmd.Process.Pid, "status", scanResult.status, "duration", duration.Round(time.Millisecond).String())

		// Build usage map. OpenClaw doesn't report model per-step, so we
		// attribute all usage to the configured model (or "unknown").
		var usage map[string]TokenUsage
		u := scanResult.usage
		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			model := opts.Model
			if model == "" {
				model = "unknown"
			}
			usage = map[string]TokenUsage{model: u}
		}

		resCh <- Result{
			Status:     scanResult.status,
			Output:     scanResult.output,
			Error:      scanResult.errMsg,
			DurationMs: duration.Milliseconds(),
			SessionID:  scanResult.sessionID,
			Usage:      usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// ── Event handlers ──

// openclawEventResult holds accumulated state from processing the event stream.
type openclawEventResult struct {
	status    string
	errMsg    string
	output    string
	sessionID string
	usage     TokenUsage
}

// processEvents reads NDJSON lines from r, dispatches events to ch, and returns
// the accumulated result.
func (b *openclawBackend) processEvents(r io.Reader, ch chan<- Message) openclawEventResult {
	var output strings.Builder
	var sessionID string
	var usage TokenUsage
	finalStatus := "completed"
	var finalError string

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event openclawEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.SessionID != "" {
			sessionID = event.SessionID
		}

		switch event.Type {
		case "text":
			b.handleOCTextEvent(event, ch, &output)
		case "thinking":
			b.handleOCThinkingEvent(event, ch)
		case "tool_call":
			b.handleOCToolCallEvent(event, ch)
		case "error":
			// NOTE: error events unconditionally set finalStatus to "failed" and
			// it stays sticky — subsequent text or result events won't revert it.
			// This is intentional: once an error fires, the session is considered
			// failed regardless of later events.
			b.handleOCErrorEvent(event, ch, &finalStatus, &finalError)
		case "step_start":
			trySend(ch, Message{Type: MessageStatus, Status: "running"})
		case "step_end":
			// Accumulate token usage from step_end events if present.
			if event.Data != nil {
				usage.InputTokens += openclawInt64(event.Data, "inputTokens")
				usage.OutputTokens += openclawInt64(event.Data, "outputTokens")
				usage.CacheReadTokens += openclawInt64(event.Data, "cacheReadTokens")
				usage.CacheWriteTokens += openclawInt64(event.Data, "cacheWriteTokens")
			}
		case "result":
			// The result event only updates status on explicit failure. A
			// "completed" result is a no-op because finalStatus defaults to
			// "completed". Any unrecognized status (e.g. "partial") is also
			// treated as success — update this if OpenClaw adds new statuses.
			if event.Data != nil {
				if s, ok := event.Data["status"].(string); ok && s != "" {
					if s == "error" || s == "failed" {
						finalStatus = "failed"
						if msg, ok := event.Data["error"].(string); ok {
							finalError = msg
						}
					}
				}
			}
		}
	}

	// Check for scanner errors (e.g. broken pipe, read errors).
	if scanErr := scanner.Err(); scanErr != nil {
		b.cfg.Logger.Warn("openclaw stdout scanner error", "error", scanErr)
		if finalStatus == "completed" {
			finalStatus = "failed"
			finalError = fmt.Sprintf("stdout read error: %v", scanErr)
		}
	}

	return openclawEventResult{
		status:    finalStatus,
		errMsg:    finalError,
		output:    output.String(),
		sessionID: sessionID,
		usage:     usage,
	}
}

// openclawInt64 safely extracts an int64 from a JSON-decoded map value (which
// may be float64 due to Go's JSON number handling).
func openclawInt64(data map[string]any, key string) int64 {
	v, ok := data[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}

func (b *openclawBackend) handleOCTextEvent(event openclawEvent, ch chan<- Message, output *strings.Builder) {
	text := openclawExtractText(event.Data)
	if text != "" {
		output.WriteString(text)
		trySend(ch, Message{Type: MessageText, Content: text})
	}
}

func (b *openclawBackend) handleOCThinkingEvent(event openclawEvent, ch chan<- Message) {
	text := openclawExtractText(event.Data)
	if text != "" {
		trySend(ch, Message{Type: MessageThinking, Content: text})
	}
}

// handleOCToolCallEvent processes "tool_call" events from OpenClaw. A single
// tool_call event may contain both the call and result when the tool has
// completed (status == "completed").
func (b *openclawBackend) handleOCToolCallEvent(event openclawEvent, ch chan<- Message) {
	if event.Data == nil {
		return
	}

	name, _ := event.Data["name"].(string)
	callID, _ := event.Data["callId"].(string)

	// Extract input.
	var input map[string]any
	if raw, ok := event.Data["input"]; ok {
		if m, ok := raw.(map[string]any); ok {
			input = m
		}
	}

	// Emit the tool-use message.
	trySend(ch, Message{
		Type:   MessageToolUse,
		Tool:   name,
		CallID: callID,
		Input:  input,
	})

	// If the tool has completed, also emit a tool-result message.
	status, _ := event.Data["status"].(string)
	if status == "completed" {
		outputStr := extractToolOutput(event.Data["output"])
		trySend(ch, Message{
			Type:   MessageToolResult,
			Tool:   name,
			CallID: callID,
			Output: outputStr,
		})
	}
}

func (b *openclawBackend) handleOCErrorEvent(event openclawEvent, ch chan<- Message, finalStatus, finalError *string) {
	errMsg := ""
	if event.Data != nil {
		if msg, ok := event.Data["message"].(string); ok {
			errMsg = msg
		}
		if errMsg == "" {
			if code, ok := event.Data["code"].(string); ok {
				errMsg = code
			}
		}
	}
	if errMsg == "" {
		errMsg = "unknown openclaw error"
	}

	b.cfg.Logger.Warn("openclaw error event", "error", errMsg)
	trySend(ch, Message{Type: MessageError, Content: errMsg})

	*finalStatus = "failed"
	*finalError = errMsg
}

// openclawExtractText extracts text content from an openclaw event data map.
// Supports both flat {"text": "..."} and nested {"content": {"text": "..."}} layouts.
func openclawExtractText(data map[string]any) string {
	if data == nil {
		return ""
	}
	// Try "text" field directly.
	if text, ok := data["text"].(string); ok {
		return text
	}
	// Try nested "content.text".
	if content, ok := data["content"].(map[string]any); ok {
		if text, ok := content["text"].(string); ok {
			return text
		}
	}
	return ""
}

// ── JSON types for `openclaw agent --output-format stream-json` stdout events ──

// openclawEvent represents a single NDJSON line from OpenClaw's stream-json output.
//
// Event types:
//
//	"step_start"  — agent step begins
//	"text"        — text output from agent
//	"thinking"    — model reasoning/thinking
//	"tool_call"   — tool invocation with call and result
//	"error"       — error from openclaw
//	"step_end"    — agent step completes
//	"result"      — final result with status
type openclawEvent struct {
	Type      string         `json:"type"`
	SessionID string         `json:"sessionId,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}
