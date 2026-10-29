/*
Copyright 2026 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

//nolint:goconst // Keep request bytes and flag values beside the behavior they exercise.
package cmd

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
)

const chartTestIndex = "apiVersion: v1\nentries: {}\n"

// TestChartServerProcess is the child process of the chart server tests. It
// sets the timeout flags by name, and runs the server without a flag it lacks.
func TestChartServerProcess(t *testing.T) {
	root := os.Getenv("KUBOCD_CHART_TEST_ROOT")
	if root == "" {
		return
	}
	for name, value := range map[string]string{
		"helmRepoReadHeaderTimeout": os.Getenv("KUBOCD_CHART_TEST_HEADER"),
		"helmRepoIdleTimeout":       os.Getenv("KUBOCD_CHART_TEST_IDLE"),
	} {
		if controllerCmd.PersistentFlags().Lookup(name) == nil {
			t.Logf("server has no %s flag", name)
			continue
		}
		if err := controllerCmd.PersistentFlags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	logger := funcr.New(func(prefix, args string) { fmt.Println(prefix, args) }, funcr.Options{})
	// startFileServer owns its listener and does not expose the address of :0.
	// Retry the short close/rebind race rather than changing its signature.
	for range 3 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "address"), []byte(address), 0600); err != nil {
			t.Fatal(err)
		}
		startFileServer(root, address, logger)
	}
	t.Fatal("chart server could not bind after three attempts")
}

func chartServerProcess(t *testing.T, header, idle string) (string, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{"index.yaml": chartTestIndex, "ready": root} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChartServerProcess$", "-test.v")
	command.Env = append(os.Environ(), "KUBOCD_CHART_TEST_ROOT="+root,
		"KUBOCD_CHART_TEST_HEADER="+header, "KUBOCD_CHART_TEST_IDLE="+idle)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("chart server subprocess: %v\n%s", exitErr, output.String())
		}
	})
	transport := &http.Transport{DisableKeepAlives: true, Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 100 * time.Millisecond}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		select {
		case <-done:
			t.Fatalf("chart server exited: %v\n%s", exitErr, output.String())
		default:
		}
		address, err := os.ReadFile(filepath.Join(root, "address"))
		if err != nil {
			continue
		}
		response, err := client.Get("http://" + string(address) + "/ready")
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr == nil && response.StatusCode == http.StatusOK && string(body) == root {
			return string(address), root
		}
	}
	t.Fatal("chart server did not become ready within 5s")
	return "", ""
}

func chartTestConn(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func chartWrite(t *testing.T, conn net.Conn, request string) {
	t.Helper()
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
}

func chartIndexResponse(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != chartTestIndex || response.Close {
		t.Fatalf("index response: status=%d body=%q close=%t error=%v", response.StatusCode, body, response.Close, err)
	}
}

func chartConnectionCut(t *testing.T, address string, headers bool) {
	t.Helper()
	conn := chartTestConn(t, address)
	reader := bufio.NewReader(conn)
	if headers {
		chartWrite(t, conn, "GET /index.yaml HTTP/1.1\r\nHost: chart.test\r\nX-Slow: ")
		stop, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					if _, err := io.WriteString(conn, "x"); err != nil {
						return
					}
				}
			}
		}()
		defer func() {
			close(stop)
			_ = conn.Close()
			<-stopped
		}()
	} else {
		chartWrite(t, conn, "GET /index.yaml HTTP/1.1\r\nHost: chart.test\r\n\r\n")
		chartIndexResponse(t, reader)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := reader.ReadByte()
	if err == nil {
		t.Fatal("unexpected response before connection closed")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("server retained connection beyond 2s (slow headers=%t): %v", headers, err)
	}
	// A child crash must not count as enforcement of a connection timeout.
	healthy := chartTestConn(t, address)
	chartWrite(t, healthy, "GET /index.yaml HTTP/1.1\r\nHost: chart.test\r\n\r\n")
	chartIndexResponse(t, bufio.NewReader(healthy))
}

func TestChartServerTimeoutFlags(t *testing.T) {
	for name, want := range map[string]string{"helmRepoReadHeaderTimeout": "5s", "helmRepoIdleTimeout": "1m0s"} {
		flag := controllerCmd.PersistentFlags().Lookup(name)
		if flag == nil {
			t.Errorf("missing controller flag %s", name)
			continue
		}
		if flag.DefValue != want || flag.Value.Type() != "duration" || !strings.Contains(flag.Usage, "0 disables") {
			t.Errorf("%s: default=%s type=%s help=%s", name, flag.DefValue, flag.Value.Type(), flag.Usage)
		}
		old, changed := flag.Value.String(), flag.Changed
		t.Cleanup(func() {
			if err := flag.Value.Set(old); err != nil {
				t.Error(err)
			}
			flag.Changed = changed
		})
		for _, value := range []string{"0", "200ms"} {
			if err := controllerCmd.PersistentFlags().Set(name, value); err != nil {
				t.Fatal(err)
			}
			got, err := controllerCmd.PersistentFlags().GetDuration(name)
			wantDuration, parseErr := time.ParseDuration(value)
			if err != nil || parseErr != nil || got != wantDuration {
				t.Fatalf("%s=%s: got %s (%v), parse %v", name, value, got, err, parseErr)
			}
		}
	}
}

func TestChartServerCutsSlowHeaders(t *testing.T) {
	address, _ := chartServerProcess(t, "200ms", "0")
	chartConnectionCut(t, address, true)
}

func TestChartServerCutsIdleKeepAlive(t *testing.T) {
	address, _ := chartServerProcess(t, "0", "200ms")
	chartConnectionCut(t, address, false)
}

func TestChartServerZeroDisablesTimeouts(t *testing.T) {
	for _, durations := range [][2]string{{"0", "200ms"}, {"200ms", "0"}, {"0", "0"}} {
		t.Run(strings.Join(durations[:], "/"), func(t *testing.T) {
			address, _ := chartServerProcess(t, durations[0], durations[1])
			for i, duration := range durations {
				if duration != "0" {
					chartConnectionCut(t, address, i == 0)
					continue
				}
				conn := chartTestConn(t, address)
				reader := bufio.NewReader(conn)
				request := "GET /index.yaml HTTP/1.1\r\nHost: chart.test\r\n\r\n"
				if i == 0 {
					chartWrite(t, conn, strings.TrimSuffix(request, "\r\n"))
					time.Sleep(800 * time.Millisecond)
					chartWrite(t, conn, "\r\n")
				} else {
					chartWrite(t, conn, request)
					chartIndexResponse(t, reader)
					time.Sleep(800 * time.Millisecond)
					chartWrite(t, conn, request)
				}
				chartIndexResponse(t, reader)
				_ = conn.Close()
			}
		})
	}
}

func chartArchive(t *testing.T, root string, payloadSize int) []byte {
	t.Helper()
	payload := make([]byte, payloadSize)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range map[string][]byte{
		"demo/Chart.yaml": []byte("apiVersion: v2\nname: demo\nversion: 1.0.0\n"), "demo/payload.bin": payload,
	} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo-1.0.0.tgz"), buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestChartServerPreservesFileResponses(t *testing.T) {
	address, root := chartServerProcess(t, "200ms", "200ms")
	chart := chartArchive(t, root, 128)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, test := range []struct {
		method, path, byteRange string
		status                  int
		body                    []byte
	}{
		{http.MethodGet, "/index.yaml", "", 200, []byte(chartTestIndex)},
		{http.MethodGet, "/demo-1.0.0.tgz", "", 200, chart},
		{http.MethodHead, "/demo-1.0.0.tgz", "", 200, nil},
		{http.MethodGet, "/demo-1.0.0.tgz", "bytes=2-5", 206, chart[2:6]},
	} {
		t.Run(test.method+test.path+test.byteRange, func(t *testing.T) {
			request, err := http.NewRequest(test.method, "http://"+address+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.byteRange != "" {
				request.Header.Set("Range", test.byteRange)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != test.status || !bytes.Equal(body, test.body) {
				t.Fatalf("response: status=%d bytes=%d error=%v; want %d and %d bytes",
					response.StatusCode, len(body), err, test.status, len(test.body))
			}
			if test.method == http.MethodHead && response.ContentLength != int64(len(chart)) {
				t.Errorf("HEAD content length=%d, want %d", response.ContentLength, len(chart))
			}
			if test.byteRange != "" && response.Header.Get("Content-Range") != fmt.Sprintf("bytes 2-5/%d", len(chart)) {
				t.Errorf("Content-Range=%q", response.Header.Get("Content-Range"))
			}
		})
	}
}

func TestChartServerCompletesSlowDownload(t *testing.T) {
	address, root := chartServerProcess(t, "200ms", "200ms")
	chart := chartArchive(t, root, 8<<20)
	conn := chartTestConn(t, address)
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatal("expected a TCP connection")
	}
	if err := tcp.SetReadBuffer(32 << 10); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	chartWrite(t, conn, "GET /demo-1.0.0.tgz HTTP/1.1\r\nHost: chart.test\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	// The receive window and large response keep the server writing past both
	// timeouts, so a write deadline would truncate this response.
	time.Sleep(time.Second)
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || len(body) != len(chart) ||
		sha256.Sum256(body) != sha256.Sum256(chart) {
		t.Fatalf("slow download: status=%d bytes=%d/%d error=%v", response.StatusCode, len(body), len(chart), err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("download lasted only %s", elapsed)
	} else {
		t.Logf("downloaded %d bytes in %s with both timeouts set to 200ms", len(body), elapsed)
	}
}
