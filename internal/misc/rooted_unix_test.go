//go:build linux || darwin || freebsd

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

//nolint:goconst // Keep fixture values beside their assertions.
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

func TestRootedCleanupRefusesFIFOMarker(t *testing.T) {
	if os.Getenv("KUBOCD_MARKER_FIFO_PROBE") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRootedCleanupRefusesFIFOMarker$")
		command.Env = append(os.Environ(), "KUBOCD_MARKER_FIFO_PROBE=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("FIFO marker probe failed: %v: %s", err, output)
		}
		return
	}

	root := rootedFixture(t)
	if err := root.MkdirAll("child", 0755); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("child/sentinel", []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root.Path(), "child", ".transient"), 0600); err != nil {
		t.Fatal(err)
	}
	err := SafeEnsureEmptyAt(root, "child")
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("FIFO marker was accepted: %v", err)
	}
	data, err := root.ReadFile("child/sentinel")
	if err != nil || string(data) != "keep" {
		t.Fatal("FIFO marker authorized cleanup")
	}
}
