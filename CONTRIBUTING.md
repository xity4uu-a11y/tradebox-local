# Contributing to tradebox-local

Thank you for helping improve `tradebox-local`. This project focuses on practical, zero-dependency self-hosted tooling for keeping MetaTrader 5 (MT5) terminals and Expert Advisors running reliably on personal hardware and Linux containers.

## Engineering Standards

- **Pure Go stdlib core (`CGO_ENABLED=0`)**: Keep `go.mod` free of unnecessary third-party C/OpenSSL dependencies so static binaries remain reproducible across Windows, Linux, and macOS.
- **Zero stubs or mock placeholders**: Every CLI command, API route, parser rule, and plugin manifest must be tested against real local fixtures, real TCP loopback sockets, or `httptest.NewServer`.
- **Minimalist UI discipline**: Keep the embedded dashboard aligned with the Light/Dark CSS variable system (`Inter`/`SF Pro` + monospace, 1px hairline borders, muted status badges). Do not introduce emojis, neon gradients, or external CDN dependencies.
- **Legitimate self-hosting scope**: Contributions must stay strictly within process supervision, log parsing, network latency telemetry, alerting, and Linux Wine/noVNC containerization.

## Local Development Workflow

```bash
# Run static checks and all unit/integration tests
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -v -count=1 ./...

# Validate all community plugin manifests (both Go and Node validators)
go run ./cmd/tradebox validate-plugins --dir ./plugins
node scripts/validate-plugins.mjs

# Verify Node initializer dry-run
node bin/create-tradebox-local.mjs --dry-run
```

## Adding a Community Plugin

1. Create a new kebab-case manifest under `plugins/<plugin-id>.json` conforming to `plugins/schema.json`.
2. Ensure the `id` is unique across `plugins/` and `category` is one of `latency`, `journal`, `alerts`, `host`, or `container`.
3. Validate with both `node scripts/validate-plugins.mjs` and `tradebox validate-plugins --dir ./plugins`.
4. Open a pull request describing the operational scenario your preset addresses.

## Security & Secret Hygiene

Never commit broker credentials, Telegram bot tokens, Discord/Slack webhook URLs, or local `.log` files containing account numbers. Use `${ENV_VAR}` placeholders in configuration examples.
