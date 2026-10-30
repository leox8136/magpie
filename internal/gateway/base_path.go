package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

// BasePath is the explicitly configured HTTP mount, without a trailing slash.
// It is independent of proxy headers: a caller cannot choose the mount.
func BasePath() (string, error) {
	p := strings.TrimSpace(os.Getenv("MAGPIE_BASE_PATH"))
	if p == "" || p == "/" {
		return "", nil
	}
	p = strings.TrimSuffix(p, "/")
	clean := p != "/" && strings.HasPrefix(p, "/") && path.Clean(p) == p
	for _, c := range p {
		// Unreserved characters work unchanged in a URL, Cookie Path and
		// a plugin CSP source. Escapes and separators such as ';' do not.
		clean = clean && (c == '/' || c == '-' || c == '_' || c == '.' || c == '~' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
	}
	if !clean {
		return "", fmt.Errorf("MAGPIE_BASE_PATH must be a clean absolute path of unreserved URL characters, such as /magpie")
	}
	// A mount cannot occupy an existing route namespace: both the browser
	// helper and the gateway's retained root routes would be ambiguous.
	root := "/" + strings.Split(strings.TrimPrefix(p, "/"), "/")[0]
	reserved := root == "/api" || root == "/_magpie"
	for _, route := range clientPrefixes {
		reserved = reserved || root == "/"+strings.Split(strings.TrimPrefix(route, "/"), "/")[0]
	}
	if reserved {
		return "", fmt.Errorf("MAGPIE_BASE_PATH conflicts with a reserved route namespace: %s", root)
	}
	// The page and its advertised gateway must agree on their public mount.
	public := strings.TrimSpace(os.Getenv("MAGPIE_PUBLIC_URL"))
	if public != "" {
		if !strings.Contains(public, "://") {
			public = "http://" + public
		}
		if u, err := url.Parse(public); err == nil && u.Host != "" && strings.TrimRight(u.Path, "/") != "" && strings.TrimRight(u.EscapedPath(), "/") != p {
			return "", fmt.Errorf("MAGPIE_PUBLIC_URL's path must match MAGPIE_BASE_PATH")
		}
	}
	return p, nil
}

type basePathKey struct{}

// RequestBasePath is set only by MountBasePath, never by forwarding headers.
// Desktop requests and the gateway's original local routes have no mount.
func RequestBasePath(r *http.Request) string {
	p, _ := r.Context().Value(basePathKey{}).(string)
	return p
}

// MountBasePath strips the configured mount before routing and guards run.
// rootFallback keeps the gateway's original routes for local agent configs;
// the web listener uses false and serves nothing outside the mount.
func MountBasePath(prefix string, next http.Handler, rootFallback bool) http.Handler {
	if prefix == "" {
		return next
	}
	stripped := http.StripPrefix(prefix, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The page listener already stripped it before handing a client
		// route to Front in this process. Never strip a route twice.
		if RequestBasePath(r) == prefix {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.EscapedPath() == prefix {
			to := prefix + "/"
			if r.URL.RawQuery != "" {
				to += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, to, http.StatusPermanentRedirect)
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			// The boundary itself must be literal. Encoded separators or dot
			// traversal must not become another route after normalization.
			if !strings.HasPrefix(r.URL.EscapedPath(), prefix+"/") {
				http.NotFound(w, r)
				return
			}
			rest := strings.TrimPrefix(r.URL.Path, prefix)
			if rest != "/" && path.Clean(rest) != strings.TrimSuffix(rest, "/") {
				http.Error(w, "invalid path under MAGPIE_BASE_PATH", http.StatusBadRequest)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), basePathKey{}, prefix))
			stripped.ServeHTTP(w, r)
			return
		}
		if rootFallback {
			next.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
	})
}
