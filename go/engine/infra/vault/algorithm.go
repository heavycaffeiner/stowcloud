package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/twofish"
	"golang.org/x/crypto/xts"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// A container names neither its cipher nor its hash: both are found by
// decrypting the header with every combination until the plaintext magic
// appears. That is deliberate on VeraCrypt's part, so that a container is
// indistinguishable from random bytes, and it is why this file exists as a
// pair of tables rather than a configuration field.
const (
	// cipherKeyBytes is the key size of every cipher VeraCrypt offers: all
	// five are used in their 256-bit variant only.
	cipherKeyBytes = 32
	// layerKeyBytes is one cascade layer's key material: XTS needs a second
	// key of the same size for the tweak.
	layerKeyBytes = 2 * cipherKeyBytes
	// maxCascadeDepth is the longest cascade VeraCrypt defines.
	maxCascadeDepth = 3
	// derivedKeyBytes is what a key derivation must produce: enough for the
	// longest cascade, which every shorter one takes a prefix of. PBKDF2's
	// blocks are independent so a prefix is what a shorter derivation would
	// have produced anyway, but Argon2id mixes the requested length into its
	// own input, so asking for less than this yields different bytes.
	derivedKeyBytes = maxCascadeDepth * layerKeyBytes
)

// blockFunc is the shape golang.org/x/crypto/xts takes. Every cipher here
// has a 16-byte block, which is the only size that package accepts.
type blockFunc func([]byte) (cipher.Block, error)

// encryptionAlgorithm is one cipher or cascade a volume may be encrypted
// with.
//
// layers is in the order encryption applies them, which is the reverse of
// the order the name reads: AES-Twofish-Serpent encrypts with Serpent
// first, then Twofish, then AES. The key area holds every layer's primary
// key in that same order, then every layer's secondary key, so layer i
// takes key[i*32:(i+1)*32] and key[depth*32+i*32:...].
type encryptionAlgorithm struct {
	name   string
	layers []blockFunc
}

func newTwofishCipher(key []byte) (cipher.Block, error) {
	return twofish.NewCipher(key)
}

// supportedAlgorithms lists every cipher and cascade VeraCrypt can put on a
// non-system volume, cheapest first so that the common single-cipher cases
// are reached before the cascades during trial decryption.
//
// Held behind a function rather than a package variable, because a variable
// is state a caller could reassign; this table never changes at runtime.
func supportedAlgorithms() []encryptionAlgorithm {
	return []encryptionAlgorithm{
		{name: "AES", layers: []blockFunc{aes.NewCipher}},
		{name: "Serpent", layers: []blockFunc{newSerpentCipher}},
		{name: "Twofish", layers: []blockFunc{newTwofishCipher}},
		{name: "Camellia", layers: []blockFunc{newCamelliaCipher}},
		{name: "Kuznyechik", layers: []blockFunc{newKuznyechikCipher}},
		{name: "AES-Twofish", layers: []blockFunc{newTwofishCipher, aes.NewCipher}},
		{name: "Serpent-AES", layers: []blockFunc{aes.NewCipher, newSerpentCipher}},
		{name: "Twofish-Serpent", layers: []blockFunc{newSerpentCipher, newTwofishCipher}},
		{name: "Camellia-Kuznyechik", layers: []blockFunc{newKuznyechikCipher, newCamelliaCipher}},
		{name: "Camellia-Serpent", layers: []blockFunc{newSerpentCipher, newCamelliaCipher}},
		{name: "Kuznyechik-AES", layers: []blockFunc{aes.NewCipher, newKuznyechikCipher}},
		{name: "Kuznyechik-Twofish", layers: []blockFunc{newTwofishCipher, newKuznyechikCipher}},
		{name: "AES-Twofish-Serpent", layers: []blockFunc{newSerpentCipher, newTwofishCipher, aes.NewCipher}},
		{name: "Serpent-Twofish-AES", layers: []blockFunc{aes.NewCipher, newTwofishCipher, newSerpentCipher}},
		{name: "Kuznyechik-Serpent-Camellia", layers: []blockFunc{newCamelliaCipher, newSerpentCipher, newKuznyechikCipher}},
	}
}

// cascade is one volume's chain of XTS instances.
//
// A cascade is not XTS over a composed block cipher: each layer is a
// complete XTS pass over the whole buffer with its own key pair, run
// forward to encrypt and backward to decrypt.
type cascade struct {
	name   string
	layers []*xts.Cipher
}

// newCascade builds alg's chain from key, which must hold the primary keys
// for every layer followed by the secondary keys for every layer.
func newCascade(alg encryptionAlgorithm, key []byte) (*cascade, error) {
	depth := len(alg.layers)
	if need := depth * layerKeyBytes; len(key) < need {
		return nil, fmt.Errorf("vault: %s needs %d key bytes, got %d", alg.name, need, len(key))
	}
	layers := make([]*xts.Cipher, 0, depth)
	// xts.NewCipher splits one buffer in half, so each layer's primary and
	// secondary key are copied out of their two regions side by side.
	pair := make([]byte, layerKeyBytes)
	for i, newBlock := range alg.layers {
		copy(pair[:cipherKeyBytes], key[i*cipherKeyBytes:])
		copy(pair[cipherKeyBytes:], key[(depth+i)*cipherKeyBytes:])
		c, err := xts.NewCipher(newBlock, pair)
		if err != nil {
			return nil, fmt.Errorf("vault: %s layer %d: %w", alg.name, i, err)
		}
		layers = append(layers, c)
	}
	return &cascade{name: alg.name, layers: layers}, nil
}

// Encrypt runs the chain forward over one data unit. dst and src may alias.
func (c *cascade) Encrypt(dst, src []byte, unit uint64) {
	c.layers[0].Encrypt(dst, src, unit)
	for _, l := range c.layers[1:] {
		l.Encrypt(dst, dst, unit)
	}
}

// Decrypt runs the chain backward over one data unit. dst and src may alias.
func (c *cascade) Decrypt(dst, src []byte, unit uint64) {
	last := len(c.layers) - 1
	c.layers[last].Decrypt(dst, src, unit)
	for i := last - 1; i >= 0; i-- {
		c.layers[i].Decrypt(dst, dst, unit)
	}
}

// pbkdf2DefaultIterations is what a file container uses when the operator
// names no PIM. Every PRF uses the same count; the lower counts in
// VeraCrypt's tables belong to boot volumes, which this driver never opens.
const pbkdf2DefaultIterations = 500_000

// pbkdf2Iterations is the count for a given PIM. Zero means the operator
// named none.
func pbkdf2Iterations(pim uint32) int {
	if pim == 0 {
		return pbkdf2DefaultIterations
	}
	return 15_000 + int(pim)*1_000
}

// argon2DefaultPIM is the PIM whose cost parameters Argon2id uses when the
// operator names none: 416 MiB over 6 passes.
const argon2DefaultPIM = 12

// argon2Cost is Argon2id's memory cost in KiB and its pass count for a
// given PIM. Memory stops growing at PIM 31, where it reaches 1 GiB, and
// time keeps growing past it.
func argon2Cost(pim uint32) (memoryKiB, passes uint32) {
	if pim == 0 {
		pim = argon2DefaultPIM
	}
	capped := pim
	if capped > 31 {
		capped = 31
	}
	memoryKiB = (64 + (capped-1)*32) * 1024
	if pim > 31 {
		return memoryKiB, 13 + (pim - 31)
	}
	return memoryKiB, 3 + (pim-1)/3
}

// headerKDF is one header key derivation VeraCrypt offers for a file
// container. Exactly one of newHash and argon2 is set.
//
// token is what an operator writes to name this one, spelled the way
// VeraCrypt's own command line spells it.
type headerKDF struct {
	token   string
	name    string
	newHash func() hash.Hash
	argon2  bool
}

func newBlake2s256() hash.Hash {
	// The keyed constructor only fails on an oversized key and there is no
	// key here, so an unkeyed BLAKE2s-256 cannot fail to construct.
	h, err := blake2s.New256(nil)
	if err != nil {
		panic("vault: unkeyed blake2s-256 rejected: " + err.Error())
	}
	return h
}

// supportedHeaderKDFs are tried in order against every header this driver
// opens, cheapest first: a wrong passphrase runs all of them, and Argon2id
// alone costs hundreds of megabytes and several passes.
//
// Held behind a function rather than a package variable, because a variable
// is state a caller could reassign; this table never changes at runtime.
func supportedHeaderKDFs() []headerKDF {
	return []headerKDF{
		{token: "sha512", name: "PBKDF2-HMAC-SHA-512", newHash: sha512.New},
		{token: "sha256", name: "PBKDF2-HMAC-SHA-256", newHash: sha256.New},
		{token: "blake2s", name: "PBKDF2-HMAC-BLAKE2s-256", newHash: newBlake2s256},
		{token: "whirlpool", name: "PBKDF2-HMAC-Whirlpool", newHash: newWhirlpool},
		{token: "streebog", name: "PBKDF2-HMAC-Streebog", newHash: newStreebog512},
		{token: "argon2id", name: "Argon2id", argon2: true},
	}
}

// ErrUnknownHash names a hash token this build does not implement, which is
// a configuration mistake rather than a property of the container.
var ErrUnknownHash = errors.New("vault: unknown hash")

// headerKDFsFor narrows the trial set to the one an operator named, or
// returns the whole table when they named none.
//
// Naming it is worth doing. A container using the last entry in the table
// pays for every earlier derivation before its own is reached, and two of
// those are Whirlpool and Streebog at 500000 iterations, so an unhinted
// mount of a Streebog container costs a good fifteen seconds where a hinted
// one costs eight. VeraCrypt's own command line takes the same hint for the
// same reason.
func headerKDFsFor(token string) ([]headerKDF, error) {
	all := supportedHeaderKDFs()
	if token == "" {
		return all, nil
	}
	for _, kdf := range all {
		if kdf.token == token {
			return []headerKDF{kdf}, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownHash, token)
}

// deriveHeaderKey produces the full derivedKeyBytes of header key material
// for one KDF, which every cipher and cascade then takes its prefix of.
//
// password.Reveal() aliases the Secret's own buffer; the copy each KDF
// forces by requiring a string or its own allocation is the one copy
// secret.Secret's own contract already documents it cannot prevent.
func deriveHeaderKey(password secret.Secret, salt []byte, kdf headerKDF, pim uint32) ([]byte, error) {
	if kdf.argon2 {
		memoryKiB, passes := argon2Cost(pim)
		// Parallelism is 1 for every case VeraCrypt writes, and the length
		// is part of Argon2id's input, so it must be the full width here
		// rather than the selected cipher's.
		return argon2.IDKey(password.Reveal(), salt, passes, memoryKiB, 1, derivedKeyBytes), nil
	}
	return pbkdf2.Key(kdf.newHash, string(password.Reveal()), salt, pbkdf2Iterations(pim), derivedKeyBytes)
}
