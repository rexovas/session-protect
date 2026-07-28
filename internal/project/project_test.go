package project

import "testing"

func TestClaudeProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/example/projects/my-app": "-Users-example-projects-my-app",
		"/Users/example/app.v2":          "-Users-example-app-v2",
		"/Users/example/my_app":          "-Users-example-my-app",
		"/a/b c/d":                       "-a-b-c-d",
	}
	for path, want := range cases {
		if got := claudeProjectSlug(path); got != want {
			t.Errorf("claudeProjectSlug(%q) = %q, want %q", path, got, want)
		}
	}
}
