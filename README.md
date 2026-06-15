# 💧 `liquid` — Liquid templates for Starlark

[![Go Reference](https://pkg.go.dev/badge/github.com/starpkg/liquid.svg)](https://pkg.go.dev/github.com/starpkg/liquid)
[![codecov](https://codecov.io/gh/starpkg/liquid/graph/badge.svg)](https://codecov.io/gh/starpkg/liquid)
![binary footprint](https://img.shields.io/badge/binary_footprint-%2B0.6_MB-blue)

Render [Liquid](https://shopify.github.io/liquid/) templates from Starlark, built
on [osteele/liquid](https://github.com/osteele/liquid) (v1.4.0).

## Overview

starpkg gives Starlark scripts **support for necessary local operations** plus
**simple abstractions over common online services**, for ease of use. `liquid`
is a **local capability** — a pure-Go, offline text-templating primitive with no
network or filesystem reach. It depends downward on `starpkg/base` (the
module/config system), `1set/starlet` (the Machine + the `dataconv` value
bridge), and transitively `1set/starlight` + `go.starlark.net`; nothing in the
ecosystem depends on it.

Liquid is a **sandboxed** template language: a template can only see the
variables you place in its bindings — there is no implicit access to host state
or script globals. This module mirrors that model exactly: variables are passed
**explicitly** as a bindings dict and/or keyword arguments.

For the complete per-builtin reference — signatures, parameters, returns,
errors, examples — and the configuration accessors, see
**[docs/API.md](docs/API.md)**.

## Installation

```bash
go get github.com/starpkg/liquid
```

## Quickstart

Wire the module into a Starlet interpreter, then `load("liquid", …)` from a
script:

```go
package main

import (
	"fmt"

	"github.com/1set/starlet"
	"github.com/starpkg/liquid"
)

func main() {
	mod := liquid.NewModule()
	interpreter := starlet.NewWithLoaders(nil, nil, starlet.ModuleLoaderMap{
		"liquid": mod.LoadModule(),
	})

	script := `
load("liquid", "render")
out = render("Hello {{ name }}!", {"name": "World"})
print(out)
`
	if _, err := interpreter.RunScript([]byte(script), nil); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}
```

From Starlark, render in one call, or compile once and render many times:

```python
load("liquid", "render", "parse")

# One-shot: bindings dict plus keyword arguments (kwargs win on a name conflict)
render("Hello {{ name }}! You have {{ count }} messages.",
       {"name": "World", "count": 3})
# => "Hello World! You have 3 messages."

# Compile once, render repeatedly
tmpl = parse("{% for x in xs %}{{ x }}{% endfor %}")
tmpl.render({"xs": [1, 2, 3]})        # => "123"
```

## Starlark API at a glance

Top-level builtins (`load("liquid", …)`):

- `render(source, bindings=None, **kwargs)` — parse and render a template in one
  call; returns the rendered string.
- `parse(source)` — compile a template once; returns a `Template` object for
  repeated rendering.

Template object (returned by `parse`):

- `Template.render(bindings=None, **kwargs)` — render the compiled template with
  the given bindings (same merge rule as `render`).

Both render entry points support `{% for %}` / `{% if %}` / `{% unless %}` /
`{% case %}` control flow and the 48 standard Shopify filters (`upcase`, `join`,
`default`, `sort`, …); `{% include %}` is disabled. See
**[docs/API.md](docs/API.md)** for full signatures, the filter set, the exact
error-message shapes, and examples of every builtin and method above.

## Configuration

The module's options (`max_output_size`, `strict`) are configured via
environment variables (`LIQUID_MAX_OUTPUT_SIZE` / `LIQUID_STRICT`) or per-option
`get_<key>` / `set_<key>` accessor builtins, and bound how every subsequent
`render` / `parse` call behaves. Rendered output is capped at `max_output_size`
bytes (256 KiB by default); `strict` turns an undefined variable into an error.
See the [Configuration section of docs/API.md](docs/API.md#configuration) for the
full option table, defaults, accessors, and safety details.

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE) file
for details.
