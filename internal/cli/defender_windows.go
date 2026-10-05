//go:build windows

package cli

import "fmt"

// Microsoft Defender quarantines wslkit when it registers a scheduled task
// that runs wslkit itself. Measured on Windows 10 22H2 with security
// intelligence 1.459.561.0, 2026-10-05: `guard install` was flagged as
// Behavior:Win32/Persistence.A!ml run from %TEMP% and as
// Behavior:Win32/Execution.A!ml run from %LOCALAPPDATA%\Programs, within
// seconds. Defender quarantined the binary and deleted the task as well, so
// the guard was gone and so was the tool. On 2026-09-14 the same command was
// not flagged.
//
// It is a behaviour verdict on an unsigned binary of little reputation, and
// the lasting fix is signing (#19). Until then the two commands that do this
// refuse without --force, so nobody loses the binary by surprise.

// defenderTaskWarning explains the refusal and the way past it.
const defenderTaskWarning = `Microsoft Defender currently quarantines wslkit when it registers a scheduled
task that runs wslkit (Behavior:Win32/Persistence.A!ml or Execution.A!ml),
and deletes the task too. wslkit is not signed yet, which is what that
verdict rests on (https://github.com/wslkit/wslkit/issues/19).

To go ahead anyway, add --force, and if Defender flags it, restore wslkit
from Windows Security > Protection history and allow it there. See
https://wslkit.github.io/wslkit/install/#microsoft-defender`

// refuseScheduledTask stops a command that would register a task, unless it
// was forced. It reports whether the caller should stop.
func (a *App) refuseScheduledTask(command string, force bool) bool {
	if force {
		return false
	}
	fmt.Fprintf(a.Stderr, "%s registers a scheduled task, and was not run.\n\n%s\n", command, defenderTaskWarning)
	return true
}
