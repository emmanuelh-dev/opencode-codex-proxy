# OpenCode Codex Proxy

Local bridge for using OpenCode Go models from Codex's Responses-only custom-provider interface.

| Model | OpenCode endpoint | Proxy behavior |
| --- | --- | --- |
| `grok-4.5` | `/responses` | Removes Codex's unsupported `client_metadata`. |
| `deepseek-v4-flash` | `/chat/completions` | Translates Responses requests, messages, tools, tool outputs, and streamed events. |

The server listens only on `127.0.0.1` by default. It reads `.env` when it starts; `.env` is ignored by Git.

## Run

```bash
cp .env.example .env
# Set OPENCODE_GO_API_KEY in .env
go run .
```

Check that it is running:

```bash
curl -i http://127.0.0.1:8787/healthz
```

## Codex configuration

Add this provider once to `~/.codex/config.toml`:

```toml
[model_providers.opencode_proxy]
name = "OpenCode Go local proxy"
base_url = "http://127.0.0.1:8787/v1"
env_key = "OPENCODE_GO_API_KEY"
wire_api = "responses"
requires_openai_auth = false
```

The value of `OPENCODE_GO_API_KEY` must also be available to the Codex process, because Codex validates a custom provider's `env_key`. The proxy ignores Codex's authorization header and uses the value in its own `.env` to call OpenCode.

Create `~/.codex/grok-go.config.toml`:

```toml
model_provider = "opencode_proxy"
model = "grok-4.5"
```

Create `~/.codex/deepseek-flash-go.config.toml`:

```toml
model_provider = "opencode_proxy"
model = "deepseek-v4-flash"
```

Then start the proxy and run either profile:

```bash
codex --profile grok-go
codex --profile deepseek-flash-go
```

## Verify

```bash
go test ./...
go vet ./...
```

This project deliberately has no third-party dependencies and never logs request bodies or credentials.
