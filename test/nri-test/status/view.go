/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package status

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/nri/test/nri-test/framework"
)

// CellState is what a run reported about one input field.
type CellState int

const (
	// StateUnmeasured is a field the run did not measure.
	StateUnmeasured CellState = iota
	// StateAgreed is a field the run measured and recorded no finding on.
	StateAgreed
	// StateFinding is a field the run recorded a finding on.
	StateFinding
)

// String names a CellState.
func (s CellState) String() string {
	switch s {
	case StateUnmeasured:
		return "unmeasured"
	case StateAgreed:
		return "agreed"
	case StateFinding:
		return "finding"
	}

	return fmt.Sprintf("CellState(%d)", int(s))
}

// HandlerCell is one handler's result for one input field.
type HandlerCell struct {
	// Handler is the runtime handler the result was measured with.
	Handler string
	// State is what the handler's run reported about the field.
	State CellState
	// Text is the handler's findings on the field, and is empty without one.
	Text string
}

// Cell is a column's result for one input field. PerHandler holds every
// handler's own result, and is empty when the handlers reported the same State
// and Text.
type Cell struct {
	// State is the state the column reports for the field.
	State CellState
	// Text is the findings the handlers agree on, and is empty otherwise.
	Text string
	// PerHandler is each handler's result, named.
	PerHandler []HandlerCell
}

// HandlerValue is one handler's value for one row of a column.
type HandlerValue struct {
	// Handler is the runtime handler the value was measured with, and is
	// empty when every handler of the column reported the same value.
	Handler string
	// Value is what that handler's run reported.
	Value string
}

// Column is what one runtime and version reported, over every handler that
// reported it.
//
// Every row below is one run's measurement per handler, never a total across
// them: a count summed over the handlers would grow with the number of runs
// rather than with what the runtime does.
type Column struct {
	// Runtime and Version are the runtime the results came from.
	Runtime, Version string
	// Handlers are the runtime handlers the results were measured with.
	Handlers []string
	// NRIVersion, KernelRelease and Arch are the environment each handler's
	// run reported.
	NRIVersion, KernelRelease, Arch []HandlerValue
	// Collecting is how many expectation failures each test of a handler's run
	// reported: every one, or the first it hit.
	Collecting []HandlerValue
	// Measured is how many input fields each handler's run covered.
	Measured []HandlerValue
	// FieldsWithFinding is how many input fields each handler's run recorded a
	// finding on.
	FieldsWithFinding []HandlerValue
	// Expectations and Operations are how many findings of each kind each
	// handler's run recorded.
	Expectations, Operations []HandlerValue
	// FailedUnseen says a run failed and recorded no finding.
	FailedUnseen bool
}

// Row is one input field across the columns.
type Row struct {
	// Field is the field's qualified name.
	Field string
	// Cells is one result per column, in the columns' order.
	Cells []Cell
	// State is the most severe state the cells report, and is StateUnmeasured
	// when there are none.
	State CellState
	// MeasuredIn names the columns that measured the field and recorded no
	// finding on it.
	MeasuredIn []string
	// UnmeasuredIn names the columns whose cell for the field is not a
	// measurement.
	UnmeasuredIn []string
	// Note is the hand-written note about the field, or empty when it has
	// none.
	Note string
}

// Selection is one run that ran a selection of the tests, and the selection.
type Selection struct {
	// Column names the runtime, version and handler of the run.
	Column string
	// Tests is the -test.run value the run was filtered by.
	Tests string
}

// Skip is one test that did not run, and the runs it did not run in.
type Skip struct {
	// Test and Reason are what the test recorded when it skipped.
	Test, Reason string
	// Columns names the runtime, version and handler of every run that
	// recorded it.
	Columns []string
}

// Other is one finding that is not about an input field.
type Other struct {
	// Column names the runtime, version and handler that reported it.
	Column string
	// Kind is what sort of failure it records, as framework.Kind names it:
	// an expectation that was not met, or a driver call that failed.
	Kind string
	// Test, Feature, Surface and Text are the finding.
	Test, Feature, Surface, Text string
}

// View is a set of fragments in the form a document renders.
type View struct {
	// Columns is one entry per runtime and version, ordered by both.
	Columns []Column
	// Rows is one entry per input field, in framework.InputFields order.
	Rows []Row
	// TotalFields is the number of input fields there are.
	TotalFields int
	// Others is every finding that is not about an input field.
	Others []Other
	// Selections is every run that ran a selection of the tests rather than
	// all of them.
	//
	// What such a run did not report is unmeasured rather than clean, so the
	// document's negative claims are claims about the tests that ran.
	Selections []Selection
	// Skips is the distinct skips the runs recorded, each naming the runs that
	// recorded it.
	Skips []Skip
}

// RowsWithFinding returns the view's rows a column recorded a finding on, in
// the rows' own order.
func (v *View) RowsWithFinding() []Row {
	return v.rowsInState(StateFinding)
}

// RowsUnmeasured returns the view's rows no column recorded a finding on and
// some column did not measure, in the rows' own order.
func (v *View) RowsUnmeasured() []Row {
	return v.rowsInState(StateUnmeasured)
}

// OtherExpectations returns the view's findings that are about the runtime's
// behaviour rather than about an input field, in the order they arrived.
func (v *View) OtherExpectations() []Other {
	var others []Other

	for _, o := range v.Others {
		if o.Kind != framework.KindOperation.String() {
			others = append(others, o)
		}
	}

	return others
}

// OtherOperations returns the view's driver calls that failed, in the order
// they arrived.
//
// Apart from the expectations above, because what the two say is different: a
// refused call ended its test, so its expectations were never evaluated and
// nothing about the runtime's NRI behaviour was measured by it. A reader who
// took one for an unmet expectation would conclude that the suite measured
// something.
func (v *View) OtherOperations() []Other {
	var others []Other

	for _, o := range v.Others {
		if o.Kind == framework.KindOperation.String() {
			others = append(others, o)
		}
	}

	return others
}

// rowsInState returns the view's rows whose State is state.
func (v *View) rowsInState(state CellState) []Row {
	var rows []Row

	for _, row := range v.Rows {
		if row.State == state {
			rows = append(rows, row)
		}
	}

	return rows
}

// Build returns the view of frags. It fails on a finding whose feature is
// neither an input field nor a known behavioural one.
func Build(frags []framework.Fragment) (*View, error) {
	notes, err := Notes()
	if err != nil {
		return nil, err
	}

	groups := group(frags)

	v := &View{
		Columns:     make([]Column, 0, len(groups)),
		Rows:        make([]Row, 0, len(framework.InputFields)),
		TotalFields: len(framework.InputFields),
		Others:      []Other{},
		Skips:       []Skip{},
	}

	for _, g := range groups {
		column, err := columnOf(g)
		if err != nil {
			return nil, err
		}

		others, err := othersOf(g)
		if err != nil {
			return nil, err
		}

		v.Columns = append(v.Columns, column)
		v.Others = append(v.Others, others...)
		v.Skips = appendSkips(v.Skips, g)
		v.Selections = append(v.Selections, selectionsOf(g)...)
	}

	for _, field := range framework.InputFields {
		row := Row{
			Field: fieldName(field),
			Cells: make([]Cell, 0, len(groups)),
		}
		for i, g := range groups {
			cell := cellOf(g, row.Field)
			row.Cells = append(row.Cells, cell)

			name := v.Columns[i].Runtime + " " + v.Columns[i].Version
			switch cell.State {
			case StateAgreed:
				row.MeasuredIn = append(row.MeasuredIn, name)
			case StateUnmeasured:
				row.UnmeasuredIn = append(row.UnmeasuredIn, name)
			}
		}
		row.State = rowState(row.Cells)
		row.Note = notes[row.Field]

		v.Rows = append(v.Rows, row)
	}

	return v, nil
}

// fieldName is the qualified name of f.
func fieldName(f framework.InputField) string {
	return f.Message + "." + f.Name
}

// inputFields holds the qualified name of every field in
// framework.InputFields.
var inputFields = func() map[string]bool {
	fields := map[string]bool{}
	for _, f := range framework.InputFields {
		fields[fieldName(f)] = true
	}

	return fields
}()

// behaviouralFeatures are the features of a finding about the runtime's
// behaviour.
var behaviouralFeatures = map[string]bool{
	"annotation": true,
	"cpuset":     true,
	"env":        true,
	"mems":       true,
	"mount":      true,

	string(framework.Configured):                  true,
	string(framework.Synchronized):                true,
	string(framework.Shutdown):                    true,
	string(framework.Disconnected):                true,
	string(framework.Stopped):                     true,
	string(framework.UpdateDropped):               true,
	string(framework.RunPodSandbox):               true,
	string(framework.UpdatePodSandbox):            true,
	string(framework.PostUpdatePodSandbox):        true,
	string(framework.StopPodSandbox):              true,
	string(framework.RemovePodSandbox):            true,
	string(framework.CreateContainer):             true,
	string(framework.PostCreateContainer):         true,
	string(framework.StartContainer):              true,
	string(framework.PostStartContainer):          true,
	string(framework.UpdateContainer):             true,
	string(framework.PostUpdateContainer):         true,
	string(framework.StopContainer):               true,
	string(framework.RemoveContainer):             true,
	string(framework.ValidateContainerAdjustment): true,
}

// fragmentGroup is the fragments of one runtime and version.
type fragmentGroup struct {
	runtime, version string
	frags            []framework.Fragment
}

// group returns frags by runtime and version, ordered by runtime and version,
// and each group's fragments ordered by handler.
func group(frags []framework.Fragment) []fragmentGroup {
	type runtimeKey struct{ runtime, version string }

	var groups []fragmentGroup

	at := map[runtimeKey]int{}
	for _, f := range frags {
		key := runtimeKey{f.Header.Runtime, f.Header.RuntimeVersion}

		i, ok := at[key]
		if !ok {
			i = len(groups)
			at[key] = i
			groups = append(groups, fragmentGroup{
				runtime: key.runtime,
				version: key.version,
			})
		}

		groups[i].frags = append(groups[i].frags, f)
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].runtime != groups[j].runtime {
			return groups[i].runtime < groups[j].runtime
		}

		return groups[i].version < groups[j].version
	})

	for i := range groups {
		sort.Slice(groups[i].frags, func(a, b int) bool {
			return handlerName(groups[i].frags[a]) < handlerName(groups[i].frags[b])
		})
	}

	return groups
}

// handlerName is the runtime handler a fragment's run selected, or the
// runtime's default.
func handlerName(f framework.Fragment) string {
	if f.Header.RuntimeHandler == "" {
		return "default"
	}

	return tableSafe(f.Header.RuntimeHandler)
}

// columnOf returns the group's column. It fails on coverage of a field that is
// not an input field.
func columnOf(g fragmentGroup) (Column, error) {
	c := Column{
		Runtime: tableSafe(g.runtime),
		Version: tableSafe(g.version),
	}

	for _, f := range g.frags {
		handler := handlerName(f)
		c.Handlers = append(c.Handlers, handler)

		c.NRIVersion = appendValue(c.NRIVersion, handler, f.Header.NRIVersion)
		c.KernelRelease = appendValue(c.KernelRelease, handler, f.Environment.KernelRelease)
		c.Arch = appendValue(c.Arch, handler, f.Environment.Arch)
		c.Collecting = appendValue(c.Collecting, handler, failureMode(f.Header.Collecting))

		measured := map[string]bool{}
		for _, cov := range f.Coverage {
			if !inputFields[cov.Field] {
				return Column{}, fmt.Errorf("%s: test %s reports coverage of the "+
					"unknown field %q", label(g, f), cov.Hook, cov.Field)
			}

			measured[cov.Field] = true
		}

		withFinding := map[string]bool{}
		expectations, operations := 0, 0
		for _, fd := range f.Findings {
			if isOperation(fd) {
				operations++

				continue
			}

			expectations++
			if inputFields[fd.Feature] {
				withFinding[fd.Feature] = true
			}
		}

		c.Measured = appendCount(c.Measured, handler, len(measured))
		c.FieldsWithFinding = appendCount(c.FieldsWithFinding, handler, len(withFinding))
		c.Expectations = appendCount(c.Expectations, handler, expectations)
		c.Operations = appendCount(c.Operations, handler, operations)

		if f.Header.Failed && len(f.Findings) == 0 {
			c.FailedUnseen = true
		}
	}

	c.NRIVersion = shared(c.NRIVersion)
	c.KernelRelease = shared(c.KernelRelease)
	c.Arch = shared(c.Arch)
	c.Collecting = shared(c.Collecting)
	c.Measured = shared(c.Measured)
	c.FieldsWithFinding = shared(c.FieldsWithFinding)
	c.Expectations = shared(c.Expectations)
	c.Operations = shared(c.Operations)

	return c, nil
}

// label names the runtime, version and handler one fragment's run reported.
func label(g fragmentGroup, f framework.Fragment) string {
	return tableSafe(fmt.Sprintf("%s %s (%s)", g.runtime, g.version, handlerName(f)))
}

// appendValue appends handler's value, or that it recorded none.
func appendValue(values []HandlerValue, handler, value string) []HandlerValue {
	if value == "" {
		value = "not recorded"
	}

	return append(values, HandlerValue{Handler: handler, Value: tableSafe(value)})
}

// appendCount appends handler's count.
func appendCount(values []HandlerValue, handler string, count int) []HandlerValue {
	return append(values, HandlerValue{Handler: handler, Value: strconv.Itoa(count)})
}

// shared returns values as one unnamed entry when every handler reported the
// same value, and values itself otherwise.
func shared(values []HandlerValue) []HandlerValue {
	if len(values) == 0 {
		return values
	}

	for _, v := range values[1:] {
		if v.Value != values[0].Value {
			return values
		}
	}

	return []HandlerValue{{Value: values[0].Value}}
}

// failureMode names how many expectation failures each test of a run
// reported.
func failureMode(collecting bool) string {
	if collecting {
		return "every expectation failure"
	}

	return "the first failure in each test"
}

// tableSafe returns s with the characters that would break a Markdown table
// row escaped or replaced: a pipe escaped, and every line break and tab a
// space.
//
// Here rather than in the template because the template has no function set
// beyond text/template's builtins, and a free-form error or skip reason
// reaches the document from whoever wrote the test that recorded it.
func tableSafe(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	for _, blank := range []string{"\r\n", "\n", "\r", "\t"} {
		s = strings.ReplaceAll(s, blank, " ")
	}

	return s
}

// cellOf returns the group's cell for the field named by name.
func cellOf(g fragmentGroup, name string) Cell {
	handlers := make([]HandlerCell, 0, len(g.frags))
	for _, f := range g.frags {
		handlers = append(handlers, handlerCellOf(f, name))
	}

	if len(handlers) == 0 {
		return Cell{}
	}

	merged := true
	for _, h := range handlers[1:] {
		if h.State != handlers[0].State || h.Text != handlers[0].Text {
			merged = false

			break
		}
	}

	if merged {
		return Cell{State: handlers[0].State, Text: handlers[0].Text}
	}

	cell := Cell{PerHandler: handlers}
	for _, h := range handlers {
		if severity(h.State) > severity(cell.State) {
			cell.State = h.State
		}
	}

	return cell
}

// rowState returns the most severe state of cells, and StateUnmeasured when
// cells is empty.
func rowState(cells []Cell) CellState {
	if len(cells) == 0 {
		return StateUnmeasured
	}

	state := cells[0].State
	for _, c := range cells[1:] {
		if severity(c.State) > severity(state) {
			state = c.State
		}
	}

	return state
}

// severity orders the states from agreement, through an unmeasured field, to
// a finding.
func severity(s CellState) int {
	switch s {
	case StateFinding:
		return 2
	case StateUnmeasured:
		return 1
	}

	return 0
}

// handlerCellOf returns one handler's cell for the field named by name.
func handlerCellOf(f framework.Fragment, name string) HandlerCell {
	h := HandlerCell{Handler: handlerName(f)}

	var findings []framework.FragmentFinding
	for _, fd := range f.Findings {
		if !isOperation(fd) && fd.Feature == name {
			findings = append(findings, fd)
		}
	}

	switch {
	case len(findings) > 0:
		h.State = StateFinding
		h.Text = findingsText(findings)
	case covers(f, name):
		h.State = StateAgreed
	}

	return h
}

// covers reports whether f measured the field named by name.
func covers(f framework.Fragment, name string) bool {
	for _, cov := range f.Coverage {
		if cov.Field == name {
			return true
		}
	}

	return false
}

// othersOf returns the group's findings that are not about an input field. It
// fails on a finding whose feature is neither an input field nor a known
// behavioural one.
func othersOf(g fragmentGroup) ([]Other, error) {
	var others []Other

	for _, f := range g.frags {
		column := label(g, f)

		for _, fd := range f.Findings {
			if !isOperation(fd) {
				if inputFields[fd.Feature] {
					continue
				}
				if !behaviouralFeatures[fd.Feature] {
					return nil, fmt.Errorf("%s: test %s reports the unknown feature %q",
						column, fd.Test, fd.Feature)
				}
			}

			others = append(others, Other{
				Column:  column,
				Kind:    tableSafe(fd.Kind),
				Test:    tableSafe(fd.Test),
				Feature: tableSafe(fd.Feature),
				Surface: tableSafe(fd.Surface),
				Text:    findingText(fd),
			})
		}
	}

	return others, nil
}

// isOperation reports whether fd records a failed driver call.
func isOperation(fd framework.FragmentFinding) bool {
	return fd.Kind == framework.KindOperation.String()
}

// findingsText joins the findings' texts with a semicolon.
func findingsText(findings []framework.FragmentFinding) string {
	texts := make([]string, 0, len(findings))
	for _, fd := range findings {
		texts = append(texts, findingText(fd))
	}

	return strings.Join(texts, "; ")
}

// findingText renders a finding's reason, or its whole error where it has no
// reason, led by the hook it was taken at.
func findingText(fd framework.FragmentFinding) string {
	text := fd.Reason
	if text == "" {
		text = fd.Error
	}
	if text == "" {
		text = "no error recorded"
	}

	if fd.Hook != "" {
		return tableSafe(fd.Hook + ": " + text)
	}

	return tableSafe(text)
}

// selectionsOf returns the group's runs that ran a selection of the tests.
func selectionsOf(g fragmentGroup) []Selection {
	var selections []Selection

	for _, f := range g.frags {
		if f.Selection == "" {
			continue
		}

		selections = append(selections, Selection{
			Column: label(g, f),
			Tests:  tableSafe(f.Selection),
		})
	}

	return selections
}

// appendSkips appends the group's skips, naming the group's run on a skip
// skips already holds.
//
// The run is on the skip because a skip in one column alone is a gap in that
// runtime's results, and the same skip in every column is a gap in the suite.
// Merged without their runs, the two read alike.
func appendSkips(skips []Skip, g fragmentGroup) []Skip {
	for _, f := range g.frags {
		column := label(g, f)

		for _, s := range f.Skips {
			test, reason := tableSafe(s.Test), tableSafe(s.Reason)

			at := slices.IndexFunc(skips, func(o Skip) bool {
				return o.Test == test && o.Reason == reason
			})
			if at < 0 {
				skips = append(skips, Skip{
					Test:    test,
					Reason:  reason,
					Columns: []string{column},
				})

				continue
			}

			if !slices.Contains(skips[at].Columns, column) {
				skips[at].Columns = append(skips[at].Columns, column)
			}
		}
	}

	return skips
}

// SkipGroup is the tests that skipped for one reason, and the runs that
// recorded it.
type SkipGroup struct {
	// Reason is what the tests skipped with.
	Reason string
	// Tests are the tests that skipped for it, in the order they arrived.
	Tests []string
	// Columns names the runtime, version and handler of every run that
	// recorded it.
	Columns []string
}

// SkipGroups returns the view's skips grouped by reason, in the order the
// reasons arrived.
func (v *View) SkipGroups() []SkipGroup {
	groups := []SkipGroup{}

	for _, s := range v.Skips {
		at := slices.IndexFunc(groups, func(g SkipGroup) bool {
			return g.Reason == s.Reason
		})
		if at < 0 {
			groups = append(groups, SkipGroup{
				Reason:  s.Reason,
				Tests:   []string{s.Test},
				Columns: slices.Clone(s.Columns),
			})

			continue
		}

		groups[at].Tests = append(groups[at].Tests, s.Test)

		for _, c := range s.Columns {
			if !slices.Contains(groups[at].Columns, c) {
				groups[at].Columns = append(groups[at].Columns, c)
			}
		}
	}

	return groups
}
