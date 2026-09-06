package drill

// SandboxResourceClaim is one durable expected Docker resource generation.
type SandboxResourceClaim struct {
	Kind       string
	Name       string
	Generation string
}
