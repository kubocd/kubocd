package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"log/slog"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
)

type Operation struct {
	WorkDir   string
	ImageRepo string
	ImageTag  string
	Insecure  bool
	Anonymous bool
}

func DumpOci(op *Operation) error {

	loc, err := fetchOciImage("", op)
	if err != nil {
		return err
	}
	index := &v1.Index{}
	if err = misc.LoadJson(path.Join(loc, "index.json"), index); err != nil {
		return fmt.Errorf("fail to decode index.json: %v", err)
	}
	fmt.Printf("---------------------- index.json:\n%s\n", misc.Map2Yaml(index))

	for idx, descriptor := range index.Manifests {
		if err := dumpEntry(fmt.Sprintf("index.descriptor#%d", idx), descriptor.MediaType, descriptor.Digest, op.WorkDir); err != nil {
			return err
		}
		if descriptor.MediaType == "application/vnd.oci.image.manifest.v1+json" || descriptor.MediaType == "application/vnd.docker.distribution.manifest.v2+json" {
			manifest := &v1.Manifest{}
			if err := misc.LoadJson(digestToFile(descriptor.Digest, op.WorkDir), manifest); err != nil {
				fmt.Printf("fail to decode manifest '%s': %v", descriptor.Digest, err)
			}
			// Dump config
			err := dumpEntry(fmt.Sprintf("index.descriptor#%d.config", idx), manifest.Config.MediaType, manifest.Config.Digest, op.WorkDir)
			if err != nil {
				return err
			}
			// And dump loyers
			for idx2, layer := range manifest.Layers {
				err := dumpEntry(fmt.Sprintf("index.descriptor#%d.layer[%d]", idx, idx2), layer.MediaType, layer.Digest, op.WorkDir)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil

}

func fetchOciImage(printPrefix string, op *Operation) (string, error) {
	ctx := context.Background()

	// Prepare target archive folder
	loc := path.Join(op.WorkDir, "oci-image")
	err := misc.SafeEnsureEmpty(loc)
	if err != nil {
		return loc, err
	}

	// Set up an OCI layout as a destination
	dst, err := oci.New(loc)
	if err != nil {
		return loc, err
	}

	// Connect to the remote repository
	//fmt.Printf("Connect to repo '%s'\n", imageRepo)
	repo, err := remote.NewRepository(op.ImageRepo)
	if err != nil {
		return loc, fmt.Errorf("fail to connect to repo '%s': %v", op.ImageRepo, err)
	}
	repo.PlainHTTP = op.Insecure

	if !op.Anonymous {
		splits := strings.Split(op.ImageRepo, "/")
		regHost := splits[0]
		userName, secret, err := GetCredentials(regHost)
		if err != nil {
			return loc, fmt.Errorf("failed to get credentials for repository '%s': %v", regHost, err)
		}
		if secret != "" {
			repo.Client = &auth.Client{
				Client: retry.DefaultClient,
				Cache:  auth.NewCache(),
				Credential: auth.StaticCredential(regHost, auth.Credential{
					Username: userName,
					Password: secret,
				}),
			}
		} else {
			slog.Debug("No credentials provided. Will connect anonymously\n")
		}
	} else {
		fmt.Printf("Will connect anonymously as required\n")
	}

	fmt.Printf("%sPulling image '%s:%s'\n", printPrefix, op.ImageRepo, op.ImageTag)
	// Pull the image
	_, err = oras.Copy(ctx, repo, op.ImageTag, dst, op.ImageTag, oras.DefaultCopyOptions)
	if err != nil {
		return loc, fmt.Errorf("fail to pull image '%s:%s': %v", op.ImageRepo, op.ImageTag, err)
	}
	//fmt.Printf("OCI image downloaded to %s\n", loc)
	//fmt.Printf("---------------------- descriptor:\n%v\n", misc.Map2YamlStr(descriptor))
	return loc, nil
}

func dumpEntry(prefix string, mediaType string, digest digest.Digest, workDir string) error {
	if strings.HasSuffix(mediaType, "+json") {
		content := make(map[string]interface{})
		err := misc.LoadJson(digestToFile(digest, workDir), &content)
		if err != nil {
			fmt.Printf("fail to decode manifest '%s' (%s) in json: %v", digest, mediaType, err)
		}
		fmt.Printf("-------------------- %s blob:%s... mediaType:'%s'\n%s\n", prefix, digest[:15], mediaType, misc.Map2Yaml(content))
	} else if strings.HasSuffix(mediaType, "tar+gzip") || strings.HasSuffix(mediaType, "tar.gzip") {
		contents, err := misc.ListTarGzContents(digestToFile(digest, workDir))
		if err != nil {
			return err
		}
		fmt.Printf("-------------------- %s blob:%s... mediaType:'%s'\n%s\n", prefix, digest[:15], mediaType, contents)
	} else {
		fmt.Printf("-------------------- %s blob:%s... mediaType:'%s'\n%s\n", prefix, digest[:15], mediaType, "CONTENT TYPE NOT YET HANDLED")
	}
	return nil
}

func digestToFile(digest digest.Digest, workDir string) string {
	a := strings.Split(string(digest), ":")
	if len(a) != 2 {
		panic(fmt.Sprintf("invalid digest: %s", digest))
	}
	return path.Join(workDir, "oci-image", "blobs", a[0], a[1])
}

// GetCredentials retrieves stored credentials from the macOS/linux/windows Keychain
func GetCredentials(registry string) (string, string, error) {
	user := os.Getenv(global.OciUserEnvVar)
	secret := os.Getenv(global.OciSecretEnvVar)
	if user != "" && secret != "" {
		slog.Debug(fmt.Sprintf("User authentication from %s and %s", global.OciUserEnvVar, global.OciSecretEnvVar))
		return user, secret, nil
	}
	helper, err := getDockerCredentialsHelper()
	if err != nil {
		return "", "", err
	}
	if helper == "" {
		slog.Debug("No authentication credentials found. Will be anonymous")
		return "", "", nil
	}
	slog.Debug(fmt.Sprintf("Using credentials helper: %s", helper))
	// Run docker-credential-osxkeychain get <registry>
	cmd := exec.Command(helper, "get")
	// Pass the registry name as input
	cmd.Stdin = bytes.NewBufferString(registry)
	// Capture the output
	output, err := cmd.Output()
	if err != nil {
		//return "", "", fmt.Errorf("failed to get credentials: %w", err)
		return "", "", nil // We don't have creds for this repository host. Not an error
	}
	// Parse JSON output
	var creds struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if err := json.Unmarshal(output, &creds); err != nil {
		return "", "", fmt.Errorf("failed to parse credentials: %w", err)
	}
	return creds.Username, creds.Secret, nil
}

func getDockerCredentialsHelper() (string, error) {
	helper := os.Getenv(global.DockerCredentialHelperEnvVar)
	if helper != "" {
		_, err := exec.LookPath(helper)
		if err != nil {
			return "", fmt.Errorf("could not find %s in PATH (Check '%s' env variable)", helper, global.DockerCredentialHelperEnvVar)
		}
		return helper, nil
	}
	var dockerCredentialsExec []string
	switch runtime.GOOS {
	case "windows":
		dockerCredentialsExec = []string{"docker-credential-wincred"}
	case "linux":
		dockerCredentialsExec = []string{"docker-credential-pass", "docker-credential-secretservice"}
	case "darwin":
		dockerCredentialsExec = []string{"docker-credential-osxkeychain"}
	default:
		dockerCredentialsExec = []string{}
	}
	for _, exe := range dockerCredentialsExec {
		_, err := exec.LookPath(exe)
		if err == nil {
			return exe, nil
		}
	}
	return "", nil
}

func GetChartArchiveFromOci(printPrefix string, op *Operation) (archivePath string, err error) {
	loc, err := fetchOciImage(printPrefix, op)
	if err != nil {
		return "", err
	}
	index := &v1.Index{}
	if err = misc.LoadJson(path.Join(loc, "index.json"), index); err != nil {
		return "", fmt.Errorf("fail to decode index.json: %v", err)
	}
	for _, descriptor := range index.Manifests {
		if descriptor.MediaType == "application/vnd.oci.image.manifest.v1+json" || descriptor.MediaType == "application/vnd.docker.distribution.manifest.v2+json" {
			manifest := &v1.Manifest{}
			if err := misc.LoadJson(digestToFile(descriptor.Digest, op.WorkDir), manifest); err != nil {
				fmt.Printf("fail to decode manifest '%s': %v", descriptor.Digest, err)
			}
			// And dump loyers
			for _, layer := range manifest.Layers {
				if layer.MediaType == "application/vnd.cncf.helm.chart.content.v1.tar+gzip" {
					// GOT IT
					return digestToFile(layer.Digest, op.WorkDir), nil
				}
			}
		}
	}
	return "", fmt.Errorf("fail to find chart archive")
}
