package room

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Topic is one entry from the topics file: the location/subject everyone
// sees, and the single hint word the imposter gets instead.
type Topic struct {
	Name string
	Hint string
}

// jsonTopic is one element of the topics JSON array. C is a category tag
// used to group entries in the source file; the game doesn't filter by it
// yet, so LoadTopics reads it only to skip past it.
type jsonTopic struct {
	C string `json:"c"`
	W string `json:"w"`
	H string `json:"h"`
}

// LoadTopics reads a JSON array of {c, w, h} objects - category, word, and
// hint. The category is currently unused; the word becomes the topic
// everyone sees and the hint goes to the imposter instead.
// Entries missing a word or hint are skipped rather than fatal, so a stray
// blank entry won't stop the server.
func LoadTopics(path string) ([]Topic, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var entries []jsonTopic
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	topics := make([]Topic, 0, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.W)
		hint := strings.TrimSpace(e.H)
		if name == "" || hint == "" {
			continue
		}
		topics = append(topics, Topic{Name: name, Hint: hint})
	}

	if len(topics) == 0 {
		return nil, fmt.Errorf("%s has no usable entries (expected: [{\"c\":...,\"w\":...,\"h\":...}])", path)
	}
	return topics, nil
}
