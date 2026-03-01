package cmd

import (
	"slices"
	"testing"
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
				ghToken: "ghp_abc123",
			},
		},
		{
			name: "gh-token with equals form",
			args: []string{"--gh-token=ghp_abc123"},
			want: bunkerFlags{
				ghToken: "ghp_abc123",
			},
		},
		{
			name: "api-key with space-separated value",
			args: []string{"--api-key", "sk-ant-abc123"},
			want: bunkerFlags{
				apiKey: "sk-ant-abc123",
			},
		},
		{
			name: "api-key with equals form",
			args: []string{"--api-key=sk-ant-abc123"},
			want: bunkerFlags{
				apiKey: "sk-ant-abc123",
			},
		},
		{
			name: "oauth-token with space-separated value",
			args: []string{"--oauth-token", "oauth_xyz789"},
			want: bunkerFlags{
				oauthToken: "oauth_xyz789",
			},
		},
		{
			name: "oauth-token with equals form",
			args: []string{"--oauth-token=oauth_xyz789"},
			want: bunkerFlags{
				oauthToken: "oauth_xyz789",
			},
		},
		{
			name: "verbose flag",
			args: []string{"--verbose"},
			want: bunkerFlags{
				isVerbose: true,
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
			name: "no-teardown is alias for keep",
			args: []string{"--no-teardown"},
			want: bunkerFlags{
				keep: true,
			},
		},
		{
			name: "mixed bunker and claude flags",
			args: []string{"--gh-token", "ghp_abc", "--model", "opus", "--verbose", "--print", "hello world"},
			want: bunkerFlags{
				ghToken:   "ghp_abc",
				isVerbose: true,
				remaining: []string{"--model", "opus", "--print", "hello world"},
			},
		},
		{
			name: "flag at end with no value does not panic",
			args: []string{"--gh-token"},
			want: bunkerFlags{
				ghToken: "",
			},
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
			},
			want: bunkerFlags{
				ghToken:    "ghp_abc",
				apiKey:     "sk-ant-123",
				oauthToken: "oauth_xyz",
				isVerbose:  true,
				keep:       true,
				quiet:      true,
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
			want: bunkerFlags{
				apiKey: "",
			},
		},
		{
			name: "oauth-token at end with no value does not panic",
			args: []string{"--oauth-token"},
			want: bunkerFlags{
				oauthToken: "",
			},
		},
		{
			name: "bunker flags interspersed among claude flags",
			args: []string{"--print", "--gh-token", "ghp_abc", "--model", "opus", "--quiet", "--dangerously-skip-permissions"},
			want: bunkerFlags{
				ghToken: "ghp_abc",
				quiet:   true,
				remaining: []string{"--print", "--model", "opus", "--dangerously-skip-permissions"},
			},
		},
		{
			name: "equals form with empty value",
			args: []string{"--gh-token="},
			want: bunkerFlags{
				ghToken: "",
			},
		},
		{
			name: "no-teardown and keep both set keep true",
			args: []string{"--no-teardown", "--keep"},
			want: bunkerFlags{
				keep: true,
			},
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

			if got.ghToken != tt.want.ghToken {
				t.Errorf("ghToken = %q, want %q", got.ghToken, tt.want.ghToken)
			}
			if got.apiKey != tt.want.apiKey {
				t.Errorf("apiKey = %q, want %q", got.apiKey, tt.want.apiKey)
			}
			if got.oauthToken != tt.want.oauthToken {
				t.Errorf("oauthToken = %q, want %q", got.oauthToken, tt.want.oauthToken)
			}
			if got.quiet != tt.want.quiet {
				t.Errorf("quiet = %v, want %v", got.quiet, tt.want.quiet)
			}
			if got.isVerbose != tt.want.isVerbose {
				t.Errorf("isVerbose = %v, want %v", got.isVerbose, tt.want.isVerbose)
			}
			if got.keep != tt.want.keep {
				t.Errorf("keep = %v, want %v", got.keep, tt.want.keep)
			}
			if !slices.Equal(got.remaining, tt.want.remaining) {
				t.Errorf("remaining = %v, want %v", got.remaining, tt.want.remaining)
			}
		})
	}
}
