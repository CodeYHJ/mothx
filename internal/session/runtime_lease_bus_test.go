package session

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRuntimeLeaseBusSubprocessHelper(t *testing.T) {
	if os.Getenv("MOTHX_RUNTIME_BUS_HELPER") != "1" {
		return
	}
	received := make(chan RuntimeLeaseNotification, 1)
	stop := SubscribeRuntimeLeaseNotifications(func(notification RuntimeLeaseNotification) {
		received <- notification
	})
	defer stop()
	if !waitForRuntimeLeaseBusListener(5 * time.Second) {
		t.Fatal("helper UDP listener did not start")
	}
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	select {
	case notification := <-received:
		_, _ = fmt.Fprintf(os.Stdout, "received %s %s %s\n", notification.Type, notification.Origin, notification.OriginInstanceID)
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not receive runtime bus broadcast")
	}
}

func TestRuntimeLeaseBusBroadcastReachesAnotherProcess(t *testing.T) {
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOTHX_RUNTIME_BUS_PORT", strconv.Itoa(port))

	stop := SubscribeRuntimeLeaseNotifications(func(RuntimeLeaseNotification) {})
	defer stop()
	if !waitForRuntimeLeaseBusListener(5 * time.Second) {
		t.Fatal("parent UDP listener did not start")
	}

	helpers := []*runtimeLeaseBusHelper{startRuntimeLeaseBusHelper(t), startRuntimeLeaseBusHelper(t)}
	for _, helper := range helpers {
		defer helper.stop()
		if line := helper.nextLine(t); line != "ready" {
			t.Fatalf("helper startup output = %q, want ready", line)
		}
	}

	publishRuntimeLeaseNotification(RuntimeLeaseNotification{
		Type:      "state_changed",
		SessionID: "broadcast-session",
		Origin:    "cli",
	})
	for _, helper := range helpers {
		line := helper.nextLine(t)
		if !strings.HasPrefix(line, "received state_changed cli ") {
			t.Fatalf("helper receipt output = %q, want state_changed from cli", line)
		}
	}
}

func TestRuntimeLeaseBusLogsUseDedicatedSubscribers(t *testing.T) {
	logs := make(chan string, 1)
	stop := SubscribeRuntimeLeaseLogs(func(message string) { logs <- message })
	defer stop()

	runtimeLeaseBusLogf("[udp] sent type=%s", "state_changed")
	select {
	case message := <-logs:
		if message != "[udp] sent type=state_changed" {
			t.Fatalf("UDP log = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UDP diagnostic subscriber")
	}
}

type runtimeLeaseBusHelper struct {
	cmd   *exec.Cmd
	lines <-chan string
}

func startRuntimeLeaseBusHelper(t *testing.T) *runtimeLeaseBusHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeLeaseBusSubprocessHelper")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "MOTHX_RUNTIME_BUS_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- strings.TrimSpace(scanner.Text())
		}
		close(lines)
	}()
	return &runtimeLeaseBusHelper{cmd: cmd, lines: lines}
}

func (helper *runtimeLeaseBusHelper) nextLine(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-helper.lines:
		if !ok {
			t.Fatal("runtime bus helper exited before responding")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runtime bus helper")
		return ""
	}
}

func (helper *runtimeLeaseBusHelper) stop() {
	if helper == nil || helper.cmd == nil || helper.cmd.Process == nil {
		return
	}
	_ = helper.cmd.Process.Kill()
	_ = helper.cmd.Wait()
}

func waitForRuntimeLeaseBusListener(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runtimeLeaseBus.Lock()
		listening := runtimeLeaseBus.listening
		runtimeLeaseBus.Unlock()
		if listening {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
