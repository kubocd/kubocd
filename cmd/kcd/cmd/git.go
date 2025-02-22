package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"io"
	"io/fs"
	"kubocd/internal/misc"
	"log/slog"
	"os"
	"path"
	"path/filepath"
)

func getHelmChartArchiveFromGit(printPrefix string, url string, branch string, tag string, chartPath string, moduleName string) (string, error) {
	// Prepare target archive folder
	loc := path.Join(workDir, "git-workdir")
	err := misc.SafeEnsureEmpty(loc)
	if err != nil {
		return "", err
	}
	repoLocation := path.Join(loc, "repo")
	chartLocation := path.Join(repoLocation, chartPath)
	archive := path.Join(loc, fmt.Sprintf("%s.tgz", moduleName))

	fmt.Printf("%sCloning git repository '%s'\n", printPrefix, url)

	_, err = git.PlainClone(repoLocation, false, &git.CloneOptions{
		//Auth:          auth,		// See KAD git services for auth handling
		URL:           url,
		Progress:      io.Discard,
		ReferenceName: misc.Ternary(tag == "", plumbing.NewBranchReferenceName(branch), plumbing.NewTagReferenceName(tag)),
	})
	if err != nil {
		return "", fmt.Errorf("failed to clone repo: %w", err)
	}

	// ----------------------------------------------------------- Build chart archive
	out, err := os.Create(archive)
	if err != nil {
		return "", fmt.Errorf("failed to create archive '%s': %w", archive, err)
	}
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	chartLocationLen := len(chartLocation)
	err = filepath.WalkDir(chartLocation, func(thePath string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Error("Error while walking git repository on path: %s: %s", thePath, err.Error())
			return nil
		}
		if !d.IsDir() {
			targetFileName := path.Join(moduleName, thePath[chartLocationLen:])
			err := addToArchive(tw, thePath, targetFileName)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return archive, nil

}
