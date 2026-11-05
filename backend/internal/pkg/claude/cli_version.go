package claude

// CLIVersion returns the Claude Code CLI version of the active client profile (#144).
// The User-Agent header and the body's cc_version both derive from it.
func CLIVersion() string {
	return ActiveClientProfile().CLIVersion
}

// DefaultUserAgent returns the active profile's Claude Code User-Agent.
func DefaultUserAgent() string {
	return ActiveClientProfile().UserAgent
}
