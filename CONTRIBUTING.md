# Contributing

Thanks for helping make DeepSeek and OpenCode Go usable from Codex.

## Before opening a pull request

1. Keep the change focused on the compatibility proxy.
2. Add or update a regression test when protocol behavior changes.
3. Run `gofmt` on changed Go files, `go test ./...`, and `go vet ./...`.
4. Never commit `.env`, API keys, request payloads containing credentials, or personal paths.

## Bug reports

Include the Codex version, the selected model, the sanitized error, and a minimal reproduction. Do not include API keys or full request logs.

## Scope

The proxy is deliberately local and dependency-free. Please discuss support for additional models or API surfaces in an issue before implementing a larger change.
