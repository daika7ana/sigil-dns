//go:build windows

package lifecycle

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// hasIPv4DefaultRoute reports whether the IPv4 routing table has a /0 route,
// i.e. DHCPv4 (or a static default gateway) has finished installing it.
func hasIPv4DefaultRoute() (bool, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil {
		return false, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	for _, row := range table.Rows() {
		if row.DestinationPrefix.PrefixLength == 0 {
			return true, nil
		}
	}

	return false, nil
}
