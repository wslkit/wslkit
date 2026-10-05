package collect

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
)

// systemd inside a distribution fails in a family of ways that all look the
// same from outside: one line at launch, "Failed to start the systemd user
// session", and then a shell where systemctl --user, the session bus and
// anything that wants XDG_RUNTIME_DIR do not work (microsoft/WSL#13826,
// #13562, #13848). Telling them apart needs systemctl, so unlike the other
// checks here this one runs a script inside the distribution rather than
// reading files over \\wsl.localhost.
//
// The script only reads. It runs as root, because systemctl's view of the
// system and other users' /proc entries need it. It does not log in as the
// user to see the user's environment: a login goes through pam_systemd, which
// starts the very session whose absence is being diagnosed. The environment is
// read from the user's existing processes instead.

// maxFailedUnits bounds the list. The first few name the problem.
const maxFailedUnits = 10

// systemdScript renders the script for one default user.
//
// The uid is the only thing substituted, and it is an integer, so nothing in
// it can be read by the shell as anything else. Every systemctl call is under
// timeout(1): a systemd still starting can leave systemctl waiting.
func systemdScript(uid int) string {
	return strings.NewReplacer("@UID@", strconv.Itoa(uid), "@MAX@", strconv.Itoa(maxFailedUnits)).Replace(`uid=@UID@
echo "pid1=$(cat /proc/1/comm 2>/dev/null)"
if command -v systemctl >/dev/null 2>&1 && [ "$(cat /proc/1/comm 2>/dev/null)" = systemd ]; then
  echo "system=$(timeout 3 systemctl is-system-running 2>/dev/null)"
  timeout 3 systemctl list-units --state=failed --no-legend --plain 2>/dev/null | awk 'NF{print "failed=" $1}' | head -@MAX@
  echo "user_service=$(timeout 3 systemctl is-active user@$uid.service 2>/dev/null)"
fi
grep -q "^[^:]*:[^:]*:$uid:" /etc/passwd 2>/dev/null && echo "user_known=yes"
if [ -d /run/user/$uid ]; then echo "run_user=$(stat -c '%u %a' /run/user/$uid 2>/dev/null)"; fi
grep -ls '^[^#]*pam_systemd' /etc/pam.d/* >/dev/null 2>&1 && echo "pam_systemd=yes"
procs=0; withxdg=0; xdg=""
for p in /proc/[0-9]*; do
  u=$(awk '/^Uid:/{print $2; exit}' "$p/status" 2>/dev/null)
  [ "$u" = "$uid" ] || continue
  procs=$((procs+1))
  v=$(tr '\0' '\n' < "$p/environ" 2>/dev/null | sed -n 's/^XDG_RUNTIME_DIR=//p' | head -1)
  if [ -n "$v" ]; then withxdg=$((withxdg+1)); [ -z "$xdg" ] && xdg=$v; fi
done
echo "user_procs=$procs"
echo "user_procs_xdg=$withxdg"
echo "xdg=$xdg"
`)
}

// parseSystemd reads what the script printed.
func parseSystemd(out string, uid int) (env.SystemdState, error) {
	s := env.SystemdState{UID: uid}
	seen := false
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		seen = true
		switch k {
		case "pid1":
			s.PID1 = v
		case "system":
			s.System = v
		case "failed":
			if v != "" {
				s.Failed = append(s.Failed, v)
			}
		case "user_service":
			s.UserService = v
		case "user_known":
			s.UserKnown = v == "yes"
		case "run_user":
			owner, mode, ok := strings.Cut(v, " ")
			if n, err := strconv.Atoi(owner); ok && err == nil {
				s.RunUserExists, s.RunUserOwner, s.RunUserMode = true, n, mode
			}
		case "pam_systemd":
			s.PAMSystemd = v == "yes"
		case "user_procs":
			s.UserProcs, _ = strconv.Atoi(v)
		case "user_procs_xdg":
			s.UserProcsXDG, _ = strconv.Atoi(v)
		case "xdg":
			s.XDG = v
		}
	}
	if !seen || s.PID1 == "" {
		return s, errors.New("the distribution printed nothing that could be read")
	}
	return s, nil
}

// systemdSource names what was run, for the provenance of the field.
func systemdSource(distro string) string {
	return fmt.Sprintf("wsl.exe -d %s -u root --exec /bin/sh (systemd state script)", distro)
}
