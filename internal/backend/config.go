package backend

// Config is the minimal slice of project configuration that the backend
// factory needs. The full project Config (spec 03) will embed or compose
// this struct so users can drive the override from config.yaml:
//
//	backend: auto   # auto | lima | wsl2 | mock
//
// Keeping the factory's input narrow means the factory has no opinion on
// where the value came from (env, flag, file) — that's the caller's job.
type Config struct {
	// Backend selects which implementation New returns. Allowed values:
	//   - "" or "auto": pick based on runtime.GOOS.
	//   - "lima":  force the Lima backend (Mac).
	//   - "wsl2":  force the WSL2 backend (Windows).
	//   - "mock":  return the in-memory recording mock — for tests and
	//              development on unsupported platforms.
	Backend string
}
