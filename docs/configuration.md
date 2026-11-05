# Configuration

Start from [examples/config.yaml](../examples/config.yaml), or use the minimal
configuration in [Get started](getting-started.md).

## File selection and precedence

`--config PATH` selects a YAML file. Without it, acpboss reads
`.config/acpboss/config.yaml` **relative to its current working directory**;
it does not resolve that default through the home directory or XDG settings.
Use an absolute config path when an editor launches the process from another
working directory.

Proxy settings resolve in this order:

```text
explicit CLI flags > ACPBOSS_* environment > selected profile > base YAML > defaults
```

Provider `env` values retain their exact key case. YAML supports `${VAR}` and
`${VAR:-fallback}` before parsing. A missing required variable fails startup;
expanded values must still form valid YAML. Keep account credentials in the
provider's normal credential store or environment, outside tracked configuration.

## Provider processes

```yaml
runtime:
  providers:
    codex:
      cmd: "npx -y codex-acp@1.11.0"
    opencode:
      cmd: "opencode acp"
    local:
      cmd: "my-acp-agent --stdio"
      extra_args: ["--model", "{{.Model}}"]
      inherit_env: true
      env:
        EXACT_CASE_KEY: value
      model: upstream-model
      system_instructions: "Answer concisely."
```

Provider IDs are routing namespaces. They use lowercase letters, digits, `_`
and `-`, starting with a letter or digit. Every provider uses the same schema;
there is no vendor-specific type field or command resolver.

| Field | Meaning |
| --- | --- |
| `cmd` | Required executable command string; choose and pin adapter versions yourself. |
| `extra_args` | Additional argv elements appended after parsing `cmd`. |
| `inherit_env` | Inherit the acpboss environment; defaults to `true`. |
| `env` | Exact-case environment overrides for that process. |
| `model` | Upstream model selected when each provider session is created. |
| `model_config_id` | Explicit provider config-option ID used for model discovery/selection. |
| `mode` | Advertised session mode selected when creating a session. |
| `reasoning_effort` | One of `minimal`, `low`, `medium`, `high`, `xhigh`; the provider must also accept it. |
| `reasoning_effort_config_id` | Explicit reasoning config ID; otherwise select by `thought_level` category. |
| `system_instructions` | Separate leading ACP text block prepended to every prompt. |

Command parsing understands single/double quotes and backslash escaping, then
launches the executable directly. Shell expansion, pipes and redirects are not
performed. Quote arguments containing spaces or `#`. Executable lookup uses
acpboss's PATH, even with `inherit_env:false`.

`{{.Model}}` expands in command arguments once at process startup from the
configured `model`. Switching a request's model uses ACP session controls;
it does not relaunch the process or re-expand argv. HTTP explicitly selects
its request model after provider defaults have been applied. Unsupported model,
mode or reasoning selections fail instead of silently falling back.

## Proxy settings

```yaml
acpboss:
  working_dir: .
  http:
    listen: 127.0.0.1:8080
  startup_timeout: 60s
  request_timeout: 5m
  shutdown_timeout: 10s
  max_in_flight: 32
  max_sessions: 128
  max_message_bytes: 8388608
```

| Setting | Default | Environment |
| --- | --- | --- |
| `working_dir` | `.` | `ACPBOSS_WORKING_DIR` |
| `http.listen` | `127.0.0.1:8080` | `ACPBOSS_HTTP_LISTEN` |
| `startup_timeout` | `60s` | `ACPBOSS_STARTUP_TIMEOUT` |
| `request_timeout` | `5m` | `ACPBOSS_REQUEST_TIMEOUT` |
| `shutdown_timeout` | `10s` | `ACPBOSS_SHUTDOWN_TIMEOUT` |
| `max_in_flight` | `32` | `ACPBOSS_MAX_IN_FLIGHT` |
| `max_sessions` | `128` | `ACPBOSS_MAX_SESSIONS` |
| `max_message_bytes` | `8388608` | `ACPBOSS_MAX_MESSAGE_BYTES` |

Relative `working_dir` values resolve against the CLI's current directory,
not the configuration file's parent. HTTP uses this directory for sessions;
ACP clients supply their own absolute existing `session/new.cwd`.

Timeouts must be positive. Limits must be positive, and `max_message_bytes`
must be at least 1024. That setting bounds HTTP bodies, ACP frames and collected
HTTP output text; base64, role framing and instruction prefixes also take space.
There is no idle session eviction. Close unused ACP sessions and DELETE unused
HTTP conversations. Retained conversations count against `max_sessions`;
`max_in_flight` bounds creation and active requests, including native RPC drain.
Handles live only in the serving process; there is no state path or recovery
after restart. Provider-owned history follows the provider's own retention.

CLI overrides:

```sh
acpboss api --config config.yaml \
  --listen 127.0.0.1:9000 --working-dir /path/to/project \
  --startup-timeout 90s --request-timeout 2m --shutdown-timeout 10s
```

There are no CLI flags for the three capacity/message limits; configure them
through YAML or the corresponding environment variables.

## Profiles

```yaml
runtime:
  providers:
    opencode:
      cmd: "opencode acp"
acpboss:
  working_dir: .
profiles:
  default:
    acpboss:
      request_timeout: 2m
  local:
    acpboss:
      http:
        listen: 127.0.0.1:9000
```

`--profile local` selects `profiles.local`. If no profile is specified, a profile
named `default` applies when present. Profile maps merge recursively into base
`runtime` and `acpboss`; lists replace lists. The selected named profile does
not additionally inherit `profiles.default`. An explicitly requested missing
profile is an error.

## Authentication and MCP

Authenticate providers beforehand using their own CLI/environment. acpboss does
not define or merge MCP servers: ACP client `session/new.mcpServers` parameters
pass to each backend. HTTP creates sessions with an empty MCP-server list.
Provider-native configuration/tools still apply. For interactive permission and
capability behavior, see [usage](usage.md) and [protocol mapping](protocol-mapping.md).
