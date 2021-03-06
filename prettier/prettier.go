package prettier

import (
	_ "embed" // Necessary for go:embed statements to work.

	"rogchap.com/v8go"
)

//go:embed standalone.js
var standalone string

//go:embed parser-markdown.js
var markdown string

type Prettier struct {
	v8ctx *v8go.Context
}

func New(options map[string]interface{}) (*Prettier, error) {
	ctx, err := v8go.NewContext()
	if err != nil {
		return nil, err
	}
	if _, err := ctx.RunScript(standalone, "standalone.js"); err != nil {
		return nil, err
	}
	if _, err := ctx.RunScript(markdown, "parser-markdown.js"); err != nil {
		return nil, err
	}
	if value, err := ctx.RunScript(`
	var options = {
		"plugins":   prettierPlugins,
	}; options`, "options.js"); err != nil {
		return nil, err
	} else {
		obj, err := value.AsObject()
		if err != nil {
			return nil, err
		}
		for key, value := range options {
			if err := obj.Set(key, value); err != nil {
				return nil, err
			}
		}
	}
	return &Prettier{
		v8ctx: ctx,
	}, nil
}

func (p *Prettier) Format(in string) (string, error) {
	if err := p.v8ctx.Global().Set("input", in); err != nil {
		return "", err
	}
	value, err := p.v8ctx.RunScript("prettier.format(input, options)", "<input>")
	if err != nil {
		return "", err
	}
	return value.String(), nil
}
