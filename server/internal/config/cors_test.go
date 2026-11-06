package config

import (
	"slices"
	"testing"
)

func TestCORSOriginSettings(t *testing.T) {
	settings := map[string]string{"OMNIGATE_DATABASE_URL": "sqlite:///tmp/test.db"}
	settings["OMNIGATE_CORS_ORIGINS"] = "*"
	settings["OMNIGATE_CONSOLE_CORS_ORIGINS"] = "https://App.Example.com:443/, http://localhost:5173"
	cfg, err := LoadFrom(env(settings))
	if err != nil || !slices.Equal(cfg.CORSOrigins, []string{"*"}) || !slices.Equal(cfg.ConsoleCORSOrigins, []string{"https://app.example.com", "http://localhost:5173"}) {
		t.Fatalf("unexpected config %v, %v", cfg, err)
	}
	for _, bad := range []string{"*", "https://*.example.com", "null", "https://example.com/a", "https://user@example.com", "https://example.com?x", "https://example.com:65536"} {
		settings["OMNIGATE_CONSOLE_CORS_ORIGINS"] = bad
		if _, err := LoadFrom(env(settings)); err == nil {
			t.Errorf("allowed credentialed origin %q", bad)
		}
	}
}
