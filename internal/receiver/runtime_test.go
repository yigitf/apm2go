package receiver

import "testing"

// The language attribute is the one thing every producer spells differently,
// and getting it wrong is silent: a service simply shows the wrong icon, or
// none. These are the exact strings the producers apm2go receives from emit.
func TestDetectRuntime(t *testing.T) {
	cases := []struct {
		name     string
		resource map[string]string
		want     string
	}{
		{
			name:     "java agent",
			resource: map[string]string{attrTelemetrySDKLanguage: "java"},
			want:     "java",
		},
		{
			name:     "OBI reports nodejs",
			resource: map[string]string{attrTelemetrySDKLanguage: "nodejs"},
			want:     "nodejs",
		},
		{
			name:     "an SDK saying node means the same language",
			resource: map[string]string{attrTelemetrySDKLanguage: "node"},
			want:     "nodejs",
		},
		{
			name:     "case and padding are not part of the value",
			resource: map[string]string{attrTelemetrySDKLanguage: " Go "},
			want:     "go",
		},
		{
			name:     "falls back to the runtime product name",
			resource: map[string]string{attrProcessRuntimeName: "OpenJDK Runtime Environment"},
			want:     "java",
		},
		{
			name:     "OpenJ9 is a JVM too",
			resource: map[string]string{attrProcessRuntimeName: "Eclipse OpenJ9 VM"},
			want:     "java",
		},
		{
			name:     "the language attribute wins over the product name",
			resource: map[string]string{attrTelemetrySDKLanguage: "python", attrProcessRuntimeName: "CPython"},
			want:     "python",
		},
		{
			name:     "nothing said yields nothing guessed",
			resource: map[string]string{attrServiceName: "orders"},
			want:     "",
		},
		{
			name:     "an unrecognised product name is not a language",
			resource: map[string]string{attrProcessRuntimeName: "some-vendor-runtime"},
			want:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectRuntime(tc.resource); got != tc.want {
				t.Errorf("detectRuntime(%v) = %q, want %q", tc.resource, got, tc.want)
			}
		})
	}
}

// OBI reports a language only for the runtimes it can read out of a process's
// own symbols. For a native binary it reports that it does not know, in one of
// several spellings — and that has to read as "nothing said", so the pipeline
// can supply what apm2go's discovery established instead. Taking it at face
// value would badge nginx as a C program and lose the useful answer.
func TestDetectRuntimeTreatsUninformativeLanguagesAsUnknown(t *testing.T) {
	for _, language := range []string{"generic", "unknown", "c", "cpp", "C++", "native"} {
		got := detectRuntime(map[string]string{attrTelemetrySDKLanguage: language})
		if got != "" {
			t.Errorf("detectRuntime(%q) = %q, want empty so the resolver can answer", language, got)
		}
	}
}
