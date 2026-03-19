package main

import "testing"

func TestNormalizePlatform(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Android hardware devices used in Raptor/Talos mobile runs
		{"android-hw-p6-13-0-arm7-shippable", "android-hw-p6"},
		{"android-hw-p6-13-0-arm64-shippable", "android-hw-p6"},
		{"android-hw-a55-14-0-arm7-shippable", "android-hw-a55"},
		{"android-hw-a55-14-0-arm64-shippable", "android-hw-a55"},
		// Linux platforms common in AWSY and Talos (version preserved)
		{"linux1804-64-shippable-qr", "linux1804"},
		{"linux2204-64-qr", "linux2204"},
		{"linux2404-64-shippable", "linux2404"},
		// macOS platforms used in Raptor (version preserved for intel vs apple silicon distinction)
		{"macosx1015-64-shippable-qr", "macosx1015"},
		{"macosx1470-64-shippable", "macosx1470"},
		// Windows platforms used in Talos and mozperftest
		{"windows11-64-2009-shippable", "windows11"},
		{"windows10-64-2009-shippable", "windows10"},
		// Unknown platforms returned as-is
		{"unknown-platform", "unknown-platform"},
		{"toolchains", "toolchains"},
		{"", ""},
	}

	for _, tt := range tests {
		got := normalizePlatform(tt.input)
		if got != tt.expected {
			t.Errorf("normalizePlatform(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
