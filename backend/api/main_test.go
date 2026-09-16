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

func TestPositiveInt(t *testing.T) {
	t.Setenv("TEST_POSITIVE_INT", "12")
	if got := positiveInt("TEST_POSITIVE_INT", 4); got != 12 {
		t.Fatalf("got %d", got)
	}
	t.Setenv("TEST_POSITIVE_INT", "0")
	if got := positiveInt("TEST_POSITIVE_INT", 4); got != 4 {
		t.Fatalf("got %d", got)
	}
}
