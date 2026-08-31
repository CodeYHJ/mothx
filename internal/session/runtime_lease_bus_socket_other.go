//go:build !unix && !windows

package session

import "syscall"

func runtimeLeaseBusListenerControl(_ string, _ string, _ syscall.RawConn) error { return nil }

func runtimeLeaseBusSenderControl(_ string, _ string, _ syscall.RawConn) error { return nil }
