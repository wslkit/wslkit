// Package env defines the environment snapshot that collectors fill and probes
// read. It is pure data: no Windows imports, so it builds everywhere and
// round-trips through JSON (schema wsldoctor/env/v1).
package env

import (
	"errors"
	"time"
)

const Schema = "wsldoctor/env/v1"

// ErrKind classifies why a Field has no value. Probes map these to UNKNOWN or
// SKIPPED; they must never map them to OK or FAIL.
type ErrKind string

const (
	ErrNone           ErrKind = ""
	ErrNotPresent     ErrKind = "not_present"     // the thing does not exist (no .wslconfig, no distros)
	ErrNeedsElevation ErrKind = "needs_elevation" // access denied unelevated
	ErrVMWakeRefused  ErrKind = "vm_wake_refused" // would have run wsl.exe; not allowed
	ErrTimeout        ErrKind = "timeout"
	ErrUnsupported    ErrKind = "unsupported" // e.g. collector not implemented on this OS
	ErrOther          ErrKind = "error"
)

// Field is a collected value with provenance. Source names where it came from
// ("HKCU\...\Lxss", "Win32_OptionalFeature", "wslservice.exe version").
type Field[T any] struct {
	Value   T       `json:"value"`
	Source  string  `json:"source,omitempty"`
	ErrKind ErrKind `json:"err_kind,omitempty"`
	Err     string  `json:"err,omitempty"`
}

func Ok[T any](v T, source string) Field[T] {
	return Field[T]{Value: v, Source: source}
}

func Fail[T any](kind ErrKind, source string, err error) Field[T] {
	f := Field[T]{Source: source, ErrKind: kind}
	if err != nil {
		f.Err = err.Error()
	}
	if f.ErrKind == ErrNone {
		f.ErrKind = ErrOther
	}
	return f
}

// Absent is a Field whose value legitimately does not exist.
func Absent[T any](source string) Field[T] {
	return Field[T]{Source: source, ErrKind: ErrNotPresent}
}

func (f Field[T]) OK() bool             { return f.ErrKind == ErrNone }
func (f Field[T]) Absent() bool         { return f.ErrKind == ErrNotPresent }
func (f Field[T]) NeedsElevation() bool { return f.ErrKind == ErrNeedsElevation }

// Collected is false for a zero Field: a collector never wrote it, typically
// because the snapshot predates the field. Probes should SKIP, not judge.
func (f Field[T]) Collected() bool { return f.Source != "" || f.ErrKind != ErrNone }

var ErrNeedsElevationSentinel = errors.New("requires elevation")

// Env is everything a probe may look at. Collectors fill disjoint sub-structs
// concurrently; nothing reads Env until collection is complete.
type Env struct {
	Schema      string    `json:"schema"`
	Tool        string    `json:"tool"` // "wsldoctor 0.1.0"
	CollectedAt time.Time `json:"collected_at"`
	Elevated    bool      `json:"elevated"`
	VMWakeOK    bool      `json:"vm_wake_allowed"`
	UserProfile string    `json:"user_profile,omitempty"` // for redaction; scrubbed in --report
	Hostname    string    `json:"hostname,omitempty"`

	Host     Host            `json:"host"`
	Runtime  Runtime         `json:"runtime"`
	Distros  Field[[]Distro] `json:"distros"`
	Config   Config          `json:"config"`
	Defender Defender        `json:"defender"`
	Events   Events          `json:"events"`
	Procs    Procs           `json:"procs"`
	Plugins  Field[[]Plugin] `json:"plugins"`

	// Collectors records how long each collector took and whether it errored,
	// so a slow or broken machine is visible in the snapshot itself.
	Collectors map[string]CollectorStat `json:"collectors,omitempty"`
}

type CollectorStat struct {
	DurationMS int64  `json:"duration_ms"`
	Err        string `json:"err,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
}

type OSBuild struct {
	Major          int    `json:"major"`
	Minor          int    `json:"minor"`
	Build          int    `json:"build"`
	UBR            int    `json:"ubr"`
	ProductName    string `json:"product_name,omitempty"`
	DisplayVersion string `json:"display_version,omitempty"` // "22H2"
	EditionID      string `json:"edition_id,omitempty"`
}

// IsWindows11 is true for builds 22000 and later.
func (b OSBuild) IsWindows11() bool { return b.Build >= 22000 }

type Service struct {
	Exists    bool   `json:"exists"`
	State     string `json:"state,omitempty"`      // Running, Stopped, StartPending...
	StartType string `json:"start_type,omitempty"` // Auto, Manual, Disabled
	Display   string `json:"display,omitempty"`
}

type Host struct {
	Arch              string                    `json:"arch"` // amd64, arm64
	OS                Field[OSBuild]            `json:"os"`
	HypervisorPresent Field[bool]               `json:"hypervisor_present"`
	TotalMemoryBytes  Field[uint64]             `json:"total_memory_bytes"`
	Features          Field[map[string]int]     `json:"features"` // Win32_OptionalFeature InstallState: 1 enabled, 2 disabled, 3 absent
	Services          Field[map[string]Service] `json:"services"`
	PendingReboot     Field[bool]               `json:"pending_reboot"`
	IPv6Disabled      Field[uint32]             `json:"ipv6_disabled_components"` // Tcpip6 DisabledComponents, 0 if unset
}

// Feature install states as reported by Win32_OptionalFeature. FeatureUnknown
// means the query did not return that feature at all.
const (
	FeatureUnknown  = 0
	FeatureEnabled  = 1
	FeatureDisabled = 2
	FeatureAbsent   = 3
)

// Feature returns the install state of an optional feature (case-insensitive),
// or FeatureUnknown when features were not collected or the name is missing.
func (h Host) Feature(name string) int {
	if !h.Features.OK() {
		return FeatureUnknown
	}
	for k, v := range h.Features.Value {
		if equalFold(k, name) {
			return v
		}
	}
	return FeatureUnknown
}

type Runtime struct {
	// Version of the Store/MSI runtime from wslservice.exe, e.g. "2.7.13.0". Absent if only inbox WSL exists.
	Version         Field[string] `json:"version"`
	InstallLocation Field[string] `json:"install_location"`
	AppxFullName    Field[string] `json:"appx_full_name"`
	MSIVersion      Field[string] `json:"msi_version"`
	InboxWslVersion Field[string] `json:"inbox_wsl_version"` // C:\Windows\System32\wsl.exe file version
	KernelVersion   Field[string] `json:"kernel_version_hklm"`
	NatNetwork      Field[string] `json:"nat_network"`
	// COMClassRegistered: HKCR\CLSID\{a9b7a1b9-0671-405c-95f1-e0612cb4ce7e} (CLSID_LxssUserSession,
	// the Store/MSI wslservice class) exists. Missing => REGDB_E_CLASSNOTREG (0x80040154).
	COMClassRegistered Field[bool] `json:"com_class_registered"`
	// LatestStable known to the tool (embedded data, or fetched with --online).
	LatestStable       Field[string] `json:"latest_stable"`
	LatestStableSource string        `json:"latest_stable_source,omitempty"`
}

// Distro mirrors one HKCU\...\Lxss\{guid} key plus VHDX facts.
type Distro struct {
	GUID        string         `json:"guid"`
	Name        string         `json:"name"`
	IsDefault   bool           `json:"is_default"`
	BasePath    string         `json:"base_path"`
	VhdFileName string         `json:"vhd_file_name,omitempty"`
	Version     int            `json:"version"` // 1 or 2
	State       int            `json:"state"`   // 1 normal, 3 installing, 4 uninstalling (Lxss)
	Flags       int            `json:"flags"`
	DefaultUid  int            `json:"default_uid"`
	RunOOBE     int            `json:"run_oobe"`
	Modern      int            `json:"modern"`
	Flavor      string         `json:"flavor,omitempty"`
	OsVersion   string         `json:"os_version,omitempty"`
	ValueNames  []string       `json:"value_names,omitempty"`
	Vhd         Field[VhdInfo] `json:"vhd"`
	VolumeFree  Field[uint64]  `json:"volume_free_bytes"`
	Running     Field[bool]    `json:"running"` // ErrVMWakeRefused unless determinable passively
}

type VhdInfo struct {
	Path           string `json:"path"`
	FileSize       uint64 `json:"file_size"`
	MagicOK        bool   `json:"magic_ok"`
	HeaderOK       bool   `json:"header_ok"`
	VirtualSize    uint64 `json:"virtual_size"`
	BlockSize      uint32 `json:"block_size"`
	AllocatedBytes uint64 `json:"allocated_bytes"` // payload blocks fully present × block size
	Sparse         bool   `json:"sparse"`
	Compressed     bool   `json:"compressed"`
	Encrypted      bool   `json:"encrypted"`
	OwnerSID       string `json:"owner_sid,omitempty"`
	// Owner classifies OwnerSID relative to the collecting user:
	// "current_user", "administrators", "system", "other", or "" when unknown.
	Owner    string `json:"owner,omitempty"`
	ParseErr string `json:"parse_err,omitempty"`
}

// Well-known owner classifications for VhdInfo.Owner.
const (
	OwnerCurrentUser    = "current_user"
	OwnerAdministrators = "administrators"
	OwnerSystem         = "system"
	OwnerOther          = "other"
)

type Config struct {
	WslConfigPath string        `json:"wslconfig_path"`
	WslConfig     Field[string] `json:"wslconfig"` // raw text; Absent if the file does not exist
	// PathsExist records, for every path-typed value in .wslconfig (kernel,
	// kernelModules, swapFile, ...), whether the file exists. Keys are the
	// unescaped path, lower-cased.
	PathsExist map[string]bool `json:"paths_exist,omitempty"`
}

type Exclusions struct {
	Paths      []string `json:"paths"`
	Processes  []string `json:"processes"`
	Extensions []string `json:"extensions"`
}

type Defender struct {
	Present         Field[bool]       `json:"present"`
	RealtimeEnabled Field[bool]       `json:"realtime_enabled"`
	RunningMode     Field[string]     `json:"running_mode"`
	Exclusions      Field[Exclusions] `json:"exclusions"` // ErrNeedsElevation unelevated
}

type Event struct {
	Channel  string    `json:"channel"`
	Provider string    `json:"provider"`
	ID       int       `json:"id"`
	Level    int       `json:"level"`
	Time     time.Time `json:"time"`
	Message  string    `json:"message,omitempty"`
}

type ChannelInfo struct {
	Records uint64 `json:"records"`
}

type Events struct {
	Window   time.Duration                 `json:"window_ns"`
	Channels map[string]Field[ChannelInfo] `json:"channels"`
	// Recent WSL-relevant events within Window: WER/Application Error for wsl*.exe,
	// Kernel-Power 42/107, VmSwitch errors.
	Recent Field[[]Event] `json:"recent"`
}

type Procs struct {
	VmmemName         string        `json:"vmmem_name,omitempty"` // "vmmemWSL" or "vmmem"
	VmmemWorkingSet   Field[uint64] `json:"vmmem_working_set"`
	WslServiceRunning Field[bool]   `json:"wslservice_running"`
}

type Plugin struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Version string `json:"version,omitempty"`
}

// New returns an Env with schema and defaults set.
func New(tool string) *Env {
	return &Env{
		Schema:      Schema,
		Tool:        tool,
		CollectedAt: time.Now().UTC(),
		Events:      Events{Channels: map[string]Field[ChannelInfo]{}},
	}
}

// DistroList returns the distro slice or nil.
func (e *Env) DistroList() []Distro {
	if !e.Distros.OK() {
		return nil
	}
	return e.Distros.Value
}

// Service looks up a service by name (case-insensitive) if services were collected.
func (e *Env) Service(name string) (Service, bool) {
	if !e.Host.Services.OK() {
		return Service{}, false
	}
	for k, v := range e.Host.Services.Value {
		if equalFold(k, name) {
			return v, true
		}
	}
	return Service{}, false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
