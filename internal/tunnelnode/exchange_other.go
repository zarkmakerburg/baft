//go:build !linux

package tunnelnode

import "os"

// exchangeDirs swaps two paths. Outside Linux there is no atomic exchange;
// the three renames leave a short window in which a crash needs the
// rotation's rollback to put the names back.
func exchangeDirs(a, b string) error {
	tmp := a + ".exchange"
	if err := os.Rename(a, tmp); err != nil {
		return err
	}
	if err := os.Rename(b, a); err != nil {
		_ = os.Rename(tmp, a)
		return err
	}
	return os.Rename(tmp, b)
}
