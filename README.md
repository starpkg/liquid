# 💧 `liquid` — Liquid templates for Starlark

[![Go Reference](https://pkg.go.dev/badge/github.com/starpkg/liquid.svg)](https://pkg.go.dev/github.com/starpkg/liquid)
[![codecov](https://codecov.io/gh/starpkg/liquid/graph/badge.svg)](https://codecov.io/gh/starpkg/liquid)
![binary footprint](https://img.shields.io/badge/binary_footprint-%2B0.6_MB-blue)

Render [Liquid](https://shopify.github.io/liquid/) templates from Starlark, built
on [osteele/liquid](https://github.com/osteele/liquid) (v1.4.0).

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

### In Go

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

### In Starlark

#### (a) One-shot render with a bindings dict

```python
load("liquid", "render")

render("Hello {{ name }}! You have {{ count }} messages.",
       {"name": "World", "count": 3})
# => "Hello World! You have 3 messages."
```

#### (b) Keyword bindings, and dict + kwargs merge / override

Keyword arguments are bindings too, and they are **merged on top of** the dict —
on a name conflict the keyword wins.

```python
load("liquid", "render")

render("Hi {{ name }}", name="Ada")          # => "Hi Ada"
render("{{ a }}-{{ b }}", {"a": 1}, b=2)     # => "1-2"   (merge)
render("{{ x }}", {"x": 1}, x=9)             # => "9"     (kwargs override the dict)
```

#### (c) Nested object access

Dict values flow through unchanged, so `{{ user.name }}` reaches into nested
objects.

```python
load("liquid", "render")

render("{{ user.name }} <{{ user.email }}>",
       {"user": {"name": "Ada", "email": "ada@example.com"}})
# => "Ada <ada@example.com>"
```

#### (d) Loops and conditionals

```python
load("liquid", "render")

# {% for %}{% endfor %} — forloop.index / .first / .last are available.
render("{% for x in items %}{{ forloop.index }}:{{ x }} {% endfor %}",
       {"items": ["a", "b", "c"]})
# => "1:a 2:b 3:c "

# {% if %}{% elsif %}{% else %}{% endif %}
render("{% if n > 5 %}big{% elsif n > 0 %}small{% else %}none{% endif %}",
       {"n": 3})
# => "small"

# {% unless %} and {% case %}{% when %} are supported too.
render("{% unless ok %}blocked{% endunless %}", {"ok": False})       # => "blocked"
render("{% case n %}{% when 1 %}one{% when 2 %}two{% else %}many{% endcase %}",
       {"n": 2})                                                     # => "two"
```

#### (e) Filters

Pipe a value through a filter with `|`. osteele/liquid v1.4.0 ships the standard
Shopify filter set:

```python
load("liquid", "render")

render("{{ name | upcase }}", {"name": "ada"})        # => "ADA"
render('{{ items | join: ", " }}', {"items": [1, 2, 3]})  # => "1, 2, 3"
render("{{ price | default: 0 }}")                     # => "0"  (undefined -> default)
render("{{ price | default: 0 }}", {"price": 42})      # => "42"
render('{{ xs | sort | join: "," }}', {"xs": [3, 1, 2]})  # => "1,2,3"  (filters chain)
```

The **48 standard filters** available in v1.4.0 are:

| Category | Filters |
|----------|---------|
| String | `append` `capitalize` `downcase` `escape` `escape_once` `lstrip` `newline_to_br` `prepend` `remove` `remove_first` `replace` `replace_first` `rstrip` `slice` `split` `strip` `strip_html` `strip_newlines` `truncate` `truncatewords` `upcase` `url_decode` `url_encode` |
| Array | `compact` `concat` `first` `join` `last` `map` `reverse` `size` `sort` `sort_natural` `uniq` |
| Math | `abs` `ceil` `divided_by` `floor` `minus` `modulo` `plus` `round` `times` |
| Other | `date` `default` `inspect` `json` `type` |

> **RE2-style limits.** osteele/liquid does **not** implement Shopify's
> regex-based filters (no `regex_replace`); `replace`/`remove`/`split` operate on
> literal substrings, not patterns. There is no `where` filter either. If you
> need regular expressions, do that work in Starlark (or the `regex` module)
> before passing values into a template.

#### (f) Compile once, render many times

`parse()` compiles a template once; the returned `Template` can be rendered
repeatedly with different bindings (cheaper than re-parsing each call).

```python
load("liquid", "parse")

tmpl = parse("{% for x in xs %}{{ x }}{% endfor %}")
tmpl.render({"xs": [1, 2, 3]})        # => "123"
tmpl.render({"xs": ["a", "b"]})       # => "ab"
```

## Errors & missing values

The module never lets template input crash the host: parse/render panics are
recovered into ordinary errors, and every error message is prefixed so you can
recognize where it came from.

### Missing variables: lenient vs. strict

By default rendering is **lenient** — an undefined variable renders as the empty
string (this matches Liquid's original semantics):

```python
load("liquid", "render")

render("[{{ missing }}]")        # => "[]"   (no error)
```

In **strict** mode (the `strict` option / `LIQUID_STRICT=true`) an undefined
variable is an error instead:

```python
render("{{ missing }}")
# error: liquid: Liquid error: undefined variable in {{ missing }}
```

### Error message shapes

All render-time errors are wrapped with a `liquid:` prefix; `parse()` errors are
wrapped with `liquid.parse:` and argument errors with `liquid.render:`. The exact
strings you will see:

| Situation | Example error |
|-----------|---------------|
| Syntax error | `liquid: Liquid error: syntax error in "" in {% if %}` |
| Unterminated block | `liquid: Liquid error: unterminated "if" block in {% if x %}` |
| Undefined filter | `liquid: Liquid error: undefined filter "nope" in {{ x \| nope }}` |
| Strict undefined variable | `liquid: Liquid error: undefined variable in {{ missing }}` |
| `{% include %}` (disabled) | `liquid: Liquid error: liquid: the {% include %} tag is disabled (no filesystem access) in {% include 'a.txt' %}` |
| Output too large | `liquid: rendered output exceeds the configured maximum size` |
| Bindings not a dict | `liquid.render: bindings must be a dict, got list` |
| Syntax error from `parse()` | `liquid.parse: Liquid error: unterminated "if" block in {% if %}` |

```python
load("liquid", "render")

render("{% if %}x{% endif %}")
# error: liquid: Liquid error: syntax error in "" in {% if %}

render("{% include 'secret.txt' %}")
# error: liquid: Liquid error: liquid: the {% include %} tag is disabled (no filesystem access) in {% include 'secret.txt' %}

render("x", [1, 2, 3])
# error: liquid.render: bindings must be a dict, got list
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
- **No host panics.** Parse/render panics are recovered and returned as errors.

## Configuration

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `max_output_size` | `int` | `262144` | Maximum rendered output size in bytes (256 KiB) |
| `strict` | `bool` | `false` | Error when a template references an undefined variable (default: render it as empty, per Liquid) |

Both are also settable via the `LIQUID_MAX_OUTPUT_SIZE` / `LIQUID_STRICT`
environment variables.

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE) file for details.
