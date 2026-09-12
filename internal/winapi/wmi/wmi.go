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

// ExecMethod invokes a static WMI method (e.g. MSFT_MpPreference.Add) with the
// given input parameters. []string values become SAFEARRAYs of BSTR. It returns
// the method's ReturnValue (0 = success) and any other output properties.
func ExecMethod(ctx context.Context, namespace, class, method string, params map[string]interface{}) (int64, map[string]interface{}, error) {
	type res struct {
		rv  int64
		out map[string]interface{}
		err error
	}
	ch := make(chan res, 1)
	go func() {
		rv, out, err := execMethod(namespace, class, method, params)
		ch <- res{rv, out, err}
	}()
	select {
	case r := <-ch:
		return r.rv, r.out, r.err
	case <-ctx.Done():
		return -1, nil, ctx.Err()
	}
}

func execMethod(namespace, class, method string, params map[string]interface{}) (int64, map[string]interface{}, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		if oleErr, ok := err.(*ole.OleError); !ok || (oleErr.Code() != 0x00000001 && oleErr.Code() != 0x80010106) {
			return -1, nil, fmt.Errorf("CoInitializeEx: %w", err)
		}
	} else {
		defer ole.CoUninitialize()
	}
	unk, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return -1, nil, fmt.Errorf("SWbemLocator: %w", err)
	}
	defer unk.Release()
	loc, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return -1, nil, err
	}
	defer loc.Release()
	svcRaw, err := oleutil.CallMethod(loc, "ConnectServer", ".", namespace)
	if err != nil {
		return -1, nil, fmt.Errorf("ConnectServer(%s): %w", namespace, err)
	}
	svc := svcRaw.ToIDispatch()
	defer svc.Release()

	classRaw, err := oleutil.CallMethod(svc, "Get", class)
	if err != nil {
		return -1, nil, fmt.Errorf("Get(%s): %w", class, err)
	}
	classObj := classRaw.ToIDispatch()
	defer classObj.Release()
	methodsRaw, err := oleutil.GetProperty(classObj, "Methods_")
	if err != nil {
		return -1, nil, err
	}
	methods := methodsRaw.ToIDispatch()
	defer methods.Release()
	mRaw, err := oleutil.CallMethod(methods, "Item", method)
	if err != nil {
		return -1, nil, fmt.Errorf("method %s: %w", method, err)
	}
	m := mRaw.ToIDispatch()
	defer m.Release()
	inDefRaw, err := oleutil.GetProperty(m, "InParameters")
	if err != nil {
		return -1, nil, err
	}
	inDef := inDefRaw.ToIDispatch()
	defer inDef.Release()
	inRaw, err := oleutil.CallMethod(inDef, "SpawnInstance_")
	if err != nil {
		return -1, nil, err
	}
	in := inRaw.ToIDispatch()
	defer in.Release()
	propsRaw, err := oleutil.GetProperty(in, "Properties_")
	if err != nil {
		return -1, nil, err
	}
	props := propsRaw.ToIDispatch()
	defer props.Release()
	for name, val := range params {
		itemRaw, err := oleutil.CallMethod(props, "Item", name)
		if err != nil {
			return -1, nil, fmt.Errorf("parameter %s: %w", name, err)
		}
		item := itemRaw.ToIDispatch()
		if _, err := oleutil.PutProperty(item, "Value", val); err != nil {
			item.Release()
			return -1, nil, fmt.Errorf("set %s: %w", name, err)
		}
		item.Release()
	}
	outRaw, err := oleutil.CallMethod(svc, "ExecMethod", class, method, in)
	if err != nil {
		return -1, nil, fmt.Errorf("ExecMethod %s.%s: %w", class, method, err)
	}
	out := outRaw.ToIDispatch()
	defer out.Release()
	result := map[string]interface{}{}
	rv := int64(0)
	if v, err := oleutil.GetProperty(out, "ReturnValue"); err == nil {
		switch t := v.Value().(type) {
		case int32:
			rv = int64(t)
		case int64:
			rv = t
		case uint32:
			rv = int64(t)
		}
		result["ReturnValue"] = v.Value()
		v.Clear()
	}
	return rv, result, nil
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
