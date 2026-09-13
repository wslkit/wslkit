package cli

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The tree is what completion is generated from, so it drifting from the
// commands the binary actually accepts is the one failure that would make the
// generated scripts worse than useless. The usage text is the third statement
// of the same list, so the two are checked against each other.
func TestCommandTreeMatchesTheUsageText(t *testing.T) {
	a, _, errb := newApp()
	a.Run(nil)
	usage := errb.String()

	inTree := map[string]bool{}
	for _, n := range CommandTree() {
		inTree[n.Name] = true
	}

	// Every command the usage advertises must be in the tree.
	re := regexp.MustCompile(`(?m)^\s+wslkit ([a-z-]+)`)
	inUsage := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(usage, -1) {
		inUsage[m[1]] = true
	}
	if len(inUsage) == 0 {
		t.Fatalf("no commands were found in the usage text:\n%s", usage)
	}
	for name := range inUsage {
		if !inTree[name] {
			t.Errorf("the usage text offers %q but the command tree does not know it, so it will not complete", name)
		}
	}
	// And every command in the tree must be advertised, or it is a command
	// nobody can discover.
	for name := range inTree {
		if !inUsage[name] {
			t.Errorf("the command tree has %q but the usage text does not mention it", name)
		}
	}
}

func TestCompletionRejectsAShellItCannotGenerate(t *testing.T) {
	var b bytes.Buffer
	err := WriteCompletion(&b, "fish")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, shell := range Shells {
		if !strings.Contains(err.Error(), shell) {
			t.Errorf("the error should list %q: %v", shell, err)
		}
	}
	if b.Len() != 0 {
		t.Errorf("nothing should have been written: %q", b.String())
	}
}

func TestCompletionUsage(t *testing.T) {
	for _, args := range [][]string{
		{"completion"},
		{"completion", "bash", "zsh"},
		{"completion", "--json"},
	} {
		a, _, errb := newApp()
		if code := a.Run(args); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if !strings.Contains(errb.String(), "powershell") {
			t.Errorf("%v: stderr %q", args, errb.String())
		}
	}
}

// Every shell must produce a script that mentions the commands, and none may
// be empty.
func TestEveryShellProducesAScript(t *testing.T) {
	for _, shell := range Shells {
		t.Run(shell, func(t *testing.T) {
			var b bytes.Buffer
			if err := WriteCompletion(&b, shell); err != nil {
				t.Fatal(err)
			}
			out := b.String()
			if len(out) < 500 {
				t.Fatalf("the script looks truncated:\n%s", out)
			}
			for _, want := range []string{"disk compact", "doctor check", "sock enable", "disk config set"} {
				if !strings.Contains(out, want) {
					t.Errorf("the script does not mention %q", want)
				}
			}
			// Names are resolved when the user presses Tab, not baked
			// in, or a script generated last month would not know about
			// a distribution installed this morning.
			if !strings.Contains(out, "disk list --json") {
				t.Error("distribution names should be resolved at completion time")
			}
		})
	}
}

// The generated script must be the same every time, or regenerating it produces
// a spurious diff.
func TestCompletionOutputIsStable(t *testing.T) {
	for _, shell := range Shells {
		var first, second bytes.Buffer
		if err := WriteCompletion(&first, shell); err != nil {
			t.Fatal(err)
		}
		if err := WriteCompletion(&second, shell); err != nil {
			t.Fatal(err)
		}
		if first.String() != second.String() {
			t.Errorf("%s: two runs produced different scripts", shell)
		}
	}
}

// Flags are offered per command, so a flag that only one command takes must not
// appear against another.
func TestCompletionOffersFlagsPerCommand(t *testing.T) {
	entries := map[string]completionEntry{}
	for _, e := range completionEntries() {
		entries[e.Path] = e
	}
	compact, ok := entries["disk compact"]
	if !ok {
		t.Fatal("disk compact is missing from the tree")
	}
	if !strings.Contains(compact.words(), "--shutdown") {
		t.Errorf("compact should offer --shutdown: %s", compact.words())
	}
	list := entries["disk list"]
	if strings.Contains(list.words(), "--shutdown") {
		t.Errorf("list should not offer --shutdown: %s", list.words())
	}
	if !strings.Contains(list.words(), "--probe") {
		t.Errorf("list should offer --probe: %s", list.words())
	}
	// A command with subcommands offers them first.
	config := entries["disk config"]
	if !strings.HasPrefix(config.words(), "path get set edit") {
		t.Errorf("config should offer its verbs first: %s", config.words())
	}
}

// The commands that take a distribution name are the ones worth completing
// names for.
func TestDistroCompletionCoversTheCommandsThatTakeOne(t *testing.T) {
	got := pathsTakingKind(argDistro)
	sort.Strings(got)
	want := []string{"disk compact", "disk info", "disk move", "disk relink", "disk trim", "disk usage"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDedupeKeepsTheFirstOfEach(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "", "c", "b"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("got %v", got)
	}
}
