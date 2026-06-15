// Package liquid provides a Starlark module for rendering Liquid templates.
//
// Liquid is a sandboxed template language: a template can only see the
// variables placed in its bindings map — there is no implicit access to the
// host or to script globals. This module mirrors that model: variables are
// passed explicitly as a bindings dict (and/or keyword arguments) from the
// script, converted to the engine's Bindings (map[string]interface{}).
//
// Safety: rendered output is capped (ADR-010), render panics are recovered into
// errors, and the filesystem {% include %} tag is disabled so a template cannot
// read host files.
package liquid

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/1set/starlet"
	"github.com/1set/starlet/dataconv"
	"github.com/1set/starlet/dataconv/types"
	"github.com/osteele/liquid"
	"github.com/osteele/liquid/render"
	"github.com/starpkg/base"
	"go.starlark.net/starlark"
)

// ModuleName is the name used in Starlark's load() for this module.
const ModuleName = "liquid"

// Configuration keys.
const (
	configKeyMaxOutputSize = "max_output_size"
	configKeyStrict        = "strict"
)

// defaultMaxOutputSize bounds rendered output (ADR-010).
const defaultMaxOutputSize = 256 * 1024 // 256 KiB

var (
	none            = starlark.None
	errOutputLimit  = errors.New("liquid: rendered output exceeds the configured maximum size")
	errIncludeUsage = errors.New("liquid: the {% include %} tag is disabled (no filesystem access)")
)

// Module wraps a ConfigurableModule with Liquid-specific functions.
type Module struct {
	cfgMod *base.ConfigurableModule
	ext    *base.ConfigurableModuleExt
}

// NewModule creates a new Module with default configuration.
func NewModule() *Module {
	return newModuleWithOptions(
		genConfigOption(configKeyMaxOutputSize, "Maximum rendered output size in bytes", defaultMaxOutputSize),
		genConfigOption(configKeyStrict, "Error when a template references an undefined variable", false),
	)
}

func genConfigOption[T any](name, description string, defaultValue T) *base.ConfigOption[T] {
	return base.NewConfigOption(defaultValue).
		WithName(name).
		WithDescription(description).
		WithEnvVar("LIQUID_" + upper(name))
}

func newModuleWithOptions(
	maxOutputSizeOpt *base.ConfigOption[int],
	strictOpt *base.ConfigOption[bool],
) *Module {
	cm, _ := base.NewConfigurableModuleWithConfigOptions(maxOutputSizeOpt, strictOpt)
	return &Module{cfgMod: cm, ext: cm.Extend()}
}

// LoadModule returns the Starlark module loader.
func (m *Module) LoadModule() starlet.ModuleLoader {
	funcs := starlark.StringDict{
		"render": starlark.NewBuiltin(ModuleName+".render", m.render),
		"parse":  starlark.NewBuiltin(ModuleName+".parse", m.parse),
	}
	return m.cfgMod.LoadModule(ModuleName, funcs)
}

// render parses and renders a template in one call.
//
//	render(source, bindings=None, **kwargs) -> str
//
// bindings is an optional dict; keyword arguments are merged on top of it.
func (m *Module) render(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	source, bindings, err := parseRenderArgs(b.Name(), args, kwargs)
	if err != nil {
		return none, err
	}
	out, err := m.renderSource(source, bindings)
	if err != nil {
		return none, err
	}
	return starlark.String(out), nil
}

// parse compiles a template once for repeated rendering.
//
//	parse(source) -> Template
func (m *Module) parse(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var source types.StringOrBytes
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "source", &source); err != nil {
		return none, err
	}
	tmpl, perr := parseString(m.newEngine(), source.GoString())
	if perr != nil {
		return none, fmt.Errorf("liquid.parse: %w", perr)
	}
	return &templateValue{tmpl: tmpl, maxOutput: m.maxOutput()}, nil
}

// parseString compiles a template, recovering panics into errors (matching the
// render paths so malformed input can never crash the host).
func parseString(engine *liquid.Engine, source string) (tmpl *liquid.Template, err error) {
	defer func() {
		if r := recover(); r != nil {
			tmpl, err = nil, fmt.Errorf("liquid: parse panic: %v", r)
		}
	}()
	return engine.ParseString(source)
}

// parseRenderArgs extracts the source and the bindings map from a render call,
// supporting both a positional bindings dict and arbitrary keyword arguments.
func parseRenderArgs(fnName string, args starlark.Tuple, kwargs []starlark.Tuple) (string, map[string]interface{}, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("%s: missing source argument", fnName)
	}
	if len(args) > 2 {
		return "", nil, fmt.Errorf("%s: got %d positional arguments, want at most 2 (source, bindings)", fnName, len(args))
	}
	var source types.StringOrBytes
	if err := source.Unpack(args[0]); err != nil {
		return "", nil, fmt.Errorf("%s: source: %w", fnName, err)
	}

	var dictArg starlark.Value
	if len(args) == 2 {
		dictArg = args[1]
	}
	bindings, err := collectBindings(fnName, dictArg, kwargs)
	if err != nil {
		return "", nil, err
	}
	return source.GoString(), bindings, nil
}

// collectBindings builds the engine bindings from an optional dict argument and
// keyword arguments (kwargs override the dict). dictArg may be nil or None.
func collectBindings(fnName string, dictArg starlark.Value, kwargs []starlark.Tuple) (map[string]interface{}, error) {
	bindings := map[string]interface{}{}
	if dictArg != nil && dictArg != starlark.None {
		dict, ok := dictArg.(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("%s: bindings must be a dict, got %s", fnName, dictArg.Type())
		}
		converted, err := dataconv.Unmarshal(dict)
		if err != nil {
			return nil, fmt.Errorf("%s: bindings: %w", fnName, err)
		}
		m, ok := converted.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s: bindings did not convert to a string-keyed map", fnName)
		}
		bindings = m
	}
	for _, kv := range kwargs {
		name := string(kv[0].(starlark.String))
		val, err := dataconv.Unmarshal(kv[1])
		if err != nil {
			return nil, fmt.Errorf("%s: keyword %q: %w", fnName, name, err)
		}
		bindings[name] = val
	}
	return bindings, nil
}

// newEngine builds a Liquid engine with the module's safety policy applied.
func (m *Module) newEngine() *liquid.Engine {
	e := liquid.NewEngine()
	if m.ext.GetBool(configKeyStrict, false) {
		e.StrictVariables()
	}
	// Disable filesystem include so a template cannot read host files.
	e.RegisterTag("include", func(render.Context) (string, error) { return "", errIncludeUsage })
	return e
}

func (m *Module) maxOutput() int {
	if v := m.ext.GetInt(configKeyMaxOutputSize); v > 0 {
		return v
	}
	return defaultMaxOutputSize
}

// renderSource renders source with bindings under the safety policy.
func (m *Module) renderSource(source string, bindings map[string]interface{}) (string, error) {
	return renderWith(m.newEngine(), source, bindings, m.maxOutput())
}

// renderWith renders with a prepared engine, recovering panics and capping output.
func renderWith(engine *liquid.Engine, source string, bindings map[string]interface{}, maxOutput int) (out string, err error) {
	cw := &cappedWriter{limit: maxOutput}
	defer func() {
		if r := recover(); r != nil {
			// The engine panics when a buffered flush fails (render.go's
			// Flush/TrimLeft), so an output-cap overflow can arrive here rather
			// than as the ParseAndFRender error below. Normalize it to the
			// documented errOutputLimit instead of a generic "render panic".
			if cw.exceeded {
				out, err = "", errOutputLimit
				return
			}
			out, err = "", fmt.Errorf("liquid: render panic: %v", r)
		}
	}()
	if serr := engine.ParseAndFRender(cw, []byte(source), liquid.Bindings(bindings)); serr != nil {
		if cw.exceeded {
			return "", errOutputLimit
		}
		return "", fmt.Errorf("liquid: %w", serr)
	}
	return cw.buf.String(), nil
}

// cappedWriter accumulates output up to limit bytes, then fails fast.
type cappedWriter struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.exceeded || w.buf.Len()+len(p) > w.limit {
		w.exceeded = true
		return 0, errOutputLimit
	}
	return w.buf.Write(p)
}

func upper(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
