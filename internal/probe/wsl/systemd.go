package wsl

import (
	"fmt"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
	"github.com/wslkit/wslkit/internal/wslconfig"
)

// SYS001 reports the health of systemd in each running distribution, and of
// the default user's session under it.
//
// The upstream thread (microsoft/WSL#13826, with #13562 and #13848) is one
// line at launch, "Failed to start the systemd user session", followed by a
// shell where systemctl --user, the session bus and XDG_RUNTIME_DIR do not
// work. That line covers several different faults, and each has its own
// finding here rather than one lump: systemd asked for but not PID 1, the
// system degraded, the user session missing while the system is fine, and
// the runtime directory wrong.
//
// It reports and explains. Changing a PAM stack or a unit behind somebody's
// back is not a fix this tool makes.

type Systemd struct{}

func (Systemd) base() probe.Base {
	return probe.Base{
		PID:        "SYS001",
		PTitle:     "systemd health, the user session and XDG_RUNTIME_DIR",
		PMilestone: "M3",
		PNeeds:     []string{"WSL004"},
	}
}

func (p Systemd) ID() string        { return p.base().ID() }
func (p Systemd) Title() string     { return p.base().Title() }
func (p Systemd) Milestone() string { return p.base().Milestone() }
func (p Systemd) Needs() []string   { return p.base().Needs() }

func (p Systemd) Run(e *env.Env) probe.Result {
	b := p.base()
	if !e.Distros.OK() && !e.Distros.Absent() {
		return b.Res(probe.Unknown, 0.1, "could not read the distribution inventory: "+e.Distros.Err)
	}
	distros := e.DistroList()
	if len(distros) == 0 {
		return b.Res(probe.OK, 0, "No distributions are registered")
	}

	var findings, details, lines, skipped []string
	checked := 0
	for _, d := range distros {
		if d.Version != 2 {
			continue
		}
		// A zero field reads as OK; one never collected, as in a snapshot
		// from before this check, must not pass for a healthy system.
		if !d.Systemd.Collected() || !d.Systemd.OK() {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", d.Name, reasonForSystemd(d.Systemd)))
			continue
		}
		checked++
		f, dt, l := judgeSystemd(d.Name, d.Systemd.Value, systemdWanted(d))
		findings = append(findings, f...)
		details = append(details, dt...)
		lines = append(lines, l...)
	}

	if checked == 0 {
		msg := "No running distribution to check"
		if len(skipped) > 0 {
			msg += " (" + strings.Join(skipped, "; ") + ")"
		}
		r := b.Res(probe.Skipped, 0, msg)
		r.Detail = "systemd is only asked about in a distribution that is already running: starting one to check would start systemd, which is the thing being checked."
		return r
	}
	if len(findings) == 0 {
		msg := fmt.Sprintf("systemd and the user session are healthy in %d distribution(s)", checked)
		if len(skipped) > 0 {
			msg += fmt.Sprintf("; %d not running", len(skipped))
		}
		r := b.Res(probe.OK, 0.5, msg)
		r.Detail = strings.Join(lines, "\n")
		return r
	}
	r := b.Res(probe.Warn, 0.5, strings.Join(findings, "; "))
	r.Detail = strings.Join(append(details, lines...), "\n")
	r.Refs = []string{
		"https://github.com/microsoft/WSL/issues/13826",
		"https://github.com/microsoft/WSL/issues/13562",
	}
	return r
}

// systemdWanted reads boot.systemd from the distribution's wsl.conf. It is
// only a hint of intent: a distribution can run systemd without it, if its
// image starts systemd some other way.
func systemdWanted(d env.Distro) bool {
	if !d.WslConf.OK() {
		return false
	}
	v, ok := wslconfig.Parse(d.WslConf.Value).Get("boot", "systemd")
	if !ok {
		return false
	}
	on, valid := wslconfig.ParseBool(v)
	return valid && on
}

// judgeSystemd turns one distribution's state into findings (short), their
// details, and lines that are worth showing but are not faults.
func judgeSystemd(name string, s env.SystemdState, wanted bool) (findings, details, lines []string) {
	add := func(f, d string) {
		findings = append(findings, name+": "+f)
		details = append(details, name+": "+d)
	}

	if s.PID1 != "systemd" {
		if wanted {
			add(fmt.Sprintf("boot.systemd=true, but PID 1 is %s", orUnset(s.PID1)),
				fmt.Sprintf("wsl.conf asks for systemd and PID 1 is %s. wsl.conf is read when the distribution starts, so a change needs wsl --terminate %s; if it is already restarted, the image may not have systemd installed at all.", orUnset(s.PID1), name))
			return
		}
		lines = append(lines, fmt.Sprintf("%s: systemd is not enabled (PID 1 is %s); boot.systemd=true in wsl.conf turns it on", name, orUnset(s.PID1)))
		return
	}

	var failed, benign []string
	for _, u := range s.Failed {
		if why, ok := benignUnderWSL[u]; ok {
			benign = append(benign, u+" ("+why+")")
			continue
		}
		failed = append(failed, u)
	}
	switch {
	case s.System == "running":
		lines = append(lines, name+": systemd is running")
	case s.System == "starting" || s.System == "initializing":
		lines = append(lines, name+": systemd is still starting; checked again on the next run it will have settled")
	case s.System == "degraded" && len(failed) == 0 && len(benign) > 0:
		lines = append(lines, fmt.Sprintf("%s: systemd says degraded, only because of %s", name, strings.Join(benign, ", ")))
	case s.System == "degraded":
		add(fmt.Sprintf("systemd is degraded, %d failed unit(s)", len(failed)),
			fmt.Sprintf("systemctl is-system-running says degraded. Failed: %s. See systemctl status <unit> inside the distribution; a unit that cannot work under WSL can be masked with systemctl mask <unit>.", unitList(failed)))
		if len(benign) > 0 {
			lines = append(lines, fmt.Sprintf("%s: also failed, and harmless under WSL: %s", name, strings.Join(benign, ", ")))
		}
	default:
		add(fmt.Sprintf("systemd reports %q", orUnset(s.System)),
			fmt.Sprintf("systemctl is-system-running says %q, which is neither running nor degraded.", orUnset(s.System)))
	}

	// The user session. A default user of root has no session of its own in
	// the sense the upstream threads mean, and its processes are the whole
	// system's, so none of what follows can be judged for it.
	if s.UID == 0 {
		lines = append(lines, name+": the default user is root, so there is no user session to check")
		return
	}
	if !s.UserKnown {
		add(fmt.Sprintf("default uid %d is not in /etc/passwd", s.UID),
			fmt.Sprintf("The registration's DefaultUid is %d, and no user in /etc/passwd has it, so WSL starts sessions as a user that does not exist. wsl --manage %s --set-default-user <name> sets a real one.", s.UID, name))
		return
	}
	if s.UserProcs == 0 {
		lines = append(lines, fmt.Sprintf("%s: no process of uid %d is running, so whether its session starts cannot be told yet", name, s.UID))
		return
	}
	runUser := fmt.Sprintf("/run/user/%d", s.UID)
	if s.UserService != "active" {
		d := fmt.Sprintf("uid %d has %d process(es), but user@%d.service is %s. This is \"Failed to start the systemd user session\" (microsoft/WSL#13826): systemctl --user, the session bus and %s are missing for that user.",
			s.UID, s.UserProcs, s.UID, orUnset(s.UserService), runUser)
		if !s.PAMSystemd {
			d += " No file in /etc/pam.d loads pam_systemd, which is what starts the session at login."
		}
		add(fmt.Sprintf("the systemd user session for uid %d is not running", s.UID), d)
		return
	}
	switch {
	case !s.RunUserExists:
		add(runUser+" does not exist",
			fmt.Sprintf("user@%d.service is active but %s is missing, so nothing can put its sockets there.", s.UID, runUser))
	case s.RunUserOwner != s.UID || s.RunUserMode != "700":
		add(fmt.Sprintf("%s is owned by uid %d with mode %s", runUser, s.RunUserOwner, s.RunUserMode),
			fmt.Sprintf("%s should belong to uid %d with mode 700; anything else and the session's own services refuse to use it.", runUser, s.UID))
	}
	switch {
	case s.UserProcsXDG > 0 && s.XDG != runUser:
		add(fmt.Sprintf("XDG_RUNTIME_DIR is %s, not %s", s.XDG, runUser),
			fmt.Sprintf("Processes of uid %d have XDG_RUNTIME_DIR=%s, so anything looking for the session bus or an agent socket looks in the wrong place. A shell profile that sets it is the usual cause.", s.UID, s.XDG))
	case s.UserProcsXDG == 0:
		add("XDG_RUNTIME_DIR is not set",
			fmt.Sprintf("None of the %d process(es) of uid %d has XDG_RUNTIME_DIR set, although its session is running (microsoft/WSL#13562).", s.UserProcs, s.UID))
	default:
		lines = append(lines, fmt.Sprintf("%s: the user session for uid %d is running, with XDG_RUNTIME_DIR=%s", name, s.UID, runUser))
	}
	return
}

// benignUnderWSL are units whose failure under WSL is expected and costs
// nothing, so they are named but are not a finding. systemd-binfmt failed on
// Ubuntu on WSL 3.0.1 here, and was the only reason that Ubuntu was degraded.
// When it does succeed it re-registers binfmt_misc and can drop WSL's own
// WSLInterop entry, after which Windows executables stop launching
// (microsoft/WSL#8843), so its failure is the better outcome.
var benignUnderWSL = map[string]string{
	"systemd-binfmt.service": "WSL registers its own binfmt handler for Windows executables",
}

func unitList(units []string) string {
	if len(units) == 0 {
		return "(none listed)"
	}
	return strings.Join(units, ", ")
}

func reasonForSystemd(f env.Field[env.SystemdState]) string {
	if !f.Collected() {
		return "not collected"
	}
	switch f.ErrKind {
	case env.ErrVMWakeRefused:
		return "not running"
	case env.ErrTimeout:
		return "timed out"
	case env.ErrUnsupported:
		return "not applicable"
	case env.ErrNone:
		return "not collected"
	default:
		if f.Err == "" {
			return "not collected"
		}
		return f.Err
	}
}
