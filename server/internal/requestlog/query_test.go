package requestlog

import "testing"

func TestNumericString(t *testing.T) {
	for in, want := range map[string]string{
		"0": "0", "1": "0.000000001", "1500000000": "1.5", "-2000000000": "-2",
		"123456789012345678901234": "123456789012345.678901234",
	} {
		s := in
		if got := numericString(&s); got != want {
			t.Errorf("numericString(%s) = %s, want %s", in, got, want)
		}
	}
	if numericString(nil) != "0" {
		t.Error("nil must be 0")
	}
}
