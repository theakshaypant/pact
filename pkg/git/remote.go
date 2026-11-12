package git

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ParseRemote parses a git remote URL (https, ssh or scp-like) into its host,
// owner and repository name. It is best-effort: any host is accepted so
// non-GitHub forges still produce usable owner/repo values. For URLs with
// nested groups (e.g. GitLab) the last two path segments are used.
func ParseRemote(raw string) (Remote, error) {
	if raw == "" {
		return Remote{}, fmt.Errorf("empty remote URL")
	}
	ep, err := transport.NewEndpoint(raw)
	if err != nil {
		return Remote{}, fmt.Errorf("parsing remote URL %q: %w", raw, err)
	}
	path := strings.TrimPrefix(ep.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[len(parts)-1] == "" || parts[len(parts)-2] == "" {
		return Remote{}, fmt.Errorf("remote URL %q does not contain an owner/repository path", raw)
	}
	host := ep.Host
	if host == "" {
		host = "local"
	}
	return Remote{
		Host:     host,
		Owner:    parts[len(parts)-2],
		Repo:     parts[len(parts)-1],
		IsGitHub: strings.EqualFold(host, "github.com"),
	}, nil
}
