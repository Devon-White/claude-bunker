package cmd

import (
	"slices"
	"testing"

	"github.com/Devon-White/claude-bunker/internal/container"
)

func TestExtractBunkerFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bunkerFlags
	}{
		{
			name: "no flags passes all args to remaining",
			args: []string{"--model", "opus", "--print", "hello"},
			want: bunkerFlags{
				remaining: []string{"--model", "opus", "--print", "hello"},
			},
		},
		{
			name: "gh-token with space-separated value",
			args: []string{"--gh-token", "ghp_abc123"},
			want: bunkerFlags{
				auth: container.AuthTokens{GhToken: "ghp_abc123"},
			},
		},
		{
			name: "gh-token with equals form",
			args: []string{"--gh-token=ghp_abc123"},
			want: bunkerFlags{
				auth: container.AuthTokens{GhToken: "ghp_abc123"},
			},
		},
		{
			name: "api-key with space-separated value",
			args: []string{"--api-key", "sk-ant-abc123"},
			want: bunkerFlags{
				auth: container.AuthTokens{ApiKey: "sk-ant-abc123"},
			},
		},
		{
			name: "api-key with equals form",
			args: []string{"--api-key=sk-ant-abc123"},
			want: bunkerFlags{
				auth: container.AuthTokens{ApiKey: "sk-ant-abc123"},
			},
		},
		{
			name: "oauth-token with space-separated value",
			args: []string{"--oauth-token", "oauth_xyz789"},
			want: bunkerFlags{
				auth: container.AuthTokens{OAuthToken: "oauth_xyz789"},
			},
		},
		{
			name: "oauth-token with equals form",
			args: []string{"--oauth-token=oauth_xyz789"},
			want: bunkerFlags{
				auth: container.AuthTokens{OAuthToken: "oauth_xyz789"},
			},
		},
		{
			name: "verbose flag",
			args: []string{"--verbose"},
			want: bunkerFlags{
				verbose: true,
			},
		},
		{
			name: "quiet flag",
			args: []string{"--quiet"},
			want: bunkerFlags{
				quiet: true,
			},
		},
		{
			name: "keep flag",
			args: []string{"--keep"},
			want: bunkerFlags{
				keep: true,
			},
		},
		{
			name: "rebuild flag",
			args: []string{"--rebuild"},
			want: bunkerFlags{
				rebuild: true,
			},
		},
		{
			name: "mixed bunker and claude flags",
			args: []string{"--gh-token", "ghp_abc", "--model", "opus", "--verbose", "--print", "hello world"},
			want: bunkerFlags{
				auth:    container.AuthTokens{GhToken: "ghp_abc"},
				verbose: true,
				remaining: []string{"--model", "opus", "--print", "hello world"},
			},
		},
		{
			name: "flag at end with no value does not panic",
			args: []string{"--gh-token"},
			want: bunkerFlags{},
		},
		{
			name: "multiple bunker flags all extracted",
			args: []string{
				"--gh-token", "ghp_abc",
				"--api-key=sk-ant-123",
				"--oauth-token", "oauth_xyz",
				"--verbose",
				"--keep",
				"--quiet",
				"--rebuild",
			},
			want: bunkerFlags{
				auth:    container.AuthTokens{GhToken: "ghp_abc", ApiKey: "sk-ant-123", OAuthToken: "oauth_xyz"},
				verbose: true,
				keep:    true,
				quiet:   true,
				rebuild: true,
			},
		},
		{
			name: "empty args returns zero-value bunkerFlags",
			args: []string{},
			want: bunkerFlags{},
		},
		{
			name: "nil args returns zero-value bunkerFlags",
			args: nil,
			want: bunkerFlags{},
		},
		{
			name: "api-key at end with no value does not panic",
			args: []string{"--api-key"},
			want: bunkerFlags{},
		},
		{
			name: "oauth-token at end with no value does not panic",
			args: []string{"--oauth-token"},
			want: bunkerFlags{},
		},
		{
			name: "bunker flags interspersed among claude flags",
			args: []string{"--print", "--gh-token", "ghp_abc", "--model", "opus", "--quiet", "--dangerously-skip-permissions"},
			want: bunkerFlags{
				auth:  container.AuthTokens{GhToken: "ghp_abc"},
				quiet: true,
				remaining: []string{"--print", "--model", "opus", "--dangerously-skip-permissions"},
			},
		},
		{
			name: "equals form with empty value",
			args: []string{"--gh-token="},
			want: bunkerFlags{},
		},
		{
			name: "only non-bunker flags all land in remaining",
			args: []string{"-p", "do something", "--model", "sonnet"},
			want: bunkerFlags{
				remaining: []string{"-p", "do something", "--model", "sonnet"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBunkerFlags(tt.args)

			if got.auth.GhToken != tt.want.auth.GhToken {
				t.Errorf("auth.GhToken = %q, want %q", got.auth.GhToken, tt.want.auth.GhToken)
			}
			if got.auth.ApiKey != tt.want.auth.ApiKey {
				t.Errorf("auth.ApiKey = %q, want %q", got.auth.ApiKey, tt.want.auth.ApiKey)
			}
			if got.auth.OAuthToken != tt.want.auth.OAuthToken {
				t.Errorf("auth.OAuthToken = %q, want %q", got.auth.OAuthToken, tt.want.auth.OAuthToken)
			}
			if got.quiet != tt.want.quiet {
				t.Errorf("quiet = %v, want %v", got.quiet, tt.want.quiet)
			}
			if got.verbose != tt.want.verbose {
				t.Errorf("verbose = %v, want %v", got.verbose, tt.want.verbose)
			}
			if got.keep != tt.want.keep {
				t.Errorf("keep = %v, want %v", got.keep, tt.want.keep)
			}
			if got.rebuild != tt.want.rebuild {
				t.Errorf("rebuild = %v, want %v", got.rebuild, tt.want.rebuild)
			}
			if !slices.Equal(got.remaining, tt.want.remaining) {
				t.Errorf("remaining = %v, want %v", got.remaining, tt.want.remaining)
			}
		})
	}
}
