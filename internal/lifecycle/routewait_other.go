//go:build !windows

package lifecycle

// hasIPv4DefaultRoute is inert off Windows; the routing race it guards is a
// boot-time Windows behaviour.
func hasIPv4DefaultRoute() (bool, error) {
	return true, nil
}
