//go:build linux

package sizeguard

// SetAvailable replaces the free-space probe, for tests only.
//
// It exists because no volume a test runs on has a root reserve, so Bavail and
// Bfree report the same figure there and nothing else can tell the two apart.
// The distinction matters on a volume that does have one: counting the reserve
// promises room that ENOSPC then refuses.
func (g *Guard) SetAvailable(f func(dir string) (uint64, error)) { g.avail = f }

// AvailableBytes is the real probe, so a test can assert on what it counts.
func AvailableBytes(dir string) (uint64, error) { return availableBytes(dir) }

// AvailableFrom is the arithmetic the probe applies to a statfs result.
//
// Split out so a test can hand it a volume that reserves blocks. A live
// filesystem cannot be asked to reserve any, and comparing two real statfs
// calls races the machine's own free space.
func AvailableFrom(blockSize int64, blocksFree, blocksAvail uint64) uint64 {
	return availableFrom(blockSize, blocksFree, blocksAvail)
}
