package onprem

import "testing"

func TestAvailableIPIgnoresForeignRangesAndInputOrder(t *testing.T) {
	for _, used := range [][]string{
		{"10.201.0.1", "10.201.0.2", "10.201.0.3", "10.200.0.4"},
		{"10.200.0.4", "10.201.0.3", "10.201.0.1", "10.201.0.2"},
	} {
		got, err := GetNextAvailableIP("10.201.0.0/24", used)
		if err != nil || got != "10.201.0.4" {
			t.Fatalf("got %s, %v", got, err)
		}
	}
}

func TestAvailableIPRejectsBroadcastAndExhaustion(t *testing.T) {
	if got, err := GetNextAvailableIP("10.201.0.0/30", []string{"10.201.0.1", "10.201.0.2"}); err == nil {
		t.Fatalf("allocated unusable address %s", got)
	}
}
