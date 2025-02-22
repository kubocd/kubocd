package cmd

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"kubocd/internal/global"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
)

func addToArchive(tw *tar.Writer, filePath string, inArchiveName string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file '%s': %w", filePath, err)
	}
	defer f.Close()

	// Get FileInfo about our file providing file size, mode, etc.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file '%s': %w", filePath, err)
	}
	// Create a tar Header from the FileInfo data
	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return fmt.Errorf("failed to create header for file '%s': %w", filePath, err)
	}
	// https://golang.org/src/archive/tar/common.go?#L626
	header.Name = inArchiveName
	// Write file header to the tar archive
	err = tw.WriteHeader(header)
	if err != nil {
		return fmt.Errorf("failed to write tar header for file '%s': %w", filePath, err)
	}
	// Copy file content to tar archive
	_, err = io.Copy(tw, f)
	if err != nil {
		return fmt.Errorf("failed to copy file %s to archive: %w", filePath, err)
	}
	return nil
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

// getDockerCredentials retrieves stored credentials from the macOS/linux/windows Keychain
func getCredentials(registry string) (string, string, error) {
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
