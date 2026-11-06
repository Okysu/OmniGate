package app

import (
	"net/http"

	"github.com/rs/cors"

	"omnigate/internal/platform/httpx"
)

func apiCORS(origins []string, console bool) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		// An empty allowlist in rs/cors means all origins.
		return func(next http.Handler) http.Handler { return next }
	}
	opts := cors.Options{
		AllowedOrigins: origins,
		AllowedMethods: []string{"GET", "HEAD", "POST"},
		AllowedHeaders: []string{"*"}, // Bearer keys and provider-specific SDK headers.
		ExposedHeaders: []string{httpx.RequestIDHeader, "Retry-After", "X-OmniGate-Compat-Warnings", "X-OmniGate-Estimated"},
		MaxAge:         600,
	}
	if console {
		opts.AllowCredentials = true
		opts.AllowedMethods = append(opts.AllowedMethods, "PUT", "PATCH", "DELETE")
		opts.AllowedHeaders = []string{"Content-Type", "X-Requested-With", "Accept"}
	}
	return cors.New(opts).Handler
}
