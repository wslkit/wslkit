// Package wslver parses and compares WSL runtime versions such as "2.7.13.0".
package wslver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is up to four numeric components. Missing trailing components are zero.
type Version [4]int

// Parse accepts "2.7.13", "2.7.13.0", "v2.7.13", and tolerates surrounding whitespace.
func Parse(s string) (Version, error) {
	var v Version
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if s == "" {
		return v, fmt.Errorf("wslver: empty version")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return v, fmt.Errorf("wslver: too many components in %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("wslver: bad component %q in %q", p, s)
		}
		v[i] = n
	}
	return v, nil
}

// MustParse panics on error; for constants in tests and data loading.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Compare returns -1, 0, 1.
func Compare(a, b Version) int {
	for i := 0; i < 4; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

func (v Version) Less(o Version) bool    { return Compare(v, o) < 0 }
func (v Version) AtLeast(o Version) bool { return Compare(v, o) >= 0 }

// String renders three components, plus the fourth only when non-zero.
func (v Version) String() string {
	if v[3] != 0 {
		return fmt.Sprintf("%d.%d.%d.%d", v[0], v[1], v[2], v[3])
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// IsZero reports an unset version.
func (v Version) IsZero() bool { return v == Version{} }
