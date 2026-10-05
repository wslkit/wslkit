//go:build windows

package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/wslkit/wslkit/internal/disk"
	"github.com/wslkit/wslkit/internal/fix"
)

// regValue is one value on a distribution's Lxss key, as it was before a disk
// command changed it.
type regValue struct {
	Name    string
	DWORD   bool
	Old     string
	Present bool
}

// journalRegistry records registry changes in the kit's undo journal, so
// `wslkit doctor undo` puts the old values back the same way it undoes a
// doctor fix.
//
// The rollback runs reg.exe on the user's own HKCU key, which needs no
// elevation, rather than adding a registry step kind the executor would have
// to grow.
func journalRegistry(id, title, guid string, values []regValue) (string, error) {
	key := `HKCU\` + disk.LxssKey + `\` + guid
	p := fix.Plan{FixID: id, Title: title, CreatedAt: time.Now()}
	for _, v := range values {
		p.Steps = append(p.Steps, fix.Step{Kind: "note", Description: fmt.Sprintf("%s changed %s on %s", title, v.Name, guid)})
		if !v.Present {
			p.Rollback = append(p.Rollback, fix.Step{
				Kind:        "exec",
				Args:        []string{"reg.exe", "delete", key, "/v", v.Name, "/f"},
				Description: fmt.Sprintf("remove %s again", v.Name),
			})
			continue
		}
		typ := "REG_SZ"
		if v.DWORD {
			typ = "REG_DWORD"
		}
		p.Rollback = append(p.Rollback, fix.Step{
			Kind:        "exec",
			Args:        []string{"reg.exe", "add", key, "/v", v.Name, "/t", typ, "/d", v.Old, "/f"},
			Description: fmt.Sprintf("put %s back to %s", v.Name, v.Old),
		})
	}
	return fix.Journal{Dir: fix.DefaultJournalDir()}.Save(p)
}

// dwordValue is the journal form of a DWORD that was there.
func dwordValue(name string, v uint32, present bool) regValue {
	return regValue{Name: name, DWORD: true, Old: strconv.FormatUint(uint64(v), 10), Present: present}
}
