package wifi

import "fmt"

// ValidateSSID enforces the 802.11 SSID length limit (1-32 bytes) and
// rejects anything that could confuse wpa_cli's own quoted-string
// protocol or corrupt an nmcli argv, outright — same "validate
// strictly, reject ambiguous input" philosophy as
// cloudagent.validNodeNumber, rather than attempting to escape it.
func ValidateSSID(ssid string) error {
	if len(ssid) == 0 {
		return fmt.Errorf("SSID is required")
	}
	if len(ssid) > 32 {
		return fmt.Errorf("SSID must be 32 bytes or fewer")
	}
	return validateNoControlOrQuote("SSID", ssid)
}

// ValidatePSK enforces the WPA2 passphrase length range (8-63 ASCII
// characters), with the same rejection philosophy as ValidateSSID.
func ValidatePSK(psk string) error {
	if len(psk) < 8 || len(psk) > 63 {
		return fmt.Errorf("WiFi password must be 8-63 characters")
	}
	return validateNoControlOrQuote("WiFi password", psk)
}

// validateNoControlOrQuote rejects any rune below 0x20, the 0x7f DEL
// character, or a literal `"`/`\` -- the two quote/backslash
// characters specifically because wpa.go manually wraps SSID/PSK
// values in literal double quotes before passing them as an explicit
// argv element to wpa_cli (wpa_supplicant's own config-value protocol
// expects that), so an embedded `"` could break out of that quoting
// from wpa_supplicant's own parser's point of view, even though no
// shell is ever involved.
func validateNoControlOrQuote(label, s string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s must not contain control characters", label)
		}
		if r == '"' || r == '\\' {
			return fmt.Errorf(`%s must not contain '"' or '\'`, label)
		}
	}
	return nil
}
