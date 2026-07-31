package wifi

import "testing"

func TestValidateSSID(t *testing.T) {
	cases := []struct {
		name    string
		ssid    string
		wantErr bool
	}{
		{"empty", "", true},
		{"typical", "MyHomeNetwork", false},
		{"exactly 32 bytes", stringOfLen(32), false},
		{"33 bytes, one over", stringOfLen(33), true},
		{"embedded quote", `bad"ssid`, true},
		{"embedded backslash", `bad\ssid`, true},
		{"embedded control char", "bad\x01ssid", true},
		{"embedded newline", "bad\nssid", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateSSID(c.ssid)
			if (err != nil) != c.wantErr {
				t.Errorf("ValidateSSID(%q) error = %v, wantErr %v", c.ssid, err, c.wantErr)
			}
		})
	}
}

func TestValidatePSK(t *testing.T) {
	cases := []struct {
		name    string
		psk     string
		wantErr bool
	}{
		{"empty", "", true},
		{"7 chars, one short", stringOfLen(7), true},
		{"exactly 8 chars", stringOfLen(8), false},
		{"typical", "correcthorsebatterystaple", false},
		{"exactly 63 chars", stringOfLen(63), false},
		{"64 chars, one over", stringOfLen(64), true},
		{"embedded quote", `bad"password12`, true},
		{"embedded backslash", `bad\password12`, true},
		{"embedded control char", "bad\x01password12", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePSK(c.psk)
			if (err != nil) != c.wantErr {
				t.Errorf("ValidatePSK(%q) error = %v, wantErr %v", c.psk, err, c.wantErr)
			}
		})
	}
}

func stringOfLen(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
