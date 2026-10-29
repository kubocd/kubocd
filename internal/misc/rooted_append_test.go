//nolint:goconst // Keep paths and contents beside their assertions.
package misc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootedAppendDocuments(t *testing.T) {
	d := rootedFixture(t)
	documents := []string{"---\nname: first\n\n", "---\nname: second\n\n", "---\nname: third\n\n"}
	want := ""
	for _, document := range documents {
		if err := d.AppendFile("outputs.yaml", []byte(document)); err != nil {
			t.Fatal(err)
		}
		want += document
		data, err := d.ReadFile("outputs.yaml")
		if err != nil || string(data) != want {
			t.Fatalf("appended documents = %q, %v; want %q", data, err, want)
		}
	}
}

func TestRootedAppendRefusesEscapingLink(t *testing.T) {
	d := rootedFixture(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.Root.Symlink(victim, "outputs.yaml"); err != nil {
		t.Fatal(err)
	}
	err := d.AppendFile("outputs.yaml", []byte("---\nname: first\n\n"))
	if err == nil || err.Error() != `path "./outputs.yaml" is not inside its trusted parent directory` {
		t.Errorf("unexpected link result: %v", err)
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "keep\n" {
		t.Fatalf("outside victim = %q, %v", data, err)
	}
}
