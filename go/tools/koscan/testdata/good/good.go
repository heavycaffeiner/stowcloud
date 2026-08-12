// Package good is a fixture. It says the same thing in English, which is what
// the rule asks for, and koscan has to stay quiet about it.
package good

const message = "error.not_found"

func log() string {
	return message // the key, not the sentence
}
