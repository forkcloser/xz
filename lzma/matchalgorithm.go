// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import "errors"

// MatchAlgorithm identifies an algorithm to find matches in the
// dictionary.
//
// HashTable4, a hash table over four-byte words, is the only algorithm and
// the zero value, so the field can be left unset. Upstream also declared a
// BinaryTree matcher; it emitted matches at distances the decoder does not
// have (its own reader rejected its output for most inputs), compressed
// text to half its size where the hash table reaches a fraction of a
// percent, and degraded quadratically on repeated words. Upstream's roadmap
// lists "fix binary tree matcher" as open. It was removed before 1.0 rather
// than shipped as a working option; a working one can be added as a new
// value.
type MatchAlgorithm byte

// Supported matcher algorithms.
const (
	HashTable4 MatchAlgorithm = iota
)

// maStrings are used by the String method.
var maStrings = map[MatchAlgorithm]string{
	HashTable4: "HashTable4",
}

// String returns a string representation of the Matcher.
func (a MatchAlgorithm) String() string {
	if s, ok := maStrings[a]; ok {
		return s
	}
	return "unknown"
}

var errUnsupportedMatchAlgorithm = errors.New(
	"lzma: unsupported match algorithm value")

// verify checks whether the matcher value is supported.
func (a MatchAlgorithm) verify() error {
	if _, ok := maStrings[a]; !ok {
		return errUnsupportedMatchAlgorithm
	}
	return nil
}

func (a MatchAlgorithm) new(dictCap int) (m matcher, err error) {
	if a == HashTable4 {
		return newHashTable(dictCap, 4)
	}
	return nil, errUnsupportedMatchAlgorithm
}
