package version

import (
	"errors"
	"regexp"
	"strings"
)

// Semantic keeps numeric components as strings: valid versions need not fit
// into an integer, and comparison must not silently overflow.
type Semantic struct {
	Major, Minor, Patch string
	Pre, Build          string
}

var semanticPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
var comparatorPattern = regexp.MustCompile(`^(>=|<=|>|<|=|\^|~)?(v?[0-9][0-9A-Za-z.+-]*)$`)

func Parse(value string) (Semantic, error) {
	if len(value) > 64 {
		return Semantic{}, errors.New("语义版本最多 64 个字符")
	}
	m := semanticPattern.FindStringSubmatch(value)
	if m == nil {
		return Semantic{}, errors.New("版本必须使用 major.minor.patch，可附加预发布标识和构建信息")
	}
	for _, part := range strings.Split(m[4], ".") {
		if numeric(part) && len(part) > 1 && part[0] == '0' {
			return Semantic{}, errors.New("预发布版本中的数字不能包含前导零")
		}
	}
	return Semantic{m[1], m[2], m[3], m[4], m[5]}, nil
}

func (v Semantic) String() string {
	s := v.Major + "." + v.Minor + "." + v.Patch
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Identity omits build metadata, which never changes version precedence.
func (v Semantic) Identity() string {
	v.Build = ""
	return v.String()
}

func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func compareNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func (v Semantic) Compare(other Semantic) int {
	for _, pair := range [][2]string{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}} {
		if c := compareNumber(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	if v.Pre == other.Pre {
		return 0
	}
	if v.Pre == "" {
		return 1
	}
	if other.Pre == "" {
		return -1
	}
	a, b := strings.Split(v.Pre, "."), strings.Split(other.Pre, ".")
	for i := 0; i < min(len(a), len(b)); i++ {
		c := strings.Compare(a[i], b[i])
		switch {
		case numeric(a[i]) && numeric(b[i]):
			c = compareNumber(a[i], b[i])
		case numeric(a[i]) && !numeric(b[i]):
			c = -1
		case !numeric(a[i]) && numeric(b[i]):
			c = 1
		}
		if c != 0 {
			return c
		}
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
}

func Compare(a, b string) (int, error) {
	left, err := Parse(a)
	if err != nil {
		return 0, err
	}
	right, err := Parse(b)
	if err != nil {
		return 0, err
	}
	return left.Compare(right), nil
}

type constraint struct {
	op string
	v  Semantic
}

func parseRange(expression string) ([][]constraint, error) {
	if len(expression) > 128 || strings.TrimSpace(expression) != expression {
		return nil, errors.New("NewPress 版本范围无效")
	}
	if expression == "" || expression == "*" {
		return [][]constraint{{}}, nil
	}
	var alternatives [][]constraint
	for _, branch := range strings.Split(expression, "||") {
		tokens := strings.Fields(strings.ReplaceAll(branch, ",", " "))
		if len(tokens) == 0 || len(tokens) > 8 {
			return nil, errors.New("NewPress 版本范围无效")
		}
		var all []constraint
		for _, token := range tokens {
			match := comparatorPattern.FindStringSubmatch(token)
			if match == nil {
				return nil, errors.New("NewPress 范围支持比较符、^、~、空格交集和 || 并集")
			}
			v, err := Parse(match[2])
			if err != nil {
				return nil, err
			}
			all = append(all, constraint{match[1], v})
		}
		alternatives = append(alternatives, all)
	}
	return alternatives, nil
}

func ValidateRange(expression string) error {
	_, err := parseRange(expression)
	return err
}

func Satisfies(value, expression string) (bool, error) {
	v, err := Parse(value)
	if err != nil {
		return false, err
	}
	alternatives, err := parseRange(expression)
	if err != nil {
		return false, err
	}
	for _, all := range alternatives {
		matches := true
		for _, c := range all {
			comparison := v.Compare(c.v)
			switch c.op {
			case "", "=":
				matches = matches && comparison == 0
			case ">=":
				matches = matches && comparison >= 0
			case ">":
				matches = matches && comparison > 0
			case "<=":
				matches = matches && comparison <= 0
			case "<":
				matches = matches && comparison < 0
			case "~":
				matches = matches && comparison >= 0 && v.Major == c.v.Major && v.Minor == c.v.Minor
			case "^":
				matches = matches && comparison >= 0 && v.Major == c.v.Major
				if c.v.Major == "0" {
					matches = matches && v.Minor == c.v.Minor
					if c.v.Minor == "0" {
						matches = matches && v.Patch == c.v.Patch
					}
				}
			}
		}
		if matches {
			return true, nil
		}
	}
	return false, nil
}

// Compatible uses the development build's target API version. Release
// candidates and other prereleases retain their normal SemVer precedence.
func Compatible(requirement string) error {
	current, err := Parse(Version)
	if err != nil {
		return err
	}
	if current.Pre == "dev" || strings.HasPrefix(current.Pre, "dev.") {
		current.Pre = ""
	}
	ok, err := Satisfies(current.String(), requirement)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("此版本要求 NewPress " + requirement + "，当前为 " + Version)
	}
	return nil
}
