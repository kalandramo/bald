# Repository Guidelines

Reference examples for the [bald](https://github.com/kalandramo/bald) framework: a Go backend (`go-bald-admin`) and a Vue 3 frontend (`go-bald-admin-web`).

## Project Structure & Module Organization

```
examples/
├── go-bald-admin/            # Go backend (independent module)
│   ├── cmd/go-bald-admin/    # Entry point (appkit.Run + interceptor chain)
│   ├── configs/              # Viper YAML config
│   ├── api/                  # Protobuf contracts + buf-generated code
│   ├── internal/             # Business logic, security, cache, observability
│   └── docs/                 # Design & requirements docs
└── go-bald-admin-web/        # Vue 3 + TypeScript frontend
    ├── src/
    │   ├── common/           # Shared APIs, assets, components, composables
    │   ├── pages/            # Feature modules (each with own apis/, components/)
    │   ├── layouts/          # Layout components
    │   ├── router/           # Vue Router config
    │   ├── pinia/            # State management
    │   ├── http/             # Axios request layer
    │   └── plugins/          # Global plugins & directives
    ├── tests/                # Vitest unit tests
    └── types/                # TypeScript declarations (auto/ is generated)
```

## Build, Test, and Development Commands

**Go backend** (`go-bald-admin/`): `task build`, `task test` (shuffled, detects state coupling), `task test:audit`, `task run` (:8080 HTTP / :9090 gRPC), `task verify` (build + vet + test).

**Frontend** (`go-bald-admin-web/`): `pnpm i`, `pnpm dev` (port 3333), `pnpm build`, `pnpm lint`, `pnpm test`.

## Coding Style & Naming Conventions

**Go:** `gofmt` formatting; short lowercase package names; `*_test.go` with `TestXxx`; wire injection via `google/wire`.

**TypeScript / Vue:** 2-space indent, double quotes, no semicolons (`@antfu/eslint-config`); strict mode; `@/` → `src/`, `@@/` → `src/common/`; SFC order `<script>` → `<template>` → `<style>`; co-locate feature code under `src/pages/<feature>/`.

## Testing Guidelines

**Go:** `-shuffle=on -count=1`; e2e file tests need real MinIO (auto-skip if env absent); no fakes/mocks/stubs.

**Frontend:** Vitest + `happy-dom`; tests in `tests/*.test.ts`.

## Commit & Pull Request Guidelines

Conventional Commits with Chinese descriptions: `feat(examples): 移植 T2-T4`, `fix(authz-casbin): 修复竞态`, `refactor(transport): 移除入口`. Prefix: `feat`/`fix`/`refactor`/`docs`/`test`/`chore`.

PRs should reference a milestone (M0–M9) and confirm `task verify` (Go) or `pnpm build` + `pnpm test` (frontend) passes.

## Agent-Specific Instructions

- Do **not** delete files without explicit approval.
- Do **not** refactor adjacent code unrelated to your task.
- Match existing style; preserve wire injection in Go; keep Vue feature modules self-contained.
