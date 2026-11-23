package reader

import (
	"net/url"
	"strings"
)

// normalizeRemote turns a git remote URL into host/owner/name, lower case,
// with no scheme, user, port, or .git. A remote on no host, such as a folder
// on the same machine, answers "".
//
// Azure DevOps is written in the form its web address takes,
// dev.azure.com/<org>/<project>/_git/<repo>, whichever form the clone used.
func normalizeRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	host, p := "", ""
	if u, err := url.Parse(raw); err == nil && strings.Contains(raw, "://") {
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "ssh", "git", "git+ssh", "ssh+git":
			host, p = u.Hostname(), u.Path
		default:
			return ""
		}
	} else if at, rest, ok := scpLike(raw); ok {
		host, p = at, rest
	} else {
		return ""
	}
	host = strings.ToLower(host)
	if host == "" {
		return ""
	}
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			if un, err := url.PathUnescape(s); err == nil {
				s = un
			}
			parts = append(parts, strings.ToLower(s))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	last := len(parts) - 1
	parts[last] = strings.TrimSuffix(parts[last], ".git")
	host, parts = azure(host, parts)
	return host + "/" + strings.Join(parts, "/")
}

// scpLike reads git's short ssh form, [user@]host:path. A Windows drive
// (C:\work) and a path with a slash before the colon are folders, not hosts.
func scpLike(raw string) (host, path string, ok bool) {
	colon := strings.Index(raw, ":")
	if colon <= 0 || strings.ContainsAny(raw[:colon], `/\`) {
		return "", "", false
	}
	host = raw[:colon]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if len(host) == 1 {
		return "", "", false
	}
	return host, raw[colon+1:], host != ""
}

// azure rewrites Azure DevOps' ssh and old visualstudio.com forms to its
// dev.azure.com web form, so a checkout names the same repository however it
// was cloned.
func azure(host string, parts []string) (string, []string) {
	switch {
	case host == "ssh.dev.azure.com" || host == "vs-ssh.visualstudio.com":
		// v3/<org>/<project>/<repo>
		if len(parts) == 4 && parts[0] == "v3" {
			return "dev.azure.com", []string{parts[1], parts[2], "_git", parts[3]}
		}
	case strings.HasSuffix(host, ".visualstudio.com"):
		org := strings.TrimSuffix(host, ".visualstudio.com")
		if len(parts) > 0 && parts[0] == "defaultcollection" {
			parts = parts[1:]
		}
		return "dev.azure.com", append([]string{org}, parts...)
	}
	return host, parts
}
