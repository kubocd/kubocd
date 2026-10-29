//nolint:goconst // Keep output fixtures beside their assertions.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func newRenderOutputFixture(t *testing.T) ociCommandFixture {
	t.Helper()
	f := newOCICommandFixture(t)
	release, err := os.ReadFile(f.release)
	if err != nil {
		t.Fatal(err)
	}
	release = []byte(strings.ReplaceAll(string(release), "insecure: true", "insecure: false"))
	if err := os.WriteFile(f.release, release, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(f.local)
	if err != nil {
		t.Fatal(err)
	}
	manifest = append(manifest, []byte(`outputs: |
  - name: first
    kind: Connection
    contract: endpoint
    values:
      address: first.example
  - name: shared
    kind: ClusterConnection
    contract: endpoint
    values:
      address: shared.example
  - name: second
    kind: Connection
    contract: endpoint
    values:
      address: second.example
`)...)
	if err := os.WriteFile(f.local, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func runRenderOutputCommand(t *testing.T, f ociCommandFixture, cwd, output string) (string, error) {
	t.Helper()
	args := []string{"render", f.release, f.local, "--output=" + output, "--workDir=" + t.TempDir()}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChartSourceCommandProcess$")
	child.Dir = cwd
	child.Env = append(os.Environ(), "KUBOCD_SOURCE_TEST_ARGS="+string(encoded), "KUBECONFIG="+f.config,
		"KUBERNETES_SERVICE_HOST=", "PATH="+filepath.Join(f.base, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	data, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("render did not finish within 10s")
	}
	return string(data), err
}

func TestRenderOutputAppendRefusesEscapingLinks(t *testing.T) {
	for _, tc := range []struct {
		file  string
		index int
	}{
		{"outputConnections.yaml", 0},
		{"outputClusterConnections.yaml", 1},
	} {
		t.Run(tc.file, func(t *testing.T) {
			f := newRenderOutputFixture(t)
			cwd := t.TempDir()
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("keep\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, filepath.Join(cwd, tc.file)); err != nil {
				t.Fatal(err)
			}
			output, err := runRenderOutputCommand(t, f, cwd, "")
			data, readErr := os.ReadFile(victim)
			if readErr != nil || string(data) != "keep\n" {
				t.Error("render changed the outside victim")
			}
			want := fmt.Sprintf("ERROR: output[%d]: could not append connection: "+
				"path %q is not inside its trusted parent directory",
				tc.index, "./"+tc.file)
			assertRenderOutputFailure(t, output, err, want)
		})
	}
}

func assertRenderOutputFailure(t *testing.T, output string, err error, want string) {
	t.Helper()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("want render exit 1, got %v", err)
	}
	if !strings.Contains(output, want+"\n") {
		t.Fatalf("want %q, got:\n%s", want, output)
	}
}

func TestRenderOutputAppendDocuments(t *testing.T) {
	for _, mode := range []string{"current directory", "output directory"} {
		t.Run(mode, func(t *testing.T) {
			f := newRenderOutputFixture(t)
			cwd := t.TempDir()
			output, directory := "", cwd
			if mode == "output directory" {
				output = t.TempDir()
				directory = filepath.Join(output, "demo")
			}
			text, err := runRenderOutputCommand(t, f, cwd, output)
			if err != nil {
				t.Fatalf("render: %v\n%s", err, text)
			}
			assertRenderOutputDocuments(t, directory, "outputConnections.yaml", "Connection", []string{"first", "second"})
			assertRenderOutputDocuments(t, directory, "outputClusterConnections.yaml", "ClusterConnection", []string{"shared"})
		})
	}
}

func assertRenderOutputDocuments(t *testing.T, directory, file, kind string, names []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, file))
	if err != nil {
		t.Fatal(err)
	}
	documents := strings.Split(string(data), "---\n")
	if documents[0] != "" || len(documents) != len(names)+1 {
		t.Fatalf("unexpected YAML documents in %s: %q", file, data)
	}
	for i, name := range names {
		var doc struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name, Namespace string
			} `json:"metadata"`
			Spec struct {
				Contract, OutputName string
				Values               map[string]string
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(documents[i+1]), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.APIVersion != "kubocd.kubotal.io/v1alpha1" || doc.Kind != kind ||
			doc.Spec.Contract != "endpoint" || doc.Spec.OutputName != name || doc.Spec.Values["address"] != name+".example" {
			t.Fatalf("unexpected document %d in %s: %+v", i, file, doc)
		}
		wantName, wantNamespace := "kcd-demo-"+name, "default"
		if kind == "ClusterConnection" {
			wantName, wantNamespace = "kcd-default-demo-"+name, ""
		}
		if doc.Metadata.Name != wantName || doc.Metadata.Namespace != wantNamespace {
			t.Fatalf("unexpected metadata in %s: %+v", file, doc.Metadata)
		}
		if !strings.HasSuffix(documents[i+1], "\n\n") {
			t.Fatalf("missing document trailing newline in %s", file)
		}
	}
}
