// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lzma

import "errors"

// MatchAlgorithm identifies an algorithm to find matches in the
// dictionary.
//
// HashTable4, a hash table over four-byte words, is the only algorithm and
// the zero value, so the field can be left unset; another algorithm is a
// new value, never a change to this one.
type MatchAlgorithm byte

// Supported matcher algorithms.
const (
	HashTable4 MatchAlgorithm = iota
)

// String returns a string representation of the Matcher.
func (a MatchAlgorithm) String() string {
	if a == HashTable4 {
		return "HashTable4"
	}

	return "unknown"
}

var errUnsupportedMatchAlgorithm = errors.New(
	"lzma: unsupported match algorithm value",
)

// verify checks whether the matcher value is supported.
func (a MatchAlgorithm) verify() error {
	if a != HashTable4 {
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
