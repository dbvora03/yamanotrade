package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseEventCollections(t *testing.T) {
	address := word("0000000000000000000000001111111111111111111111111111111111111111")
	hash := word("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	cases := []struct {
		name       string
		log        ethLog
		collection string
		field      string
		want       any
	}{
		{"series created", fixture(seriesCreatedTopic, []string{word("1"), hash, hash}, nil), "series_created", "series_id", "1"},
		{"round opened", fixture(roundOpenedTopic, []string{word("1"), word("2"), hash}, []string{word("3"), word("4")}), "round_opened", "scheduled_arrival", "3"},
		{"range bet placed", fixture(rangeBetPlacedTopic, []string{word("1"), word("2"), word("3")}, []string{address, word("4"), word("5"), word("6"), word("7")}), "range_bet_placed", "bettor", "0x1111111111111111111111111111111111111111"},
		{"oracle report accepted", fixture(oracleReportAcceptedTopic, []string{word("1"), word("2"), word("3")}, []string{word("4")}), "oracle_report_accepted", "nonce", "3"},
		{"round settled", fixture(roundSettledTopic, []string{word("1"), word("2")}, []string{word("3"), word("4"), word("5")}), "round_settled", "winning_bucket", int32(3)},
		{"round refunded", fixture(roundRefundedTopic, []string{word("1"), word("2")}, []string{hash}), "round_refunded", "reason", hash},
		{"ticket claimed", fixture(ticketClaimedTopic, []string{word("1"), address}, []string{word("2")}), "ticket_claimed", "owner", "0x1111111111111111111111111111111111111111"},
		{"ticket refunded", fixture(ticketRefundedTopic, []string{word("1"), address}, []string{word("2")}), "ticket_refunded", "amount", "2"},
		{"oracle changed", fixture(oracleChangedTopic, []string{address}, []string{word("1")}), "oracle_changed", "enabled", true},
		{"oracle threshold changed", fixture(oracleThresholdChangedTopic, nil, []string{word("2")}), "oracle_threshold_changed", "threshold", "2"},
		{"fees withdrawn", fixture(feesWithdrawnTopic, []string{address}, []string{word("2")}), "fees_withdrawn", "amount", "2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collection, document, err := parseEvent(tc.log)
			if err != nil {
				t.Fatal(err)
			}
			if collection != tc.collection {
				t.Fatalf("collection = %q, want %q", collection, tc.collection)
			}
			if got := document[tc.field]; got != tc.want {
				t.Fatalf("%s = %#v, want %#v", tc.field, got, tc.want)
			}
		})
	}
}

func TestParseEventRejectsInvalidBool(t *testing.T) {
	_, _, err := parseEvent(fixture(oracleChangedTopic, []string{word("1")}, []string{word("2")}))
	if err == nil || !strings.Contains(err.Error(), "invalid boolean") {
		t.Fatalf("expected invalid boolean error, got %v", err)
	}
}

func fixture(topic string, indexed, data []string) ethLog {
	topics := append([]string{topic}, indexed...)
	return ethLog{
		Address:         "0x1111111111111111111111111111111111111111",
		Topics:          topics,
		Data:            "0x" + strings.Join(stripPrefix(data), ""),
		BlockNumber:     "0x1",
		BlockHash:       "0xabc",
		TransactionHash: "0xdef",
		LogIndex:        "0x0",
	}
}

func stripPrefix(words []string) []string {
	result := make([]string, len(words))
	for n, value := range words {
		result[n] = strings.TrimPrefix(value, "0x")
	}
	return result
}

func word(value string) string {
	if len(value) <= 16 {
		return fmt.Sprintf("0x%064x", mustParseUint(value))
	}
	return "0x" + value
}

func mustParseUint(value string) uint64 {
	var parsed uint64
	if _, err := fmt.Sscan(value, &parsed); err != nil {
		panic(err)
	}
	return parsed
}
