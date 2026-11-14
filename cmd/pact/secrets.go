package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theakshaypant/pact/pkg/runner"
)

// secretFlags configure the Kubernetes Secrets created in the run
// namespace. They are bound to run only.
type secretFlags struct {
	secrets []string
	file    string

	// cmd is the command the flags are bound to, so toSecrets can reject
	// an explicitly empty --secret-file the way paramFlags treats
	// --param-file.
	cmd *cobra.Command
}

func (f *secretFlags) bind(cmd *cobra.Command) {
	f.cmd = cmd
	cmd.Flags().StringArrayVar(&f.secrets, "secret", nil,
		"Kubernetes Secret value as name.key=value, created in the run namespace (repeatable)")
	cmd.Flags().StringVar(&f.file, "secret-file", "",
		"dotenv file (name.key=value lines) of secret values; --secret overrides it (relative to the working directory)")
}

// toSecrets merges the file's entries with the flag entries — a flag wins
// on the same name.key — and validates them. It returns nil secrets when
// neither is set.
func (f *secretFlags) toSecrets() (runner.Secrets, error) {
	if f.cmd != nil && f.cmd.Flags().Changed("secret-file") && strings.TrimSpace(f.file) == "" {
		return nil, errors.New("--secret-file must not be empty")
	}
	merged := map[string]string{}
	if f.file != "" {
		var err error
		if merged, err = parseSecretFile(f.file); err != nil {
			return nil, err
		}
	}
	for _, s := range f.secrets {
		ref, value, err := parseSecretFlag(s)
		if err != nil {
			return nil, err
		}
		merged[ref] = value
	}
	return runner.NewSecrets(merged)
}

// parseSecretFlag splits a --secret value at the first =. The name.key
// reference is trimmed; the value is kept verbatim and may be empty or
// contain =. Errors never quote the input, so a value cannot leak into
// an error message.
func parseSecretFlag(s string) (ref, value string, err error) {
	ref, value, found := strings.Cut(s, "=")
	ref = strings.TrimSpace(ref)
	if !found || ref == "" {
		return "", "", errors.New("invalid --secret: want name.key=value")
	}
	return ref, value, nil
}

// parseSecretFile reads a dotenv secrets file.
func parseSecretFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading --secret-file: %w", err)
	}
	return parseDotenv(bytes.NewReader(data), path)
}
