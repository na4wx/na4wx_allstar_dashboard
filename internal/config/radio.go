package config

import "fmt"

// RadioFiles are the two USB sound-card radio interface drivers HamVoIP
// supports; a node uses one or the other, never both, depending on
// which chan_usbradio/chan_simpleusb variant its rxchannel references
// (e.g. "USBRADIO/usb" vs "SimpleUSB/usb"). Both files share the same
// per-device field set except usbradio.conf's few USBRADIO-only knobs.
const (
	UsbradioConfFile  = "usbradio.conf"
	SimpleusbConfFile = "simpleusb.conf"
)

// RadioDevice is one USB sound-fob stanza: audio levels, carrier/CTCSS
// detection source, and PTT polarity. Field names match app_rpt's
// documented usbradio.conf/simpleusb.conf keys directly.
// JSON tags exist only for internal/cloudagent's relayed
// config.*RadioDevice actions (see that package's actions_radio.go);
// they have no effect on this struct's existing Go-field-name access
// elsewhere.
type RadioDevice struct {
	Name string `json:"name"` // stanza name, e.g. "usb"; matches the device half of rpt.conf's rxchannel (USBRADIO/<name>)

	CarrierFrom    string `json:"carrierFrom"`    // dsp | usb | usbinvert | vox | no
	CTCSSFrom      string `json:"ctcssFrom"`      // dsp | usb | usbinvert | no
	RXDemod        string `json:"rxDemod"`        // speaker | flat
	TXPrelim       string `json:"txPrelim"`       // yes | no
	TXMixA         string `json:"txMixA"`         // voice | composite | ...
	TXMixB         string `json:"txMixB"`         // no | ...
	InvertPTT      string `json:"invertPTT"`      // 0 | 1
	TXCTCSSDefault string `json:"txCTCSSDefault"` // tone frequency, e.g. "100.0"
	RXMixerSet     string `json:"rxMixerSet"`     // 0-999 receive volume
	TXMixerSet     string `json:"txMixerSet"`     // 0-999 transmit volume
	RXBoost        string `json:"rxBoost"`        // 0 | 1 — confirmed present on a real simpleusb.conf, not usbradio-only as once assumed here

	// PreEmphasis/DeEmphasis/PLFilter key names are inferred from the
	// simpleusb-tune-menu tool's on-screen labels ("PRE-emphasis",
	// "DE-emphasis", "PLfilter") rather than directly confirmed on a
	// live node the way the other fields were — correct if wrong.
	PreEmphasis string `json:"preEmphasis"` // 0 | 1
	DeEmphasis  string `json:"deEmphasis"`  // 0 | 1
	PLFilter    string `json:"plFilter"`    // 0 | 1

	// usbradio.conf only (chan_usbradio, not chan_simpleusb).
	HdwType string `json:"hdwType"` // 0 | 1
	Duplex3 string `json:"duplex3"` // 0 | 1
}

var radioDeviceFields = []struct {
	key string
	get func(*RadioDevice) *string
}{
	{"carrierfrom", func(d *RadioDevice) *string { return &d.CarrierFrom }},
	{"ctcssfrom", func(d *RadioDevice) *string { return &d.CTCSSFrom }},
	{"rxdemod", func(d *RadioDevice) *string { return &d.RXDemod }},
	{"txprelim", func(d *RadioDevice) *string { return &d.TXPrelim }},
	{"txmixa", func(d *RadioDevice) *string { return &d.TXMixA }},
	{"txmixb", func(d *RadioDevice) *string { return &d.TXMixB }},
	{"invertptt", func(d *RadioDevice) *string { return &d.InvertPTT }},
	{"txctcssdefault", func(d *RadioDevice) *string { return &d.TXCTCSSDefault }},
	{"rxmixerset", func(d *RadioDevice) *string { return &d.RXMixerSet }},
	{"txmixerset", func(d *RadioDevice) *string { return &d.TXMixerSet }},
	{"rxboost", func(d *RadioDevice) *string { return &d.RXBoost }},
	{"preemphasis", func(d *RadioDevice) *string { return &d.PreEmphasis }},
	{"deemphasis", func(d *RadioDevice) *string { return &d.DeEmphasis }},
	{"plfilter", func(d *RadioDevice) *string { return &d.PLFilter }},
	{"hdwtype", func(d *RadioDevice) *string { return &d.HdwType }},
	{"duplex3", func(d *RadioDevice) *string { return &d.Duplex3 }},
}

// isRadioFile guards against operating on any file other than the two
// known radio-interface configs, since these methods are exposed over
// HTTP with the filename taken from the URL.
func isRadioFile(file string) bool {
	return file == UsbradioConfFile || file == SimpleusbConfFile
}

// ApplyShariUSBPreset overwrites just the fields the SHARI USB
// interface is documented to need, leaving everything else on d
// (audio levels, tone frequency, etc.) untouched. Sourced from two
// independent write-ups that agree on these values — the SHARI PiHat
// setup guide (N8AR / kits4hams.com) and a community configuration
// post (gc0ezq.com) — for the SimpleUSB audio codec side only. This
// does NOT touch the SA818 radio module itself (frequency, CTCSS,
// squelch, transmit power) — that's programmed separately over a
// serial connection, not stored in this config file.
func ApplyShariUSBPreset(d *RadioDevice) {
	d.CarrierFrom = "usbinvert"
	d.CTCSSFrom = "no"
	d.RXBoost = "0"
	d.PreEmphasis = "1"
	d.DeEmphasis = "1"
	d.PLFilter = "1"
}

// nonDeviceSections lists stanza names that show up in
// usbradio.conf/simpleusb.conf but aren't devices — [general] is the
// standard Asterisk convention for a driver-wide defaults section
// (confirmed present, and empty, on a real HamVoIP node), not
// something app_rpt's rxchannel would ever reference.
var nonDeviceSections = map[string]bool{
	"general": true,
}

// ListRadioDevices returns device stanza names in file order, excluding
// non-device sections like [general].
func (s *Store) ListRadioDevices(file string) ([]string, error) {
	if !isRadioFile(file) {
		return nil, fmt.Errorf("config: %s is not a radio interface file", file)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load(file)
	if err != nil {
		return nil, err
	}
	var devices []string
	for _, sec := range f.Sections() {
		if !nonDeviceSections[sec] {
			devices = append(devices, sec)
		}
	}
	return devices, nil
}

// LoadRadioDevice reads one device stanza from file.
func (s *Store) LoadRadioDevice(file, name string) (*RadioDevice, error) {
	if !isRadioFile(file) {
		return nil, fmt.Errorf("config: %s is not a radio interface file", file)
	}
	if nonDeviceSections[name] {
		return nil, fmt.Errorf("config: %q is not a device section", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load(file)
	if err != nil {
		return nil, err
	}
	if !f.HasSection(name) {
		return nil, fmt.Errorf("config: device %s not found in %s", name, file)
	}
	d := &RadioDevice{Name: name}
	for _, fld := range radioDeviceFields {
		if v, ok := f.Get(name, fld.key); ok {
			*fld.get(d) = v
		}
	}
	return d, nil
}

// SaveRadioDevice writes d back to file, creating its section if it's a
// new device.
func (s *Store) SaveRadioDevice(file string, d *RadioDevice) error {
	if !isRadioFile(file) {
		return fmt.Errorf("config: %s is not a radio interface file", file)
	}
	if d.Name == "" {
		return fmt.Errorf("config: device name is required")
	}
	if nonDeviceSections[d.Name] {
		return fmt.Errorf("config: %q is a reserved section name, not a valid device name", d.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load(file)
	if err != nil {
		return err
	}

	f.EnsureSection(d.Name)
	for _, fld := range radioDeviceFields {
		v := *fld.get(d)
		if v == "" {
			f.Delete(d.Name, fld.key)
			continue
		}
		f.Set(d.Name, fld.key, v)
	}

	return s.save(file, f)
}

// DeleteRadioDevice removes a device stanza entirely.
func (s *Store) DeleteRadioDevice(file, name string) error {
	if !isRadioFile(file) {
		return fmt.Errorf("config: %s is not a radio interface file", file)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load(file)
	if err != nil {
		return err
	}
	f.DeleteSection(name)
	return s.save(file, f)
}
