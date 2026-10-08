package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
)

func validConfig(port int) string {
	return fmt.Sprintf(`
app:
  name: template
  version: 1.0.0
  env: development
server:
  mode: test
  port: %d
  timeout: 5s
  trusted_proxies: []
logger:
  level: INFO
`, port)
}

func writeConfig(t *testing.T, contents string) {
	t.Helper()

	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.MkdirAll("configs", 0o750); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	if err := os.WriteFile(filepath.Join("configs", "config.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func freePort(t *testing.T) (int, net.Listener) {
	t.Helper()

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", listener.Addr())
	}
	return addr.Port, listener
}

// configOnFreePort writes a valid config for a port nothing is listening on, and returns the port.
func configOnFreePort(t *testing.T) int {
	t.Helper()
	port, listener := freePort(t)
	_ = listener.Close()
	writeConfig(t, validConfig(port))
	return port
}

func swap[T any](t *testing.T, seam *T, replacement T) {
	t.Helper()

	orig := *seam
	*seam = replacement
	t.Cleanup(func() { *seam = orig })
}

func captureStderr(t *testing.T) func() string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = writer

	return func() string {
		os.Stderr = orig
		_ = writer.Close()
		out, _ := io.ReadAll(reader)
		_ = reader.Close()
		return string(out)
	}
}

func getHealth(t *testing.T, port int) *http.Response {
	t.Helper()

	for range 100 {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err == nil {
			return resp
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil
}

func wantRunError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("run = %v, want an error mentioning %q", err, contains)
	}
}

func waitFor(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after the context was cancelled")
		return nil
	}
}

func TestMainFunc(t *testing.T) {
	t.Run("positive: serves until interrupted and exits without calling exit", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.Process.Signal(os.Interrupt) is not supported on Windows")
		}
		port := configOnFreePort(t)
		code := -1
		swap(t, &exit, func(c int) { code = c })

		done := make(chan struct{})
		go func() { main(); close(done) }()
		_ = getHealth(t, port).Body.Close() // main has registered for signals once it serves

		self, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatalf("FindProcess: %v", err)
		}
		if err := self.Signal(os.Interrupt); err != nil {
			t.Fatalf("Signal: %v", err)
		}
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("main did not return after SIGINT")
		}
		if code != -1 {
			t.Errorf("exit(%d) called, want a clean return", code)
		}
	})

	t.Run("negative: a missing config exits 1 and names it", func(t *testing.T) {
		t.Chdir(t.TempDir())
		var code int
		swap(t, &exit, func(c int) { code = c })
		stderr := captureStderr(t)

		main()

		if out := stderr(); code != 1 || !strings.Contains(out, "config.yaml") {
			t.Errorf("exit = %d, stderr = %q; want 1 and the missing config named", code, out)
		}
	})

	t.Run("negative: an invalid config exits 1 and says why", func(t *testing.T) {
		writeConfig(t, "app:\n  name: template\n")
		var code int
		swap(t, &exit, func(c int) { code = c })
		stderr := captureStderr(t)

		main()

		if out := stderr(); code != 1 || !strings.Contains(out, "invalid config") {
			t.Errorf("exit = %d, stderr = %q; want 1 and the validation failure", code, out)
		}
	})
}

func TestRun(t *testing.T) {
	t.Run("positive: serves until the context is cancelled", func(t *testing.T) {
		port := configOnFreePort(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- run(ctx) }()

		resp := getHealth(t, port)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Healthy") {
			t.Errorf("GET /health = %d %q", resp.StatusCode, body)
		}

		cancel()
		if err := waitFor(t, done); err != nil {
			t.Errorf("run returned %v, want a clean shutdown", err)
		}
	})

	t.Run("negative: missing config", func(t *testing.T) {
		t.Chdir(t.TempDir())
		wantRunError(t, run(context.Background()), "config.yaml")
	})

	t.Run("negative: logger cannot be built", func(t *testing.T) {
		configOnFreePort(t)
		if err := os.WriteFile("logs", []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write logs: %v", err)
		}
		wantRunError(t, run(context.Background()), "logger")
	})

	t.Run("negative: router fails to build", func(t *testing.T) {
		configOnFreePort(t)
		swap(t, &newRouter, func(*config.Config, *container.Container) (*gin.Engine, error) {
			return nil, errors.New("router refused to build")
		})
		wantRunError(t, run(context.Background()), "router refused to build")
	})

	t.Run("negative: port already in use", func(t *testing.T) {
		port, listener := freePort(t)
		defer func() { _ = listener.Close() }()
		writeConfig(t, validConfig(port))
		wantRunError(t, run(context.Background()), "listen")
	})

	t.Run("negative: serving fails", func(t *testing.T) {
		configOnFreePort(t)
		swap(t, &listen, func(network, addr string) (net.Listener, error) {
			dead, err := net.Listen(network, addr)
			if err != nil {
				return nil, err
			}
			_ = dead.Close()
			return dead, nil
		})
		wantRunError(t, run(context.Background()), "serve")
	})

	t.Run("negative: dependencies fail to close", func(t *testing.T) {
		configOnFreePort(t)
		swap(t, &closeDeps, func(deps *container.Container) error {
			_ = deps.Close()
			return errors.New("pool still busy")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		wantRunError(t, run(ctx), "close dependencies: pool still busy")
	})

	t.Run("negative: shutdown overruns server.shutdown_timeout", func(t *testing.T) {
		port, listener := freePort(t)
		_ = listener.Close()
		const timeout = "  timeout: 5s\n"
		writeConfig(t, strings.Replace(validConfig(port), timeout, timeout+"  shutdown_timeout: 300ms\n", 1))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- run(ctx) }()
		_ = getHealth(t, port).Body.Close()

		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = conn.Close() }()
		time.Sleep(100 * time.Millisecond)

		cancel()
		wantRunError(t, waitFor(t, done), "shutdown server")
	})
}
