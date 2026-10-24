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

package hack

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// gitRepos is a bare repository, origin, and a clone of it, with git
// configured by the test only.
type gitRepos struct {
	t      *testing.T
	env    []string
	origin string
	clone  string
}

// testEnv is the environment of the process without the variables of git, and
// with the variables given, which replace those of the same name.
func testEnv(variables ...string) []string {
	given := make(map[string]bool, len(variables))
	for _, variable := range variables {
		name, _, _ := strings.Cut(variable, "=")
		given[name] = true
	}
	env := make([]string, 0, len(os.Environ())+len(variables))
	for _, variable := range os.Environ() {
		if name, _, _ := strings.Cut(variable, "="); !given[name] && !strings.HasPrefix(name, "GIT_") {
			env = append(env, variable)
		}
	}
	return append(env, variables...)
}

func needs(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

func newGitRepos(t *testing.T) *gitRepos {
	t.Helper()
	needs(t, "git", "bash")
	dir := t.TempDir()
	config := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(config, []byte("[user]\n\tname = test\n\temail = test@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repos := &gitRepos{t: t, env: testEnv("GIT_CONFIG_GLOBAL="+config, "GIT_CONFIG_NOSYSTEM=1"),
		origin: filepath.Join(dir, "origin.git"), clone: filepath.Join(dir, "clone")}
	repos.git(dir, "init", "-q", "--bare", repos.origin)
	repos.git(dir, "clone", "-q", repos.origin, repos.clone)
	repos.commit()
	return repos
}

func (r *gitRepos) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *gitRepos) commit() string {
	r.t.Helper()
	r.git(r.clone, "commit", "-q", "--allow-empty", "-m", "change")
	return r.git(r.clone, "rev-parse", "HEAD")
}

// originTag returns the commit that the tag of origin points to, or "".
func (r *gitRepos) originTag(tag string) string {
	r.t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", "refs/tags/"+tag+"^{commit}")
	cmd.Dir = r.origin
	cmd.Env = r.env
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func run(t *testing.T, dir string, env []string, script string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	path, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

func (r *gitRepos) releaseTag(version string) (stdout string, stderr string, err error) {
	r.t.Helper()
	return run(r.t, r.clone, r.env, "release-tag.sh", version)
}

// The outputs of release-tag.sh.
const (
	finalOutput    = "snapshot=false\n"
	snapshotOutput = "snapshot=true\n"
)

func TestAFinalTagIsCreatedOnceAndNeverMoved(t *testing.T) {
	for _, annotated := range []bool{false, true} {
		repos := newGitRepos(t)
		first := repos.git(repos.clone, "rev-parse", "HEAD")
		if annotated {
			repos.git(repos.clone, "tag", "-a", "-m", "v1.2.3", "v1.2.3")
			repos.git(repos.clone, "push", "-q", "origin", "refs/tags/v1.2.3")
		} else if stdout, stderr, err := repos.releaseTag("v1.2.3"); err != nil || stdout != finalOutput {
			t.Fatalf("first publication: got error %v, output %q and %s, want snapshot=false", err, stdout, stderr)
		}
		if got := repos.originTag("v1.2.3"); got != first {
			t.Fatalf("got the tag of origin on %q, want the published commit %s", got, first)
		}
		// A run again on the same commit, as after a partial publication
		if stdout, stderr, err := repos.releaseTag("v1.2.3"); err != nil || stdout != finalOutput {
			t.Fatalf("annotated %t, same commit: got error %v, output %q and %s, want snapshot=false", annotated, err, stdout,
				stderr)
		}
		repos.commit()
		_, stderr, err := repos.releaseTag("v1.2.3")
		if err == nil || !strings.Contains(stderr, "The tag v1.2.3 is on "+first) {
			t.Fatalf("annotated %t, next commit: got error %v and %s, want a refusal", annotated, err, stderr)
		}
		if got := repos.originTag("v1.2.3"); got != first {
			t.Fatalf("annotated %t, next commit: the tag of origin moved to %q, want %s", annotated, got, first)
		}
	}
}

// A control: a snapshot tag moves to each published commit.
func TestASnapshotTagFollowsThePublishedCommit(t *testing.T) {
	repos := newGitRepos(t)
	commit := repos.git(repos.clone, "rev-parse", "HEAD")
	for range 2 {
		if stdout, stderr, err := repos.releaseTag("v1.2.3-snapshot"); err != nil || stdout != snapshotOutput {
			t.Fatalf("got error %v, output %q and %s, want snapshot=true", err, stdout, stderr)
		}
		if got := repos.originTag("v1.2.3-snapshot"); got != commit {
			t.Fatalf("got the tag of origin on %q, want the published commit %s", got, commit)
		}
		commit = repos.commit()
	}
}

// A final tag pushed by another run between the lookup of the script and its
// push stays where that run put it. The other run is a hook on the local tag:
// from a pre-push hook, too late, the tag makes even a forced push fail, as the
// push expects it absent.
func TestAFinalTagCreatedMeanwhileIsKept(t *testing.T) {
	repos := newGitRepos(t)
	first := repos.git(repos.clone, "rev-parse", "HEAD")
	other := repos.commit()
	repos.git(repos.clone, "push", "-q", "origin", "HEAD:refs/heads/other")
	repos.git(repos.clone, "checkout", "-q", first)
	hook := "#!/usr/bin/env bash\n[ \"$1\" = committed ] || exit 0\nwhile read -r _ _ ref; do\n" +
		"  if [ \"$ref\" = refs/tags/v1.2.3 ]; then git --git-dir=\"$TEST_ORIGIN\" tag v1.2.3 \"$TEST_OTHER\"; fi\ndone\n"
	repos.env = append(repos.env, "TEST_ORIGIN="+repos.origin, "TEST_OTHER="+other)
	hookFile := filepath.Join(repos.clone, ".git", "hooks", "reference-transaction")
	if err := os.WriteFile(hookFile, []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := repos.releaseTag("v1.2.3"); err == nil {
		t.Fatalf("got no error and %s, want the push refused", stderr)
	}
	if got := repos.originTag("v1.2.3"); got != other {
		t.Fatalf("got the tag of origin on %q, want it kept on %s", got, other)
	}
}

// A control: versions with a pre-release part are snapshots, the others final.
func TestTheVersionsOfSemVerAreAccepted(t *testing.T) {
	for version, want := range map[string]string{"v1.2.3-rc.1": snapshotOutput, "v1.2.3-0": snapshotOutput,
		"v1.2.3-x-y.7": snapshotOutput, "v10.20.30": finalOutput} {
		repos := newGitRepos(t)
		if stdout, stderr, err := repos.releaseTag(version); err != nil || stdout != want {
			t.Errorf("%s: got error %v, output %q and %s, want %q", version, err, stdout, stderr, want)
		}
	}
}

func TestATagNameThatIsNotAVersionIsRefused(t *testing.T) {
	repos := newGitRepos(t)
	for _, version := range []string{"1.2.3", "v1.2", "latest", "v1.2.3-", "v1.2.3.4", "v01.2.3", "v1.02.3", "v1.2.03",
		"v1.2.3-01", "v1.2.3-rc..1", "v1.2.3+build.1"} {
		if _, stderr, err := repos.releaseTag(version); err == nil || !strings.Contains(stderr, "is not a version") {
			t.Errorf("%q: got error %v and %s, want a refusal", version, err, stderr)
		}
	}
	if tags := repos.git(repos.origin, "tag", "-l"); tags != "" {
		t.Fatalf("got the tags %q in origin, want none", tags)
	}
}

// fakeGh is a gh command that writes its arguments to the file of
// FAKE_GH_ARGS, one by line, and prints FAKE_GH_ANSWER, or fails when
// FAKE_GH_FAIL is set.
const fakeGh = `#!/usr/bin/env bash
printf '%s\n' "$@" > "$FAKE_GH_ARGS"
if [ -n "${FAKE_GH_FAIL:-}" ]; then echo "HTTP 401: Bad credentials" >&2; exit 1; fi
printf '%s' "$FAKE_GH_ANSWER"
`

func TestAManualPublicationNeedsTheCIOfAPushToMain(t *testing.T) {
	needs(t, "bash")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args")
	wantArgs := []string{"run", "list", "--repo", "kubocd/kubocd", "--workflow", "ci.yml", "--commit", "0123abcd",
		"--event", "push", "--branch", "main", "--status", "success", "--json", "databaseId", "--jq", "length"}
	for _, c := range []struct {
		name   string
		env    []string
		passes bool
	}{
		{"one successful run", []string{"FAKE_GH_ANSWER=1"}, true},
		{"two successful runs", []string{"FAKE_GH_ANSWER=2"}, true},
		{"no successful run", []string{"FAKE_GH_ANSWER=0"}, false},
		{"an empty answer", []string{"FAKE_GH_ANSWER="}, false},
		{"an answer that is not a count", []string{"FAKE_GH_ANSWER=[]"}, false},
		{"a failed gh", []string{"FAKE_GH_ANSWER=1", "FAKE_GH_FAIL=1"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := testEnv(append(c.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GITHUB_REPOSITORY=kubocd/kubocd", "FAKE_GH_ARGS="+argsFile)...)
			_, stderr, err := run(t, t.TempDir(), env, "ci-passed.sh", "0123abcd")
			if c.passes && err != nil || !c.passes && err == nil {
				t.Fatalf("got error %v and %s, want it to pass: %t", err, stderr, c.passes)
			}
			args, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"); !slices.Equal(got, wantArgs) {
				t.Fatalf("got gh %q, want gh %q", got, wantArgs)
			}
		})
	}
}

// workflowStep is the part of a step of a GitHub workflow that the test reads.
type workflowStep struct {
	ID              string            `json:"id"`
	If              string            `json:"if"`
	Uses            string            `json:"uses"`
	Run             string            `json:"run"`
	With            map[string]string `json:"with"`
	ContinueOnError interface{}       `json:"continue-on-error"`
}

// workflowJob is the part of a job of a GitHub workflow that the test reads.
type workflowJob struct {
	If              string         `json:"if"`
	ContinueOnError interface{}    `json:"continue-on-error"`
	Steps           []workflowStep `json:"steps"`
}

// buildArtifactWorkflow reads the triggers and the job build-artifact of the
// workflow build-artifact.
func buildArtifactWorkflow(t *testing.T) (map[string]interface{}, workflowJob) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "build-artifacts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]interface{}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	// YAML 1.1 reads the key on as true.
	triggers, ok := workflow["on"].(map[string]interface{})
	if !ok {
		triggers, _ = workflow["true"].(map[string]interface{})
	}
	var jobs struct {
		Jobs map[string]workflowJob `json:"jobs"`
	}
	if err := yaml.Unmarshal(data, &jobs); err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Jobs["build-artifact"]
	if !ok || len(jobs.Jobs) != 1 {
		t.Fatalf("got the jobs %v, want build-artifact only", jobs.Jobs)
	}
	return triggers, job
}

func TestTheBuildArtifactWorkflowRunsOnceTheCIOfAPushToMainPassed(t *testing.T) {
	triggers, job := buildArtifactWorkflow(t)
	triggerNames := make([]string, 0, len(triggers))
	for name := range triggers {
		triggerNames = append(triggerNames, name)
	}
	slices.Sort(triggerNames)
	if !slices.Equal(triggerNames, []string{"workflow_dispatch", "workflow_run"}) {
		t.Fatalf("got the triggers %q, want workflow_run and workflow_dispatch only", triggerNames)
	}
	runTrigger, err := yaml.Marshal(triggers["workflow_run"])
	if err != nil {
		t.Fatal(err)
	}
	if want := "branches:\n- main\ntypes:\n- completed\nworkflows:\n- CI\n"; string(runTrigger) != want {
		t.Fatalf("got the trigger workflow_run\n%s\nwant\n%s", runTrigger, want)
	}
	wantIf := "github.event_name == 'workflow_dispatch' || (github.event.workflow_run.conclusion == 'success' && " +
		"github.event.workflow_run.event == 'push' && " +
		"github.event.workflow_run.head_repository.full_name == github.repository)"
	if got := strings.Join(strings.Fields(job.If), " "); got != wantIf {
		t.Errorf("got the condition of the job %q, want %q", got, wantIf)
	}
	if job.ContinueOnError != nil {
		t.Errorf("got continue-on-error %v on the job, want none", job.ContinueOnError)
	}
	for i, step := range job.Steps {
		if step.ContinueOnError != nil {
			t.Errorf("got continue-on-error %v on the step %d, want none", step.ContinueOnError, i)
		}
	}
}

func TestTheBuildArtifactWorkflowTagsTheTestedCommitBeforeItPublishes(t *testing.T) {
	_, job := buildArtifactWorkflow(t)
	place := func(text string) int {
		return slices.IndexFunc(job.Steps, func(step workflowStep) bool { return strings.Contains(step.Run, text) })
	}
	const testedCommit = "${{ github.event.workflow_run.head_sha || github.sha }}"
	checkout := job.Steps[0]
	if !strings.HasPrefix(checkout.Uses, "actions/checkout@") || checkout.With["ref"] != testedCommit {
		t.Errorf("got the first step %+v, want the checkout of the commit that the CI tested", checkout)
	}
	gate := place("bash hack/ci-passed.sh")
	if gate < 0 || job.Steps[gate].If != "github.event_name == 'workflow_dispatch'" ||
		strings.TrimSpace(job.Steps[gate].Run) != `bash hack/ci-passed.sh "$GITHUB_SHA"` {
		t.Fatalf("got the gate of a manual run at %d, want it for workflow_dispatch, alone in its step", gate)
	}
	tag := place("bash hack/release-tag.sh")
	wantTag := "APP_VERSION=$(grep -m1 '^APP_VERSION' Makefile | awk '{print $3}')\n" +
		`bash hack/release-tag.sh "$APP_VERSION" >> "$GITHUB_OUTPUT"`
	if tag < gate || job.Steps[tag].ID != "tag" || strings.TrimSpace(job.Steps[tag].Run) != wantTag {
		t.Fatalf("got the tag step at %d, want the step tag after the gate at %d, with only the script", tag, gate)
	}
	for _, publication := range []string{"gh release delete", "make cli-release", "make charts", "make docker-buildx"} {
		if step := place(publication); step < tag {
			t.Errorf("got %q at %d, want it after the tag step at %d", publication, step, tag)
		}
	}
	if step := place("gh release delete"); step >= 0 && job.Steps[step].If != "steps.tag.outputs.snapshot == 'true'" {
		t.Errorf("got the deletion of the release under %q, want it for a snapshot only", job.Steps[step].If)
	}
	for _, forced := range []string{"git tag -f", "git push -f"} {
		if step := place(forced); step >= 0 {
			t.Errorf("got %q in the step %d, want the tag step to move the tags", forced, step)
		}
	}
}

// The release of a final version is kept: a run again on its commit uploads
// its assets again.
func TestGoreleaserReplacesTheAssetsOfAReleaseThatExists(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Release struct {
			ReplaceExistingArtifacts bool `json:"replace_existing_artifacts"`
		} `json:"release"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil || !config.Release.ReplaceExistingArtifacts {
		t.Fatalf("got the error %v and replace_existing_artifacts %t, want it set", err,
			config.Release.ReplaceExistingArtifacts)
	}
}
