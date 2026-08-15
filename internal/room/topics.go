package room

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
)

// Topic is one row of the CSV: the location/subject everyone sees, and the
// single hint word the imposter gets instead.
type Topic struct {
	Name string
	Hint string
}

// LoadTopics reads a two-column CSV: topic,hint
// A leading "topic,hint" header row is optional and skipped if present.
// Blank lines and rows missing either column are ignored rather than fatal,
// so a stray newline at the end of the file won't stop the server.
func LoadTopics(path string) ([]Topic, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true

	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	topics := make([]Topic, 0, len(rows))
	for i, row := range rows {
		if len(row) < 2 {
			continue
		}
		name := strings.TrimSpace(row[0])
		hint := strings.TrimSpace(row[1])
		if name == "" || hint == "" {
			continue
		}
		if i == 0 && strings.EqualFold(name, "topic") {
			continue
		}
		topics = append(topics, Topic{Name: name, Hint: hint})
	}

	if len(topics) == 0 {
		return nil, fmt.Errorf("%s has no usable rows (expected: topic,hint)", path)
	}
	return topics, nil
}
