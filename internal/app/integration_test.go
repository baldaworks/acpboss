package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	bossapp "github.com/baldaworks/acpboss/internal/app"
	"github.com/baldaworks/acpboss/internal/protocol"
	"github.com/baldaworks/acpboss/internal/proxy"
	"github.com/baldaworks/acpboss/internal/testprovider"
	acp "github.com/coder/acp-go-sdk"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"go.yaml.in/yaml/v3"
)

func TestACPFixtureProcess(t *testing.T) { testprovider.MaybeRun() }

func TestBinaryModes(t *testing.T) {
	if testing.Short() {
		t.Skip("real CLI build and process smoke")
	}
	binary := filepath.Join(t.TempDir(), "acpboss")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/acpboss")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, mode := range []string{"stdio", "api"} {
		t.Run(mode, func(t *testing.T) {
			c := testprovider.Config(t, map[string]string{"alpha": "image-accept", "beta": "legacy"})
			var pidfiles []string
			for id, p := range c.Runtime.Providers {
				file := filepath.Join(t.TempDir(), id+".pid")
				p.Env["FIXTURE_PIDFILE"] = file
				pidfiles = append(pidfiles, file)
			}
			body, err := yaml.Marshal(map[string]any{"runtime": c.Runtime, "acpboss": map[string]any{"working_dir": c.Settings.WorkingDir, "http": map[string]any{"listen": "127.0.0.1:0"}, "request_timeout": "2s", "startup_timeout": "3s", "shutdown_timeout": "2s"}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "--config", path, mode)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready := make(chan string, 1)
			logs := make(chan string, 1)
			go func() {
				scan := bufio.NewScanner(stderr)
				var all strings.Builder
				for scan.Scan() {
					line := scan.Text()
					all.WriteString(line + "\n")
					if strings.HasPrefix(line, "acpboss listening on ") {
						ready <- strings.TrimPrefix(line, "acpboss listening on ")
					}
				}
				logs <- all.String()
			}()
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			stopped := false
			t.Cleanup(func() {
				if !stopped {
					_ = cmd.Process.Kill()
					var exit *exec.ExitError
					if err := <-wait; err != nil && !errors.As(err, &exit) {
						t.Errorf("wait for cleanup: %v", err)
					}
				}
			})
			if mode == "stdio" {
				var text strings.Builder
				permissions := 0
				waiting := make(chan struct{}, 1)
				conn, release := protocol.Open(func(_ context.Context, method string, raw json.RawMessage) (any, *acp.RequestError) {
					var p map[string]any
					_ = json.Unmarshal(raw, &p)
					switch method {
					case "session/update":
						text.WriteString(proxy.Text(p))
						if proxy.Text(p) == "waiting" {
							waiting <- struct{}{}
						}
						return nil, nil
					case "session/request_permission":
						permissions++
						return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "yes"}}, nil
					}
					return nil, acp.NewMethodNotFound(method)
				}, stdin, stdout, 8<<20, time.Second)
				release()
				call := func(method string, p any) map[string]any {
					t.Helper()
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					v, err := acp.SendRequest[map[string]any](conn, ctx, method, p)
					if err != nil {
						t.Fatal(method, err)
					}
					return v
				}
				call("initialize", map[string]any{"protocolVersion": 1})
				v := call("session/new", map[string]any{"cwd": c.Settings.WorkingDir, "mcpServers": []any{}})
				id := v["sessionId"].(string)
				call("session/set_config_option", map[string]any{"sessionId": id, "configId": "model", "value": "beta/other"})
				call("session/prompt", map[string]any{"sessionId": id, "prompt": proxy.TextPrompt("hello")})
				if text.String() != "beta:other:hello" {
					t.Fatal(text.String())
				}
				text.Reset()
				call("session/prompt", map[string]any{"sessionId": id, "prompt": proxy.TextPrompt("permission")})
				if permissions != 1 {
					t.Fatal(permissions)
				}
				promptDone := make(chan error, 1)
				go func() {
					_, err := acp.SendRequest[map[string]any](conn, t.Context(), "session/prompt", map[string]any{"sessionId": id, "prompt": proxy.TextPrompt("hang")})
					promptDone <- err
				}()
				select {
				case <-waiting:
				case <-time.After(time.Second):
					t.Fatal("prompt did not start")
				}
				if err := conn.SendNotification(t.Context(), "session/cancel", map[string]any{"sessionId": id}); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-promptDone:
					if err == nil {
						t.Fatal("cancelled prompt succeeded")
					}
				case <-time.After(time.Second):
					t.Fatal("ACP cancellation hung")
				}
				// A failing provider must leave the other provider usable.
				fresh := call("session/new", map[string]any{"cwd": c.Settings.WorkingDir, "mcpServers": []any{}})["sessionId"].(string)
				if _, err := acp.SendRequest[map[string]any](conn, t.Context(), "session/prompt", map[string]any{"sessionId": fresh, "prompt": proxy.TextPrompt("crash")}); err == nil {
					t.Fatal("crashed provider succeeded")
				}
				call("session/set_config_option", map[string]any{"sessionId": fresh, "configId": "model", "value": "beta/other"})
				text.Reset()
				call("session/prompt", map[string]any{"sessionId": fresh, "prompt": proxy.TextPrompt("survived")})
				if text.String() != "beta:other:survived" {
					t.Fatal(text.String())
				}
				_ = stdin.Close()
			} else {
				var address string
				select {
				case address = <-ready:
				case err := <-wait:
					stopped = true
					t.Fatalf("HTTP startup failed: %v %s", err, <-logs)
				case <-time.After(5 * time.Second):
					t.Fatal("HTTP readiness timed out")
				}
				waitHTTPModel(t, "http://"+address+"/v1/", "beta/other")
				client := responses.NewResponseService(option.WithBaseURL("http://"+address+"/v1/"), option.WithAPIKey("fixture"), option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
				params := responses.ResponseNewParams{Model: shared.ResponsesModel("beta/other"), Store: param.NewOpt(false), Input: responses.ResponseNewParamsInputUnion{OfString: param.NewOpt("http")}}
				value, err := client.New(t.Context(), params)
				if err != nil || value.OutputText() != "beta:other:http" {
					t.Fatal(value, err)
				}
				media := responses.ResponseNewParams{Model: shared.ResponsesModel("alpha/org/model"), Store: param.NewOpt(false), Input: responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfInputItemContentList: responses.ResponseInputMessageContentListParam{{OfInputImage: &responses.ResponseInputImageParam{Detail: "auto", ImageURL: param.NewOpt("data:image/png;base64,YQ==")}}}}}}}}}
				imageResponse, err := client.New(t.Context(), media)
				if err != nil || imageResponse.Status != "completed" {
					t.Fatal("image request through binary", imageResponse, err)
				}
				stream := client.NewStreaming(t.Context(), params)
				terminals := 0
				for stream.Next() {
					if stream.Current().Type == "response.completed" {
						terminals++
					}
				}
				if err := stream.Err(); err != nil {
					t.Fatal(err)
				}
				_ = stream.Close()
				if terminals != 1 {
					t.Fatal(terminals)
				}
				for _, tc := range []struct{ input, status string }{{"partial-fail", "failed"}, {"limit", "incomplete"}, {"permission", "failed"}, {"hang", "failed"}} {
					params.Input.OfString = param.NewOpt(tc.input)
					got, err := client.New(t.Context(), params)
					if err != nil {
						t.Fatal(tc.input, err)
					}
					if string(got.Status) != tc.status {
						t.Fatalf("%s: status=%s err=%v", tc.input, got.Status, err)
					}
				}
				cancelCtx, cancel := context.WithCancel(t.Context())
				params.Input.OfString = param.NewOpt("hang")
				interrupted := client.NewStreaming(cancelCtx, params)
				for interrupted.Next() {
					if interrupted.Current().Type == "response.output_text.delta" {
						break
					}
				}
				if interrupted.Err() != nil {
					t.Fatal(interrupted.Err())
				}
				cancel()
				_ = interrupted.Close()
				params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{
					{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt("http")}}},
					param.Override[responses.ResponseInputItemUnionParam](json.RawMessage(value.Output[0].RawJSON())),
					{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt("next")}}},
				}}
				got, err := client.New(t.Context(), params)
				if err != nil || !strings.Contains(got.OutputText(), "beta:other:http") {
					t.Fatal("output replay", got, err)
				}
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
			select {
			case err := <-wait:
				stopped = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("CLI shutdown hung")
			}
			output := <-logs
			if !strings.Contains(output, "lifecycle state=ready") {
				t.Fatal("missing lifecycle diagnostics", output)
			}
			if strings.Contains(output, "ACPBOSS_FIXTURE") {
				t.Fatal("provider environment leaked into diagnostics", output)
			}
			if runtime.GOOS == "linux" {
				for _, file := range pidfiles {
					data, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					pid, err := strconv.Atoi(string(data))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); !os.IsNotExist(err) {
						t.Fatalf("owned provider still exists: %d", pid)
					}
				}
			}
			_ = stdout.Close()
			_ = stderr.Close()
		})
	}
}

func TestHTTPStartsWithAllProvidersUnavailableAndStopsRetries(t *testing.T) {
	c := testprovider.Config(t, map[string]string{"missing": ""})
	provider := c.Runtime.Providers["missing"]
	provider.Cmd = filepath.Join(t.TempDir(), "missing-provider")
	c.Runtime.Providers["missing"] = provider
	c.Settings.HTTP.Listen = "127.0.0.1:0"
	c.Settings.ShutdownTimeout = time.Second

	ctx, cancel := context.WithCancel(t.Context())
	diagnosticReader, diagnosticWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(diagnosticReader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	done := make(chan error, 1)
	go func() {
		done <- bossapp.Run(ctx, "api", c, strings.NewReader(""), io.Discard, diagnosticWriter)
	}()

	var address string
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for address == "" {
		select {
		case line := <-lines:
			if value, ok := strings.CutPrefix(line, "acpboss listening on "); ok {
				address = value
			}
		case <-deadline.C:
			t.Fatal("HTTP listener did not become ready")
		}
	}
	response, err := http.Get("http://" + address + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var models struct {
		Data []any
	}
	if err := json.NewDecoder(response.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(models.Data) != 0 {
		t.Fatalf("degraded model catalog: status=%d models=%v", response.StatusCode, models.Data)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not interrupt provider retry")
	}
	_ = diagnosticWriter.Close()
	_ = diagnosticReader.Close()
}

func TestHTTPConversationRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("real CLI build and process restart")
	}
	binary := filepath.Join(t.TempDir(), "acpboss")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/acpboss")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	c := testprovider.Config(t, map[string]string{"alpha": ""})
	body, err := yaml.Marshal(map[string]any{"runtime": c.Runtime, "acpboss": map[string]any{"working_dir": c.Settings.WorkingDir, "http": map[string]any{"listen": "127.0.0.1:0"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	start := func() (string, func()) {
		cmd := exec.Command(binary, "api", "--config", path)
		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				if address, ok := strings.CutPrefix(scanner.Text(), "acpboss listening on "); ok {
					ready <- "http://" + address + "/v1/"
				}
			}
		}()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(4 * time.Second):
				_ = cmd.Process.Kill()
				var exit *exec.ExitError
				if err := <-done; err != nil && !errors.As(err, &exit) {
					t.Error(err)
				}
				t.Error("restart shutdown hung")
			}
		}
		t.Cleanup(stop)
		select {
		case address := <-ready:
			waitHTTPModel(t, address, "alpha/org/model")
			return address, stop
		case <-time.After(5 * time.Second):
			t.Fatal("restart readiness timed out")
			return "", stop
		}
	}
	first, stop := start()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, first+"conversations", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var conversation struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(response.Body).Decode(&conversation)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 200 || conversation.ID == "" {
		t.Fatal(response.StatusCode, conversation, err)
	}
	stop()
	second, _ := start()
	response, err = http.Get(second + "conversations/" + conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("restarted process recovered runtime handle", response.StatusCode)
	}
}

func waitHTTPModel(t *testing.T, baseURL, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "models")
		if err == nil {
			var catalog struct {
				Data []struct {
					ID string
				}
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&catalog)
			_ = response.Body.Close()
			if decodeErr == nil {
				for _, model := range catalog.Data {
					if model.ID == want {
						return
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("model %q did not become ready", want)
}
