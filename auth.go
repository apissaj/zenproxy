package main

import (
	"net/http"
	"strings"
)

// AuthMode selects the upstream routing tier.
type AuthMode int

const (
	AuthRoutePublic AuthMode = iota // no key -> free Zen models
	AuthRouteZen                    // zen:<key> -> paid Zen catalog
	AuthRouteGo                     // go:<key> -> Go subscription catalog
	AuthRouteAuto                   // bare sk- key -> auto (Go-only models go to Go)
)

// UpstreamAuth is the resolved authentication for a request.
type UpstreamAuth struct {
	Mode   AuthMode
	Token  string
	Source string
}

func authModeString(m AuthMode) string {
	switch m {
	case AuthRoutePublic:
		return "public"
	case AuthRouteZen:
		return "zen"
	case AuthRouteGo:
		return "go"
	case AuthRouteAuto:
		return "auto"
	}
	return "unknown"
}

// isValidOpenCodeKey accepts only sk- prefixed OpenCode keys.
// Anthropic sk-ant-* keys must never be forwarded upstream.
func isValidOpenCodeKey(token string) bool {
	if strings.HasPrefix(token, "sk-ant-") {
		return false
	}
	return strings.HasPrefix(token, "sk-") && len(token) > 15
}

// extractUpstreamAuth resolves the auth tier from Authorization / x-api-key.
// Missing, "public", and placeholder keys fall back to the free public tier.
func extractUpstreamAuth(r *http.Request) UpstreamAuth {
	token := ""
	source := "none"
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		source = "authorization"
	}
	if token == "" {
		if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
			token = key
			source = "x-api-key"
		}
	}
	if token == "" || token == "public" {
		src := source
		if token == "" {
			src = "none"
		}
		return UpstreamAuth{Mode: AuthRoutePublic, Source: src}
	}
	if rest, ok := strings.CutPrefix(token, "go:"); ok && isValidOpenCodeKey(rest) {
		return UpstreamAuth{Mode: AuthRouteGo, Token: rest, Source: source}
	}
	if rest, ok := strings.CutPrefix(token, "zen:"); ok && isValidOpenCodeKey(rest) {
		return UpstreamAuth{Mode: AuthRouteZen, Token: rest, Source: source}
	}
	if isValidOpenCodeKey(token) {
		return UpstreamAuth{Mode: AuthRouteAuto, Token: token, Source: source}
	}
	return UpstreamAuth{Mode: AuthRoutePublic, Source: source}
}

func (a UpstreamAuth) authHeader() string {
	if a.Mode == AuthRoutePublic {
		return "Bearer public"
	}
	return "Bearer " + a.Token
}

// useGoEndpoint decides whether the request targets the Go catalog endpoint.
func (a UpstreamAuth) useGoEndpoint(modelID string) bool {
	switch a.Mode {
	case AuthRouteGo:
		return isModelInGoCatalog(modelID)
	case AuthRouteAuto:
		return isGoCatalogOnly(modelID)
	default:
		return false
	}
}
