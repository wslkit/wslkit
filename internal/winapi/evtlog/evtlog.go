//go:build windows

// Package evtlog reads the Windows event log through wevtapi.dll: channel
// record counts and XPath-filtered queries rendered to a small struct.
package evtlog

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	mod               = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtQuery      = mod.NewProc("EvtQuery")
	procEvtNext       = mod.NewProc("EvtNext")
	procEvtClose      = mod.NewProc("EvtClose")
	procEvtOpenLog    = mod.NewProc("EvtOpenLog")
	procEvtGetLogInfo = mod.NewProc("EvtGetLogInfo")
	procEvtRender     = mod.NewProc("EvtRender")
)

const (
	evtQueryChannelPath      = 0x1
	evtQueryReverseDirection = 0x200
	evtOpenChannelPath       = 0x1
	evtLogNumberOfLogRecords = 6
	evtRenderEventXml        = 1
)

var ErrAccessDenied = errors.New("access denied")

func wrap(e error) error {
	if errno, ok := e.(windows.Errno); ok && errno == windows.ERROR_ACCESS_DENIED {
		return ErrAccessDenied
	}
	return e
}

// RecordCount returns the number of records in a channel.
func RecordCount(channel string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(channel)
	if err != nil {
		return 0, err
	}
	h, _, e := procEvtOpenLog.Call(0, uintptr(unsafe.Pointer(p)), evtOpenChannelPath)
	if h == 0 {
		return 0, wrap(e)
	}
	defer procEvtClose.Call(h)
	var buf [24]byte // EVT_VARIANT
	var used uint32
	r, _, e := procEvtGetLogInfo.Call(h, evtLogNumberOfLogRecords, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)))
	if r == 0 {
		return 0, wrap(e)
	}
	return *(*uint64)(unsafe.Pointer(&buf[0])), nil
}

// Event is the subset of the rendered XML we keep.
type Event struct {
	Provider string
	ID       int
	Level    int
	Time     time.Time
	Message  string // concatenated EventData values; not the localised message
}

type xmlEvent struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID     string `xml:"EventID"`
		Level       string `xml:"Level"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	EventData struct {
		Data []struct {
			Name  string `xml:"Name,attr"`
			Value string `xml:",chardata"`
		} `xml:"Data"`
	} `xml:"EventData"`
}

// Query returns up to max events matching the XPath, newest first.
func Query(channel, xpath string, max int) ([]Event, error) {
	cp, err := windows.UTF16PtrFromString(channel)
	if err != nil {
		return nil, err
	}
	qp, err := windows.UTF16PtrFromString(xpath)
	if err != nil {
		return nil, err
	}
	h, _, e := procEvtQuery.Call(0, uintptr(unsafe.Pointer(cp)), uintptr(unsafe.Pointer(qp)), evtQueryChannelPath|evtQueryReverseDirection)
	if h == 0 {
		return nil, wrap(e)
	}
	defer procEvtClose.Call(h)

	var out []Event
	handles := make([]uintptr, 16)
	for len(out) < max {
		var returned uint32
		r, _, e := procEvtNext.Call(h, uintptr(len(handles)), uintptr(unsafe.Pointer(&handles[0])), 2000, 0, uintptr(unsafe.Pointer(&returned)))
		if r == 0 {
			if errno, ok := e.(windows.Errno); ok && (errno == windows.ERROR_NO_MORE_ITEMS || errno == windows.WAIT_TIMEOUT) {
				break
			}
			return out, wrap(e)
		}
		for i := 0; i < int(returned); i++ {
			ev, err := render(handles[i])
			procEvtClose.Call(handles[i])
			if err == nil && len(out) < max {
				out = append(out, ev)
			}
		}
		if returned == 0 {
			break
		}
	}
	return out, nil
}

func render(h uintptr) (Event, error) {
	var used, props uint32
	// First call to size the buffer.
	procEvtRender.Call(0, h, evtRenderEventXml, 0, 0, uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
	if used == 0 {
		return Event{}, fmt.Errorf("EvtRender sizing failed")
	}
	buf := make([]uint16, used/2+1)
	r, _, e := procEvtRender.Call(0, h, evtRenderEventXml, uintptr(used), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
	if r == 0 {
		return Event{}, e
	}
	s := windows.UTF16ToString(buf)
	var x xmlEvent
	if err := xml.Unmarshal([]byte(s), &x); err != nil {
		return Event{}, err
	}
	ev := Event{Provider: x.System.Provider.Name}
	ev.ID, _ = strconv.Atoi(strings.TrimSpace(x.System.EventID))
	ev.Level, _ = strconv.Atoi(strings.TrimSpace(x.System.Level))
	ev.Time, _ = time.Parse(time.RFC3339Nano, x.System.TimeCreated.SystemTime)
	var parts []string
	for _, d := range x.EventData.Data {
		v := strings.TrimSpace(d.Value)
		if v == "" {
			continue
		}
		if d.Name != "" {
			parts = append(parts, d.Name+"="+v)
		} else {
			parts = append(parts, v)
		}
	}
	ev.Message = strings.Join(parts, " ")
	return ev, nil
}

// SinceMillis builds the timediff clause for XPath filters.
func SinceMillis(d time.Duration) string {
	return fmt.Sprintf("TimeCreated[timediff(@SystemTime) <= %d]", d.Milliseconds())
}
