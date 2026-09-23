package cli

// The command tree, described once so that shell completion is generated from
// it rather than hand-written. A hand-written completion script drifts the
// moment a flag is added; a generated one cannot, and a test checks that every
// branch here is one the router actually accepts.

// argKind says what a positional argument accepts, which is what completion
// needs to know to offer anything useful.
type argKind string

const (
	argNone   argKind = ""
	argDistro argKind = "distro" // a WSL distribution name
	argPath   argKind = "path"   // any file
	argDir    argKind = "dir"    // a directory
	argShell  argKind = "shell"  // one of the shells completion supports
	argOther  argKind = "other"  // something completion cannot guess
)

// cmdNode is one command or subcommand.
type cmdNode struct {
	Name string
	Desc string
	// Flags as they are written, long names first.
	Flags []string
	// Positional is what each positional slot accepts, in order.
	Positional []argKind
	Subs       []cmdNode
}

// diskFlagNames are the flags every disk subcommand takes.
var diskFlagNames = []string{"--json", "--verbose", "-v", "--dry-run", "--yes", "-y", "--timeout", "--log"}

func withDiskFlags(extra ...string) []string {
	return append(append([]string{}, diskFlagNames...), extra...)
}

// runFlagNames are the flags the doctor's read-only commands take.
var runFlagNames = []string{
	"--json", "--report", "--verbose", "--only", "--from-snapshot",
	"--elevated", "--allow-vm-wake", "--timeout", "--no-redact", "--online",
}

// Shells lists what completion can be generated for.
var Shells = []string{"powershell", "bash", "zsh"}

// CommandTree describes the whole kit.
func CommandTree() []cmdNode {
	return []cmdNode{
		{
			Name: "doctor",
			Desc: "Diagnose why WSL is broken or slow, then fix it",
			Subs: []cmdNode{
				{Name: "check", Desc: "Read-only ranked diagnosis", Flags: runFlagNames},
				{Name: "explain", Desc: "Decode a WSL error code and run the probes that explain it", Flags: runFlagNames, Positional: []argKind{argOther}},
				{Name: "fix", Desc: "Plan or apply one remediation", Flags: []string{"--apply", "--json"}, Positional: []argKind{argOther}},
				{Name: "undo", Desc: "List journal entries, or replay one rollback", Flags: []string{"--dry-run", "-y", "--yes"}, Positional: []argKind{argOther}},
			},
		},
		{
			Name: "agent",
			Desc: "Guest agent: install it into a distribution, run the Windows daemon",
			Subs: []cmdNode{
				{Name: "install", Desc: "Install the agent into a distribution", Flags: []string{"-d", "--json"}},
				{Name: "uninstall", Desc: "Remove the agent from a distribution", Flags: []string{"-d", "--json"}},
				{Name: "start", Desc: "Start the Windows daemon"},
				{Name: "stop", Desc: "Stop the Windows daemon"},
				{Name: "status", Desc: "Show whether the daemon and agent are connected", Flags: []string{"--json"}},
				{Name: "serve", Desc: "Run the daemon in the foreground"},
				{Name: "autostart", Desc: "Start the daemon when you log in", Positional: []argKind{argOther}},
				{Name: "vm-id", Desc: "Show the id of the running WSL utility VM"},
			},
		},
		{
			Name: "sock",
			Desc: "Bridge Windows sockets into a distribution",
			Subs: []cmdNode{
				{Name: "list", Desc: "Available presets and what they bridge"},
				{Name: "enable", Desc: "Wire a preset into a distribution", Flags: []string{"-d"}, Positional: []argKind{argOther}},
				{Name: "disable", Desc: "Remove a preset from a distribution", Flags: []string{"-d"}, Positional: []argKind{argOther}},
				{Name: "status", Desc: "What is enabled, and whether the agent is connected", Flags: []string{"-d"}},
			},
		},
		{
			Name: "disk",
			Desc: "Inspect and maintain the virtual disks behind distributions",
			Subs: []cmdNode{
				{Name: "list", Desc: "Every distribution and what its disk costs", Flags: withDiskFlags("--probe")},
				{Name: "info", Desc: "Everything known about one distribution", Flags: withDiskFlags("--probe"), Positional: []argKind{argDistro}},
				{Name: "trim", Desc: "Ask the guest to release the blocks it no longer uses", Flags: withDiskFlags("--trim-timeout"), Positional: []argKind{argDistro}},
				{Name: "compact", Desc: "Trim, stop, then shrink the disk file", Flags: withDiskFlags("--all", "--file", "--orphans", "--auto", "--min-reclaim", "--scan", "--no-trim", "--restart", "--shutdown", "--unlock-timeout", "--trim-timeout"), Positional: []argKind{argDistro}},
				{Name: "usage", Desc: "Where the space inside a distribution went", Flags: withDiskFlags("--top", "--by-directory", "--depth"), Positional: []argKind{argDistro}},
				{Name: "orphans", Desc: "Virtual disks that no distribution claims", Flags: withDiskFlags("--scan", "--delete", "--relink", "--to")},
				{Name: "relink", Desc: "Point a distribution at a disk that has moved", Flags: withDiskFlags(), Positional: []argKind{argDistro, argPath}},
				{Name: "move", Desc: "Move a distribution disk, then check it still boots", Flags: withDiskFlags("--keep-source"), Positional: []argKind{argDistro, argDir}},
				{Name: "rebuild", Desc: "Export and import a distribution into a fresh disk", Flags: withDiskFlags("--work-dir", "--keep-archive", "--restart", "--transfer-timeout"), Positional: []argKind{argDistro}},
				{Name: "trash", Desc: "Unregister a distribution but keep its disk", Flags: withDiskFlags("--list", "--purge", "--older-than", "--shutdown"), Positional: []argKind{argDistro}},
				{Name: "undelete", Desc: "Register a trashed distribution again", Flags: withDiskFlags(), Positional: []argKind{argOther}},
				{Name: "config", Desc: "Show or change the disk settings", Flags: withDiskFlags(), Subs: []cmdNode{
					{Name: "path", Desc: "Print the path of the settings file"},
					{Name: "get", Desc: "Print one setting, or all of them", Positional: []argKind{argOther}},
					{Name: "set", Desc: "Change one setting", Positional: []argKind{argOther, argOther}},
					{Name: "edit", Desc: "Open the settings file in an editor"},
				}},
			},
		},
		{Name: "top", Desc: "What the utility VM is using, and which distribution is responsible", Flags: []string{"--json", "--interval", "--once", "--watch", "--wsl", "--wslc", "--raw", "--timeout"}, Positional: []argKind{argDistro}},
		{
			Name: "limit",
			Desc: "Cap what one distribution may use",
			Subs: []cmdNode{
				{Name: "show", Desc: "What each distribution is capped at", Flags: []string{"-d", "--json", "--timeout"}},
				{Name: "set", Desc: "Cap a distribution", Flags: []string{"-d", "--memory", "--high", "--cpus", "--swap", "--no-swap", "--dry-run", "--timeout"}},
				{Name: "clear", Desc: "Remove the caps", Flags: []string{"-d", "--dry-run", "--timeout"}},
			},
		},
		{
			Name: "guard",
			Desc: "Get WSL answering again after the machine has slept",
			Subs: []cmdNode{
				{Name: "run-once", Desc: "Probe now, and recover if it is needed", Flags: []string{"--dry-run", "--elevated", "--max-step", "--quiet"}},
				{Name: "install", Desc: "Run it on resume and at logon", Flags: []string{"--elevated", "--max-step"}},
				{Name: "uninstall", Desc: "Remove the scheduled tasks"},
				{Name: "status", Desc: "What is installed, and what the last run did"},
			},
		},
		{
			Name: "proxy",
			Desc: "Get a Windows proxy configuration working inside a distribution",
			Subs: []cmdNode{
				{Name: "show", Desc: "What Windows is configured to do, and what a distribution would get", Flags: []string{"--for", "--pac", "--http", "--https", "--json"}},
				{Name: "apply", Desc: "Write the proxy into a distribution, everywhere that reads one", Flags: []string{"-d", "--dry-run", "-y", "--yes", "--for", "--pac", "--http", "--https", "--timeout"}},
				{Name: "revert", Desc: "Take the proxy configuration out again", Flags: []string{"-d", "--dry-run", "-y", "--yes", "--timeout"}},
				{Name: "serve", Desc: "Run a local forward proxy that asks Windows per request", Flags: []string{"--port", "--upstream", "--direct", "--pac", "--cache-ttl", "--loopback-only", "--quiet"}},
				{Name: "check", Desc: "Can the distribution actually reach the local proxy?", Flags: []string{"-d", "--port", "--addr", "--timeout"}},
			},
		},
		{Name: "completion", Desc: "Print a shell completion script", Positional: []argKind{argShell}},
		{Name: "version", Desc: "Print the version"},
		{Name: "help", Desc: "Print usage"},
	}
}

// globalFlags are accepted wherever a command is.
var globalFlags = []string{"--help", "-h", "--version", "-v"}
