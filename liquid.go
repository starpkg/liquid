// Package liquid provides a Starlark module for rendering Liquid templates.
//
// Liquid is a sandboxed template language: a template can only see the
// variables placed in its bindings map — there is no implicit access to the
// host or to script globals. This module mirrors that model: variables are
// passed explicitly as a bindings dict (and/or keyword arguments) from the
// script, converted to the engine's Bindings (map[string]interface{}).
//
// Safety: rendered output is capped (ADR-010), a single render is bounded by a
// wall-clock deadline (the thread context plus the host-only max_render_time),
// render panics are recovered into errors, and the filesystem {% include %} tag
// is disabled so a template cannot read host files.
package liquid

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"

	"github.com/1set/starlet"
	"github.com/1set/starlet/dataconv"
	"github.com/1set/starlet/dataconv/types"
	"github.com/osteele/liquid"
	"github.com/osteele/liquid/render"
	"github.com/starpkg/base"
	"github.com/starpkg/base/util"
	"go.starlark.net/starlark"
)

// ModuleName is the name used in Starlark's load() for this module.
const ModuleName = "liquid"

// Configuration keys.
const (
	configKeyMaxOutputSize = "max_output_size"
	configKeyStrict        = "strict"
	configKeyMaxRenderTime = "max_render_time"
)

// defaultMaxOutputSize bounds rendered output (ADR-010).
const defaultMaxOutputSize = 256 * 1024 // 256 KiB

// defaultMaxRenderTime bounds a single render's wall-clock time. 0 = no
// module-level limit (the render is still bounded by the thread context
// deadline, if the host set one). Default 0 keeps historical behavior.
const defaultMaxRenderTime = 0.0

var (
	none             = starlark.None
	errOutputLimit   = errors.New("liquid: rendered output exceeds the configured maximum size")
	errIncludeUsage  = errors.New("liquid: the {% include %} tag is disabled (no filesystem access)")
	errRenderTimeout = errors.New("liquid: render exceeded the time limit")
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
		// max_render_time is a DoS lever (bounds a single render's wall-clock
		// time), so it is HOST-ONLY: a script must not be able to disable or raise
		// it. base snapshots its env at construction, so runtime env mutation
		// (e.g. via a runtime.setenv builtin) cannot widen it either.
		genConfigOption(configKeyMaxRenderTime, "Maximum wall-clock seconds for a single render (0 = no module limit)", defaultMaxRenderTime).
			SetHostOnly(true),
	)
}

func genConfigOption[T any](name, description string, defaultValue T) *base.ConfigOption[T] {
	return base.NewNamedConfigOption(ModuleName, name, description, defaultValue)
}

func newModuleWithOptions(
	maxOutputSizeOpt *base.ConfigOption[int],
	strictOpt *base.ConfigOption[bool],
	maxRenderTimeOpt *base.ConfigOption[float64],
) *Module {
	cm, _ := base.NewConfigurableModuleWithConfigOptions(maxOutputSizeOpt, strictOpt, maxRenderTimeOpt)
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
	out, err := m.renderSource(thread, source, bindings)
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
	return &templateValue{tmpl: tmpl, maxOutput: m.maxOutput(), maxRenderTime: m.maxRenderTime()}, nil
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

func (m *Module) maxRenderTime() float64 {
	return m.ext.GetFloat(configKeyMaxRenderTime, defaultMaxRenderTime)
}

// renderSource renders source with bindings under the safety policy.
func (m *Module) renderSource(thread *starlark.Thread, source string, bindings map[string]interface{}) (string, error) {
	return renderBounded(thread, m.maxRenderTime(), m.maxOutput(), engineRender(m.newEngine(), source, bindings))
}

// renderWith renders with a prepared engine under the output cap and no time
// limit. It is a convenience for the one-shot render path used by tests.
func renderWith(engine *liquid.Engine, source string, bindings map[string]interface{}, maxOutput int) (string, error) {
	return renderBounded(&starlark.Thread{}, 0, maxOutput, engineRender(engine, source, bindings))
}

// engineRender returns the render step for renderBounded: parse+render source
// into the capped writer, normalizing an output-cap overflow to errOutputLimit.
// The bindings are deep-copied up front (see isolateBindings).
func engineRender(engine *liquid.Engine, source string, bindings map[string]interface{}) func(*cappedWriter) (string, error) {
	safe := isolateBindings(bindings)
	return func(cw *cappedWriter) (string, error) {
		if serr := engine.ParseAndFRender(cw, []byte(source), liquid.Bindings(safe)); serr != nil {
			if cw.exceeded {
				return "", errOutputLimit
			}
			return "", fmt.Errorf("liquid: %w", serr)
		}
		return cw.buf.String(), nil
	}
}

// visitKey identifies a map/slice by its header pointer, length, and concrete
// type, so a shared or cyclic reference is copied once (no aliasing, no
// exponential blow-up on a cycle) while two different slice views over the same
// backing array — or two distinct named types that happen to share a pointer and
// length — stay distinct (avoiding a cross-type Set panic).
type visitKey struct {
	ptr    uintptr
	length int
	typ    reflect.Type
}

// isolateBindings returns a copy of the bindings in which every map and slice is
// deep-copied. renderBounded may leave a render goroutine running after a
// timeout (osteele/liquid has no cancellation); that goroutine keeps reading the
// bindings. A map or slice the script passed by reference (dataconv can unwrap a
// host GoMap/GoSlice by reference) is the dangerous case: the host mutating it
// concurrently would be a data race, or a fatal concurrent-map access. Copying
// maps and slices here — synchronously, before the goroutine starts — removes
// that sharing for the shapes real bindings take (script-built dicts/lists, and
// host maps/slices).
//
// Everything else is shared as-is: scalars and strings are immutable; time.Time
// and other value types are immutable in practice and cannot be deep-copied
// through their unexported state anyway; structs, pointers, and closures are not
// what a template binding normally carries. A host that injects such a mutable
// value into a script AND mutates it concurrently with a timed-out render is the
// documented residual — pass immutable data, or rely on the host/sandbox
// execution deadline for the hard bound.
func isolateBindings(bindings map[string]interface{}) map[string]interface{} {
	seen := map[visitKey]interface{}{}
	out := make(map[string]interface{}, len(bindings))
	for k, v := range bindings {
		out[k] = copyBindingValue(v, seen)
	}
	return out
}

// copyBindingValue deep-copies maps and slices (via one reflect path, covering
// both plain map[string]interface{}/[]interface{} and typed collections),
// threading the visited set for cycle safety; any other value is shared.
func copyBindingValue(v interface{}, seen map[visitKey]interface{}) interface{} {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		return copyReflectMap(rv, seen)
	case reflect.Slice:
		return copyReflectSlice(rv, seen)
	default:
		return v
	}
}

func copyReflectMap(rv reflect.Value, seen map[visitKey]interface{}) interface{} {
	if rv.IsNil() {
		return rv.Interface()
	}
	key := visitKey{rv.Pointer(), rv.Len(), rv.Type()}
	if c, ok := seen[key]; ok {
		return c
	}
	out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
	seen[key] = out.Interface()
	elemType := rv.Type().Elem()
	iter := rv.MapRange()
	for iter.Next() {
		// Keys are shared (they are almost always immutable scalars/strings).
		out.SetMapIndex(iter.Key(), asElem(copyBindingValue(iter.Value().Interface(), seen), elemType))
	}
	return out.Interface()
}

func copyReflectSlice(rv reflect.Value, seen map[visitKey]interface{}) interface{} {
	if rv.IsNil() || rv.Len() == 0 {
		return rv.Interface()
	}
	key := visitKey{rv.Pointer(), rv.Len(), rv.Type()}
	if c, ok := seen[key]; ok {
		return c
	}
	out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
	seen[key] = out.Interface()
	elemType := rv.Type().Elem()
	for i := 0; i < rv.Len(); i++ {
		out.Index(i).Set(asElem(copyBindingValue(rv.Index(i).Interface(), seen), elemType))
	}
	return out.Interface()
}

// asElem wraps a copied value as a reflect.Value assignable to elemType,
// substituting a typed zero for a nil interface element (reflect.ValueOf(nil)
// is the invalid Value and cannot be assigned).
func asElem(v interface{}, elemType reflect.Type) reflect.Value {
	if v == nil {
		return reflect.Zero(elemType)
	}
	return reflect.ValueOf(v)
}

// renderBounded runs a render under the output cap AND a wall-clock deadline
// (the thread context deadline plus, if positive, the module's max_render_time),
// recovering panics into errors.
//
// osteele/liquid's render is a synchronous call with no context support, so it
// runs in a goroutine and the deadline unblocks the caller with errRenderTimeout
// rather than wedging the interpreter on a single builtin call. NOTE: on timeout
// the render goroutine is abandoned — it stops on its own when it finishes or
// when its output hits the cap (an output-producing runaway fills the 256 KiB
// cap almost immediately), but a hard CPU bound for a no-output template
// (e.g. `{% for i in (1..1e9) %}{% endfor %}`) requires an OS/sandbox limit.
func renderBounded(thread *starlark.Thread, timeout float64, maxOutput int, do func(cw *cappedWriter) (string, error)) (string, error) {
	ctx, cancel := util.OpContext(thread, util.DurationFromSeconds(timeout))
	defer cancel()

	// If the deadline has already passed (e.g. an expired thread context), don't
	// start work — return the timeout deterministically.
	if err := ctx.Err(); err != nil {
		return "", errRenderTimeout
	}

	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1) // buffered so an abandoned goroutine never blocks
	go func() {
		cw := &cappedWriter{limit: maxOutput}
		var out string
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					// The engine panics when a buffered flush fails, so an
					// output-cap overflow can surface here. Normalize it.
					if cw.exceeded {
						out, err = "", errOutputLimit
						return
					}
					out, err = "", fmt.Errorf("liquid: render panic: %v", r)
				}
			}()
			out, err = do(cw)
		}()
		ch <- result{out, err}
	}()

	select {
	case res := <-ch:
		return res.out, res.err
	case <-ctx.Done():
		return "", errRenderTimeout
	}
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
