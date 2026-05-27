package main

import "testing"

func TestEnvBoolTrue_RecognisesTruthyValues(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on", "TRUE", "Yes", "ON"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("SEEK_TEST_ENV_GATE", v)
			if !envBoolTrue("SEEK_TEST_ENV_GATE") {
				t.Errorf("expected %q to be truthy", v)
			}
		})
	}
}

func TestEnvBoolTrue_RecognisesFalsyValues(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "off", "FALSE", " ", "bogus"} {
		t.Run("falsy/"+v, func(t *testing.T) {
			t.Setenv("SEEK_TEST_ENV_GATE", v)
			if envBoolTrue("SEEK_TEST_ENV_GATE") {
				t.Errorf("expected %q to be falsy", v)
			}
		})
	}
}

func TestEnvBoolTrue_TrimsWhitespace(t *testing.T) {
	t.Setenv("SEEK_TEST_ENV_GATE", "  yes  ")
	if !envBoolTrue("SEEK_TEST_ENV_GATE") {
		t.Errorf("whitespace-padded truthy value should still be true")
	}
}
