//go:build darwin || linux

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

//nolint:goconst // Fixture names are clearer beside their cases.
package cmn

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestChartSourceRefusesSpecialFilesBeforeOpen(t *testing.T) {
	if kind := os.Getenv("CHART_SPECIAL_PROBE"); kind != "" {
		chart := t.TempDir()
		if kind == "socket" {
			short, err := os.MkdirTemp("", "cs-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(short) })
			chart = short
		}
		sourceChart(t, chart)
		marker := sourceHelm(t, ":")
		name := filepath.Join(chart, "special")
		switch kind {
		case "socket":
			// Unix socket paths have a short platform-specific length limit.
			listener, err := net.Listen("unix", name)
			if errors.Is(err, syscall.EPERM) {
				t.Skip("sandbox does not permit Unix sockets")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
		case "generated":
			sourceHelm(t, "mkfifo \"$3/special\"")
		default:
			if kind == "metadata" {
				name = filepath.Join(chart, "Chart.yaml")
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			}
			if err := syscall.Mkfifo(name, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "link" {
				sourceLink(t, "special", filepath.Join(chart, "a-link"))
			}
		}
		_, err := getHelmCharArchiveFromLocal("", chart, "main", t.TempDir())
		if kind == "generated" {
			if err == nil || !strings.Contains(err.Error(), "generated chart path") {
				t.Fatalf("generated special file was not refused: %v", err)
			}
		} else {
			want := "not a regular file or directory"
			sourceRefused(t, err, marker, want)
		}
		return
	}
	for _, kind := range []string{"fifo", "metadata", "link", "socket", "generated"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestChartSourceRefusesSpecialFilesBeforeOpen$")
			cmd.Env = append(os.Environ(), "CHART_SPECIAL_PROBE="+kind)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("special file was opened before rejection (probe timed out)")
			}
			if strings.Contains(string(output), "--- SKIP") {
				t.Skip(string(output))
			}
			if err != nil {
				t.Fatalf("special file probe failed: %s", output)
			}
		})
	}
}
