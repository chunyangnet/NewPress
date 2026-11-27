package plugin

// The verification key is part of the official distribution. The copy in the
// public repository is informational and must never replace this trust root.
func officialSource() MarketSource {
	return MarketSource{
		URLs: []string{
			"https://raw.githubusercontent.com/Orangefen/newpress_Plug/main/catalog.extensions-v5.json",
			"https://gitee.com/chunyangnet_0/newpress_plug/raw/main/catalog.extensions-v5.json",
		},
		PublicKey: "aOWw9OexcXTxEL+befgVlkmzRstzryvw8cgGwlQq9Og=",
	}
}
