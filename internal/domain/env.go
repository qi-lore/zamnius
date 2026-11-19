package domain

// Environment variable names that are part of the documented contract. They are
// defined once because three packages need them and a hint that names a variable
// the process does not read is worse than no hint at all (ADR-0010).
const (
	// ModelAPIKeyEnvVar carries the credential. It is never written to state.
	ModelAPIKeyEnvVar = "ZAMNIUS_MODEL_API_KEY"
	// ModelBaseURLEnvVar carries the provider root: the endpoint is process
	// configuration, never a workflow definition field.
	ModelBaseURLEnvVar = "ZAMNIUS_MODEL_BASE_URL"
)
