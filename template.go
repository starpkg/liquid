package liquid

import (
	"fmt"

	"github.com/osteele/liquid"
	"go.starlark.net/starlark"
)

// templateValue is a compiled Liquid template returned by parse(). It can be
// rendered repeatedly with different bindings.
type templateValue struct {
	tmpl      *liquid.Template
	maxOutput int
}

var (
	_ starlark.Value    = (*templateValue)(nil)
	_ starlark.HasAttrs = (*templateValue)(nil)
)

func (t *templateValue) String() string       { return "<liquid.Template>" }
func (t *templateValue) Type() string         { return "liquid.Template" }
func (t *templateValue) Freeze()              {}
func (t *templateValue) Truth() starlark.Bool { return starlark.True }
func (t *templateValue) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: liquid.Template")
}

func (t *templateValue) AttrNames() []string { return []string{"render"} }

func (t *templateValue) Attr(name string) (starlark.Value, error) {
	if name == "render" {
		return starlark.NewBuiltin("liquid.Template.render", t.render), nil
	}
	return nil, nil
}

// render renders the compiled template.
//
//	Template.render(bindings=None, **kwargs) -> str
func (t *templateValue) render(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 1 {
		return none, fmt.Errorf("%s: got %d positional arguments, want at most 1 (bindings)", b.Name(), len(args))
	}
	var dictArg starlark.Value
	if len(args) == 1 {
		dictArg = args[0]
	}
	bindings, err := collectBindings(b.Name(), dictArg, kwargs)
	if err != nil {
		return none, err
	}
	out, err := t.renderTemplate(bindings)
	if err != nil {
		return none, err
	}
	return starlark.String(out), nil
}

// renderTemplate renders the compiled template, recovering panics and capping output.
func (t *templateValue) renderTemplate(bindings map[string]interface{}) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = "", fmt.Errorf("liquid: render panic: %v", r)
		}
	}()
	cw := &cappedWriter{limit: t.maxOutput}
	if serr := t.tmpl.FRender(cw, liquid.Bindings(bindings)); serr != nil {
		if cw.exceeded {
			return "", errOutputLimit
		}
		return "", fmt.Errorf("liquid: %w", serr)
	}
	return cw.buf.String(), nil
}
