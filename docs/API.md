# `liquid` — Starlark API Reference

The complete reference for every script-facing builtin, object method, and
configuration accessor exposed by the `liquid` module. For an overview,
installation, and a quickstart, see the [README](../README.md).

The module exposes two top-level builtins via `load("liquid", …)` — `render`
(parse and render in one call) and `parse` (compile once, render many times) —
plus a set of configuration accessors (`get_<key>` / `set_<key>`) generated from
the module's options. A compiled template object returned by `parse` carries a
single `render` method.

Liquid is a **sandboxed** template language: a template can only see the
variables you place in its bindings — there is no implicit access to host state
or script globals. Variables are passed **explicitly** as a bindings dict and/or
keyword arguments.

## Contents

- [Functions](#functions)
  - [`render`](#rendersource-bindingsnone-kwargs)
  - [`parse`](#parsesource)
- [Template object](#template-object)
  - [`Template.render`](#templaterenderbindingsnone-kwargs)
- [Bindings: dict and keyword merge](#bindings-dict-and-keyword-merge)
- [Filters](#filters)
- [Tags and control flow](#tags-and-control-flow)
- [Errors and missing values](#errors-and-missing-values)
- [Variable flow](#variable-flow)
- [Safety](#safety)
- [Configuration](#configuration)

## Functions

### `render(source, bindings=None, **kwargs)`

Parses and renders a Liquid template in one call.

**Parameters:**

- `source` (string or bytes): The template source to parse and render.
- `bindings` (dict, optional): Template variables as a dict (default: `None`,
  i.e. no dict bindings).
- `**kwargs`: Additional template variables passed as keyword arguments. They are
  **merged on top of** `bindings` — on a name conflict the keyword wins.

**Returns:** The rendered template as a string.

**Errors:**

- `liquid.render: bindings must be a dict, got <type>` — the positional
  `bindings` argument was not a dict.
- `liquid: …` — a parse or render error from the engine (see
  [Errors and missing values](#errors-and-missing-values)).

**Example:**

```python
load("liquid", "render")

# One-shot render with a bindings dict
render("Hello {{ name }}! You have {{ count }} messages.",
       {"name": "World", "count": 3})
# => "Hello World! You have 3 messages."
```

### `parse(source)`

Compiles a template once so it can be rendered repeatedly with different
bindings — cheaper than re-parsing on every call.

**Parameters:**

- `source` (string or bytes): The template source to compile.

**Returns:** A [`Template`](#template-object) object.

**Errors:** A syntax error is reported with a `liquid.parse:` prefix, e.g.
`liquid.parse: Liquid error: unterminated "if" block in {% if %}`.

**Example:**

```python
load("liquid", "parse")

tmpl = parse("{% for x in xs %}{{ x }}{% endfor %}")
tmpl.render({"xs": [1, 2, 3]})        # => "123"
tmpl.render({"xs": ["a", "b"]})       # => "ab"
```

## Template object

A compiled template returned by [`parse`](#parsesource). It exposes a single
attribute, the `render` method, for repeated rendering without re-parsing. Its
type name is `liquid.Template`. The object is unhashable and renders as
`<liquid.Template>`.

### `Template.render(bindings=None, **kwargs)`

Renders a previously compiled template with the given bindings. Bindings follow
the same rules as [`render`](#rendersource-bindingsnone-kwargs): a positional
dict plus keyword arguments merged on top (kwargs win on conflict).

**Parameters:**

- `bindings` (dict, optional): Template variables as a dict (default: `None`).
- `**kwargs`: Additional template variables; merged on top of `bindings`.

**Returns:** The rendered template as a string.

**Errors:** Same shapes as [`render`](#rendersource-bindingsnone-kwargs): a
`liquid.Template.render: bindings must be a dict, got <type>` argument error, or
a `liquid:` render error.

**Example:**

```python
load("liquid", "parse")

tmpl = parse("Hi {{ name }}")
tmpl.render({"name": "Ada"})    # => "Hi Ada"
tmpl.render(name="Bob")         # => "Hi Bob"  (keyword binding)
```

## Bindings: dict and keyword merge

Both `render` and `Template.render` accept template variables two ways: an
optional positional `bindings` dict, and arbitrary keyword arguments. Keyword
arguments are bindings too, and they are **merged on top of** the dict — on a
name conflict the keyword wins.

```python
load("liquid", "render")

render("Hi {{ name }}", name="Ada")          # => "Hi Ada"
render("{{ a }}-{{ b }}", {"a": 1}, b=2)     # => "1-2"   (merge)
render("{{ x }}", {"x": 1}, x=9)             # => "9"     (kwargs override the dict)
```

Dict values flow through unchanged, so `{{ user.name }}` reaches into nested
objects:

```python
load("liquid", "render")

render("{{ user.name }} <{{ user.email }}>",
       {"user": {"name": "Ada", "email": "ada@example.com"}})
# => "Ada <ada@example.com>"
```

## Filters

Pipe a value through a filter with `|`. osteele/liquid v1.4.0 ships the standard
Shopify filter set:

```python
load("liquid", "render")

render("{{ name | upcase }}", {"name": "ada"})            # => "ADA"
render('{{ items | join: ", " }}', {"items": [1, 2, 3]})  # => "1, 2, 3"
render("{{ price | default: 0 }}")                        # => "0"  (undefined -> default)
render("{{ price | default: 0 }}", {"price": 42})         # => "42"
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

## Tags and control flow

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

## Errors and missing values

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

In **strict** mode (the `strict` option / `LIQUID_STRICT=true`, see
[Configuration](#configuration)) an undefined variable is an error instead:

```python
render("{{ missing }}")
# error: liquid: Liquid error: undefined variable in {{ missing }}
```

### Error message shapes

All render-time errors are wrapped with a `liquid:` prefix; `parse()` errors are
wrapped with `liquid.parse:` and argument errors with `liquid.render:` (or
`liquid.Template.render:` for the template method). The exact strings you will
see:

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

```text
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
  global capture (this matches Liquid's original semantics). A non-dict
  `bindings` argument is a script error.
- **`{% include %}` is disabled.** The filesystem include tag is overridden to
  error, so a template cannot read host files.
- **Output is bounded.** Rendered output is capped at `max_output_size` bytes
  (256 KiB by default); exceeding it returns an error instead of exhausting
  memory.
- **No host panics.** Parse/render panics are recovered and returned as errors.

## Configuration

Each module configuration option is exposed to scripts as a pair of generated
accessor builtins (loaded from the `liquid` module alongside the functions
above):

- **`get_<key>()`** — returns the current value of the option.
- **`set_<key>(value)`** — sets the option (returns `None`).

An option's value resolves in priority order: an explicit `set_<key>` value, the
environment variable, then the default. These options apply to every subsequent
`render` / `parse` call (and to templates compiled by `parse`).

None of the `liquid` options are secret, so every option exposes **both**
`get_<key>` and `set_<key>`. (A secret option would expose only its `set_<key>`
accessor — never a getter — but this module has none.)

| Option | Getter | Setter | Type | Env var | Default | Description |
|--------|--------|--------|------|---------|---------|-------------|
| `max_output_size` | `get_max_output_size` | `set_max_output_size` | int | `LIQUID_MAX_OUTPUT_SIZE` | `262144` | Maximum rendered output size in bytes (256 KiB); exceeding it returns an error |
| `strict` | `get_strict` | `set_strict` | bool | `LIQUID_STRICT` | `false` | Error when a template references an undefined variable (default: render it as empty, per Liquid) |

**Example:**

```python
load(
    "liquid",
    "render",
    # getters
    "get_max_output_size", "get_strict",
    # setters
    "set_max_output_size", "set_strict",
)

set_strict(True)
print(get_strict())          # True

render("{{ missing }}")      # now errors instead of rendering empty
```
