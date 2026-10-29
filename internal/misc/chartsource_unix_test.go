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

package misc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestChartReaderRejectsFIFOWithoutOpening(t *testing.T) {
	if os.Getenv("CHART_READER_PROBE") == "1" {
		dir := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
			t.Fatal(err)
		}
		source, err := OpenChartSource(dir, false)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = source.Close() }()
		f, err := source.OpenRegular("fifo")
		if f != nil {
			_ = f.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "not a regular file or directory") {
			t.Fatalf("special file was not refused: %v", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChartReaderRejectsFIFOWithoutOpening$")
	cmd.Env = append(os.Environ(), "CHART_READER_PROBE=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("special file was opened before rejection (probe timed out)")
	}
	if err != nil {
		t.Fatalf("special file probe failed: %s", output)
	}
}
