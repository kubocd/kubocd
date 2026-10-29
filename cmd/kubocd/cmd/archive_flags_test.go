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

//nolint:goconst // Keep archive fixtures and boundary diagnostics beside their assertions.
package cmd

import (
	"context"
	"encoding/json"
	"kubocd/internal/testarchive"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestCLIArchiveCommandProcess(t *testing.T) {
	encoded := os.Getenv("KUBOCD_ARCHIVE_TEST_ARGS")
	if encoded == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		t.Fatal(err)
	}
	rootCmd.SetArgs(args)
	Execute()
}

func archiveCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIArchiveCommandProcess$")
	command.Env = append(os.Environ(), "KUBOCD_ARCHIVE_TEST_ARGS="+string(encoded))
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("archive command timed out: %s", output)
	}
	return string(output), err
}

func TestCLIArchiveLimitFlags(t *testing.T) {
	manifest, base := testarchive.LocalChart(t, (1<<20)+1)
	args := []string{"dump", "package", manifest, "--charts",
		"-w", filepath.Join(base, "work"), "-o", filepath.Join(base, "out")}
	t.Run("default refusal", func(t *testing.T) {
		output, err := archiveCommand(t, args...)
		if err == nil || !strings.Contains(output, "archive metadata exceeds 1048576 bytes") {
			t.Fatalf("command accepted oversized metadata: %v\n%s", err, output)
		}
	})
	manifest, base = testarchive.LocalChart(t, 1025)
	args = []string{"dump", "package", manifest, "--charts",
		"-w", filepath.Join(base, "work"), "-o", filepath.Join(base, "out"),
		"--maxArchiveMetadataSize=1024"}
	t.Run("configured refusal", func(t *testing.T) {
		output, err := archiveCommand(t, args...)
		if err == nil || !strings.Contains(output, "archive metadata exceeds 1024 bytes") {
			t.Fatalf("command ignored configured metadata limit: %v\n%s", err, output)
		}
	})
	for _, flag := range []string{"--maxArchiveMetadataSize=1025", "--disableArchiveLimits"} {
		t.Run(flag, func(t *testing.T) {
			output, err := archiveCommand(t, append(append([]string(nil), args...), flag)...)
			if err != nil {
				t.Fatalf("archive option did not reach dump package: %v\n%s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(base, "out", "demo", "charts", "main", "main", "Chart.yaml"))
			if err != nil || len(data) != 1025 {
				t.Fatalf("healthy raised-limit bytes changed: %d, %v", len(data), err)
			}
		})
	}
	for _, command := range [][]string{{"package", "missing"}, {"render", "missing"}, {"dump", "package", "missing"},
		{"dump", "helmRepository", "https://invalid.invalid"}, {"dump", "oci", "oci://invalid.invalid/demo:1"}} {
		for _, flag := range []string{"maxArchiveStreamSize", "maxArchiveContentSize",
			"maxArchiveMetadataSize", "maxArchiveListingSize", "maxArchiveEntries"} {
			t.Run(strings.Join(command, " ")+flag, func(t *testing.T) {
				for _, value := range []string{"0", "-1", "9223372036854775807"} {
					args := append(append([]string(nil), command...), "--"+flag+"="+value, "--disableArchiveLimits")
					output, err := archiveCommand(t, args...)
					if err == nil || !strings.Contains(output, "must be between 1 and") {
						t.Fatalf("invalid archive limit was not refused: %v\n%s", err, output)
					}
				}
			})
		}
	}
}

func TestCLIArchiveFlagScopeAndHelp(t *testing.T) {
	for _, cmd := range []*cobra.Command{packageCmd, renderCmd, dumpPackageCmd, dumpHrCmd, dumpOciCmd} {
		for flag, want := range map[string]string{"maxArchiveStreamSize": "47087616", "maxArchiveContentSize": "41943040",
			"maxArchiveEntries": "1000", "maxArchiveMetadataSize": "1048576", "maxArchiveListingSize": "1048576",
			"disableArchiveLimits": strconv.FormatBool(false)} {
			f := cmd.Flags().Lookup(flag)
			if f == nil || f.DefValue != want {
				t.Fatalf("%s does not expose %s default %s", cmd.Name(), flag, want)
			}
		}
		for _, alias := range cmd.Aliases {
			if found, _, err := cmd.Parent().Find([]string{alias}); err != nil || found != cmd {
				t.Fatalf("alias %s does not reach limits", alias)
			}
		}
		if !strings.Contains(cmd.Long, "per traversal") || !strings.Contains(cmd.Long, "100 MiB") {
			t.Fatalf("%s help omits budget scope", cmd.Name())
		}
	}
	for _, cmd := range []*cobra.Command{rootCmd, controllerCmd, dumpCmd, dumpContextCmd, dumpConfigCmd} {
		if cmd.Flags().Lookup("maxArchiveEntries") != nil || cmd.PersistentFlags().Lookup("maxArchiveEntries") != nil {
			t.Fatalf("archive flags leaked to %s", cmd.Name())
		}
	}
}
