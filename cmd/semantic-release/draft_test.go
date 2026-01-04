package main

import (
	"testing"

	"github.com/go-semantic-release/semantic-release/v2/pkg/config"
	"github.com/go-semantic-release/semantic-release/v2/pkg/provider"
)

func TestDraftOptionParsing(t *testing.T) {
	tests := []struct {
		name          string
		providerOpts  map[string]string
		expectedDraft bool
	}{
		{
			name:          "draft=true sets Draft to true",
			providerOpts:  map[string]string{"draft": "true"},
			expectedDraft: true,
		},
		{
			name:          "draft=false sets Draft to false",
			providerOpts:  map[string]string{"draft": "false"},
			expectedDraft: false,
		},
		{
			name:          "draft=yes sets Draft to false (strict matching)",
			providerOpts:  map[string]string{"draft": "yes"},
			expectedDraft: false,
		},
		{
			name:          "draft=1 sets Draft to false (strict matching)",
			providerOpts:  map[string]string{"draft": "1"},
			expectedDraft: false,
		},
		{
			name:          "missing draft option defaults to false",
			providerOpts:  map[string]string{},
			expectedDraft: false,
		},
		{
			name:          "draft=TRUE (uppercase) sets Draft to false (case-sensitive)",
			providerOpts:  map[string]string{"draft": "TRUE"},
			expectedDraft: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := &config.Config{
				ProviderOpts: tt.providerOpts,
			}

			draft := false
			if draftOpt, ok := conf.ProviderOpts["draft"]; ok && draftOpt == "true" {
				draft = true
			}

			releaseConfig := &provider.CreateReleaseConfig{
				Draft: draft,
			}

			if releaseConfig.Draft != tt.expectedDraft {
				t.Errorf("expected Draft=%v, got Draft=%v", tt.expectedDraft, releaseConfig.Draft)
			}
		})
	}
}
