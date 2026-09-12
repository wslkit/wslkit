//go:build windows

// Package services queries the Service Control Manager with the minimum
// access rights so it works unelevated.
package services

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type Info struct {
	Exists    bool
	State     string
	StartType string
	Display   string
}

// Query returns one Info per name. Missing services have Exists=false.
func Query(names []string) (map[string]Info, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	m := &mgr.Mgr{Handle: h}
	defer m.Disconnect()
	out := make(map[string]Info, len(names))
	for _, name := range names {
		info := Info{}
		np, err := windows.UTF16PtrFromString(name)
		if err != nil {
			out[name] = info
			continue
		}
		sh, err := windows.OpenService(h, np, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
		if err != nil {
			out[name] = info // ERROR_SERVICE_DOES_NOT_EXIST or access denied: either way not usable
			continue
		}
		s := &mgr.Service{Name: name, Handle: sh}
		info.Exists = true
		if st, err := s.Query(); err == nil {
			info.State = stateName(st.State)
		}
		if cfg, err := s.Config(); err == nil {
			info.StartType = startName(cfg.StartType)
			info.Display = cfg.DisplayName
		}
		s.Close()
		out[name] = info
	}
	return out, nil
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "StartPending"
	case svc.StopPending:
		return "StopPending"
	case svc.Running:
		return "Running"
	case svc.ContinuePending:
		return "ContinuePending"
	case svc.PausePending:
		return "PausePending"
	case svc.Paused:
		return "Paused"
	}
	return "Unknown"
}

func startName(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "Auto"
	case mgr.StartManual:
		return "Manual"
	case mgr.StartDisabled:
		return "Disabled"
	case windows.SERVICE_BOOT_START:
		return "Boot"
	case windows.SERVICE_SYSTEM_START:
		return "System"
	}
	return "Unknown"
}
