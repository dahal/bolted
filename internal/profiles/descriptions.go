package profiles

// descriptions is the canonical one-line summary of each vendored
// starter profile. The keys here are the authoritative profile-name
// set: List() / Names() / Get() all consult it and any profile yaml
// without a description (or any description without a yaml) is treated
// as a bug — see profiles_test.go.
var descriptions = map[string]string{
	"minimal":   "base image only",
	"fullstack": "gh, gcloud, kubectl, terraform, jq, ripgrep, fzf",
	"data":      "gh, gcloud (bq), jq, duckdb, python3",
	"mobile":    "gh, java (JDK), fastlane",
}
