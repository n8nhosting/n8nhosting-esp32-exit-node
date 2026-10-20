package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGostSlotConfigGenerates(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available; gost config generator test needs a POSIX shell")
	}
	cmd := exec.Command("sh", "../deploy/gost/gen-config.sh", "../deploy/gost/gost.yaml")
	cmd.Env = append(os.Environ(), "SLOT_COUNT=2", "PLUGIN_SECRET=ps", "SLOT_SECRET=ss")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: slot-1", `addr: ":3131"`, "name: slot-2", `addr: ":3132"`, "auth?slot=2", "username: slot-2", "password: ss", "name: chain-slot-1"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(string(out), "name: service-0") != 1 {
		t.Fatal("base config must be kept once")
	}
}
