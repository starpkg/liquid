package liquid

// Tests for the liquid module.
//
// Sections:
//   - rendering via the Starlark API (render / parse, dict + kwargs bindings)
//   - safety: include disabled, output cap, strict mode

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
