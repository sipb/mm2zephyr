package prettier

import (
	_ "embed" // Necessary for go:embed statements to work.

	"github.com/robertkrimen/otto"
)

//go:embed standalone.js
var standalone string

//go:embed parser-markdown.js
var markdown string

type Prettier struct {
	vm *otto.Otto
}

func New() (*Prettier, error) {
	vm := otto.New()
	if _, err := vm.Run(standalone); err != nil {
		return nil, err
	}
	if _, err := vm.Run(markdown); err != nil {
		return nil, err
	}
	return &Prettier{
		vm: vm,
	}, nil
}

func (p *Prettier) Format(in string, options map[string]interface{}) (string, error) {
	value, err := p.vm.Call("prettier.format", nil, options)
	if err != nil {
		return "", err
	}
	return value.String(), nil
}
