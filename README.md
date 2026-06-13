# 💧 `liquid` — Liquid templates for Starlark

[![Go Reference](https://pkg.go.dev/badge/github.com/starpkg/liquid.svg)](https://pkg.go.dev/github.com/starpkg/liquid)

Render [Liquid](https://shopify.github.io/liquid/) templates from Starlark, built
on [osteele/liquid](https://github.com/osteele/liquid).

Liquid is a **sandboxed** template language: a template can only see the
variables you place in its bindings — there is no implicit access to host state
or script globals. This module mirrors that model exactly: variables are passed
**explicitly** as a bindings dict and/or keyword arguments.

## Installation

```bash
go get github.com/starpkg/liquid
```

## Functions

| Function | Signature | Description |
|----------|-----------|-------------|
| `render` | `render(source, bindings=None, **kwargs) -> str` | Parse and render `source` in one call. `bindings` is an optional dict; keyword arguments are merged on top of it (kwargs win on conflict). |
| `parse` | `parse(source) -> Template` | Compile a template once for repeated rendering. |
| `Template.render` | `Template.render(bindings=None, **kwargs) -> str` | Render a compiled template with the given bindings. |

## Usage

```python
load("liquid", "render", "parse")

# One-shot render with a bindings dict
render("Hello {{ name }}! You have {{ count }} messages.",
       {"name": "World", "count": 3})
# => "Hello World! You have 3 messages."

# Keyword arguments are bindings too
render("Hi {{ name }}", name="Ada")          # => "Hi Ada"
render("{{ a }}-{{ b }}", {"a": 1}, b=2)     # => "1-2"

# Nested objects and loops
render("{% for x in items %}{{ x }} {% endfor %}", {"items": [1, 2, 3]})
# => "1 2 3 "

# Compile once, render many times
tmpl = parse("{{ greeting }}, {{ name }}!")
tmpl.render({"greeting": "Hi", "name": "Ada"})   # => "Hi, Ada!"
tmpl.render({"greeting": "Bye", "name": "Bob"})  # => "Bye, Bob!"
```

## Variable flow

```
Starlark dict {"user": {"name": "Ada"}, "count": 3}
      │  Starlark -> Go (dataconv; dict keys become strings)
      ▼
Go map[string]interface{}{ "user": map[string]interface{}{"name": "Ada"}, "count": 3 }
      │  == liquid.Bindings
      ▼
osteele/liquid engine  ->  rendered string
```

## Safety

- **Sandboxed bindings.** Templates see only what you pass; there is no implicit
  global capture (this matches Liquid's original semantics).
- **`{% include %}` is disabled.** The filesystem include tag is overridden to
  error, so a template cannot read host files.
- **Output is bounded.** Rendered output is capped at `max_output_size` bytes
  (256 KiB by default); exceeding it returns an error instead of exhausting
  memory.
- **No host panics.** Render panics are recovered and returned as errors.

## Configuration

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `max_output_size` | `int` | `262144` | Maximum rendered output size in bytes (256 KiB) |
| `strict` | `bool` | `false` | Error when a template references an undefined variable (default: render it as empty, per Liquid) |

Both are also settable via the `LIQUID_MAX_OUTPUT_SIZE` / `LIQUID_STRICT`
environment variables.
