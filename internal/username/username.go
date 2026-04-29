// Package username generates Reddit-style two-word handles like "polite-okapi"
// from curated adjective and animal wordlists. Returns lowercase, hyphen-joined
// strings constrained to [a-z-]; safe to drop into a UNIQUE column.
//
// We don't pull a third-party petname library because the lists are small,
// the surface area is tiny, and we want to control the vocabulary so nothing
// edgy or offensive can be generated for an anonymous campus app.
package username

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// adjectives — curated, calm, family-friendly, all single tokens, all
// lowercase ASCII. Avoid anything that could land as a slur or a value
// judgement on a person ("dumb", "fat", etc.).
var adjectives = []string{
	"ancient", "bold", "brave", "breezy", "bright", "brisk", "calm",
	"cheery", "chill", "clever", "cosmic", "cosy", "crafty", "crisp",
	"curious", "dapper", "deft", "eager", "fierce", "fluffy", "fond",
	"frosty", "fuzzy", "gentle", "gilded", "glad", "grand", "happy",
	"hazy", "humble", "jazzy", "jolly", "keen", "kind", "lively",
	"lonely", "loyal", "lucky", "mellow", "merry", "mighty", "mild",
	"misty", "modest", "neat", "nifty", "noble", "peppy", "perky",
	"plucky", "polite", "proud", "quick", "quiet", "regal", "ruddy",
	"sage", "salty", "scrappy", "sharp", "silent", "silly", "sleek",
	"smart", "snappy", "snug", "spry", "steady", "stout", "sturdy",
	"sunny", "swift", "tame", "tidy", "tiny", "vivid", "wise",
	"witty", "zealous", "zesty",
}

// animals — single-word common ASCII names; nothing too obscure that a
// user couldn't read it back to a friend. Order doesn't matter.
var animals = []string{
	"badger", "beaver", "bison", "boar", "camel", "crane", "crow",
	"deer", "dove", "duck", "eagle", "falcon", "finch", "fox", "frog",
	"gecko", "goat", "goose", "hawk", "heron", "ibex", "jay", "koala",
	"lemur", "lion", "llama", "lynx", "magpie", "marmot", "mink",
	"mole", "moth", "mule", "newt", "okapi", "orca", "oryx", "ostrich",
	"otter", "owl", "panda", "panther", "parrot", "pony", "puma",
	"quail", "rabbit", "ram", "rat", "raven", "ray", "robin", "salmon",
	"seal", "shark", "sloth", "snail", "sparrow", "swan", "tapir",
	"tiger", "toad", "trout", "turtle", "viper", "walrus", "weasel",
	"whale", "wolf", "yak", "zebra",
}

// Random returns a fresh "adjective-animal" candidate. There are roughly
// len(adjectives) * len(animals) distinct outputs; collisions in the DB
// are handled by the caller (claim with retry).
func Random() string {
	a := adjectives[randIndex(len(adjectives))]
	b := animals[randIndex(len(animals))]
	return a + "-" + b
}

// RandomWithSuffix returns a candidate with a 4-digit numeric suffix —
// used as a fallback when the bare two-word form keeps colliding. Format:
// "polite-okapi-4217". Suffix space (10000) on top of ~5000 base combos
// gives 50M+ unique handles before exhaustion.
func RandomWithSuffix() string {
	return fmt.Sprintf("%s-%04d", Random(), randIndex(10000))
}

// randIndex returns a uniformly distributed int in [0, n) using crypto/rand.
// We use crypto/rand instead of math/rand so usernames don't become
// predictable from a session timestamp (defensive — not load-bearing).
func randIndex(n int) int {
	if n <= 0 {
		return 0
	}
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	v := binary.BigEndian.Uint64(buf[:])
	return int(v % uint64(n))
}
