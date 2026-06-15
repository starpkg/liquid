# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`starpkg/liquid` is an **L4 domain module** of the Star\* ecosystem: it exposes the [Liquid](https://shopify.github.io/liquid/) template language to Starlark scripts. A script loads the module, hands it a template source plus a bindings dict (and/or keyword arguments), and gets back the rendered string.

The starpkg layer is **support for necessary local operations + simple abstractions over common online services, for ease of use.** `liquid` sits firmly on the **local** side: it is a pure-Go, offline text-templating primitive — no network, no filesystem reach (the `{% include %}` tag is deliberately disabled). It wraps [`osteele/liquid`](https://github.com/osteele/liquid) v1.4.0, the pure-Go Liquid engine.

Layer position: depends downward on `starpkg/base` (the module/config system — `ConfigurableModule`, `ConfigOption`), `1set/starlet` (the `ModuleLoader` type + the `dataconv` value bridge that turns a Starlark dict into `map[string]interface{}`), and transitively `1set/starlight` + `go.starlark.net`. Nothing in the ecosystem depends on it.

## Dev commands

Pure Go library with a Makefile. From this repo:

```bash
make test                                  # -race -cover, the working bar
make ci                                    # -race -cover profile + bench compile (what CI runs)
go test ./... -run TestRenderBindings      # a single test
gofmt -l . && go vet ./...                 # must be clean before commit
go run github.com/1set/meta/doccov@master . # the doc-coverage gate (README <-> builtins)
```

**Verify on the go floor in Docker** — this repo's floor is **go 1.19** (its `go.mod`, matching the `go.starlark.net` baseline that needs `maphash.String`), which may be older than the local toolchain. Behavior on the floor must be checked in a container:

```bash
docker run --rm -v "$PWD":/src -v "$HOME/go/pkg/mod":/go/pkg/mod -w /src golang:1.19 go test -race -count=1 ./...
```

There are no network/credential-gated tests in this module — everything runs offline and deterministically. Integration scripts under `../test/liquid/*.star` (if any) live in the **private `starpkg/test` repo** and auto-skip when that directory is absent (e.g. in CI).

## Architecture (the part that spans files)

The module is a thin, hardened bridge: **Starlark args → Go `liquid.Bindings` → `osteele/liquid` engine → bounded string**. Two source files:

- **`liquid.go`** — the module entry and all host-side policy. `Module` holds a `*base.ConfigurableModule` plus its `*base.ConfigurableModuleExt` (the typed config reader). `NewModule()` builds it with the two config options. `LoadModule()` registers the two script-facing builtins: **`render`** (parse + render in one call) and **`parse`** (compile once, returns a `liquid.Template` object). This file also holds the argument plumbing (`parseRenderArgs`, `collectBindings`), the engine factory (`newEngine` — applies the safety policy), the render core (`renderSource` / `renderWith`), the output-bounding `cappedWriter`, and the panic-recovering `parseString`.
- **`template.go`** — `templateValue`, the compiled-template object returned by `parse()`. It implements `starlark.Value` + `starlark.HasAttrs`, exposing a single attribute — the **`render`** method (`liquid.Template.render`) — for repeated rendering without re-parsing. Its `renderTemplate` mirrors `renderWith`'s recover-and-cap discipline.

**Value flow** (the one non-obvious cross-cutting path): a Starlark bindings dict is converted to Go by `starlet/dataconv.Unmarshal`, which yields a `map[string]interface{}` with string keys; keyword arguments are unmarshalled the same way and merged on top (kwargs win on a name conflict). That map is `liquid.Bindings`, fed straight to the engine. Nested dicts flow through unchanged, so `{{ user.name }}` reaches into them.

**Third-party SDK wrap points** — every call into `osteele/liquid` is funneled through a guard: `engine.ParseString` via `parseString` (recover), `engine.ParseAndFRender` via `renderWith` (recover + cap), `tmpl.FRender` via `renderTemplate` (recover + cap). `newEngine` is the single place the engine is constructed and the only place its policy (strict variables, disabled include) is set.

## Invariants / hardening (preserve when editing)

The module runs templates the host may not fully trust. The guarantees below are easy to silently break; keep them when editing.

1. **No host panics from template input.** `osteele/liquid` can panic on some malformed input. Every entry into it is wrapped in a `defer/recover` that converts the panic into an ordinary error: `parseString` (for `parse()`), `renderWith` (for `render()`), `renderTemplate` (for `Template.render`). Don't remove a deferred recover, and don't add a new engine call path that bypasses one.
2. **Bounded output.** Rendering writes through `cappedWriter{limit: maxOutput}`, which fails fast once `buf.Len()+len(p)` would exceed the cap — so a hostile `{% for %}` blow-up can't exhaust host memory. `max_output_size` (config / `LIQUID_MAX_OUTPUT_SIZE`, default `262144` = 256 KiB, ADR-010) sets the cap; exceeding it returns `errOutputLimit`. New render paths must route through `cappedWriter`, not a raw `bytes.Buffer`.
3. **No filesystem reach.** `newEngine` overrides the `include` tag with a handler that always errors (`errIncludeUsage`), so a template cannot read host files. Don't re-enable the stock filesystem `{% include %}`.
4. **Sandboxed bindings.** Templates see only the bindings passed in — there is no implicit capture of script globals or host state (this matches Liquid's original semantics). Bindings must be a real dict; a non-dict argument is a script error (`bindings must be a dict, got <type>`).
5. **Backward compatibility.** `NewModule()` defaults to **lenient** rendering (undefined variables render empty) and the 256 KiB cap. `strict` mode (config / `LIQUID_STRICT`) is opt-in. Any new safety lever must default to the historical behavior so old scripts run identically.

## Test organization

Group by functional goal — **do not add one `*_test.go` per fix.** `liquid_test.go` is the single home, opened with a commented section list; add a new test as a **section** there, not a new file. The sections: rendering via the Starlark API (`TestRenderBindings`, `TestParseReuse`, `TestRenderBadBindings`), filters & tags (`TestFilters`, `TestTagsAndControlFlow`), error & missing-value behavior (`TestErrorMessageShapes`, `TestUndefinedVariableLenientVsStrict`), safety/hardening (`TestIncludeDisabled`, `TestOutputCap`, `TestCappedWriter`, `TestStrictVariables`, `TestMalformedTemplateNoPanic`, `TestParseStringRecoversPanic`), config end-to-end (`TestStrictOptionThroughModule`, `TestMaxOutputSizeThroughModule`), and the `templateValue` surface (`TestTemplateValueSurface`). Tests are table/example-driven; no third-party test framework. Tests that assert exact wrapped error strings (`liquid:` / `liquid.parse:` / `liquid.render:` prefixes) use substring matches so engine source-location detail can vary.

## Documentation

Three layers must stay in sync (enforced by the doc standard, `plan/starpkg文档标准（DOC-STD）`):

- **`README.md`** — every script-facing builtin (`render`, `parse`) and object method (`Template.render`) documented as a backtick whole-word, with signatures, the bindings/kwargs merge rule, the standard filter set, and the exact error-message shapes. Host levers (`max_output_size`, `strict`, the `LIQUID_*` env vars) under *Configuration* / *Safety*. Names and signatures must match the code.
- **GoDoc** — package comment (`liquid.go`) + a doc comment on every exported symbol (`ModuleName`, `Module`, `NewModule`, `LoadModule`), first word = the symbol name (gated by `revive`'s `exported` rule in CI).
- **doc-coverage gate** — `go run github.com/1set/meta/doccov@master .` (wired into `.github/workflows/build.yml` via `doc-coverage: true`) fails CI if a registered builtin is not documented as a backtick word in the README.

## Release discipline

- **Floor = go 1.19** (this repo's `go.mod`, tracking the `go.starlark.net` baseline `ffb3f39`). A repo's floor only rises in its own isolated pin PR.
- **CI matrix** = `[1.19.x, 1.25.x]` via the centralized reusable workflow in `1set/meta` (`go-ci.yml`, pinned to a full commit SHA for supply-chain safety; bump the pin when meta's workflow changes).
- **Pin upgrade is the last PR of the series.** Upgrading the `go.starlark.net` / 1set deps / go floor happens as one isolated PR *after* the bug/doc fixes, with the CI floor leg flipped in the same PR. Never tag a release before the pin PR merges.
- **Bumping the version, the go floor, or tagging are user-confirmed actions** — never tag autonomously; draft the title + notes and tag only after explicit approval; default to patch bumps; a published tag is immutable in the Go module proxy.
