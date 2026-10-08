package cli

// The relay argv tests compare exact argument lists, so the CLI's
// known_hosts path is fixed for the whole package.
func init() { knownHostsPath = func() string { return "/cfg/known_hosts" } }
