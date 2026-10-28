package names

// Seed rows get deterministic names so the load test can verify every read
// against the expected value without a lookup table. loadtest/load.js carries
// a copy of these lists and of SeedName; TestSeedNameMatchesLoadTest keeps the
// two in sync.

// SeedFirst is the list of first names used by SeedName.
var SeedFirst = []string{
	"Ada", "Alan", "Barbara", "Charles", "Dennis", "Donald", "Edsger", "Frances",
	"Grace", "Hedy", "John", "Katherine", "Ken", "Leslie", "Linus", "Margaret",
	"Niklaus", "Radia", "Rob", "Shafi", "Tim", "Vint", "Whitfield", "Yukihiro",
}

// SeedLast is the list of last names used by SeedName.
var SeedLast = []string{
	"Lovelace", "Turing", "Liskov", "Shannon", "Ritchie", "Knuth", "Dijkstra", "Allen",
	"Hopper", "Lamarr", "McCarthy", "Johnson", "Thompson", "Lamport", "Torvalds", "Hamilton",
	"Wirth", "Perlman", "Pike", "Goldwasser", "Berners Lee", "Cerf", "Diffie", "Matsumoto",
	"van Rossum", "Kernighan", "Stroustrup",
}

// SeedName returns the name of the i-th seeded row (1-based). Every 50th row is
// a single word and some last names contain a space, so the split rule's edge
// cases are present in the data the backfill has to convert.
func SeedName(i int64) string {
	first := SeedFirst[i%int64(len(SeedFirst))]
	if i%50 == 0 {
		return first
	}
	last := SeedLast[(i/int64(len(SeedFirst)))%int64(len(SeedLast))]
	return first + " " + last
}
