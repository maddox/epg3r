package web

// A picker is the one chooser this UI uses. Closed it reads like a button; open it shows
// its options. Single choice replaces the value and closes; multiple choice toggles and
// stays open while the list behind it filters. Every select in the app is one of these,
// so they share a height, a caret, and a panel.
type picker struct {
	Name     string         // form field the options post under
	Label    string         // what a multiple-choice control reads; single choice reads its option
	Multiple bool           // checkboxes rather than radios
	Options  []pickerOption //
}

// pickerOption is one row in the panel.
type pickerOption struct {
	Value    string // posted value; empty means "no choice", which is a choice too
	Label    string
	Selected bool
}

// Summary is what the closed control reads: the chosen option on single choice, the
// field's name on multiple, where the count carries the rest.
func (p picker) Summary() string {
	if !p.Multiple {
		for _, o := range p.Options {
			if o.Selected {
				return o.Label
			}
		}
	}
	return p.Label
}

// Count is how many options are chosen, shown as a badge on multiple choice.
func (p picker) Count() int {
	n := 0
	for _, o := range p.Options {
		if o.Selected {
			n++
		}
	}
	return n
}

// searchAbove is where a list stops being scannable. Well above the longest hand-written
// picker in the app, so only the generated ones get a box.
const searchAbove = 20

// Search reports whether the panel gets a filter box. A list long enough to scroll is a
// list nobody wants to read, and the time zones are four hundred deep.
func (p picker) Search() bool { return len(p.Options) > searchAbove }

// Control is the input type the options render as.
func (p picker) Control() string {
	if p.Multiple {
		return "checkbox"
	}
	return "radio"
}

// choose marks the option holding current, and reports the picker.
func choose(name string, opts []pickerOption, current string) picker {
	p := picker{Name: name, Options: opts}
	for i := range p.Options {
		p.Options[i].Selected = p.Options[i].Value == current
	}
	return p
}

// Chosen lists the values selected, so callers need not walk Options themselves.
func (p picker) Chosen() []string {
	var out []string
	for _, o := range p.Options {
		if o.Selected {
			out = append(out, o.Value)
		}
	}
	return out
}

// stringPicker builds a chooser over plain strings, where the value is the label. Used
// for settings whose choices are a fixed vocabulary.
func stringPicker(name, current string, choices []string) picker {
	opts := make([]pickerOption, 0, len(choices))
	for _, c := range choices {
		opts = append(opts, pickerOption{Value: c, Label: c})
	}
	return choose(name, opts, current)
}

// leaguePicker offers every league in the catalog, or all of them.
func (s *Server) leaguePicker(current string) picker {
	opts := []pickerOption{{Value: "", Label: "All leagues"}}
	for _, lg := range s.Catalog.Leagues {
		opts = append(opts, pickerOption{Value: lg.Key, Label: lg.Name})
	}
	return choose("league", opts, current)
}
