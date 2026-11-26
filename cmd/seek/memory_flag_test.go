package main

import (
	"strings"
	"testing"
)

// memory_flag_test.go covers card c_4rp2py's two decision functions.
// The E2E consequences (prompt bytes, tool registration, child pids)
// live in memory_isolated_e2e_test.go.

func TestParseMemoryFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    memoryMode
		wantErr bool
	}{
		{"", memoryReadWrite, false},    // absent / interpolated-empty → default
		{"rw", memoryReadWrite, false},  // explicit default
		{"ro", memoryReadOnly, false},   //
		{"off", memoryOff, false},       //
		{" RO ", memoryReadOnly, false}, // case + surrounding space
		{"OFF", memoryOff, false},
		{"read-only", memoryReadWrite, true}, // not a spelling we accept
		{"yes", memoryReadWrite, true},
		{"rw,ro", memoryReadWrite, true},
	}
	for _, c := range cases {
		got, err := parseMemoryFlag(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseMemoryFlag(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			if !strings.Contains(err.Error(), "off|ro|rw") {
				t.Errorf("parseMemoryFlag(%q) error should name the accepted values: %v", c.in, err)
			}
			continue
		}
		if got != c.want {
			t.Errorf("parseMemoryFlag(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMemoryMode_String(t *testing.T) {
	for mode, want := range map[memoryMode]string{
		memoryOff: "off", memoryReadOnly: "ro", memoryReadWrite: "rw",
	} {
		if got := mode.String(); got != want {
			t.Errorf("memoryMode.String() = %q, want %q", got, want)
		}
	}
}

// resolveMemoryMode is where --isolated folds into the memory axis.
// The table pins every combination the CLI accepts, including the two
// shapes that must exit 2: --isolated cannot coexist with a memory mode
// that LOADS memory (that would make the "same command, same bytes on
// any machine" promise false).
func TestResolveMemoryMode_Table(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		memoryGiven bool
		isolated    bool
		want        memoryMode
		wantErr     bool
	}{
		{"absent everything → historical rw", "", false, false, memoryReadWrite, false},
		{"explicit rw → rw", "rw", true, false, memoryReadWrite, false},
		{"explicit ro → ro", "ro", true, false, memoryReadOnly, false},
		{"explicit off → off", "off", true, false, memoryOff, false},
		// An explicitly empty value is "absent", same contract as -tools "".
		{"--memory= (empty) → rw", "", true, false, memoryReadWrite, false},
		{"isolated alone → off", "", false, true, memoryOff, false},
		{"isolated + explicit off → off", "off", true, true, memoryOff, false},
		{"isolated + --memory= (empty) → off", "", true, true, memoryOff, false},
		{"isolated + ro → exit 2", "ro", true, true, memoryReadOnly, true},
		{"isolated + rw → exit 2", "rw", true, true, memoryReadWrite, true},
		{"bad value → exit 2", "sometimes", true, false, memoryReadWrite, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveMemoryMode(c.raw, c.memoryGiven, c.isolated)
			if (err != nil) != c.wantErr {
				t.Fatalf("resolveMemoryMode(%q, given=%v, isolated=%v) err = %v, wantErr %v",
					c.raw, c.memoryGiven, c.isolated, err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("mode = %v, want %v", got, c.want)
			}
			if c.wantErr && !strings.Contains(err.Error(), "--isolated") && !strings.Contains(err.Error(), "--memory") {
				t.Errorf("error should name the offending flag: %v", err)
			}
		})
	}
}
