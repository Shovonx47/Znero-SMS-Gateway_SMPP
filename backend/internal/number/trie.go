package number

import (
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
)

type PrefixMatch struct {
	Prefix       string
	CountryID    uuid.UUID
	CountryName  string
	CountryISO2  string
	CallingCode  string
	OperatorID   *uuid.UUID
	OperatorName string
	NumberType   string
	Priority     int
}

type trieNode struct {
	children map[rune]*trieNode
	match    *PrefixMatch
}

func newTrieNode() *trieNode {
	return &trieNode{
		children: make(map[rune]*trieNode),
	}
}

// PrefixTrie stores number prefixes with atomic swap support for lock-free reads.
type PrefixTrie struct {
	root atomic.Pointer[trieNode]
}

func NewPrefixTrie() *PrefixTrie {
	t := &PrefixTrie{}
	t.root.Store(newTrieNode())
	return t
}

func (t *PrefixTrie) Reload(prefixes []prefix.NumberPrefix) {
	newRoot := newTrieNode()

	for _, p := range prefixes {
		// Clean prefix: remove '+' and whitespace
		clean := cleanNumber(p.Prefix)
		if clean == "" {
			continue
		}

		curr := newRoot
		for _, r := range clean {
			child, exists := curr.children[r]
			if !exists {
				child = newTrieNode()
				curr.children[r] = child
			}
			curr = child
		}

		opName := p.OperatorName
		if opName == "" {
			opName = "unknown"
		}

		match := &PrefixMatch{
			Prefix:       p.Prefix,
			CountryID:    p.CountryID,
			CountryName:  p.CountryName,
			CountryISO2:  p.CountryISO2,
			OperatorID:   p.OperatorID,
			OperatorName: opName,
			NumberType:   p.NumberType,
			Priority:     p.Priority,
		}

		// Prefer higher priority (lower priority int) or more specific data
		if curr.match == nil || p.Priority < curr.match.Priority {
			curr.match = match
		}
	}

	t.root.Store(newRoot)
}

// LongestMatch traverses the trie to find the longest matching prefix for the number.
func (t *PrefixTrie) LongestMatch(number string) (*PrefixMatch, bool) {
	clean := cleanNumber(number)
	if clean == "" {
		return nil, false
	}

	root := t.root.Load()
	if root == nil {
		return nil, false
	}

	curr := root
	var bestMatch *PrefixMatch

	for _, r := range clean {
		child, exists := curr.children[r]
		if !exists {
			break
		}
		curr = child
		if curr.match != nil {
			bestMatch = curr.match
		}
	}

	if bestMatch != nil {
		return bestMatch, true
	}
	return nil, false
}

func cleanNumber(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
