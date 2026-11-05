// Package testprovider supplies deterministic real ACP fixture processes.
package testprovider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaybeRun runs the fixture when the test binary is reexecuted as a provider.
func MaybeRun() {
	if os.Getenv("ACPBOSS_FIXTURE") != "1" {
		return
	}
	run()
	os.Exit(0)
}

type fixture struct {
	mu         sync.Mutex
	writeMu    sync.Mutex
	next       int
	sessions   map[string]string
	history    map[string]int
	cancels    map[string]chan struct{}
	callbacks  map[string]chan map[string]any
	name, mode string
}

func run() {
	f := &fixture{sessions: map[string]string{}, history: map[string]int{}, cancels: map[string]chan struct{}{}, callbacks: map[string]chan map[string]any{}, name: os.Getenv("FIXTURE_NAME"), mode: os.Getenv("FIXTURE_MODE")}
	if path := os.Getenv("FIXTURE_PIDFILE"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)
	}
	s := bufio.NewScanner(os.Stdin)
	s.Buffer(make([]byte, 4096), 8<<20)
	for s.Scan() {
		var msg map[string]any
		if json.Unmarshal(s.Bytes(), &msg) != nil {
			continue
		}
		method, _ := msg["method"].(string)
		p, _ := msg["params"].(map[string]any)
		id := msg["id"]
		if method == "" {
			key := fmt.Sprint(id)
			f.mu.Lock()
			ch := f.callbacks[key]
			f.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		sid, _ := p["sessionId"].(string)
		switch method {
		case "initialize":
			if f.mode == "hang-init" {
				continue
			}
			if f.mode == "fail-init" {
				f.failure(id)
				continue
			}
			f.result(id, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"close": map[string]any{}}, "promptCapabilities": map[string]any{"image": strings.Contains(f.mode, "image") || strings.HasPrefix(f.mode, "media"), "audio": strings.HasPrefix(f.mode, "media"), "embeddedContext": strings.HasPrefix(f.mode, "media")}}, "authMethods": []any{map[string]any{"id": "login", "name": "Login"}}})
		case "authenticate":
			f.result(id, map[string]any{})
		case "session/new":
			if path := os.Getenv("FIXTURE_SESSIONFILE"); path != "" {
				raw, _ := json.Marshal(p)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					f.failure(id)
					continue
				}
			}
			f.mu.Lock()
			f.next++
			sid = "same-" + strconv.Itoa(f.next)
			f.sessions[sid] = "org/model"
			f.mu.Unlock()
			result := map[string]any{"sessionId": sid, "configOptions": f.options("org/model"), "_meta": map[string]any{"fixture": true}}
			if f.mode == "legacy" {
				delete(result, "configOptions")
				result["models"] = map[string]any{"currentModelId": "org/model", "availableModels": []any{map[string]any{"modelId": "org/model", "name": "Model"}, map[string]any{"modelId": "other", "name": "Other"}}}
			}
			if f.mode == "no-models" {
				delete(result, "configOptions")
			}
			f.result(id, result)
		case "session/set_config_option":
			value, _ := p["value"].(string)
			f.mu.Lock()
			f.sessions[sid] = value
			f.mu.Unlock()
			f.result(id, map[string]any{"configOptions": f.options(value)})
		case "session/set_model":
			value, _ := p["modelId"].(string)
			f.mu.Lock()
			f.sessions[sid] = value
			f.mu.Unlock()
			f.result(id, map[string]any{})
		case "session/close":
			f.mu.Lock()
			if ch := f.cancels[sid]; ch != nil {
				select {
				case <-ch:
				default:
					close(ch)
				}
			}
			delete(f.sessions, sid)
			f.mu.Unlock()
			f.result(id, map[string]any{})
		case "session/cancel":
			f.mu.Lock()
			if ch := f.cancels[sid]; ch != nil {
				select {
				case <-ch:
				default:
					close(ch)
				}
			}
			f.mu.Unlock()
		case "session/prompt":
			f.mu.Lock()
			ch := make(chan struct{})
			f.cancels[sid] = ch
			f.mu.Unlock()
			go f.prompt(id, sid, p, ch)
		case "$/cancel_request":
		default:
			f.failure(id)
		}
	}
}
func (f *fixture) write(v any) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_ = json.NewEncoder(os.Stdout).Encode(v)
}
func (f *fixture) result(id, result any) {
	f.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
func (f *fixture) failure(id any) {
	f.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": "fixture failure"}})
}
func (f *fixture) options(model string) []any {
	if f.mode == "grouped" {
		return []any{map[string]any{"id": "custom", "name": "Model", "type": "select", "category": "model", "currentValue": model, "options": []any{map[string]any{"group": "a", "name": "A", "options": []any{map[string]any{"value": "org/model", "name": "Model"}, map[string]any{"value": "other", "name": "Other"}}}}}}
	}
	return []any{map[string]any{"id": "model", "name": "Model", "type": "select", "category": "model", "currentValue": model, "options": []any{map[string]any{"value": "org/model", "name": "Model"}, map[string]any{"value": "other", "name": "Other"}}}}
}
func (f *fixture) update(sid, text string) {
	f.write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}}})
}
func (f *fixture) prompt(id any, sid string, p map[string]any, cancel <-chan struct{}) {
	blocks, _ := p["prompt"].([]any)
	if path := os.Getenv("FIXTURE_PROMPTFILE"); path != "" {
		body, _ := json.Marshal(p)
		if err := os.WriteFile(path, body, 0600); err != nil {
			f.failure(id)
			return
		}
	}
	if f.mode == "media" {
		for _, block := range blocks {
			f.write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": block, "toolCallId": "media-tool"}}})
		}
		f.result(id, map[string]any{"stopReason": "end_turn"})
		return
	}
	var text strings.Builder
	for _, b := range blocks {
		v, _ := b.(map[string]any)
		s, _ := v["text"].(string)
		text.WriteString(s)
	}
	input := text.String()
	if f.mode == "media-output" {
		f.update(sid, "partial text")
		f.write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "image", "mimeType": "image/png", "data": "YQ=="}}}})
		f.result(id, map[string]any{"stopReason": "end_turn"})
		return
	}
	if input == "empty" {
		f.result(id, map[string]any{"stopReason": "end_turn"})
		return
	}
	if input == "crash" {
		os.Exit(3)
	}
	if input == "hang" {
		f.update(sid, "waiting")
		if f.mode == "unresponsive" {
			time.Sleep(time.Second)
		} else {
			<-cancel
		}
		f.result(id, map[string]any{"stopReason": "cancelled"})
		return
	}
	if input == "permission" {
		key := "callback-" + sid
		ch := make(chan map[string]any, 1)
		f.mu.Lock()
		f.callbacks[key] = ch
		f.mu.Unlock()
		f.write(map[string]any{"jsonrpc": "2.0", "id": key, "method": "session/request_permission", "params": map[string]any{"sessionId": sid, "toolCall": map[string]any{"toolCallId": "call-1", "title": "Fixture tool"}, "options": []any{map[string]any{"optionId": "yes", "name": "Allow", "kind": "allow_once"}, map[string]any{"optionId": "no", "name": "Deny", "kind": "reject_once"}}}})
		select {
		case <-ch:
		case <-cancel:
		}
		f.mu.Lock()
		delete(f.callbacks, key)
		f.mu.Unlock()
	}
	f.mu.Lock()
	model := f.sessions[sid]
	if input == "history" {
		input = strconv.Itoa(f.history[sid])
	}
	f.history[sid]++
	f.mu.Unlock()
	f.update(sid, f.name+":"+model+":")
	if input == "partial-fail" {
		f.failure(id)
		return
	}
	if input == "delay" {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-cancel:
			f.result(id, map[string]any{"stopReason": "cancelled"})
			return
		}
	}
	if input == "burst" {
		for range 256 {
			f.update(sid, "chunk")
		}
	}
	f.update(sid, input)
	reason := "end_turn"
	if input == "limit" {
		reason = "max_tokens"
	}
	f.result(id, map[string]any{"stopReason": reason})
}
