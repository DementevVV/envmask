package main

import (
	"os"
	"strings"
	"testing"
)

func TestSampleNoLeaks(t *testing.T) {
	src, err := os.ReadFile("testdata/sample.env")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []bool{false, true} {
		out, st := maskEnv(string(src), options{types: typ, keepComments: true})
		for _, bad := range []string{"S3cr3tP", "8f3a9c1e", "ghp_old", "MIIEow", "secret_abcdefgh", "abc123def456", "db.internal"} {
			if strings.Contains(out, bad) {
				t.Errorf("types=%v: leaked %q", typ, bad)
			}
		}
		if leak := findLeak(out, st.secrets); leak != "" {
			t.Errorf("types=%v: findLeak reported a leak", typ)
		}
		if !strings.Contains(out, "SMTP_PASSWORD=\n") {
			t.Errorf("types=%v: empty value must stay empty", typ)
		}
		if st.vars != 17 {
			t.Errorf("types=%v: vars=%d, want 17", typ, st.vars)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"true": "bool", "42": "int", "1.5": "float", "30s": "duration",
		"postgres://u:p@h/db": "url:postgres+creds", "redis://h:6379": "url:redis",
		"a@b.io": "email", "a,b,c": "list:3", "xyz": "string",
	}
	for in, want := range cases {
		if got := classify(in); got != want {
			t.Errorf("classify(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	for _, s := range []string{"changeme", "your_api_key", "<token>", "xxxx", "TODO", "key_here"} {
		if !placeholder.MatchString(s) {
			t.Errorf("%q should be a placeholder", s)
		}
	}
	if placeholder.MatchString("sk_live_9a8b7c") {
		t.Error("real-looking key must not be a placeholder")
	}
}
