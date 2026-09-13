package disk

import (
	"context"
	"fmt"
	"io"
)

// The operation lifecycle. Every mutating disk command is a Plan, then an
// Execute, and the split is what makes --dry-run trustworthy: planning is
// read-only, so a dry run is free of side effects rather than merely free of
// mutations.
//
//	plan := op.Plan()          preconditions and the list of steps
//	report, err := op.Execute(...)
//
// An operation never calls its own rollback. Run does that, so the rule about
// when to roll back lives in one place.

// Step is one thing an operation will do, described before it happens.
//
// The two flags are about rollback, and most steps set neither. Stopping a
// distribution, running a trim and starting it again are all things the
// operation does rather than changes it would need to take back.
type Step struct {
	// Description is what the user is told, in the imperative.
	Description string
	// Undoable marks a step that registers a rollback entry, such as
	// rewriting a registry value or copying a file into place.
	Undoable bool
	// Irreversible marks a step past which no rollback is possible, such as
	// rewriting a disk in place or deleting the original.
	Irreversible bool
}

// Warning is something true about the plan that the user should know before it
// runs, paired with what to do about it.
type Warning struct {
	Message string
	Remedy  string
}

// Plan is the read-only result of preparing an operation.
type Plan struct {
	// Subject is what is being acted on: a distribution name, or a path
	// when the operation targets a loose file.
	Subject string
	// SubjectKey names the subject in JSON output.
	SubjectKey string
	Steps      []Step
	Warnings   []Warning
}

// Add appends a step.
func (p *Plan) Add(format string, args ...any) {
	p.Steps = append(p.Steps, Step{Description: fmt.Sprintf(format, args...)})
}

// AddUndoable appends a step that registers a rollback entry.
func (p *Plan) AddUndoable(format string, args ...any) {
	p.Steps = append(p.Steps, Step{Description: fmt.Sprintf(format, args...), Undoable: true})
}

// AddIrreversible appends a step past which no rollback is possible.
func (p *Plan) AddIrreversible(format string, args ...any) {
	p.Steps = append(p.Steps, Step{Description: fmt.Sprintf(format, args...), Irreversible: true})
}

// Warn appends a warning.
func (p *Plan) Warn(message, remedy string) {
	p.Warnings = append(p.Warnings, Warning{Message: message, Remedy: remedy})
}

// Valid checks the one structural rule: no undoable change may be scheduled
// after an irreversible one.
//
// Past the point of no return there is no rollback to run, so a step that
// registers one there would promise a safety net that does not exist. Steps
// that change nothing needing rollback, such as starting a distribution again
// afterwards, are unaffected.
//
// Violating this is a programming mistake, not a user error.
func (p Plan) Valid() error {
	seen := false
	for _, s := range p.Steps {
		if s.Irreversible {
			seen = true
			continue
		}
		if seen && s.Undoable {
			return fmt.Errorf("disk: the step %q would register a rollback after an irreversible step, where no rollback can run; this is a bug in wslkit, please report it with the command you ran", s.Description)
		}
	}
	return nil
}

// RenderDryRun writes the plan as the answer to --dry-run.
func RenderDryRun(w io.Writer, p Plan, prefix string) {
	fmt.Fprintf(w, "%s--dry-run: nothing was changed. It would have:\n", prefix)
	for _, s := range p.Steps {
		fmt.Fprintf(w, "  %s\n", s.Description)
	}
	for _, warn := range p.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn.Message)
		if warn.Remedy != "" {
			fmt.Fprintf(w, "           %s\n", warn.Remedy)
		}
	}
}

// DryRunJSON is the object printed for --dry-run --json.
func DryRunJSON(p Plan) map[string]any {
	key := p.SubjectKey
	if key == "" {
		key = "distribution"
	}
	steps := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, s.Description)
	}
	warnings := make([]map[string]any, 0, len(p.Warnings))
	for _, w := range p.Warnings {
		warnings = append(warnings, map[string]any{"message": w.Message, "remedy": w.Remedy})
	}
	return map[string]any{
		key:        p.Subject,
		"dry_run":  true,
		"steps":    steps,
		"warnings": warnings,
	}
}

// Progress is how an operation reports what it is doing. A console
// implementation prints step lines and a percentage; --json uses a sink that
// discards, since progress on stdout would corrupt the stream.
type Progress interface {
	// Step announces that a described step has begun.
	Step(description string)
	// Fraction reports progress within the current step. Returning false
	// asks the operation to stop.
	Fraction(current, total uint64) bool
	// Status replaces a transient line, such as a countdown.
	Status(line string)
}

// DiscardProgress reports nothing and never cancels.
type DiscardProgress struct{}

func (DiscardProgress) Step(string)                  {}
func (DiscardProgress) Fraction(uint64, uint64) bool { return true }
func (DiscardProgress) Status(string)                {}

// ConsoleProgress writes step lines to a writer.
type ConsoleProgress struct {
	W io.Writer
	// Ctx cancels the operation when the caller goes away.
	Ctx context.Context
}

func (c ConsoleProgress) Step(description string) {
	fmt.Fprintf(c.W, "  %s ...\n", description)
}

func (c ConsoleProgress) Fraction(current, total uint64) bool {
	if c.Ctx != nil && c.Ctx.Err() != nil {
		return false
	}
	return true
}

func (c ConsoleProgress) Status(line string) {
	// Transient: overwritten by whatever comes next rather than scrolling.
	fmt.Fprintf(c.W, "  %s\r", line)
}
