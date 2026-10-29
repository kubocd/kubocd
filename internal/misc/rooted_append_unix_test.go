//go:build linux || darwin || freebsd

//nolint:goconst // Keep paths beside their assertions.
package misc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRootedAppendPermissions(t *testing.T) {
	d := rootedFixture(t)
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)
	if err := d.AppendFile("outputs.yaml", []byte("---\nname: first\n\n")); err != nil {
		t.Fatal(err)
	}
	info, err := d.Root.Stat("outputs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0644); got != want {
		t.Fatalf("created mode = %04o; want %04o", got, want)
	}
}

func TestRootedAppendRefusesFIFO(t *testing.T) {
	if os.Getenv("KUBOCD_APPEND_FIFO_PROBE") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRootedAppendRefusesFIFO$")
		child.Env = append(os.Environ(), "KUBOCD_APPEND_FIFO_PROBE=1")
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("FIFO append probe: %v, %v\n%s", err, ctx.Err(), output)
		}
		return
	}
	d := rootedFixture(t)
	if err := syscall.Mkfifo(filepath.Join(d.Path(), "outputs.yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	err := d.AppendFile("outputs.yaml", []byte("---\nname: first\n\n"))
	if err == nil || err.Error() != `path "outputs.yaml" is not a regular file` {
		t.Fatalf("unexpected FIFO result: %v", err)
	}
}
