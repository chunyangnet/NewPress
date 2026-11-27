package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCDNConfigListsAndFiles(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_CDN_CLOUDFLARE_CIDRS", "173.245.48.1/20,173.245.48.0/20\n2606:4700::/32")
	file := filepath.Join(t.TempDir(), "esa.txt")
	if err := os.WriteFile(file, []byte("# fixture, not a live provider list\n47.246.0.0/16\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_CDN_ESA_CIDRS_FILE", file)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CDNProxies) != 2 || len(cfg.CDNProxies[0].CIDRs) != 2 || cfg.CDNProxies[0].CIDRs[0].String() != "173.245.48.0/20" || cfg.CDNProxies[1].Provider != "esa" {
		t.Fatalf("CDN config: %+v", cfg.CDNProxies)
	}
	t.Setenv("NEWPRESS_CDN_ESA_CIDRS", "")
	cfg, err = Load()
	if err != nil || len(cfg.CDNProxies) != 1 {
		t.Fatalf("explicit disable did not override file: %+v %v", cfg.CDNProxies, err)
	}
}

func TestCDNConfigRejectsUnsafeAmbiguousAndUnboundedLists(t *testing.T) {
	for _, value := range []string{"0.0.0.0/0", "::/0", "128.0.0.0/1", "8.0.0.0/5", "172.0.0.0/8", "192.128.0.0/9", "127.0.0.1/32", "10.0.0.0/8", "fc00::/7", "::ffff:8.8.8.8/128", "2001:db8::/32", "3fff::/20", "173.245.48.0/20,", strings.Repeat("8.8.8.0/24,", maximumCDNPrefixes) + "8.8.8.0/24", strings.Repeat(" ", maximumCDNListBytes+1)} {
		if _, err := parseCDNCIDRs(value); err == nil {
			t.Fatalf("accepted invalid list (length %d)", len(value))
		}
	}
	isolateConfigEnvironment(t)
	t.Setenv("NEWPRESS_CDN_CLOUDFLARE_CIDRS", "173.245.48.0/20")
	t.Setenv("NEWPRESS_CDN_ESA_CIDRS", "173.245.48.1/32")
	if _, err := Load(); err == nil {
		t.Fatal("overlapping providers accepted")
	}
	t.Setenv("NEWPRESS_CDN_ESA_CIDRS", "")
	t.Setenv("NEWPRESS_TRUSTED_PROXY_CIDRS", "173.245.0.0/16")
	if _, err := Load(); err == nil {
		t.Fatal("overlapping generic proxy and CDN accepted")
	}
}

func TestCDNFileIsBoundedAndEmptyFileRejected(t *testing.T) {
	isolateConfigEnvironment(t)
	file := filepath.Join(t.TempDir(), "cidrs.txt")
	t.Setenv("NEWPRESS_CDN_EDGEONE_CIDRS_FILE", file)
	for _, data := range []string{"", strings.Repeat("x", maximumCDNListBytes+1)} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
}

func TestCDNOriginTokenValidationAndFile(t *testing.T) {
	isolateConfigEnvironment(t)
	key := "NEWPRESS_CDN_ESA_ORIGIN_TOKEN"
	t.Setenv("NEWPRESS_CDN_ESA_CIDRS", "47.246.0.0/16")
	for _, token := range []string{"short", strings.Repeat("a", 513), strings.Repeat("a", 32) + "\nx"} {
		t.Setenv(key, token)
		if _, err := Load(); err == nil {
			t.Fatal("invalid origin token accepted")
		}
	}
	os.Unsetenv(key)
	file := filepath.Join(t.TempDir(), "origin-token")
	if err := os.WriteFile(file, []byte(strings.Repeat("a", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(key+"_FILE", file)
	cfg, err := Load()
	if err != nil || cfg.CDNProxies[0].OriginToken != strings.Repeat("a", 32) {
		t.Fatal("token file not loaded", err)
	}
	t.Setenv("NEWPRESS_CDN_ESA_CIDRS", "")
	if _, err := Load(); err == nil {
		t.Fatal("token without networks accepted")
	}
}
