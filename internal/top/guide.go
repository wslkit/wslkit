package top

import (
	"strings"
	"unicode"
)

// ReadingGuide explains what top's report means. It is the same every run, so
// it is printed by `wslkit top help` rather than under every report, where it
// was noise from the second run on. The report's notes section is left for
// what that run found.
var ReadingGuide = strings.Join([]string{
	section("the VM lines",
		"page cache (reclaimable) is memory the VM holds that Windows can take back under pressure; anonymous (unreclaimable) is what it cannot while the VM needs it, short of the VM swapping it out. A VM that looks large but is mostly page cache is not a problem.",
		"stalled over the last 10 s is Linux pressure stall information: the share of the last ten seconds in which at least one task waited for memory, for the disk, or for a CPU. Usage says a resource is busy; this says it is short. A few percent is ordinary activity; a value that stays high is what holds things up.",
		"network is for the whole VM: distributions share one network namespace, so there is no honest number per distribution.",
		"Windows charges it is the working set of the VM's vmmem process: what Task Manager shows for it.",
	),
	section("the rows",
		upperFirst(cgroupNote),
		upperFirst(processesNote)+" "+upperFirst(cgroupOnlyNote),
		groupsNote,
		"CPU is a share of one processor, so 150% is one and a half cores. READ and WRITE are disk bytes per second. PIDS counts processes and threads. STALL MEM/IO is the row's own pressure, as on the VM line.",
	),
	section("wslc",
		sessionsNote,
		"A session is shown only while its VM is running. top never starts one: it tells a running VM from a stopped one by whether Windows reports the session's disk in use, without asking wslc.",
	),
}, "\n")

// section lays out one heading and its paragraphs, each wrapped to fit an
// 80-column window.
func section(title string, paras ...string) string {
	var b strings.Builder
	b.WriteString(title + ":\n")
	for _, p := range paras {
		for _, line := range wrap(p, 74) {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// wrap breaks text into lines of at most width runes, at spaces.
func wrap(text string, width int) []string {
	var lines []string
	var line []rune
	for _, word := range strings.FieldsFunc(text, unicode.IsSpace) {
		w := []rune(word)
		if len(line) > 0 && len(line)+1+len(w) > width {
			lines = append(lines, string(line))
			line = nil
		}
		if len(line) > 0 {
			line = append(line, ' ')
		}
		line = append(line, w...)
	}
	if len(line) > 0 {
		lines = append(lines, string(line))
	}
	return lines
}
