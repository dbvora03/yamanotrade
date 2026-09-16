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

func TestODPTEndpointUsesDefaultAndEnvironmentOverride(t *testing.T) {
	t.Setenv("ODPT_ENDPOINT", "")
	if got := odptEndpoint(); got != "https://api.odpt.org/api/v4/odpt:Train" {
		t.Fatalf("default endpoint = %q", got)
	}

	t.Setenv("ODPT_ENDPOINT", " https://api-challenge.odpt.org/api/v4/odpt:Train ")
	if got := odptEndpoint(); got != "https://api-challenge.odpt.org/api/v4/odpt:Train" {
		t.Fatalf("override endpoint = %q", got)
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
