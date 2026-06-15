package liquid

// Tests for the liquid module.
//
// Sections:
//   - rendering via the Starlark API (render / parse, dict + kwargs bindings)
//   - argument parsing & validation (parseRenderArgs / collectBindings error branches)
//   - filters & tags (standard filters, loops/conditionals, control flow)
//   - error & missing-value behavior (exact wrapped message shapes)
//   - safety: include disabled, output cap, strict mode, malformed-input hardening
//   - config options end-to-end (strict / max_output_size via the module path)
//   - templateValue surface (type/str repr, unhashable, value protocol, attr/arg errors)

import (
	"fmt"
	"strings"
	"testing"

	"github.com/1set/starlet"
	osliquid "github.com/osteele/liquid"
	"go.starlark.net/starlark"
)

// runRender loads the module, runs the script, and returns the value assigned to
// the global `out` (as a string) or any execution error.
func runRender(t *testing.T, mod *Module, script string) (string, error) {
	t.Helper()
	m := starlet.NewDefault()
	m.SetScriptContent([]byte(script))
	m.SetLazyloadModules(map[string]starlet.ModuleLoader{ModuleName: mod.LoadModule()})
	res, err := m.Run()
	if err != nil {
		return "", err
	}
	if v, ok := res["out"]; ok {
		return fmt.Sprintf("%v", v), nil
	}
	return "", nil
}

// --- rendering via the Starlark API ------------------------------------------

func TestRenderBindings(t *testing.T) {
	cases := []struct {
		name, script, want string
	}{
		{"dict", `load("liquid","render")
out = render("Hello {{ name }}!", {"name": "World"})`, "Hello World!"},
		{"kwargs", `load("liquid","render")
out = render("Hi {{ name }}", name="Ada")`, "Hi Ada"},
		{"dict+kwargs", `load("liquid","render")
out = render("{{ a }}-{{ b }}", {"a": 1}, b=2)`, "1-2"},
		{"kwargs override dict", `load("liquid","render")
out = render("{{ x }}", {"x": 1}, x=9)`, "9"},
		{"nested object", `load("liquid","render")
out = render("{{ user.name }}", {"user": {"name": "Ada"}})`, "Ada"},
		{"loop", `load("liquid","render")
out = render("{% for x in items %}{{ x }},{% endfor %}", {"items": [1, 2, 3]})`, "1,2,3,"},
		{"no bindings", `load("liquid","render")
out = render("static")`, "static"},
	}
	mod := NewModule()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := runRender(t, mod, c.script)
			if err != nil {
				t.Fatalf("run error: %v", err)
			}
			if got != c.want {
				t.Errorf("render = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseReuse(t *testing.T) {
	script := `load("liquid", "parse")
tmpl = parse("{% for x in xs %}{{ x }}{% endfor %}")
out = tmpl.render({"xs": [1, 2, 3]}) + "|" + tmpl.render({"xs": ["a", "b"]})`
	got, err := runRender(t, NewModule(), script)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "123|ab" {
		t.Errorf("parse/render reuse = %q, want %q", got, "123|ab")
	}
}

func TestRenderBadBindings(t *testing.T) {
	_, err := runRender(t, NewModule(), `load("liquid","render")
out = render("x", [1,2,3])`) // list, not dict
	if err == nil || !strings.Contains(err.Error(), "bindings must be a dict") {
		t.Errorf("expected dict-type error, got %v", err)
	}
}

// --- argument parsing & validation -------------------------------------------

// TestParseRenderArgs exercises parseRenderArgs directly (no TTY/network): the
// happy path plus each clean-error branch a script can hit — missing source,
// too many positionals, and a non-string source. Asserts both the returned
// source/bindings and the exact wrapped error strings the README promises.
func TestParseRenderArgs(t *testing.T) {
	dict := starlark.NewDict(1)
	if err := dict.SetKey(starlark.String("k"), starlark.MakeInt(1)); err != nil {
		t.Fatalf("dict setup: %v", err)
	}
	cases := []struct {
		name       string
		args       starlark.Tuple
		kwargs     []starlark.Tuple
		wantSource string
		wantErrSub string // "" means expect no error
	}{
		{
			name:       "source only",
			args:       starlark.Tuple{starlark.String("hi {{ x }}")},
			wantSource: "hi {{ x }}",
		},
		{
			name:       "source + dict",
			args:       starlark.Tuple{starlark.String("t"), dict},
			wantSource: "t",
		},
		{
			name:       "source + None bindings",
			args:       starlark.Tuple{starlark.String("t"), starlark.None},
			wantSource: "t",
		},
		{
			name:       "bytes source",
			args:       starlark.Tuple{starlark.Bytes("raw {{ x }}")},
			wantSource: "raw {{ x }}",
		},
		{
			name:       "missing source",
			args:       starlark.Tuple{},
			wantErrSub: "liquid.render: missing source argument",
		},
		{
			name:       "too many positionals",
			args:       starlark.Tuple{starlark.String("t"), dict, starlark.String("extra")},
			wantErrSub: "liquid.render: got 3 positional arguments, want at most 2 (source, bindings)",
		},
		{
			name:       "non-string source",
			args:       starlark.Tuple{starlark.MakeInt(42)},
			wantErrSub: "liquid.render: source:",
		},
		{
			name:       "non-dict bindings",
			args:       starlark.Tuple{starlark.String("t"), starlark.MakeInt(7)},
			wantErrSub: "liquid.render: bindings must be a dict, got int",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, bindings, err := parseRenderArgs("liquid.render", c.args, c.kwargs)
			if c.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrSub) {
					t.Fatalf("error = %v, want it to contain %q", err, c.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if src != c.wantSource {
				t.Errorf("source = %q, want %q", src, c.wantSource)
			}
			if bindings == nil {
				t.Errorf("bindings = nil, want a non-nil map")
			}
		})
	}
}

// TestCollectBindingsBranches covers the merge rules and error branches of
// collectBindings beyond the smoke test in TestCollectBindings: nil/None dict,
// dict + kwargs merge, kwargs overriding the dict, a non-dict argument, and a
// keyword value that dataconv cannot convert (an unrecognized Starlark type).
func TestCollectBindingsBranches(t *testing.T) {
	mkDict := func(k string, v starlark.Value) *starlark.Dict {
		d := starlark.NewDict(1)
		if err := d.SetKey(starlark.String(k), v); err != nil {
			t.Fatalf("dict setup: %v", err)
		}
		return d
	}

	t.Run("nil dict yields empty map", func(t *testing.T) {
		b, err := collectBindings("liquid.render", nil, nil)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(b) != 0 {
			t.Errorf("bindings = %v, want empty", b)
		}
	})

	t.Run("dict + kwargs merge", func(t *testing.T) {
		b, err := collectBindings("liquid.render", mkDict("a", starlark.MakeInt(1)),
			[]starlark.Tuple{{starlark.String("b"), starlark.MakeInt(2)}})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if fmt.Sprintf("%v", b["a"]) != "1" || fmt.Sprintf("%v", b["b"]) != "2" {
			t.Errorf("merged bindings = %v, want a=1 b=2", b)
		}
	})

	t.Run("kwargs override dict", func(t *testing.T) {
		b, err := collectBindings("liquid.render", mkDict("x", starlark.MakeInt(1)),
			[]starlark.Tuple{{starlark.String("x"), starlark.MakeInt(9)}})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if fmt.Sprintf("%v", b["x"]) != "9" {
			t.Errorf("bindings[x] = %v, want 9 (kwargs win)", b["x"])
		}
	})

	t.Run("non-dict argument errors", func(t *testing.T) {
		_, err := collectBindings("liquid.render", starlark.MakeInt(5), nil)
		if err == nil || !strings.Contains(err.Error(), "bindings must be a dict, got int") {
			t.Errorf("err = %v, want a non-dict error", err)
		}
	})

	t.Run("unconvertible keyword value errors", func(t *testing.T) {
		fn := starlark.NewBuiltin("noop", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.None, nil
		})
		_, err := collectBindings("liquid.render", starlark.None,
			[]starlark.Tuple{{starlark.String("f"), fn}})
		if err == nil || !strings.Contains(err.Error(), `keyword "f":`) {
			t.Errorf("err = %v, want a keyword-conversion error", err)
		}
	})

	t.Run("unconvertible dict value errors", func(t *testing.T) {
		fn := starlark.NewBuiltin("noop", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
			return starlark.None, nil
		})
		d := starlark.NewDict(1)
		if err := d.SetKey(starlark.String("f"), fn); err != nil {
			t.Fatalf("dict setup: %v", err)
		}
		_, err := collectBindings("liquid.render", d, nil)
		if err == nil || !strings.Contains(err.Error(), "bindings:") {
			t.Errorf("err = %v, want a dict-value conversion error", err)
		}
	})
}

// TestParseArgErrors covers parse()'s argument validation: a non-string source
// is an UnpackArgs error, and a syntax-error source is wrapped with the
// liquid.parse: prefix (the panic-recovering parseString sits behind it).
func TestParseArgErrors(t *testing.T) {
	cases := []struct {
		name, script, wantSub string
	}{
		{
			name: "non-string source",
			script: `load("liquid","parse")
out = parse(123)`,
			wantSub: "want string or bytes",
		},
		{
			name: "syntax error wrapped",
			script: `load("liquid","parse")
out = parse("{% if %}")`,
			wantSub: "liquid.parse: Liquid error",
		},
	}
	mod := NewModule()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := runRender(t, mod, c.script)
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("error = %v, want it to contain %q", err, c.wantSub)
			}
		})
	}
}

// TestMaxOutputDefaulting covers the maxOutput getter's two arms: an explicit
// positive configured value is used, and a non-positive (0 / unset) value falls
// back to the 256 KiB default (preserving the historical bound).
func TestMaxOutputDefaulting(t *testing.T) {
	// Explicit positive value flows through.
	custom := newModuleWithOptions(
		genConfigOption(configKeyMaxOutputSize, "", 4096),
		genConfigOption(configKeyStrict, "", false),
	)
	if got := custom.maxOutput(); got != 4096 {
		t.Errorf("maxOutput() = %d, want 4096", got)
	}

	// A non-positive configured value falls back to the default.
	zero := newModuleWithOptions(
		genConfigOption(configKeyMaxOutputSize, "", 0),
		genConfigOption(configKeyStrict, "", false),
	)
	if got := zero.maxOutput(); got != defaultMaxOutputSize {
		t.Errorf("maxOutput() with 0 = %d, want default %d", got, defaultMaxOutputSize)
	}

	// The plain constructor uses the default.
	if got := NewModule().maxOutput(); got != defaultMaxOutputSize {
		t.Errorf("NewModule().maxOutput() = %d, want default %d", got, defaultMaxOutputSize)
	}
}

// --- filters & tags ----------------------------------------------------------

// TestFilters documents the standard osteele/liquid v1.4.0 filters this module
// exposes (the README enumerates the full set). Each case asserts the rendered
// output so the README examples stay honest.
func TestFilters(t *testing.T) {
	cases := []struct {
		name, script, want string
	}{
		{"upcase", `load("liquid","render")
out = render("{{ name | upcase }}", {"name": "ada"})`, "ADA"},
		{"downcase", `load("liquid","render")
out = render("{{ name | downcase }}", {"name": "ADA"})`, "ada"},
		{"capitalize", `load("liquid","render")
out = render("{{ name | capitalize }}", {"name": "ada lovelace"})`, "Ada lovelace"},
		{"join", `load("liquid","render")
out = render('{{ items | join: ", " }}', {"items": [1, 2, 3]})`, "1, 2, 3"},
		{"default missing", `load("liquid","render")
out = render("{{ price | default: 0 }}")`, "0"},
		{"default present", `load("liquid","render")
out = render("{{ price | default: 0 }}", {"price": 42})`, "42"},
		{"size", `load("liquid","render")
out = render("{{ items | size }}", {"items": [1, 2, 3, 4]})`, "4"},
		{"first/last", `load("liquid","render")
out = render("{{ xs | first }}/{{ xs | last }}", {"xs": [10, 20, 30]})`, "10/30"},
		{"plus", `load("liquid","render")
out = render("{{ n | plus: 5 }}", {"n": 3})`, "8"},
		{"replace (literal substring)", `load("liquid","render")
out = render('{{ s | replace: "a", "X" }}', {"s": "banana"})`, "bXnXnX"},
		{"chained sort | join", `load("liquid","render")
out = render('{{ xs | sort | join: "," }}', {"xs": [3, 1, 2]})`, "1,2,3"},
		{"truncate", `load("liquid","render")
out = render('{{ s | truncate: 5 }}', {"s": "Hello World"})`, "He..."},
	}
	mod := NewModule()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := runRender(t, mod, c.script)
			if err != nil {
				t.Fatalf("run error: %v", err)
			}
			if got != c.want {
				t.Errorf("render = %q, want %q", got, c.want)
			}
		})
	}
}

// TestTagsAndControlFlow exercises the loop and conditional tags the README
// documents: for (with forloop.index), if/elsif/else, unless, case/when, and
// assign. Nested object access via {{ user.name }} is covered here too.
func TestTagsAndControlFlow(t *testing.T) {
	cases := []struct {
		name, script, want string
	}{
		{"for + forloop.index", `load("liquid","render")
out = render("{% for x in items %}{{ forloop.index }}:{{ x }} {% endfor %}", {"items": ["a", "b", "c"]})`, "1:a 2:b 3:c "},
		{"if/elsif/else", `load("liquid","render")
out = render("{% if n > 5 %}big{% elsif n > 0 %}small{% else %}none{% endif %}", {"n": 3})`, "small"},
		{"unless", `load("liquid","render")
out = render("{% unless ok %}blocked{% endunless %}", {"ok": False})`, "blocked"},
		{"case/when", `load("liquid","render")
out = render("{% case n %}{% when 1 %}one{% when 2 %}two{% else %}many{% endcase %}", {"n": 2})`, "two"},
		{"assign", `load("liquid","render")
out = render("{% assign g = 'hi' %}{{ g }}")`, "hi"},
		{"nested object access", `load("liquid","render")
out = render("{{ user.name }} <{{ user.email }}>", {"user": {"name": "Ada", "email": "ada@example.com"}})`, "Ada <ada@example.com>"},
	}
	mod := NewModule()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := runRender(t, mod, c.script)
			if err != nil {
				t.Fatalf("run error: %v", err)
			}
			if got != c.want {
				t.Errorf("render = %q, want %q", got, c.want)
			}
		})
	}
}

// --- error & missing-value behavior ------------------------------------------

// TestErrorMessageShapes pins the exact wrapped error-string shapes the README
// promises (substring match, so source-location detail can vary). Each row is a
// behavior a reader needs to recognize: syntax errors, undefined filters, the
// disabled include tag, a non-dict bindings argument, and the parse() prefix.
func TestErrorMessageShapes(t *testing.T) {
	cases := []struct {
		name, script, wantSub string
	}{
		{"syntax error", `load("liquid","render")
out = render("{% if %}x{% endif %}")`, `liquid: Liquid error: syntax error`},
		{"unterminated block", `load("liquid","render")
out = render("{% if x %}no end")`, `liquid: Liquid error: unterminated "if" block`},
		{"undefined filter", `load("liquid","render")
out = render("{{ x | nope }}", {"x": 1})`, `liquid: Liquid error: undefined filter "nope"`},
		{"include disabled", `load("liquid","render")
out = render("{% include 'secret.txt' %}")`, `the {% include %} tag is disabled (no filesystem access)`},
		{"bindings not a dict", `load("liquid","render")
out = render("x", [1, 2, 3])`, `liquid.render: bindings must be a dict, got list`},
		{"parse() syntax error", `load("liquid","parse")
tmpl = parse("{% if %}")`, `liquid.parse: Liquid error`},
	}
	mod := NewModule()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := runRender(t, mod, c.script)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantSub)
			}
		})
	}
}

// TestUndefinedVariableLenientVsStrict documents the missing-value contract
// through the public render() path: lenient (default) renders an undefined
// variable as empty; strict turns it into an error.
func TestUndefinedVariableLenientVsStrict(t *testing.T) {
	const script = `load("liquid","render")
out = "[" + render("{{ missing }}") + "]"`

	// Lenient (default): undefined renders empty, so out == "[]".
	if got, err := runRender(t, NewModule(), script); err != nil || got != "[]" {
		t.Errorf("lenient render = (%q, %v), want (\"[]\", nil)", got, err)
	}

	// Strict via the constructor option: undefined errors.
	strict := newModuleWithOptions(
		genConfigOption(configKeyMaxOutputSize, "", defaultMaxOutputSize),
		genConfigOption(configKeyStrict, "", true),
	)
	_, err := runRender(t, strict, `load("liquid","render")
out = render("{{ missing }}")`)
	if err == nil || !strings.Contains(err.Error(), "undefined variable") {
		t.Errorf("strict module: expected undefined-variable error, got %v", err)
	}
}

// --- safety ------------------------------------------------------------------

func TestIncludeDisabled(t *testing.T) {
	_, err := runRender(t, NewModule(), `load("liquid","render")
out = render("{% include 'secret.txt' %}", {})`)
	if err == nil || !strings.Contains(err.Error(), "include") {
		t.Errorf("expected include-disabled error, got %v", err)
	}
}

func TestOutputCap(t *testing.T) {
	// Render a string longer than a tiny cap.
	engine := osliquid.NewEngine()
	_, err := renderWith(engine, "{{ s }}", map[string]interface{}{"s": strings.Repeat("x", 100)}, 10)
	if err != errOutputLimit {
		t.Errorf("expected errOutputLimit, got %v", err)
	}
	// Within the cap it succeeds.
	out, err := renderWith(engine, "{{ s }}", map[string]interface{}{"s": "short"}, 1024)
	if err != nil || out != "short" {
		t.Errorf("renderWith small output = (%q, %v), want (\"short\", nil)", out, err)
	}
}

func TestCappedWriter(t *testing.T) {
	w := &cappedWriter{limit: 5}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := w.Write([]byte("def")); err == nil {
		t.Error("write past limit should error")
	}
	if !w.exceeded {
		t.Error("exceeded flag should be set")
	}
}

func TestStrictVariables(t *testing.T) {
	// Lenient (default): undefined renders empty.
	lenient := osliquid.NewEngine()
	if out, err := renderWith(lenient, "[{{ missing }}]", nil, 1024); err != nil || out != "[]" {
		t.Errorf("lenient undefined = (%q, %v), want (\"[]\", nil)", out, err)
	}
	// Strict: undefined errors.
	strict := osliquid.NewEngine()
	strict.StrictVariables()
	if _, err := renderWith(strict, "{{ missing }}", nil, 1024); err == nil {
		t.Error("strict undefined variable should error")
	}
}

func TestCollectBindings(t *testing.T) {
	kwargs := []starlark.Tuple{{starlark.String("k"), starlark.MakeInt(7)}}
	b, err := collectBindings("test", starlark.None, kwargs)
	if err != nil {
		t.Fatalf("collectBindings: %v", err)
	}
	if got, ok := b["k"]; !ok || fmt.Sprintf("%v", got) != "7" {
		t.Errorf("bindings[k] = %v, want 7", got)
	}
}

// TestMalformedTemplateNoPanic feeds syntax-error templates to BOTH render()
// and parse(): each must return a clean error rather than panic. parse() routes
// its ParseString through a defer/recover (parseString), matching the render
// paths, so a panicking parse degrades to an error instead of crashing the host.
func TestMalformedTemplateNoPanic(t *testing.T) {
	malformed := []string{
		`{% if %}`,          // unterminated if block
		`{% for %}`,         // unterminated for block
		`{{ x | }}`,         // dangling filter pipe
		`{% endfor %}`,      // stray end tag
		`{% unknown_tag %}`, // undefined tag
	}
	mod := NewModule()
	for _, src := range malformed {
		t.Run(src, func(t *testing.T) {
			// render() must surface a clean error, not panic.
			renderScript := fmt.Sprintf(`load("liquid","render")
out = render(%q)`, src)
			if _, err := runRender(t, mod, renderScript); err == nil {
				t.Errorf("render(%q): expected an error, got nil", src)
			}
			// parse() must surface a clean error, not panic.
			parseScript := fmt.Sprintf(`load("liquid","parse")
out = parse(%q)`, src)
			if _, err := runRender(t, mod, parseScript); err == nil {
				t.Errorf("parse(%q): expected an error, got nil", src)
			}
		})
	}
}

// TestParseStringRecoversPanic exercises the recover arm of parseString
// directly: a nil engine makes the underlying ParseString call panic, which the
// defer/recover must convert into an error rather than letting it escape as a
// host panic.
func TestParseStringRecoversPanic(t *testing.T) {
	tmpl, err := parseString(nil, `{{ x }}`)
	if err == nil {
		t.Fatalf("expected an error from a panicking parse, got tmpl=%v", tmpl)
	}
	if !strings.Contains(err.Error(), "parse panic") {
		t.Errorf("error = %v, want it to mention \"parse panic\"", err)
	}
}

// TestRenderRecoversPanic exercises the non-cap arm of the render recover in
// both render paths: a panic that is NOT an output-cap overflow must surface as
// the generic "liquid: render panic: ..." error rather than escaping as a host
// panic. A nil engine / nil template makes the underlying SDK call panic; the
// defer/recover converts it into an ordinary error (invariant #1).
func TestRenderRecoversPanic(t *testing.T) {
	t.Run("renderWith nil engine", func(t *testing.T) {
		out, err := renderWith(nil, "{{ x }}", map[string]interface{}{"x": 1}, 1024)
		if out != "" {
			t.Errorf("out = %q, want empty", out)
		}
		if err == nil || !strings.Contains(err.Error(), "render panic") {
			t.Errorf("err = %v, want a 'render panic' error", err)
		}
	})
	t.Run("renderTemplate nil template", func(t *testing.T) {
		tv := &templateValue{tmpl: nil, maxOutput: 1024}
		out, err := tv.renderTemplate(map[string]interface{}{"x": 1})
		if out != "" {
			t.Errorf("out = %q, want empty", out)
		}
		if err == nil || !strings.Contains(err.Error(), "render panic") {
			t.Errorf("err = %v, want a 'render panic' error", err)
		}
	})
}

// TestOutputCapNormalizedOnFlushPanic guards the hardening fix for the cap path.
// osteele/liquid panics when a *buffered* flush fails (render.go's Render/Flush
// and TrimLeft), so a template whose overflow lands during a trailing-whitespace
// flush would otherwise surface as a generic "render panic: ...rendered output
// exceeds..." wrapper. The recover arm checks cw.exceeded first and normalizes
// every cap overflow to the documented errOutputLimit, regardless of whether it
// arrived as the FRender error or a flush panic.
func TestOutputCapNormalizedOnFlushPanic(t *testing.T) {
	// The rendered content (5 'Z') fits under the 8-byte cap, but the template's
	// trailing whitespace gets buffered by the engine's trimWriter and only
	// flushed at end-of-render — and THAT flush overflows the cap. osteele/liquid
	// turns a failed buffered flush into a panic (render.go's Render/Flush), so
	// without the cw.exceeded check in the recover arm this would surface as a
	// "render panic: ...rendered output exceeds..." wrapper. The fix normalizes it
	// to the documented errOutputLimit.
	const src = "{{ a }}\n   " // 5 content bytes + "\n   " (4 ws bytes) = 9 > 8
	bindings := map[string]interface{}{"a": strings.Repeat("Z", 5)}

	t.Run("renderWith", func(t *testing.T) {
		_, err := renderWith(osliquid.NewEngine(), src, bindings, 8)
		if err != errOutputLimit {
			t.Fatalf("renderWith over-cap flush = %v, want errOutputLimit (clean, not a panic wrapper)", err)
		}
	})

	t.Run("Template.renderTemplate", func(t *testing.T) {
		tmpl, perr := parseString(osliquid.NewEngine(), src)
		if perr != nil {
			t.Fatalf("parse: %v", perr)
		}
		tv := &templateValue{tmpl: tmpl, maxOutput: 8}
		if _, err := tv.renderTemplate(bindings); err != errOutputLimit {
			t.Fatalf("renderTemplate over-cap flush = %v, want errOutputLimit", err)
		}
	})

	// End-to-end through the public render() API the error message must be the
	// documented one (no "render panic" leak), and not crash the host.
	t.Run("through render() API", func(t *testing.T) {
		t.Setenv("LIQUID_MAX_OUTPUT_SIZE", "8")
		_, err := runRender(t, NewModule(), `load("liquid","render")
out = render("{{ a }}\n   ", {"a": "ZZZZZ"})`)
		if err == nil || !strings.Contains(err.Error(), "rendered output exceeds the configured maximum size") {
			t.Fatalf("render() over-cap = %v, want the documented output-limit message", err)
		}
		if strings.Contains(err.Error(), "render panic") {
			t.Errorf("error leaked a 'render panic' wrapper: %v", err)
		}
	})

	// A direct-write overflow (content itself exceeds the cap) still returns the
	// same clean errOutputLimit via the normal ParseAndFRender error path — both
	// the flush-panic and direct-write overflows converge on one documented error.
	t.Run("direct-write overflow", func(t *testing.T) {
		_, err := renderWith(osliquid.NewEngine(), "{{ a }}",
			map[string]interface{}{"a": strings.Repeat("Z", 64)}, 8)
		if err != errOutputLimit {
			t.Fatalf("renderWith direct over-cap = %v, want errOutputLimit", err)
		}
	})

	// A compiled template enforces the cap on a direct-write overflow too (the
	// renderTemplate normal-error arm).
	t.Run("Template direct-write overflow", func(t *testing.T) {
		tmpl, perr := parseString(osliquid.NewEngine(), "{{ a }}")
		if perr != nil {
			t.Fatalf("parse: %v", perr)
		}
		tv := &templateValue{tmpl: tmpl, maxOutput: 8}
		if _, err := tv.renderTemplate(map[string]interface{}{"a": strings.Repeat("Z", 64)}); err != errOutputLimit {
			t.Fatalf("renderTemplate direct over-cap = %v, want errOutputLimit", err)
		}
	})
}

// --- config options end-to-end (through the module's public render() path) ---

// TestStrictOptionThroughModule exercises the `strict` config option end-to-end:
// an undefined variable must error when strict, via the Starlark render() API.
func TestStrictOptionThroughModule(t *testing.T) {
	const script = `load("liquid","render")
out = render("[{{ missing }}]")`

	// strict via the constructor option (the explicit public builder path).
	strict := newModuleWithOptions(
		genConfigOption(configKeyMaxOutputSize, "", defaultMaxOutputSize),
		genConfigOption(configKeyStrict, "", true),
	)
	if _, err := runRender(t, strict, script); err == nil ||
		!strings.Contains(err.Error(), "undefined variable") {
		t.Errorf("strict module: expected undefined-variable error, got %v", err)
	}

	// strict via the LIQUID_STRICT environment variable.
	t.Setenv("LIQUID_STRICT", "true")
	if _, err := runRender(t, NewModule(), script); err == nil ||
		!strings.Contains(err.Error(), "undefined variable") {
		t.Errorf("LIQUID_STRICT=true: expected undefined-variable error, got %v", err)
	}

	// lenient (default): the undefined variable renders empty, no error.
	t.Setenv("LIQUID_STRICT", "false")
	if got, err := runRender(t, NewModule(), script); err != nil || got != "[]" {
		t.Errorf("lenient render = (%q, %v), want (\"[]\", nil)", got, err)
	}
}

// TestMaxOutputSizeThroughModule sets a tiny LIQUID_MAX_OUTPUT_SIZE and asserts
// the output cap fires end-to-end through the render() API.
func TestMaxOutputSizeThroughModule(t *testing.T) {
	t.Setenv("LIQUID_MAX_OUTPUT_SIZE", "8")
	// Output longer than the 8-byte cap must error.
	_, err := runRender(t, NewModule(), `load("liquid","render")
out = render("{{ s }}", {"s": "0123456789"})`)
	if err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Errorf("tiny cap: expected output-limit error, got %v", err)
	}
	// Output within the cap still renders.
	if got, err := runRender(t, NewModule(), `load("liquid","render")
out = render("{{ s }}", {"s": "ok"})`); err != nil || got != "ok" {
		t.Errorf("within cap render = (%q, %v), want (\"ok\", nil)", got, err)
	}
}

// --- templateValue surface ----------------------------------------------------

// TestTemplateValueSurface parses a template and asserts its type()/str() repr
// and that using it as a dict key errors (it is unhashable).
func TestTemplateValueSurface(t *testing.T) {
	got, err := runRender(t, NewModule(), `load("liquid","parse")
tmpl = parse("{{ x }}")
out = type(tmpl) + " " + str(tmpl)`)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if want := "liquid.Template <liquid.Template>"; got != want {
		t.Errorf("type/str repr = %q, want %q", got, want)
	}

	// Using a template as a dict key must error (unhashable type).
	_, err = runRender(t, NewModule(), `load("liquid","parse")
tmpl = parse("{{ x }}")
out = {tmpl: 1}`)
	if err == nil || !strings.Contains(err.Error(), "unhashable") {
		t.Errorf("expected unhashable-key error, got %v", err)
	}
}

// TestTemplateValueProtocol exercises the starlark.Value / HasAttrs protocol of
// templateValue directly: Type/String repr, Truth (always true), Freeze (a
// no-op that must not panic), AttrNames (exactly ["render"]), the render attr
// lookup, and an unknown attribute returning (nil, nil) so Starlark reports a
// no-such-attr error.
func TestTemplateValueProtocol(t *testing.T) {
	tv := &templateValue{maxOutput: defaultMaxOutputSize}

	if got := tv.Type(); got != "liquid.Template" {
		t.Errorf("Type() = %q, want %q", got, "liquid.Template")
	}
	if got := tv.String(); got != "<liquid.Template>" {
		t.Errorf("String() = %q, want %q", got, "<liquid.Template>")
	}
	if got := tv.Truth(); got != starlark.True {
		t.Errorf("Truth() = %v, want True", got)
	}
	// Freeze is a no-op; it must not panic.
	tv.Freeze()

	if _, err := tv.Hash(); err == nil || !strings.Contains(err.Error(), "unhashable") {
		t.Errorf("Hash() error = %v, want unhashable", err)
	}

	if names := tv.AttrNames(); len(names) != 1 || names[0] != "render" {
		t.Errorf("AttrNames() = %v, want [render]", names)
	}

	got, err := tv.Attr("render")
	if err != nil {
		t.Fatalf("Attr(render): %v", err)
	}
	if _, ok := got.(*starlark.Builtin); !ok {
		t.Errorf("Attr(render) = %T, want *starlark.Builtin", got)
	}

	// Unknown attribute: (nil, nil) so the interpreter reports no-such-attr.
	if v, err := tv.Attr("nope"); v != nil || err != nil {
		t.Errorf("Attr(nope) = (%v, %v), want (nil, nil)", v, err)
	}
}

// TestTemplateRenderArgErrors covers the argument-validation branches of
// Template.render before any engine call: too many positional arguments, and a
// non-dict bindings argument. Both must be clean script errors.
func TestTemplateRenderArgErrors(t *testing.T) {
	cases := []struct {
		name, script, wantSub string
	}{
		{
			name: "too many positionals",
			script: `load("liquid","parse")
tmpl = parse("{{ x }}")
out = tmpl.render({}, {})`,
			wantSub: "liquid.Template.render: got 2 positional arguments, want at most 1 (bindings)",
		},
		{
			name: "non-dict bindings",
			script: `load("liquid","parse")
tmpl = parse("{{ x }}")
out = tmpl.render([1, 2, 3])`,
			wantSub: "liquid.Template.render: bindings must be a dict, got list",
		},
	}
	mod := NewModule()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := runRender(t, mod, c.script)
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("error = %v, want it to contain %q", err, c.wantSub)
			}
		})
	}
}

// TestTemplateRenderReuseAndUnknownAttr exercises the compiled-template object
// from script: rendering with kwargs (not just a dict), unknown-attribute
// access surfacing a no-such-attr error, and missing-arg-tolerant render().
func TestTemplateRenderReuseAndUnknownAttr(t *testing.T) {
	// render() with kwargs, then with no bindings at all (lenient: both
	// undefined variables render empty, leaving only the literal space).
	got, err := runRender(t, NewModule(), `load("liquid","parse")
tmpl = parse("{{ greeting }}{{ name }}")
out = tmpl.render(greeting="Hi ", name="Ada") + "|" + "[" + tmpl.render() + "]"`)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "Hi Ada|[]" {
		t.Errorf("template kwargs/no-binding render = %q, want %q", got, "Hi Ada|[]")
	}

	// Accessing an unknown attribute is a clean script error.
	_, err = runRender(t, NewModule(), `load("liquid","parse")
tmpl = parse("{{ x }}")
out = tmpl.nope`)
	if err == nil || !strings.Contains(err.Error(), "has no .nope") {
		t.Errorf("unknown attr error = %v, want a no-such-attr error", err)
	}
}

// TestTemplateRenderTimeError covers a render-time (non-cap) error surfacing
// through Template.render: an undefined filter parses successfully but errors
// only when rendered, and the error must be wrapped with the liquid: prefix and
// returned cleanly (not panic). This drives renderTemplate's non-cap error arm
// and the render method's error return.
func TestTemplateRenderTimeError(t *testing.T) {
	// Direct path: a parsed template with an undefined filter errors at render.
	tmpl, perr := parseString(NewModule().newEngine(), "{{ x | nope }}")
	if perr != nil {
		t.Fatalf("parse should succeed (filter resolution is deferred): %v", perr)
	}
	tv := &templateValue{tmpl: tmpl, maxOutput: defaultMaxOutputSize}
	out, err := tv.renderTemplate(map[string]interface{}{"x": 1})
	if out != "" {
		t.Errorf("out = %q, want empty on error", out)
	}
	if err == nil || !strings.Contains(err.Error(), `liquid: Liquid error: undefined filter "nope"`) {
		t.Fatalf("renderTemplate error = %v, want the wrapped undefined-filter error", err)
	}

	// End-to-end through the Starlark Template.render path.
	_, serr := runRender(t, NewModule(), `load("liquid","parse")
tmpl = parse("{{ x | nope }}")
out = tmpl.render({"x": 1})`)
	if serr == nil || !strings.Contains(serr.Error(), `undefined filter "nope"`) {
		t.Errorf("Template.render error = %v, want the undefined-filter error", serr)
	}
}
