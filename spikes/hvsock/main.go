//go:build windows

// Spike: AF_HYPERV from an unelevated user process against the WSL VM.
//
//	hvsock connect <vmid> <port>   connect to a Linux AF_VSOCK listener, echo one line
//	hvsock listen  <vmid> <port>   listen on an unregistered template-GUID port; print what arrives
//
// Answers the open question for `wslkit sock`: does host-side listen need a
// GuestCommunicationServices registry entry (admin), or does the VSOCK template
// GUID work unregistered for both directions like WSL's own wslhost.exe?
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Println("usage: hvsock connect|listen <vmid> <port>")
		os.Exit(2)
	}
	var vm guid.GUID
	switch os.Args[2] {
	case "wildcard": // HV_GUID_WILDCARD: any VM
		vm = guid.GUID{}
	case "children": // HV_GUID_CHILDREN: any child partition
		vm, _ = guid.FromString("90db8b89-0d35-4f79-8ce9-49ea0ac8b7cd")
	default:
		var err error
		vm, err = guid.FromString(os.Args[2])
		if err != nil {
			panic(err)
		}
	}
	port, _ := strconv.Atoi(os.Args[3])
	addr := &winio.HvsockAddr{VMID: vm, ServiceID: winio.VsockServiceID(uint32(port))}
	switch os.Args[1] {
	case "connect":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := winio.Dial(ctx, addr)
		if err != nil {
			fmt.Println("connect FAILED:", err)
			os.Exit(1)
		}
		defer func() { _ = c.Close() }()
		fmt.Fprintln(c, "hello from windows user mode")
		line, err := bufio.NewReader(c).ReadString('\n')
		fmt.Printf("connect OK; reply=%q err=%v\n", line, err)
	case "listen":
		l, err := winio.ListenHvsock(addr)
		if err != nil {
			fmt.Println("listen FAILED:", err)
			os.Exit(1)
		}
		defer func() { _ = l.Close() }()
		fmt.Println("listen OK on", addr.String(), "- waiting 20s for a guest connection")
		done := make(chan struct{})
		go func() {
			c, err := l.Accept()
			if err != nil {
				fmt.Println("accept FAILED:", err)
				close(done)
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			fmt.Printf("accept OK; guest said %q\n", line)
			fmt.Fprintln(c, "hello from windows listener")
			_ = c.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			fmt.Println("no guest connection within 20s")
		}
	}
}
