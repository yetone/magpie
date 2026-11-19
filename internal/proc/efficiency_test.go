package proc

import "testing"

func TestEfficiencyActivePolicy(t *testing.T) {
	for _, c := range []struct {
		name       string
		onBattery  bool
		saver      bool
		foreground bool
		want       bool
	}{
		{"AC in background", false, false, false, false},
		{"AC in foreground", false, false, true, false},
		{"battery in background", true, false, false, true},
		{"battery in foreground", true, false, true, false},
		{"Energy Saver on AC in background", false, true, false, true},
		{"Energy Saver on AC in foreground", false, true, true, false},
		{"battery and Energy Saver in background", true, true, false, true},
		{"battery and Energy Saver in foreground", true, true, true, false},
	} {
		got := EfficiencyActive(PowerState{OnBattery: c.onBattery, EnergySaver: c.saver}, c.foreground)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPowerFromWindowsStatus(t *testing.T) {
	for _, c := range []struct {
		name       string
		acLine     byte
		statusFlag byte
		ok         bool
		want       PowerState
	}{
		{"failed read never assumes battery or saver", 0, 1, false, PowerState{}},
		{"unplugged without saver", 0, 0, true, PowerState{OnBattery: true}},
		{"plugged in without saver", 1, 0, true, PowerState{}},
		{"plugged in with Energy Saver", 1, 1, true, PowerState{EnergySaver: true}},
		{"unplugged with Energy Saver", 0, 1, true, PowerState{OnBattery: true, EnergySaver: true}},
		{"unknown AC status without saver", 255, 0, true, PowerState{}},
		{"unknown AC status with Energy Saver", 255, 1, true, PowerState{EnergySaver: true}},
		{"unrecognised status flag", 1, 2, true, PowerState{}},
	} {
		got := powerFromWindowsStatus(c.acLine, c.statusFlag, c.ok)
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
