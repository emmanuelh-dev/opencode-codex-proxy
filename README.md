# OpenCode Codex Proxy

A small local Go compatibility proxy that lets [OpenAI Codex](https://github.com/openai/codex) use **DeepSeek V4 Flash** and **Grok 4.5** through [OpenCode Go](https://opencode.ai/docs/go). It translates Codex's Responses API requests to OpenCode Go's Chat Completions endpoint when needed.

> This is an independent, community-maintained project. It is not affiliated with OpenAI, OpenCode, DeepSeek, or xAI.

```text
Codex Desktop or CLI
        |
        | POST /v1/responses
        v
OpenCode Codex Proxy (localhost)
        |
        +-- Grok 4.5 ---------> OpenCode Go /responses
        |
        +-- DeepSeek V4 Flash -> OpenCode Go /chat/completions
```

## What it solves

| Model | OpenCode Go endpoint | Proxy behavior |
| --- | --- | --- |
| `grok-4.5` | `/responses` | Removes Codex metadata and flattens namespace tools that the upstream does not accept. |
| `deepseek-v4-flash` | `/chat/completions` | Translates Responses input, messages, tools, tool outputs, and streamed events. |

The server binds to `127.0.0.1` by default. It loads `.env` at startup, and `.env` is ignored by Git.

## Quick start

```bash
git clone https://github.com/emmanuelh-dev/opencode-codex-proxy.git
cd opencode-codex-proxy
cp .env.example .env
# Set OPENCODE_GO_API_KEY in .env
go run .
```

Confirm that the proxy is running:

```bash
curl -i http://127.0.0.1:8787/healthz
```

## Configure Codex

Add this provider once to `~/.codex/config.toml`:

```toml
[model_providers.opencode_proxy]
name = "OpenCode Go local proxy"
base_url = "http://127.0.0.1:8787/v1"
env_key = "OPENCODE_GO_API_KEY"
wire_api = "responses"
requires_openai_auth = false
```

Codex validates a custom provider's `env_key`, so `OPENCODE_GO_API_KEY` must also be available to the Codex process. The proxy uses its own `.env` value for OpenCode requests.

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

Start the proxy, then run either profile:

```bash
codex --profile grok-go
codex --profile deepseek-flash-go
```

## Compatibility and limitations

- The DeepSeek route supports streamed Responses requests and Codex function/custom tool calls.
- Unsupported Responses input or content types return a clear error rather than being silently changed.
- Only `grok-4.5` and `deepseek-v4-flash` are intentionally supported. Requests for other models return `400`.
- `/v1/models` intentionally returns an empty list; configure a Codex profile with the desired model instead.

## Development

```bash
go test ./...
go vet ./...
```

The project has no third-party Go dependencies and never logs request bodies or credentials.

## Contributing

Contributions that improve compatibility, tests, setup instructions, or model support are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

[MIT](LICENSE)
