package settings

import (
	"os"
	"strings"
)

// NameEnv names this magpie where Settings doesn't (Name): a Docker or
// `magpie web` deployment's name, set where it is deployed.
const NameEnv = "MAGPIE_NAME"

// NameMost is how many characters Name keeps: enough for a tab's title.
const NameMost = 60

// CleanName is name as Name keeps it: one line, its spaces collapsed,
// NameMost characters at most.
func CleanName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > NameMost {
		name = strings.TrimSpace(string(r[:NameMost]))
	}
	return name
}

// EnvName is MAGPIE_NAME, cleaned as Name is.
func EnvName() string { return CleanName(os.Getenv(NameEnv)) }

// InstanceName is what this magpie is called: the Name set in Settings,
// which MAGPIE_NAME never overrides, else MAGPIE_NAME, else "".
func InstanceName(s Settings) string {
	if n := CleanName(s.Name); n != "" {
		return n
	}
	return EnvName()
}
