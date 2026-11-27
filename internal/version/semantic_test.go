package version

import "testing"

func TestSemanticPrecedenceAndValidation(t *testing.T) {
	for _, test := range []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.9.9", 1},
		{"v1.0.0+build.2", "1.0.0+build.99", 0},
		{"1.0.0-rc.10", "1.0.0-rc.2", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"999999999999999999999.0.0", "1000000000000000000000.0.0", -1},
	} {
		got, err := Compare(test.a, test.b)
		if err != nil || (got < 0) != (test.want < 0) || (got > 0) != (test.want > 0) {
			t.Errorf("%s vs %s = %d, %v", test.a, test.b, got, err)
		}
	}
	for _, value := range []string{"", "1.0", "01.0.0", "1.0.0-01", "1.0.0-rc..1", "1.0.0-", "1.0.0+", "1.0.0\n", "1.0.0-中文", "../1.0.0"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("invalid version accepted: %q", value)
		}
	}
}

func TestCoreVersionRanges(t *testing.T) {
	for _, test := range []struct {
		value, requirement string
		want               bool
	}{
		{"0.1.0", ">=0.1.0 <0.2.0", true},
		{"0.2.0", ">=0.1.0, <0.2.0", false},
		{"1.4.8", "^1.2.3", true},
		{"2.0.0", "^1.2.3", false},
		{"0.2.9", "^0.2.3", true},
		{"0.3.0", "^0.2.3", false},
		{"0.0.4", "^0.0.3", false},
		{"1.2.9", "~1.2.0", true},
		{"1.3.0", "~1.2.0", false},
		{"2.1.0", ">=1.0.0 <2.0.0 || >=2.1.0", true},
		{"1.0.0-rc.2", ">=1.0.0", false},
		{"1.0.0-rc.10", ">=1.0.0-rc.2", true},
		{"1.0.0", "*", true},
	} {
		got, err := Satisfies(test.value, test.requirement)
		if err != nil || got != test.want {
			t.Errorf("%s satisfies %s = %t, %v", test.value, test.requirement, got, err)
		}
	}
	for _, invalid := range []string{">=1", "latest", "||", ">=1.0.0 ||", "=>1.0.0", "1.0.x", " >=1.0.0"} {
		if err := ValidateRange(invalid); err == nil {
			t.Errorf("invalid range accepted: %q", invalid)
		}
	}
}

func TestDevelopmentCompatibilityDoesNotPromoteReleaseCandidates(t *testing.T) {
	original := Version
	defer func() { Version = original }()
	Version = "0.1.0-dev"
	if err := Compatible(">=0.1.0 <0.2.0"); err != nil {
		t.Fatal(err)
	}
	Version = "0.1.0-rc.1"
	if err := Compatible(">=0.1.0"); err == nil {
		t.Fatal("release candidate promoted to stable")
	}
}
