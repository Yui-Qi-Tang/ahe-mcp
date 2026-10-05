package cadical

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

func exactLine(raw []byte, want string) bool {
	for _, line := range strings.FieldsFunc(string(raw), func(r rune) bool { return r == '\n' || r == '\r' }) {
		if line == want {
			return true
		}
	}
	return false
}
func assignment(raw []byte, count int) (map[int]bool, error) {
	values := make(map[int]bool, count)
	ended := false
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 || parts[0] != "v" {
			continue
		}
		for _, part := range parts[1:] {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("malformed assignment literal")
			}
			if n == 0 {
				ended = true
				continue
			}
			if ended {
				return nil, fmt.Errorf("assignment continues after terminator")
			}
			id := n
			if id < 0 {
				id = -id
			}
			if id < 1 || id > count {
				return nil, fmt.Errorf("assignment literal outside variable range")
			}
			value := n > 0
			if prior, ok := values[id]; ok && prior != value {
				return nil, fmt.Errorf("contradictory assignment literals")
			}
			values[id] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading assignment: %w", err)
	}
	if !ended || len(values) != count {
		return nil, fmt.Errorf("incomplete assignment")
	}
	return values, nil
}

func hasAssignment(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "v ") {
			return true
		}
	}
	return false
}
