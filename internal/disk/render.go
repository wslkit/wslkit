package disk

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// FormatSize renders a byte count the way a person reads it.
//
// It truncates rather than rounds, so a number never reads as larger than the
// thing it describes: one byte short of a gibibyte prints as 1023.9 MiB, not as
// 1.0 GiB. Units are binary, because that is what Windows reports.
func FormatSize(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := b
	var idx int
	// Step up while the next unit still leaves a whole part.
	for idx = 0; idx < len(units)-1 && value >= unit*unit; idx++ {
		value /= unit
	}
	// value is now in [unit, unit*unit) expressed in the unit below the
	// target, so tenths come from integer arithmetic with no float rounding.
	whole := value / unit
	tenths := (value % unit) * 10 / unit
	return fmt.Sprintf("%d.%d %s", whole, tenths, units[idx])
}

// sizeCell renders an optional size, or a dash when it could not be measured.
func sizeCell(v *uint64) string {
	if v == nil {
		return "-"
	}
	return FormatSize(*v)
}

// Table renders fixed-width columns: left-aligned, two spaces between, no
// padding after the last column so lines have no trailing whitespace. Widths
// are measured in code points rather than bytes, so a non-ASCII distribution
// name does not skew the columns.
type Table struct {
	Headers []string
	Rows    [][]string
}

func (t Table) String() string {
	if len(t.Headers) == 0 {
		return ""
	}
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if i < len(widths) {
				if n := utf8.RuneCountInString(cell); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	var b strings.Builder
	writeRow := func(cells []string) {
		last := len(cells) - 1
		for i, cell := range cells {
			b.WriteString(cell)
			if i != last {
				pad := widths[i] - utf8.RuneCountInString(cell) + 2
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		b.WriteByte('\n')
	}
	writeRow(t.Headers)
	for _, row := range t.Rows {
		writeRow(row)
	}
	return b.String()
}

// Row pairs a registration with what was measured about it.
type Row struct {
	Reg     Registration
	Info    Info
	Running *bool
}

// state renders the running column.
func (r Row) state() string {
	if r.Running == nil {
		return "-"
	}
	if *r.Running {
		return "running"
	}
	return "stopped"
}

// displayName marks the default distribution.
func (r Row) displayName() string {
	if r.Reg.IsDefault {
		return r.Reg.Name + " *"
	}
	return r.Reg.Name
}

// RenderList writes the human-readable listing.
func RenderList(w io.Writer, rows []Row) {
	t := Table{Headers: []string{"NAME", "VER", "STATE", "SIZE ON DISK", "GUEST USED", "RECLAIMABLE", "PATH"}}
	for _, r := range rows {
		path := r.Reg.VhdPath()
		if path == "" {
			path = "-"
		}
		t.Rows = append(t.Rows, []string{
			r.displayName(),
			fmt.Sprintf("%d", r.Reg.Version),
			r.state(),
			sizeCell(r.Info.SizeOnDisk),
			sizeCell(r.Info.GuestUsed),
			sizeCell(r.Info.Reclaimable()),
			path,
		})
	}
	fmt.Fprint(w, t.String())
}

// Details renders aligned key/value lines: the key, a colon, then padding to
// the widest key.
type Details struct {
	Keys   []string
	Values []string
}

func (d *Details) Add(key, value string) {
	d.Keys = append(d.Keys, key)
	d.Values = append(d.Values, value)
}

func (d Details) String() string {
	width := 0
	for _, k := range d.Keys {
		if n := utf8.RuneCountInString(k); n > width {
			width = n
		}
	}
	var b strings.Builder
	for i, k := range d.Keys {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(strings.Repeat(" ", width-utf8.RuneCountInString(k)+1))
		b.WriteString(d.Values[i])
		b.WriteByte('\n')
	}
	return b.String()
}

// yesNo renders a boolean the way the tables do.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// RenderInfo writes the detailed view of one distribution.
func RenderInfo(w io.Writer, r Row, registryKey string) {
	var d Details
	d.Add("name", r.Reg.Name)
	d.Add("guid", r.Reg.GUID)
	d.Add("registry key", registryKey)
	d.Add("wsl version", fmt.Sprintf("%d", r.Reg.Version))
	d.Add("default", yesNo(r.Reg.IsDefault))
	d.Add("state", r.state())
	d.Add("base path", r.Reg.BasePath)
	if r.Reg.VhdFileName == "" {
		d.Add("vhd file name", "- (absent; defaults to "+DefaultVhdName+")")
	} else {
		d.Add("vhd file name", r.Reg.VhdFileName)
	}
	if p := r.Reg.VhdPath(); p != "" {
		d.Add("disk path", p)
	}
	d.Add("modern layout", yesNo(r.Reg.Modern != 0))
	if r.Reg.Flavor != "" {
		d.Add("flavor", r.Reg.Flavor)
	}
	if r.Reg.OsVersion != "" {
		d.Add("os version", r.Reg.OsVersion)
	}
	d.Add("default uid", fmt.Sprintf("%d", r.Reg.DefaultUID))
	d.Add("flags", fmt.Sprintf("%d (%s)", r.Reg.Flags, DecodeFlags(r.Reg.Flags)))

	i := r.Info
	if i.VirtualSize != nil {
		d.Add("virtual size", FormatSize(*i.VirtualSize))
	}
	if i.FileSize != nil {
		d.Add("file size", FormatSize(*i.FileSize))
	}
	if i.SizeOnDisk != nil {
		d.Add("size on disk", FormatSize(*i.SizeOnDisk))
	}
	if i.Allocated != nil {
		d.Add("allocated", FormatSize(*i.Allocated))
	}
	if i.Sparse != nil {
		d.Add("sparse", yesNo(*i.Sparse))
	}
	if i.BlockSize != nil {
		d.Add("block size", FormatSize(uint64(*i.BlockSize)))
	}
	if i.SectorSize != nil {
		d.Add("sector size", fmt.Sprintf("%d", *i.SectorSize))
	}
	if i.GuestUsed != nil {
		d.Add("guest used", FormatSize(*i.GuestUsed))
	}
	if i.GuestFree != nil {
		d.Add("guest free", FormatSize(*i.GuestFree))
	}
	if rec := i.Reclaimable(); rec != nil {
		d.Add("reclaimable", FormatSize(*rec))
	}
	if i.ParentPath != "" {
		d.Add("parent disk", i.ParentPath)
	}
	fmt.Fprint(w, d.String())
	for _, n := range i.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

// ---------------------------------------------------------------- JSON

// JSON output is built from maps rather than structs on purpose. The encoder
// sorts map keys, which keeps the field order stable and independent of the
// order anyone happens to add fields in, and it makes "absent" the natural
// default: a field that was not measured is simply never set.

// ListJSON is the object printed per distribution by `disk list`.
func ListJSON(r Row) map[string]any {
	o := map[string]any{
		"name":      r.Reg.Name,
		"guid":      r.Reg.GUID,
		"version":   r.Reg.Version,
		"default":   r.Reg.IsDefault,
		"vhdx_path": r.Reg.VhdPath(),
	}
	if r.Reg.Flavor != "" {
		o["flavor"] = r.Reg.Flavor
	}
	if r.Reg.OsVersion != "" {
		o["os_version"] = r.Reg.OsVersion
	}
	i := r.Info
	putU64(o, "virtual_size", i.VirtualSize)
	putU64(o, "file_size", i.FileSize)
	putU64(o, "size_on_disk", i.SizeOnDisk)
	putU64(o, "allocated_bytes", i.Allocated)
	if i.Sparse != nil {
		o["sparse"] = *i.Sparse
	}
	putU64(o, "guest_used", i.GuestUsed)
	putU64(o, "guest_free", i.GuestFree)
	putU64(o, "reclaimable", i.Reclaimable())
	if len(i.Notes) > 0 {
		o["notes"] = i.Notes
	}
	return o
}

// InfoJSON is the object printed by `disk info`: the list object plus the
// registration detail that only matters when looking at one distribution.
func InfoJSON(r Row, registryKey string) map[string]any {
	o := ListJSON(r)
	o["registry_key"] = registryKey
	o["base_path"] = r.Reg.BasePath
	o["modern"] = r.Reg.Modern != 0
	o["default_uid"] = r.Reg.DefaultUID
	o["flags"] = r.Reg.Flags
	o["flags_decoded"] = DecodeFlags(r.Reg.Flags)
	if r.Reg.VhdFileName != "" {
		o["vhd_file_name"] = r.Reg.VhdFileName
	}
	if r.Running != nil {
		o["running"] = *r.Running
	}
	if r.Info.BlockSize != nil {
		o["block_size"] = *r.Info.BlockSize
	}
	if r.Info.SectorSize != nil {
		o["sector_size"] = *r.Info.SectorSize
	}
	if r.Info.ParentPath != "" {
		o["parent_path"] = r.Info.ParentPath
	}
	return o
}

func putU64(o map[string]any, key string, v *uint64) {
	if v != nil {
		o[key] = *v
	}
}

// WriteJSONLine writes one object followed by a newline. Commands that report
// many things emit one object per line rather than an array, so a consumer can
// process the stream as it arrives.
func WriteJSONLine(w io.Writer, o map[string]any) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}
