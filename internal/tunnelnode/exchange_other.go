//go:build !linux

package tunnelnode

// exchangeDirs swaps two paths. Outside Linux there is no atomic exchange;
// the three renames leave a short window in which a crash needs the
// rotation's rollback to put the names back.
func exchangeDirs(a, b string) error {
	tmp := a + ".exchange"
	if err := renameDurable(a, tmp); err != nil {
		return err
	}
	if err := renameDurable(b, a); err != nil {
		_ = renameDurable(tmp, a)
		return err
	}
	return renameDurable(tmp, b)
}
