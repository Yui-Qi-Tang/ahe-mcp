package z3

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver/linear"
)

// Parse only the concrete get-value grammar we request, not arbitrary SMT-LIB.
func parseAssignment(raw []byte, booleans, integers int) (linear.Assignment, error) {
	a := linear.Assignment{Booleans: map[int]bool{}, Integers: map[int]string{}}
	tokens := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ").Replace(string(raw)))
	pos := 0
	take := func(want string) bool {
		if pos >= len(tokens) || tokens[pos] != want {
			return false
		}
		pos++
		return true
	}
	bad := fmt.Errorf("missing, partial or malformed get-value response")
	if !take("sat") {
		return a, bad
	}
	if booleans+integers == 0 {
		if pos != len(tokens) {
			return a, bad
		}
		return a, nil
	}
	if !take("(") {
		return a, bad
	}
	for n := 0; n < booleans+integers; n++ {
		if !take("(") || pos >= len(tokens) {
			return a, bad
		}
		name := tokens[pos]
		pos++
		if len(name) < 2 {
			return a, bad
		}
		id, err := strconv.Atoi(name[1:])
		if err != nil || id < 1 || name[1:] != strconv.Itoa(id) {
			return a, bad
		}
		if pos >= len(tokens) {
			return a, bad
		}
		switch name[0] {
		case 'b':
			if id > booleans {
				return a, bad
			}
			if _, exists := a.Booleans[id]; exists {
				return a, bad
			}
			if tokens[pos] != "true" && tokens[pos] != "false" {
				return a, bad
			}
			a.Booleans[id] = tokens[pos] == "true"
			pos++
		case 'i':
			if id > integers {
				return a, bad
			}
			if _, exists := a.Integers[id]; exists {
				return a, bad
			}
			value := tokens[pos]
			pos++
			if value == "(" {
				if !take("-") || pos >= len(tokens) {
					return a, bad
				}
				value = "-" + tokens[pos]
				pos++
				if !take(")") {
					return a, bad
				}
			}
			if _, err = linear.Integer(value); err != nil {
				return a, bad
			}
			a.Integers[id] = value
		default:
			return a, bad
		}
		if !take(")") {
			return a, bad
		}
	}
	if !take(")") || pos != len(tokens) {
		return a, bad
	}
	return a, nil
}
