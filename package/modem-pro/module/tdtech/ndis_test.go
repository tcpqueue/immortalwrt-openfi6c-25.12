package tdtech

import "testing"

func TestParseNDISStatus(t *testing.T) {
	states, err := ParseNDISStatus([]string{
		NDISStatusQuery,
		"^NDISSTATQRY: 1,1,,,\"IPV4\",0,,,\"IPV6\"",
		"^NDISSTATQRY: 3,0,,,\"IPV4\",1,,,\"IPV6\"",
		"^NDISSTATQRY: 4,0,,,\"IPV4\",3,,,\"IPV6\"",
		"^NDISSTATQRY: 11,0",
		"^NDISSTAT: 1,,,\"IPV4\"",
		"OK",
	})
	if err != nil || len(states) != 4 || states[1] != NDISConnected || states[3] != NDISConnected ||
		states[4] != NDISTransitioning || states[11] != NDISDisconnected {
		t.Fatalf("got %v, %v", states, err)
	}
	for _, input := range []string{"^NDISSTATQRY: 12,1", "^NDISSTATQRY: 1,", "^NDISSTATQRY: 1,7"} {
		if _, err := ParseNDISStatus([]string{input, "OK"}); err == nil {
			t.Errorf("accepted malformed status %q", input)
		}
	}
}
