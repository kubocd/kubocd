//go:build linux || darwin || freebsd

package cmd

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestRenderOutputAppendRefusesFIFO(t *testing.T) {
	f := newRenderOutputFixture(t)
	cwd := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(cwd, "outputConnections.yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := runRenderOutputCommand(t, f, cwd, "")
	assertRenderOutputFailure(t, output, err,
		`ERROR: output[0]: could not append connection: path "outputConnections.yaml" is not a regular file`)
}
