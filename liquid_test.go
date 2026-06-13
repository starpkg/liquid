package liquid

// Tests for the liquid module.
//
// Sections:
//   - rendering via the Starlark API (render / parse, dict + kwargs bindings)
//   - filters & tags (standard filters, loops/conditionals, control flow)
//   - error & missing-value behavior (exact wrapped message shapes)
//   - safety: include disabled, output cap, strict mode, malformed-input hardening
//   - config options end-to-end (strict / max_output_size via the module path)
//   - templateValue surface (type/str repr, unhashable as a dict key)

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
