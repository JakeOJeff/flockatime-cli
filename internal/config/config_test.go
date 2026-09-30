package config

import "testing"

func TestCheckEndpoint(t *testing.T) {
	ok := []string{
		"https://flockatime.example.org",
		"https://flockatime.example.org/",
		"http://127.0.0.1:8787",
		"http://localhost:8787",
		"http://[::1]:8787",
	}
	bad := []string{
		"http://flockatime.example.org",
		"http://192.168.1.10:8787",
		"http://localhost.example.org",
		"ftp://flockatime.example.org",
		"flockatime.example.org",
		"",
	}
	for _, e := range ok {
		if err := CheckEndpoint(e); err != nil {
			t.Errorf("%q: unexpected error %v", e, err)
		}
	}
	for _, e := range bad {
		if CheckEndpoint(e) == nil {
			t.Errorf("%q: accepted, want refused", e)
		}
	}
}
