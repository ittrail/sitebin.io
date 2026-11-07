package main

import (
	"strings"
	"testing"
)

func TestAgentRefusesAnIncompleteSetup(t *testing.T) {
	data := t.TempDir()
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no data":         {nil, "-data is required"},
		"data not a dir":  {[]string{"-data", data + "/missing"}, "is not a directory"},
		"unknown source":  {[]string{"-data", data, "-source", "s3"}, "borg or dir"},
		"dir without dir": {[]string{"-data", data, "-source", "dir"}, "needs -dir"},
	} {
		err := runAgent(c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
