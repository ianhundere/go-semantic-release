package main

import (
	"os"
	"testing"
)

func createTempFile(t *testing.T, content string) string {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "test-version-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	path := tmpFile.Name()
	t.Cleanup(func() {
		_ = os.Remove(path)
	})

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("failed to close temp file: %v", err)
	}

	return path
}

func patternForFile(path string, regex string, template string) string {
	return path + ":" + regex + ":" + template
}

func TestUpdateFilesBeforeRelease(t *testing.T) {
	tests := []struct {
		name            string
		regex           string
		template        string
		version         string
		fileContent     string
		expectedContent string
		expectError     bool
	}{
		{
			name:            "simple version replacement",
			regex:           "^.*$",
			template:        "{{version}}",
			version:         "1.2.3",
			fileContent:     "0.0.0",
			expectedContent: "1.2.3",
			expectError:     false,
		},
		{
			name:            "python version with major.minor.patch",
			regex:           `__version__ = ".*"`,
			template:        `__version__ = "{{major}}.{{minor}}.{{patch}}"`,
			version:         "2.5.8",
			fileContent:     `__version__ = "0.0.0"`,
			expectedContent: `__version__ = "2.5.8"`,
			expectError:     false,
		},
		{
			name:            "template variable substitution",
			regex:           "v.*",
			template:        "v{{version}}",
			version:         "3.0.0",
			fileContent:     "v0.0.0",
			expectedContent: "v3.0.0",
			expectError:     false,
		},
		{
			name:            "already up-to-date results in no change",
			regex:           "^.*$",
			template:        "{{version}}",
			version:         "1.2.3",
			fileContent:     "1.2.3",
			expectedContent: "1.2.3",
			expectError:     false,
		},
		{
			name:        "pattern does not match content",
			regex:       "does-not-exist",
			template:    "{{version}}",
			version:     "1.0.0",
			fileContent: "0.0.0",
			expectError: true,
		},
		{
			name:            "multiple variable types",
			regex:           "Version = .*",
			template:        "Version = {{major}}.{{minor}}.{{patch}}",
			version:         "4.2.1",
			fileContent:     "Version = 0.0.0",
			expectedContent: "Version = 4.2.1",
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempFile(t, tt.fileContent)
			pattern := patternForFile(path, tt.regex, tt.template)
			err := updateFilesBeforeRelease([]string{pattern}, tt.version)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read updated file: %v", err)
			}

			if string(content) != tt.expectedContent {
				t.Errorf("expected content %q, got %q", tt.expectedContent, string(content))
			}
		})
	}
}

func TestUpdateFilesBeforeReleasePreservesPermissions(t *testing.T) {
	path := createTempFile(t, "0.0.0")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("failed to chmod temp file: %v", err)
	}

	pattern := patternForFile(path, "^.*$", "{{version}}")
	if err := updateFilesBeforeRelease([]string{pattern}, "1.0.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("expected permissions 0755, got %v", fi.Mode().Perm())
	}
}

func TestUpdateFilesBeforeReleaseInvalidPatternFormat(t *testing.T) {
	err := updateFilesBeforeRelease([]string{"VERSION"}, "1.0.0")
	if err == nil {
		t.Error("expected error but got none")
	}
}

func TestUpdateFilesBeforeReleaseInvalidRegex(t *testing.T) {
	path := createTempFile(t, "0.0.0")
	pattern := patternForFile(path, "[invalid(", "{{version}}")
	err := updateFilesBeforeRelease([]string{pattern}, "1.0.0")
	if err == nil {
		t.Error("expected error but got none")
	}
}

func TestUpdateFilesBeforeReleaseInvalidVersion(t *testing.T) {
	path := createTempFile(t, "0.0.0")
	pattern := patternForFile(path, "^.*$", "{{version}}")
	err := updateFilesBeforeRelease([]string{pattern}, "invalid-version")
	if err == nil {
		t.Error("expected error for invalid version but got none")
	}
}

func TestUpdateFilesBeforeReleaseNonExistentFile(t *testing.T) {
	err := updateFilesBeforeRelease([]string{"/nonexistent/file:^.*$:{{version}}"}, "1.0.0")
	if err == nil {
		t.Error("expected error for non-existent file but got none")
	}
}
