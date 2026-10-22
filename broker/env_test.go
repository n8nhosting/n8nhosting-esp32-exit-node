package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// An S3 with a BMP280 adds room temperature (centi-degC), pressure (Pa) and
// the chip's own temperature to STATS; the broker reports them in degC/hPa.
func TestStatsRoomSensorFields(t *testing.T) {
	st, ok := parseStats("heap=140000 cpu=12 tempc=2508 press=100653 chiptempc=4510")
	if !ok || st.HeapFree != 140000 || st.CPUPct != 12 {
		t.Fatalf("sensor fields broke the sample: %+v", st)
	}
	if st.RoomTempC == nil || *st.RoomTempC != 25.08 || st.PressureHPa == nil || *st.PressureHPa != 1006.53 ||
		st.ChipTempC == nil || *st.ChipTempC != 45.1 {
		t.Fatalf("sensor values: %+v", st)
	}
	data, _ := json.Marshal(st)
	for _, want := range []string{`"room_temp_c":25.08`, `"pressure_hpa":1006.53`, `"chip_temp_c":45.1`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("JSON %s lacks %s", data, want)
		}
	}
	if st, _ = parseStats("heap=1 tempc=-512"); st.RoomTempC == nil || *st.RoomTempC != -5.12 {
		t.Fatalf("a cold room keeps its sign: %+v", st)
	}
}

// Values outside what a BMP280 (-40..85 degC, 300..1100 hPa) or the chip
// sensor can report are dropped, and a board without a sensor sends none.
func TestStatsRoomSensorBounds(t *testing.T) {
	for _, token := range []string{"", " tempc=-4001", " tempc=8501", " tempc=25.1", " tempc=x",
		" press=29999", " press=110001", " press=-1", " chiptempc=-4001", " chiptempc=12501"} {
		st, ok := parseStats("heap=140000 cpu=12" + token)
		if !ok || st.RoomTempC != nil || st.PressureHPa != nil || st.ChipTempC != nil || st.HeapFree != 140000 {
			t.Fatalf("invalid/absent %q: %+v", token, st)
		}
		data, _ := json.Marshal(st)
		if strings.Contains(string(data), "temp") || strings.Contains(string(data), "pressure") {
			t.Fatalf("unknown sensor values must be omitted: %s", data)
		}
	}
	if _, ok := parseStats("tempc=9999"); ok {
		t.Fatal("an invalid sensor value alone must not become a sample")
	}
}
