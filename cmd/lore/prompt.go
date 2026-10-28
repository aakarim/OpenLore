package main

import (
	"errors"
	"io"
	"slices"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
)

// prompter asks the interactive questions so commands stay testable without a
// terminal. The default renders huh forms; tests script the answers.
type prompter interface {
	// multiSelect returns the indexes of the chosen options; none means the
	// person chose nothing or cancelled. preselected indexes start ticked.
	multiSelect(title string, options []string, preselected []int) ([]int, error)
	confirm(title string) (bool, error)
}

var newPrompter = func(out io.Writer) prompter { return huhPrompter{out: out} }

func (c *client) ui() prompter {
	if c.prompt == nil {
		c.prompt = newPrompter(c.out)
	}
	return c.prompt
}

type huhPrompter struct{ out io.Writer }

// run shows one field; aborted reports that the person cancelled it.
func (h huhPrompter) run(field huh.Field) (aborted bool, err error) {
	keys := huh.NewDefaultKeyMap()
	keys.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	err = huh.NewForm(huh.NewGroup(field)).WithTheme(huh.ThemeBase()).WithKeyMap(keys).WithOutput(h.out).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return true, nil
	}
	return false, err
}
func (h huhPrompter) multiSelect(title string, options []string, preselected []int) ([]int, error) {
	opts := make([]huh.Option[int], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, i)
	}
	// huh ticks every option whose value is already in the bound slice.
	chosen := slices.Clone(preselected)
	aborted, err := h.run(huh.NewMultiSelect[int]().Title(title).Options(opts...).Value(&chosen))
	if err != nil || aborted {
		return nil, err
	}
	slices.Sort(chosen)
	return chosen, nil
}
func (h huhPrompter) confirm(title string) (bool, error) {
	var ok bool
	aborted, err := h.run(huh.NewConfirm().Title(title).Value(&ok))
	if err != nil || aborted {
		return false, err
	}
	return ok, nil
}
