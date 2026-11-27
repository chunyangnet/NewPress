package plugin

// Themes have their own repository, signing identity and rollback protection.
// The public repository's key file is informational, never a trust bootstrap.
func officialThemeSource() MarketSource {
	return MarketSource{
		URLs: []string{
			"https://raw.githubusercontent.com/Orangefen/NewPress_Theme/main/catalog.themes-v1.json",
			"https://gitee.com/chunyangnet_0/NewPress_Theme/raw/main/catalog.themes-v1.json",
		},
		PublicKey: "mTWjw9y1zAL5QK1gSdmHr9zJsURyVXzF+Wdb1T0W+Kg=",
	}
}
