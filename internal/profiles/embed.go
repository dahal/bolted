package profiles

import "embed"

// profilesFS holds the vendored bolted.yaml starter templates. Each
// file in `files/` is a self-contained, valid `provision.BoltedProfile`
// document — see profiles_test.go for the round-trip parse check.
//
//go:embed files/*.yaml
var profilesFS embed.FS
