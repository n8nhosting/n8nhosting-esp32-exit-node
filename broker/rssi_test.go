package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatsRSSIBoundsAndBackwardCompatibility(t *testing.T) {
	for _, value := range []string{"-127", "-67", "-1", "0"} {
		st, ok := parseStats("heap=140000 cpu=12 rssi=" + value)
		if !ok || st.RSSI == nil || st.HeapFree != 140000 || st.CPUPct != 12 {
			t.Fatalf("RSSI %s lost its sample: %+v", value, st)
		}
		data, err := json.Marshal(st)
		if err != nil || !strings.Contains(string(data), `"rssi":`+value) {
			t.Fatalf("RSSI %s JSON=%s err=%v", value, data, err)
		}
	}
	for _, token := range []string{"", " rssi=-128", " rssi=1", " rssi=unknown", " rssi=-67.5", " rssi=999999999999999999"} {
		st, ok := parseStats("heap=140000 cpu=12" + token)
		if !ok || st.RSSI != nil || st.HeapFree != 140000 || st.CPUPct != 12 {
			t.Fatalf("invalid/absent %q changed old fields: %+v", token, st)
		}
		data, _ := json.Marshal(st)
		if strings.Contains(string(data), `"rssi"`) {
			t.Fatalf("unknown RSSI must be omitted: %s", data)
		}
	}
	if _, ok := parseStats("rssi=-128"); ok {
		t.Fatal("invalid RSSI alone must not become a sample")
	}
}
