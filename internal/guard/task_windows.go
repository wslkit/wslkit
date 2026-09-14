//go:build windows

package guard

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// The recovery has to run when nobody is watching, which means a scheduled task
// rather than a background process: a task fires on the event, does its work
// and exits, where a resident daemon is one more thing to keep alive across the
// very suspend it is meant to survive.
//
// Three triggers, because no single one covers every machine. Kernel-Power 107
// is the classic resume; Power-Troubleshooter 1 fires where 107 does not,
// including on some Modern Standby machines; and logon covers the case where
// the machine was hibernated or restarted instead.

// TaskName is the per-user task.
const TaskName = `wslkit guard`

// ElevatedTaskName is the one with highest privileges, which is what the last
// two rungs of the ladder need.
const ElevatedTaskName = `wslkit guard (elevated)`

// InstallOptions controls registration.
type InstallOptions struct {
	// Exe is the binary the task runs. Empty means this one.
	Exe string
	// Elevated registers the highest-privileges task as well, which needs
	// administrator rights once, at install time.
	Elevated bool
	// MaxRung caps how far the recovery may climb, passed to run-once.
	MaxRung Rung
}

// Install registers the task, replacing any earlier one.
func Install(ctx context.Context, o InstallOptions) ([]string, error) {
	exe := o.Exe
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("guard: finding this executable: %w", err)
		}
		exe = self
	}
	if _, err := os.Stat(exe); err != nil {
		return nil, fmt.Errorf("guard: %s is not there, so a task pointing at it would fail silently every time: %w", exe, err)
	}

	var installed []string
	specs := []struct {
		name     string
		elevated bool
	}{{TaskName, false}}
	if o.Elevated {
		specs = append(specs, struct {
			name     string
			elevated bool
		}{ElevatedTaskName, true})
	}
	for _, s := range specs {
		xmlText, err := taskXML(exe, s.elevated, o.MaxRung)
		if err != nil {
			return installed, err
		}
		if err := registerTask(ctx, s.name, xmlText); err != nil {
			return installed, err
		}
		installed = append(installed, s.name)
	}
	return installed, nil
}

// Uninstall removes both tasks. A task that is not there is not an error: the
// point of the command is to end up with none.
func Uninstall(ctx context.Context) ([]string, error) {
	var removed []string
	for _, name := range []string{TaskName, ElevatedTaskName} {
		out, err := schtasks(ctx, "/delete", "/tn", name, "/f")
		switch {
		case err == nil:
			removed = append(removed, name)
		case strings.Contains(out, "cannot find") || strings.Contains(out, "does not exist"):
		default:
			return removed, fmt.Errorf("guard: removing the task %q: %w: %s", name, err, strings.TrimSpace(out))
		}
	}
	return removed, nil
}

// Status reports which tasks exist.
func Status(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{TaskName, ElevatedTaskName} {
		_, err := schtasks(ctx, "/query", "/tn", name)
		out[name] = err == nil
	}
	return out
}

// registerTask writes the XML to a temporary file and hands it to schtasks.
//
// The XML form rather than the flag form: /create /sc ONEVENT can express one
// trigger, and this needs three, on two different channels, plus a logon
// trigger. Nothing else about the task is unusual.
func registerTask(ctx context.Context, name, xmlText string) error {
	f, err := os.CreateTemp("", "wslkit-guard-*.xml")
	if err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	// UTF-16 with a byte-order mark: schtasks rejects anything else, with a
	// message that does not say so.
	if _, err := f.Write(utf16BOM(xmlText)); err != nil {
		_ = f.Close()
		return fmt.Errorf("guard: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	out, err := schtasks(ctx, "/create", "/tn", name, "/xml", f.Name(), "/f")
	if err != nil {
		return fmt.Errorf("guard: registering the task %q: %w: %s", name, err, strings.TrimSpace(out))
	}
	return nil
}

func schtasks(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "schtasks.exe", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return decodeWSL(out.Bytes()), err
}

// taskXML builds the task definition.
func taskXML(exe string, elevated bool, maxRung Rung) (string, error) {
	// The principal needs a name. Without one, schtasks refuses the whole
	// registration with "Access is denied", which reads as a permissions
	// problem and is really a missing field: measured, adding it is the
	// difference between refusal and success for the same unprivileged user.
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("guard: finding who is logged in: %w", err)
	}
	runLevel := "LeastPrivilege"
	if elevated {
		runLevel = "HighestAvailable"
	}
	args := "guard run-once"
	if maxRung != RungNone {
		args += " --max-step " + rungFlag(maxRung)
	}
	if elevated {
		args += " --elevated"
	}

	// The delay is on the trigger rather than in the program: a task that
	// starts late costs nothing, where a program that sleeps is a process
	// sitting around being counted by anything watching.
	def := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Gets WSL answering again after the machine has slept. Installed by wslkit guard install.</Description>
  </RegistrationInfo>
  <Triggers>
    <EventTrigger>
      <Enabled>true</Enabled>
      <Delay>PT%dS</Delay>
      <Subscription>&lt;QueryList&gt;&lt;Query Id="0" Path="System"&gt;&lt;Select Path="System"&gt;*[System[Provider[@Name='Microsoft-Windows-Kernel-Power'] and (EventID=107)]]&lt;/Select&gt;&lt;/Query&gt;&lt;/QueryList&gt;</Subscription>
    </EventTrigger>
    <EventTrigger>
      <Enabled>true</Enabled>
      <Delay>PT%dS</Delay>
      <Subscription>&lt;QueryList&gt;&lt;Query Id="0" Path="System"&gt;&lt;Select Path="System"&gt;*[System[Provider[@Name='Microsoft-Windows-Power-Troubleshooter'] and (EventID=1)]]&lt;/Select&gt;&lt;/Query&gt;&lt;/QueryList&gt;</Subscription>
    </EventTrigger>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Delay>PT%dS</Delay>
      <!-- Named, because a logon trigger without a user means "when anybody
           logs on", and registering that needs administrator rights. Without
           this the whole task is refused with "Access is denied", which reads
           as a permissions problem with the task rather than with one line
           of it. -->
      <UserId>%s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>%s</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>`,
		int(SettleDelay.Seconds()), int(SettleDelay.Seconds()), int(SettleDelay.Seconds()),
		xmlEscape(u.Username), xmlEscape(u.Username), runLevel, xmlEscape(exe), xmlEscape(args))
	return def, nil
}

func rungFlag(r Rung) string {
	switch r {
	case RungShutdown:
		return "shutdown"
	case RungForceShutdown:
		return "force"
	case RungKillService:
		return "kill"
	case RungRestartService:
		return "restart"
	}
	return "all"
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// utf16BOM encodes the XML the way schtasks insists on reading it.
func utf16BOM(s string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xff, 0xfe})
	for _, r := range s {
		if r > 0xffff {
			r = '?'
		}
		b.WriteByte(byte(r))
		b.WriteByte(byte(r >> 8))
	}
	return b.Bytes()
}

// LogPath is where a run records what it did. A recovery that runs unattended
// and leaves no account of itself is one nobody can trust or debug.
func LogPath() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "wslkit", "guard.log")
	}
	return filepath.Join(os.TempDir(), "wslkit", "guard.log")
}

// StatePath is where the set of running distributions is recorded, so a later
// run can tell a hang from a cold start.
func StatePath() string {
	return filepath.Join(filepath.Dir(LogPath()), "guard-state.json")
}
