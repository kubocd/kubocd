package cmd

import (
	"context"
	"fmt"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"kubocd/internal/misc"
	"log/slog"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
	"os"
	"path"
	"strings"
)

var ociParams struct {
	insecure  bool
	anonymous bool
}

func init() {
	ociDumpCmd.PersistentFlags().BoolVarP(&ociParams.insecure, "insecure", "i", false, "insecure (use HTTP, not HTTPS)")
	dumpOciCmd.PersistentFlags().BoolVarP(&ociParams.insecure, "insecure", "i", false, "insecure (use HTTP, not HTTPS)")

	ociDumpCmd.PersistentFlags().BoolVarP(&ociParams.anonymous, "anonymous", "a", false, "Connect anonymously. To check 'public' image status")
	dumpOciCmd.PersistentFlags().BoolVarP(&ociParams.anonymous, "anonymous", "a", false, "Connect anonymously. To check 'public' image status")

}

var ociDumpCmd = &cobra.Command{
	Use:   "dump repo:version",
	Short: "Dump OCI metadata",
	Args:  cobra.ExactArgs(1),
	Run:   ociDumpRun,
}

var dumpOciCmd = &cobra.Command{
	Use:   "oci repo:version",
	Short: "Dump OCI metadata",
	Args:  cobra.ExactArgs(1),
	Run:   ociDumpRun,
}

var ociDumpRun = func(command *cobra.Command, args []string) {
	var imageName = args[0]
	if strings.HasPrefix(imageName, "oci://") {
		imageName = imageName[7:]
	}

	var imageRepo string
	var imageTag string

	err := func() error {
		a := strings.Split(imageName, ":")
		if len(a) == 2 {
			imageRepo = a[0]
			imageTag = a[1]
		} else if len(a) == 1 {
			imageRepo = a[0]
			imageTag = "latest"
		} else {
			return fmt.Errorf("invalid image name: %s", imageName)
		}
		//fmt.Printf("OCI dump of %s:%s\n", imageRepo, imageTag)

		loc, err := fetchOciImage("", imageRepo, ociParams.insecure, imageTag)
		if err != nil {
			return err
		}
		index := &v1.Index{}
		if err = misc.LoadJson(path.Join(loc, "index.json"), index); err != nil {
			return fmt.Errorf("fail to decode index.json: %v", err)
		}
		fmt.Printf("---------------------- index.json:\n%s\n", misc.Map2Yaml(index))

		for idx, descriptor := range index.Manifests {
			if err := dumpEntry(fmt.Sprintf("index.descriptor#%d", idx), descriptor.MediaType, descriptor.Digest); err != nil {
				return err
			}
			if descriptor.MediaType == "application/vnd.oci.image.manifest.v1+json" || descriptor.MediaType == "application/vnd.docker.distribution.manifest.v2+json" {
				manifest := &v1.Manifest{}
				if err := misc.LoadJson(digestToFile(descriptor.Digest), manifest); err != nil {
					fmt.Printf("fail to decode manifest '%s': %v", descriptor.Digest, err)
				}
				// Dump config
				err := dumpEntry(fmt.Sprintf("index.descriptor#%d.config", idx), manifest.Config.MediaType, manifest.Config.Digest)
				if err != nil {
					return err
				}
				// And dump loyers
				for idx2, layer := range manifest.Layers {
					err := dumpEntry(fmt.Sprintf("index.descriptor#%d.layer[%d]", idx, idx2), layer.MediaType, layer.Digest)
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	}()

	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		os.Exit(1)
	}
}

func fetchOciImage(printPrefix string, imageRepo string, insecure bool, imageTag string) (string, error) {
	ctx := context.Background()

	// Prepare target archive folder
	loc := path.Join(workDir, "oci-image")
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
	repo, err := remote.NewRepository(imageRepo)
	if err != nil {
		return loc, fmt.Errorf("fail to connect to repo '%s': %v", imageRepo, err)
	}
	repo.PlainHTTP = insecure

	if !ociParams.anonymous {
		splits := strings.Split(imageRepo, "/")
		regHost := splits[0]
		userName, secret, err := getCredentials(regHost)
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

	fmt.Printf("%sPulling image '%s:%s'\n", printPrefix, imageRepo, imageTag)
	// Pull the image
	_, err = oras.Copy(ctx, repo, imageTag, dst, imageTag, oras.DefaultCopyOptions)
	if err != nil {
		return loc, fmt.Errorf("fail to pull image '%s:%s': %v", imageRepo, imageTag, err)
	}
	//fmt.Printf("OCI image downloaded to %s\n", loc)
	//fmt.Printf("---------------------- descriptor:\n%v\n", misc.Map2YamlStr(descriptor))
	return loc, nil
}

func dumpEntry(prefix string, mediaType string, digest digest.Digest) error {
	if strings.HasSuffix(mediaType, "+json") {
		content := make(map[string]interface{})
		err := misc.LoadJson(digestToFile(digest), &content)
		if err != nil {
			fmt.Printf("fail to decode manifest '%s' (%s) in json: %v", digest, mediaType, err)
		}
		fmt.Printf("-------------------- %s blob:%s... mediaType:'%s'\n%s\n", prefix, digest[:15], mediaType, misc.Map2Yaml(content))
	} else if strings.HasSuffix(mediaType, "tar+gzip") || strings.HasSuffix(mediaType, "tar.gzip") {
		contents, err := misc.ListTarGzContents(digestToFile(digest))
		if err != nil {
			return err
		}
		fmt.Printf("-------------------- %s blob:%s... mediaType:'%s'\n%s\n", prefix, digest[:15], mediaType, contents)
	} else {
		fmt.Printf("-------------------- %s blob:%s... mediaType:'%s'\n%s\n", prefix, digest[:15], mediaType, "CONTENT TYPE NOT YET HANDLED")
	}
	return nil
}

func digestToFile(digest digest.Digest) string {
	a := strings.Split(string(digest), ":")
	if len(a) != 2 {
		panic(fmt.Sprintf("invalid digest: %s", digest))
	}
	return path.Join(workDir, "oci-image", "blobs", a[0], a[1])
}

func getChartArchiveFromOci(printPrefix string, imageRepo string, insecure bool, imageTag string) (archivePath string, err error) {
	loc, err := fetchOciImage(printPrefix, imageRepo, insecure, imageTag)
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
			if err := misc.LoadJson(digestToFile(descriptor.Digest), manifest); err != nil {
				fmt.Printf("fail to decode manifest '%s': %v", descriptor.Digest, err)
			}
			// And dump loyers
			for _, layer := range manifest.Layers {
				if layer.MediaType == "application/vnd.cncf.helm.chart.content.v1.tar+gzip" {
					// GOT IT
					return digestToFile(layer.Digest), nil
				}
			}
		}
	}
	return "", fmt.Errorf("fail to find chart archive")
}
