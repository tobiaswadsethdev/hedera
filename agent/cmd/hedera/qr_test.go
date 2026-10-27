package main

import (
	"strings"
	"testing"
)

func TestRenderQR(t *testing.T) {
	out, err := renderQR("hedera://pair?v=1&c=abc")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// A version-2 code is 25 modules; with a 2-module quiet zone that is
	// 29 columns and ceil(29/2) = 15 rows of half blocks.
	if len(lines) != 15 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if strings.Count(l, "▀") != 29 {
			t.Fatalf("line has %d cells", strings.Count(l, "▀"))
		}
	}
}

func TestParsePairURL(t *testing.T) {
	p, err := parsePairURL("hedera://pair?v=1&m=m_x&n=studio&a=100.64.0.1&a=100.64.0.2&d=studio.tail.ts.net&p=7373&fp=sha256%3Aabc&c=code")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.hosts, ",") != "100.64.0.1,100.64.0.2,studio.tail.ts.net" || p.fp != "sha256:abc" || p.code != "code" {
		t.Fatalf("%+v", p)
	}
	for _, bad := range []string{"https://pair?v=1", "hedera://pair?v=2&p=1&fp=x&c=y&a=z", "hedera://pair?v=1&p=1&fp=x&a=z"} {
		if _, err := parsePairURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestParseInterspersed(t *testing.T) {
	fs := newFlags("t", "")
	kill := fs.Bool("kill", false, "")
	pos, rest, err := parseInterspersed(fs, []string{"s_1", "--kill", "s_2", "--", "-x", "--kill"})
	if err != nil || !*kill || strings.Join(pos, ",") != "s_1,s_2" || strings.Join(rest, ",") != "-x,--kill" {
		t.Fatalf("pos %v rest %v kill %v err %v", pos, rest, *kill, err)
	}
}
