package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func TestMissingTokenFileExits(t *testing.T) {
	cmd := exec.Command("go", "run", ".")
	cmd.Env = append(os.Environ(), "AGENT_TOKEN_FILE="+filepath.Join(t.TempDir(), "none"))
	if err := cmd.Run(); err == nil {
		t.Fatal("expected failure")
	}
}

// TestRSSUnderLoad builds the binary, drives it with requests and logs its resident memory.
func TestRSSUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	tokFile := filepath.Join(dir, "token")
	_ = os.WriteFile(tokFile, []byte("tok\n"), 0o600)
	ide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 4096)) }))
	defer ide.Close()
	proxy, control, local := freeAddr(t), freeAddr(t), freeAddr(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "AGENT_TOKEN_FILE="+tokFile, "AGENT_IDE_PORT="+ide.URL[strings.LastIndex(ide.URL, ":")+1:],
		"AGENT_PROXY_ADDR="+proxy, "AGENT_CONTROL_ADDR="+control, "AGENT_LOCAL_ADDR="+local, "GOMEMLIMIT=")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", control); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for i := 0; i < 5000; i++ {
		req, _ := http.NewRequest("GET", "http://"+proxy+"/f", nil)
		req.Header.Set("X-EduIDE-Instance-Token", "tok")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
	out, _ := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(cmd.Process.Pid)).Output()
	kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	t.Logf("RSS after 5000 proxied requests: %d KiB (includes the file-backed binary)", kb)
	if status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid)); err == nil {
		for _, l := range strings.Split(string(status), "\n") {
			if v, ok := strings.CutPrefix(l, "RssAnon:"); ok {
				anon, _ := strconv.Atoi(strings.Fields(v)[0])
				t.Logf("RssAnon: %d KiB", anon)
				if anon > 10*1024 {
					t.Errorf("anonymous RSS %d KiB exceeds 10 MiB", anon)
				}
			}
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
}
