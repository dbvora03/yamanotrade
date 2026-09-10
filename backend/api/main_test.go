package main

import "testing"

func TestODPTConsumerKeyUsesDefaultAndEnvironmentOverride(t *testing.T) {
	t.Setenv("ODPT_CONSUMER_KEY", "")
	if got := odptConsumerKey(); got != defaultODPTConsumerKey {
		t.Fatal("expected the committed default key")
	}

	t.Setenv("ODPT_CONSUMER_KEY", "test-override")
	if got := odptConsumerKey(); got != "test-override" {
		t.Fatal("expected the environment override")
	}
}
