package main

import "testing"

func TestNormalizePlatform(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Android hardware devices used in Raptor/Talos mobile runs
		{"android-hw-p6-13-0-arm7-shippable", "android"},
		{"android-hw-p6-13-0-arm64-shippable", "android"},
		{"android-hw-a55-14-0-arm7-shippable", "android"},
		{"android-hw-a55-14-0-arm64-shippable", "android"},
		// Linux platforms common in AWSY and Talos
		{"linux1804-64-shippable-qr", "linux"},
		{"linux2204-64-qr", "linux"},
		{"linux2404-64-shippable", "linux"},
		// macOS platforms used in Raptor
		{"macosx1015-64-shippable-qr", "macos"},
		{"macosx1470-64-shippable", "macos"},
		// Windows platforms used in Talos and mozperftest
		{"windows11-64-2009-shippable", "windows"},
		{"windows10-64-2009-shippable", "windows"},
		// Unknown/unrecognised
		{"unknown-platform", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := normalizePlatform(tt.input)
		if got != tt.expected {
			t.Errorf("normalizePlatform(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
