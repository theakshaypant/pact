package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theakshaypant/pact/pkg/pac"
)

// paramFlags configure the custom PipelineRun params supplied on the CLI.
// They are shared by resolve and run.
type paramFlags struct {
	params []string
	file   string

	// cmd is the command the flags are bound to, so toParams can reject
	// an explicitly empty --param-file the way eventFlags treats
	// --comment.
	cmd *cobra.Command
}

func (f *paramFlags) bind(cmd *cobra.Command) {
	f.cmd = cmd
	cmd.Flags().StringArrayVar(&f.params, "param", nil,
		"custom param as name=value, substituted as {{ name }} and merged into spec.params (repeatable)")
	cmd.Flags().StringVar(&f.file, "param-file", "",
		"dotenv file (name=value lines) of custom params; --param overrides it (relative to the working directory)")
}

// toParams merges the file's params with the flag params — flags win on
// the same name — and validates the names. It returns nil params when
// neither is set.
func (f *paramFlags) toParams() (pac.Params, error) {
	if f.cmd != nil && f.cmd.Flags().Changed("param-file") && strings.TrimSpace(f.file) == "" {
		return nil, errors.New("--param-file must not be empty")
	}
	merged := map[string]string{}
	if f.file != "" {
		var err error
		if merged, err = parseParamFile(f.file); err != nil {
			return nil, err
		}
	}
	for _, p := range f.params {
		name, value, err := parseParamFlag(p)
		if err != nil {
			return nil, err
		}
		merged[name] = value
	}
	return pac.NewParams(merged)
}

// parseParamFlag splits a --param value at the first =. The name is
// trimmed; the value is kept verbatim and may be empty or contain =.
func parseParamFlag(s string) (name, value string, err error) {
	name, value, found := strings.Cut(s, "=")
	name = strings.TrimSpace(name)
	if !found {
		return "", "", fmt.Errorf("invalid --param %q: want name=value", s)
	}
	if name == "" {
		return "", "", fmt.Errorf("invalid --param %q: empty name", s)
	}
	return name, value, nil
}

// parseParamFile reads a dotenv params file.
func parseParamFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading --param-file: %w", err)
	}
	return parseDotenv(bytes.NewReader(data), path)
}

// parseDotenv parses name=value lines: # comments, blank lines, CRLF, a
// UTF-8 BOM and one pair of surrounding quotes on the value are handled;
// a later line wins. It is deliberately tkn-simple: no escape sequences,
// no inline comments, no export prefix.
func parseDotenv(r io.Reader, name string) (map[string]string, error) {
	params := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("%s:%d: want name=value", name, lineNo)
		}
		if k = strings.TrimSpace(k); k == "" {
			return nil, fmt.Errorf("%s:%d: empty name", name, lineNo)
		}
		params[k] = unquote(strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	return params, nil
}

// unquote strips one pair of matching surrounding quotes.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
