//go:build windows

// Package wmi runs WQL queries through late-bound COM (WbemScripting.SWbemLocator).
// It is used for exactly the facts that have no direct Win32 API: optional
// features, hypervisor presence, Defender preferences.
package wmi

import (
	"context"
	"fmt"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// Row is one WMI object's requested properties. Arrays come back as []interface{}.
type Row map[string]interface{}

// Query runs wql in namespace and returns the named properties of each row.
// It honours ctx by running on a dedicated OS thread and abandoning the result
// on deadline; the COM call itself cannot be interrupted.
func Query(ctx context.Context, namespace, wql string, props ...string) ([]Row, error) {
	type res struct {
		rows []Row
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		rows, err := query(namespace, wql, props)
		ch <- res{rows, err}
	}()
	select {
	case r := <-ch:
		return r.rows, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func query(namespace, wql string, props []string) ([]Row, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		if oleErr, ok := err.(*ole.OleError); !ok || (oleErr.Code() != 0x00000001 && oleErr.Code() != 0x80010106) {
			return nil, fmt.Errorf("CoInitializeEx: %w", err)
		}
	} else {
		defer ole.CoUninitialize()
	}
	unk, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return nil, fmt.Errorf("SWbemLocator: %w", err)
	}
	defer unk.Release()
	loc, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}
	defer loc.Release()
	svcRaw, err := oleutil.CallMethod(loc, "ConnectServer", ".", namespace)
	if err != nil {
		return nil, fmt.Errorf("ConnectServer(%s): %w", namespace, err)
	}
	svc := svcRaw.ToIDispatch()
	defer svc.Release()
	resRaw, err := oleutil.CallMethod(svc, "ExecQuery", wql)
	if err != nil {
		return nil, fmt.Errorf("ExecQuery: %w", err)
	}
	set := resRaw.ToIDispatch()
	defer set.Release()
	countV, err := oleutil.GetProperty(set, "Count")
	if err != nil {
		return nil, fmt.Errorf("result count: %w", err)
	}
	n := int(countV.Val)
	rows := make([]Row, 0, n)
	for i := 0; i < n; i++ {
		itemRaw, err := oleutil.CallMethod(set, "ItemIndex", i)
		if err != nil {
			return rows, fmt.Errorf("ItemIndex(%d): %w", i, err)
		}
		item := itemRaw.ToIDispatch()
		row := Row{}
		for _, p := range props {
			v, err := oleutil.GetProperty(item, p)
			if err != nil {
				continue
			}
			row[p] = variantValue(v)
			v.Clear()
		}
		item.Release()
		rows = append(rows, row)
	}
	return rows, nil
}

func variantValue(v *ole.VARIANT) interface{} {
	if v.VT&ole.VT_ARRAY != 0 {
		arr := v.ToArray()
		if arr == nil {
			return []interface{}{}
		}
		return arr.ToValueArray()
	}
	return v.Value()
}

// Strings converts an array property to []string; scalars become a one-element slice.
func Strings(v interface{}) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case string:
		return []string{t}
	default:
		return []string{fmt.Sprint(t)}
	}
}
